package llm

import (
	"context"
	"errors"
	"testing"

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

	client, err := NewClient(context.Background(), "router-mf:0.5")
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

	client, err := NewClient(context.Background(), "router-mf:0.5")
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

	_, err := NewClient(context.Background(), "gpt-4o-mini")
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
