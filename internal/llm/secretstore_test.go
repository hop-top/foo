package llm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"hop.top/aim"
	kitllm "hop.top/kit/go/ai/llm"
	"hop.top/kit/go/storage/secret"
)

// fakeStore is a map-backed secret.Store: a test states which secrets
// exist instead of reaching a real keychain or vault. errs makes a key
// fail the way a locked or unreachable backend does.
type fakeStore struct {
	vals map[string]string
	errs map[string]error
}

func newFakeStore(kv ...string) *fakeStore {
	s := &fakeStore{vals: map[string]string{}, errs: map[string]error{}}
	for i := 0; i+1 < len(kv); i += 2 {
		s.vals[kv[i]] = kv[i+1]
	}
	return s
}

func (s *fakeStore) Get(_ context.Context, key string) (*secret.Secret, error) {
	if err, ok := s.errs[key]; ok {
		return nil, err
	}
	v, ok := s.vals[key]
	if !ok {
		return nil, secret.ErrNotFound
	}
	return &secret.Secret{Key: key, Value: []byte(v)}, nil
}

func (s *fakeStore) List(_ context.Context, prefix string) ([]string, error) {
	var out []string
	for k := range s.vals {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}

func (s *fakeStore) Exists(ctx context.Context, key string) (bool, error) {
	_, err := s.Get(ctx, key)
	return err == nil, nil
}

// stubStore holds each named secret with a placeholder value.
func stubStore(present ...string) secret.Store {
	s := newFakeStore()
	for _, k := range present {
		s.vals[k] = "value"
	}
	return s
}

// TestRun_ReadsConfiguredStore is the defect: a key held only in foo's
// configured secret store was reported configured by `provider show`
// but refused by the run's precheck, which always read the env backend.
// Every run path — bare id, URI-form --model, pool pick — must read it.
func TestRun_ReadsConfiguredStore(t *testing.T) {
	unsetKeyEnv(t)
	store := newFakeStore("openrouter_api_key", "sk-or-store", "openai_api_key", "sk-oa-store")
	ctx := context.Background()

	uri, _, err := resolveURIForModel(ctx, store, "openrouter://openai/gpt-4.1-nano")
	if err != nil {
		t.Fatalf("URI-form model with its key in the store: %v", err)
	}
	if !strings.Contains(uri, "api_key=sk-or-store") {
		t.Errorf("URI-form model = %q, want the store's key appended", uri)
	}

	uri, _, err = resolveURIForModel(ctx, store, "gpt-4o")
	if err != nil {
		t.Fatalf("bare id with its key in the store: %v", err)
	}
	if !strings.Contains(uri, "api_key=sk-oa-store") {
		t.Errorf("bare id = %q, want the store's key appended", uri)
	}

	uri, err = applyKey(ctx, store, "openrouter://openai/gpt-4.1-nano")
	if err != nil {
		t.Fatalf("pool pick with its key in the store: %v", err)
	}
	if !strings.Contains(uri, "api_key=sk-or-store") {
		t.Errorf("pool pick = %q, want the store's key appended", uri)
	}
}

// TestNewClient_ThreadsConfiguredStore: the store reaches the precheck
// through ClientOpts, the one way a caller hands it in.
func TestNewClient_ThreadsConfiguredStore(t *testing.T) {
	unsetKeyEnv(t)
	model := "openrouter://openai/gpt-4.1-nano?base_url=http://127.0.0.1:9/v1"

	if _, err := NewClient(context.Background(), ClientOpts{Model: model, Secrets: stubStore("openrouter_api_key")}); err != nil {
		t.Fatalf("key in the configured store: %v", err)
	}
	if _, err := NewClient(context.Background(), ClientOpts{Model: model}); err == nil ||
		!strings.Contains(err.Error(), "missing OPENROUTER_API_KEY") {
		t.Fatalf("no store, no env: err = %v, want the missing-key precheck", err)
	}
}

// TestNewClient_PickerPathReadsConfiguredStore: a pool pick's precheck
// reads the same store as an explicit model's.
func TestNewClient_PickerPathReadsConfiguredStore(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("LLM_POOL_DISABLE", "")
	writeLLMYAML(t, "pool:\n  - scheme: openrouter\n    model: openai/gpt-4.1-nano\n")
	reg := newFixtureRegistry(t, aim.Model{
		Provider: "openrouter", ID: "openai/gpt-4.1-nano", Name: "gpt-4.1-nano",
		Cost:  &aim.Cost{Input: 0.1, Output: 0.4},
		Limit: aim.Limits{Context: 128000},
	})

	opts := ClientOpts{Registry: reg, Budget: kitllm.BudgetCheap, Secrets: stubStore("openrouter_api_key")}
	if _, err := NewClient(context.Background(), opts); err != nil {
		t.Fatalf("pool pick with its key in the configured store: %v", err)
	}
}

// TestApplyKey_StoreBeforeEnv pins the order kit applies to a
// provider's own key: the configured store under foo's lowercase
// secret name, then the env var.
func TestApplyKey_StoreBeforeEnv(t *testing.T) {
	unsetKeyEnv(t)
	t.Setenv("OPENROUTER_API_KEY", "sk-or-env")
	ctx := context.Background()
	uri := "openrouter://openai/gpt-4.1-nano"
	sent := func(store secret.Store) string {
		t.Helper()
		got, err := applyKey(ctx, store, uri)
		if err != nil {
			t.Fatalf("applyKey: %v", err)
		}
		return parseOrFatal(t, got).Params["api_key"]
	}

	if k := sent(newFakeStore("openrouter_api_key", "sk-or-store")); k != "sk-or-store" {
		t.Errorf("store and env both set: got %q, want the store's key", k)
	}

	// A store without the key falls through to the env var.
	if k := sent(newFakeStore()); k != "sk-or-env" {
		t.Errorf("store empty: got %q, want the env key", k)
	}

	// A backend error is a miss, not a failure: the env var still counts.
	failing := newFakeStore()
	failing.errs["openrouter_api_key"] = errors.New("keyring locked")
	if k := sent(failing); k != "sk-or-env" {
		t.Errorf("store errors: got %q, want the env key", k)
	}

	// An empty stored value is no key.
	if k := sent(newFakeStore("openrouter_api_key", "")); k != "sk-or-env" {
		t.Errorf("store holds empty value: got %q, want the env key", k)
	}

	// The documented lowercase name is what the store is asked for.
	if k := sent(newFakeStore("OPENROUTER_API_KEY", "sk-or-upper")); k != "sk-or-env" {
		t.Errorf("store asked under the upper-case name: got %q, want the lowercase name only", k)
	}
}

// TestApplyKey_NilStoreIsEnvOnly: with no store configured the lookup
// is the environment (and llm.yaml), as before stores existed.
func TestApplyKey_NilStoreIsEnvOnly(t *testing.T) {
	unsetKeyEnv(t)
	ctx := context.Background()
	const uri = "openrouter://openai/gpt-4.1-nano"
	if _, err := applyKey(ctx, nil, uri); err == nil {
		t.Error("nothing set: want the missing-key precheck")
	}
	t.Setenv("OPENROUTER_API_KEY", "sk-or-env")
	got, err := applyKey(ctx, nil, uri)
	if err != nil {
		t.Fatalf("env set: %v", err)
	}
	if k := parseOrFatal(t, got).Params["api_key"]; k != "sk-or-env" {
		t.Errorf("env set: got %q, want sk-or-env", k)
	}
}

// TestFallbackURIs_ReadConfiguredStore: a fallback entry gets its key
// from the same store as the primary, not only from the environment.
func TestFallbackURIs_ReadConfiguredStore(t *testing.T) {
	xdg := unsetKeyEnv(t)
	writeLLMConfig(t, xdg, "pool: []\n")
	t.Setenv("LLM_FALLBACK", "openrouter://openai/gpt-4.1-nano")

	got := fallbackURIs(context.Background(), newFakeStore("openrouter_api_key", "sk-or-store"), "ollama://llama3.2")
	if len(got) != 1 || !strings.Contains(got[0], "api_key=sk-or-store") {
		t.Errorf("fallbackURIs = %v, want the entry keyed from the store", got)
	}
}

// TestNewClient_FallbacksReadConfiguredStore: a client's fallback chain
// is keyed from the store the client was handed, so a fallback whose key
// lives only there is kept, not dropped as keyless.
func TestNewClient_FallbacksReadConfiguredStore(t *testing.T) {
	xdg := unsetKeyEnv(t)
	writeLLMConfig(t, xdg, "pool: []\n")
	warnings := captureWarnings(t)
	t.Setenv("LLM_FALLBACK", "openrouter://openai/gpt-4.1-nano")

	opts := ClientOpts{Model: "openai://gpt-4o-mini?api_key=fake-oa&base_url=http://127.0.0.1:1/v1", Secrets: stubStore("openrouter_api_key")}
	if _, err := NewClient(context.Background(), opts); err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if strings.Contains(warnings.String(), "llm.fallback.dropped") {
		t.Errorf("fallback dropped although its key is in the configured store: %s", warnings.String())
	}
}

// TestAuthIndex_AgreesWithPrecheckUnderStore: `provider show` and a run
// read one store, so a key only in it is "configured" exactly when the
// precheck accepts it, and a key only in the environment likewise.
func TestAuthIndex_AgreesWithPrecheckUnderStore(t *testing.T) {
	for name, tc := range map[string]struct {
		store secret.Store
		env   map[string]string
	}{
		"store only":            {store: stubStore("groq_api_key", "openrouter_api_key", "google_api_key")},
		"env only, store empty": {store: stubStore(), env: map[string]string{"GROQ_API_KEY": "fake-groq"}},
		"no store":              {env: map[string]string{"OPENAI_API_KEY": "fake-oa"}},
	} {
		t.Run(name, func(t *testing.T) {
			unsetKeyEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			idx := NewAuthIndexFrom(context.Background(), catalogEnv, tc.store)
			for _, scheme := range schemesUnderTest() {
				want := precheckPasses(tc.store, scheme)
				if got := idx.Lookup(scheme).Satisfied(); got != want {
					t.Errorf("Lookup(%q).Satisfied() = %v, precheck passes = %v", scheme, got, want)
				}
			}
			if name == "store only" && !idx.Lookup("groq").Satisfied() {
				t.Error("groq key is in the store; want configured")
			}
		})
	}
}
