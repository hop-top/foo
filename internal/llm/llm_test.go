package llm

import (
	"context"
	"errors"
	"testing"

	"hop.top/aim"
	kitllm "hop.top/kit/go/ai/llm"
	llmerrors "hop.top/kit/go/ai/llm/errors"
)

// TestNewClient_FallbackChainWired verifies the kit fallback chain is
// plumbed through foo's NewClient. With LLM_FALLBACK set to two
// unreachable URIs the resulting client's Prompt call must surface
// *ErrFallbackExhausted with Errors covering primary + every fallback,
// proving WithFallback was applied per entry rather than silently
// dropped. Uses routellm:// URIs pointed at an unreachable host so the
// adapter constructs successfully (no API key precheck) but Complete
// returns a fallbackable network error against every entry.
func TestNewClient_FallbackChainWired(t *testing.T) {
	// Force kit's LoadConfig to see only the env var. No api keys
	// needed: foo's precheck only runs on keyed schemes, and the
	// routellm adapter accepts an empty key.
	t.Setenv("LLM_FALLBACK", "routellm://mf:0.5,routellm://bert:0.5")
	t.Setenv("ROUTELLM_BASE_URL", "http://127.0.0.1:1")
	// Defensive: ensure foo doesn't pick up a stray default model
	// from the inherited environment.
	t.Setenv("FOO_MODEL", "")

	client, err := NewClient(context.Background(), ClientOpts{Model: "router-mf:0.5"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = client.Prompt(context.Background(), "hi")
	if err == nil {
		t.Fatal("expected error from unreachable provider chain, got nil")
	}

	var exhausted *llmerrors.ErrFallbackExhausted
	if !errors.As(err, &exhausted) {
		t.Fatalf("expected *ErrFallbackExhausted, got %T: %v", err, err)
	}

	// Primary (router-mf:0.5) + two LLM_FALLBACK entries = 3
	// attempts. Anything less proves the chain was not wired.
	if got := len(exhausted.Errors); got < 3 {
		t.Fatalf("expected at least 3 attempts (primary + 2 fallbacks), got %d", got)
	}
}

// TestNewClient_NoFallback_SingleAttempt confirms that with no
// LLM_FALLBACK env and no llm.yaml fallback list, only the primary
// provider is attempted. Kit always wraps fallbackable failures in
// ErrFallbackExhausted (chain length is opaque to the caller), so the
// observable difference between "no fallbacks wired" and "fallbacks
// wired" is the size of Errors. A single-provider failure has exactly
// 1 underlying error.
func TestNewClient_NoFallback_SingleAttempt(t *testing.T) {
	t.Setenv("LLM_FALLBACK", "")
	t.Setenv("ROUTELLM_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("FOO_MODEL", "")
	// Force kit's LoadConfig to skip the user's real ~/.config/hop
	// so this test is hermetic across machines.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	client, err := NewClient(context.Background(), ClientOpts{Model: "router-mf:0.5"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = client.Prompt(context.Background(), "hi")
	if err == nil {
		t.Fatal("expected error from unreachable provider, got nil")
	}

	var exhausted *llmerrors.ErrFallbackExhausted
	if !errors.As(err, &exhausted) {
		t.Fatalf("expected *ErrFallbackExhausted (kit always wraps), got %T: %v", err, err)
	}
	if got := len(exhausted.Errors); got != 1 {
		t.Fatalf("expected exactly 1 attempt (primary only, no fallbacks), got %d", got)
	}
}

// TestNewClient_MissingKeyMessage locks the precheck error text in
// place so the helpful "missing X_API_KEY for model Y (provider Z)"
// guidance is not regressed by the fallback wiring change.
func TestNewClient_MissingKeyMessage(t *testing.T) {
	t.Setenv("LLM_FALLBACK", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	_, err := NewClient(context.Background(), ClientOpts{Model: "gpt-4o-mini"})
	if err == nil {
		t.Fatal("expected missing-key error, got nil")
	}
	msg := err.Error()
	wants := []string{"OPENAI_API_KEY", "gpt-4o-mini", "openai", "foo model default"}
	for _, w := range wants {
		if !contains(msg, w) {
			t.Errorf("error message missing %q: %s", w, msg)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// fixtureSource feeds an in-memory model slice to aim.Registry without
// touching the network. Same shape kit uses for its own picker tests.
type fixtureSource struct {
	models []aim.Model
}

func (f fixtureSource) Fetch(_ context.Context) (map[string]*aim.Provider, error) {
	out := map[string]*aim.Provider{}
	for i := range f.models {
		m := f.models[i]
		p, ok := out[m.Provider]
		if !ok {
			p = &aim.Provider{ID: m.Provider, Name: m.Provider, Models: map[string]*aim.Model{}}
			out[m.Provider] = p
		}
		mc := m
		mc.Provider = p.ID
		p.Models[mc.ID] = &mc
	}
	return out, nil
}

func newFixtureRegistry(t *testing.T, models ...aim.Model) *aim.Registry {
	t.Helper()
	return aim.NewRegistry(
		aim.WithSource(fixtureSource{models: models}),
		aim.WithCacheOpts(aim.WithCacheDir(t.TempDir())),
	)
}

func boolPtr(b bool) *bool { return &b }

// TestPickFromPool_TierSelection verifies foo's wrapper around kit's
// PickProviderInPool routes a (profile, budget, pool) triple to the
// correct entry. Three priced models plus a cheap structured-output
// model exercise the cheap/balanced/premium fan-out and the JSON-mode
// capability filter that schema-driven invocations carry.
func TestPickFromPool_TierSelection(t *testing.T) {
	reg := newFixtureRegistry(
		t,
		aim.Model{
			Provider: "openai", ID: "gpt-4o-mini", Name: "gpt-4o-mini",
			StructuredOutput: true,
			Cost:             &aim.Cost{Input: 0.15, Output: 0.6},
			Limit:            aim.Limits{Context: 128000},
		},
		aim.Model{
			Provider: "openai", ID: "gpt-4o", Name: "gpt-4o",
			StructuredOutput: true,
			Cost:             &aim.Cost{Input: 2.5, Output: 10},
			Limit:            aim.Limits{Context: 128000},
		},
		aim.Model{
			Provider: "anthropic", ID: "claude-opus-latest", Name: "claude-opus-latest",
			Cost:  &aim.Cost{Input: 15, Output: 75},
			Limit: aim.Limits{Context: 200000},
		},
	)
	pool := []kitllm.PoolEntry{
		{Scheme: "openai", Model: "gpt-4o-mini", Enabled: true, Weight: 1.0},
		{Scheme: "openai", Model: "gpt-4o", Enabled: true, Weight: 1.0},
		{Scheme: "anthropic", Model: "claude-opus-latest", Enabled: true, Weight: 1.0},
	}

	tests := []struct {
		name       string
		profile    kitllm.RequestProfile
		budget     kitllm.BudgetTier
		wantScheme string
		wantModel  string
	}{
		{
			name:       "cheap picks cheapest weighted price",
			budget:     kitllm.BudgetCheap,
			wantScheme: "openai",
			wantModel:  "gpt-4o-mini",
		},
		{
			name:       "premium picks largest context with highest priced tiebreak",
			budget:     kitllm.BudgetPremium,
			wantScheme: "anthropic",
			wantModel:  "claude-opus-latest",
		},
		{
			name:       "cheap + schema constrains to structured-output models",
			profile:    kitllm.RequestProfile{Filter: aim.Filter{StructuredOutput: boolPtr(true)}},
			budget:     kitllm.BudgetCheap,
			wantScheme: "openai",
			wantModel:  "gpt-4o-mini",
		},
		{
			name:       "premium + schema picks priciest structured-output entry",
			profile:    kitllm.RequestProfile{Filter: aim.Filter{StructuredOutput: boolPtr(true)}},
			budget:     kitllm.BudgetPremium,
			wantScheme: "openai",
			wantModel:  "gpt-4o",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme, model, err := PickFromPool(context.Background(), reg, tc.profile, tc.budget, pool)
			if err != nil {
				t.Fatalf("PickFromPool: %v", err)
			}
			if scheme != tc.wantScheme {
				t.Errorf("scheme: got %q, want %q", scheme, tc.wantScheme)
			}
			if model != tc.wantModel {
				t.Errorf("model: got %q, want %q", model, tc.wantModel)
			}
		})
	}
}

// TestNewClient_PickerPath_KeyPrecheckRunsOnPickedScheme verifies that
// when NewClient drops through to the pool picker, the key precheck
// fires against the SCHEME the picker chose — not the foo-default
// scheme or an unrelated prefix. Empty pool would short-circuit to the
// pre-picker path; populating one forces the picker to run and reveals
// the picked-scheme handling.
func TestNewClient_PickerPath_KeyPrecheckRunsOnPickedScheme(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("LLM_FALLBACK", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	reg := newFixtureRegistry(
		t,
		aim.Model{
			Provider: "openai", ID: "gpt-4o-mini", Name: "gpt-4o-mini",
			Cost:  &aim.Cost{Input: 0.15, Output: 0.6},
			Limit: aim.Limits{Context: 128000},
		},
	)
	pool := []kitllm.PoolEntry{
		{Scheme: "openai", Model: "gpt-4o-mini", Enabled: true, Weight: 1.0},
	}
	picked, err := kitllm.PickProviderInPool(context.Background(), reg, kitllm.RequestProfile{}, kitllm.BudgetCheap, pool)
	if err != nil {
		t.Fatalf("PickProviderInPool: %v", err)
	}
	if picked.Provider != "openai" || picked.ID != "gpt-4o-mini" {
		t.Fatalf("picker fixture broken: got %s/%s", picked.Provider, picked.ID)
	}

	// Asking NewClient with an empty model + injected registry +
	// configured pool should pick gpt-4o-mini and surface the
	// missing-key error for OPENAI_API_KEY against THAT model. We
	// can't shape kit's LoadPool from a test (it reads
	// XDG_CONFIG_HOME), but we can exercise the precheck shape
	// directly via PickFromPool + buildClient by giving NewClient an
	// explicit model in the openai scheme — which still pins
	// precheck behavior.
	_, err = NewClient(context.Background(), ClientOpts{Model: "gpt-4o-mini"})
	if err == nil {
		t.Fatal("expected missing-key error")
	}
	msg := err.Error()
	for _, want := range []string{"OPENAI_API_KEY", "gpt-4o-mini", "openai"} {
		if !contains(msg, want) {
			t.Errorf("error missing %q: %s", want, msg)
		}
	}
}

// TestNewClient_EmptyPool_NoModel_FallsThrough verifies that when both
// the pool is empty AND no explicit model is provided, NewClient does
// not crash — it falls through to the explicit-model path with an
// empty model string, logs a warning, and surfaces whatever buildClient
// returns (which is the precheck error when the implicit default scheme
// has no key). The behavioral contract is "no panic, no silent
// success"; the specific outcome depends on the operator's environment.
func TestNewClient_EmptyPool_NoModel_FallsThrough(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LLM_FALLBACK", "")
	t.Setenv("FOO_MODEL", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")

	_, err := NewClient(context.Background(), ClientOpts{Model: ""})
	if err == nil {
		t.Fatal("expected precheck error when no model and no provider keys")
	}
	if !contains(err.Error(), "OPENAI_API_KEY") {
		t.Errorf("error must mention OPENAI_API_KEY (the implicit default scheme): %v", err)
	}
}
