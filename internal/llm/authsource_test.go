package llm

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"hop.top/kit/go/storage/secret"
)

// keyEnvVars is every variable that can satisfy a key check, catalog
// alternatives included, so a test starts from a machine with no key
// anywhere.
var keyEnvVars = []string{
	"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GOOGLE_API_KEY",
	"GOOGLE_GENERATIVE_AI_API_KEY", "GEMINI_API_KEY", "OPENROUTER_API_KEY",
	"GROQ_API_KEY", "XAI_API_KEY", "TOGETHER_API_KEY", "FIREWORKS_API_KEY",
	"DEEPSEEK_API_KEY", "MISTRAL_API_KEY", "LMSTUDIO_API_KEY",
	"OLLAMA_API_KEY", "ROUTELLM_API_KEY", "TRITON_API_KEY",
	"DIGITALOCEAN_ACCESS_TOKEN", "LLM_API_KEY",
}

// unsetKeyEnv removes every key variable for the test (t.Setenv first,
// so the operator's values come back afterwards) and points
// XDG_CONFIG_HOME at an empty dir, so no real llm.yaml is read.
func unsetKeyEnv(t *testing.T) string {
	t.Helper()
	for _, v := range keyEnvVars {
		t.Setenv(v, "")
		_ = os.Unsetenv(v)
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return dir
}

// writeLLMConfig writes $XDG_CONFIG_HOME/hop/llm.yaml.
func writeLLMConfig(t *testing.T, xdg, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(xdg, "hop"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "hop", "llm.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// catalogEnv is the requirement aim publishes for the kit schemes, in
// its own shape: google lists three spellings, lmstudio declares a key
// the local server does not need, fireworks and together are spelled
// otherwise. Kit's key plan answers for all of them; the lists matter
// only to a provider no adapter serves.
var catalogEnv = map[string][]string{
	"openai":       {"OPENAI_API_KEY"},
	"anthropic":    {"ANTHROPIC_API_KEY"},
	"google":       {"GOOGLE_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY", "GEMINI_API_KEY"},
	"openrouter":   {"OPENROUTER_API_KEY"},
	"groq":         {"GROQ_API_KEY"},
	"xai":          {"XAI_API_KEY"},
	"togetherai":   {"TOGETHER_API_KEY"},
	"fireworks-ai": {"FIREWORKS_API_KEY"},
	"deepseek":     {"DEEPSEEK_API_KEY"},
	"mistral":      {"MISTRAL_API_KEY"},
	"lmstudio":     {"LMSTUDIO_API_KEY"},
}

// precheckPasses reports whether a run's key precheck would accept a
// model on scheme with store — the verdict the index must reproduce.
func precheckPasses(store secret.Store, scheme string) bool {
	_, _, err := resolveURIForModel(context.Background(), store, scheme+"://some-model")
	return err == nil
}

// TestAuthIndex_LLMAPIKeySatisfiesKeyedSchemes: LLM_API_KEY alone makes
// every keyed scheme usable at run time, so neither surface may call one
// missing, and the source says which key it was.
func TestAuthIndex_LLMAPIKeySatisfiesKeyedSchemes(t *testing.T) {
	unsetKeyEnv(t)
	t.Setenv("LLM_API_KEY", "fake-llm-key")
	idx := NewAuthIndexFrom(context.Background(), catalogEnv, stubStore())

	got := idx.Lookup("openai")
	if got.Status() != "configured" {
		t.Fatalf("openai status = %q, want configured via LLM_API_KEY", got.Status())
	}
	if got.Source != KeySourceLLMAPIKey {
		t.Errorf("openai Source = %q, want %q", got.Source, KeySourceLLMAPIKey)
	}
	// secret_key keeps naming the scheme's own key, the one to set to
	// stop sharing LLM_API_KEY.
	if got.SecretKey != "openai_api_key" {
		t.Errorf("openai SecretKey = %q, want openai_api_key", got.SecretKey)
	}
	if !idx.Satisfied("groq") {
		t.Error("groq: LLM_API_KEY satisfies the precheck, so the listing must keep its rows")
	}
	if cp := idx.ConfiguredProviders(); len(cp) == 0 {
		t.Error("ConfiguredProviders() empty with LLM_API_KEY set; the footer would claim no key is configured")
	}
}

// TestAuthIndex_LLMConfigKeyBelongsToItsScheme: providers.<scheme>.api_key
// in llm.yaml counts for that scheme and no other.
func TestAuthIndex_LLMConfigKeyBelongsToItsScheme(t *testing.T) {
	xdg := unsetKeyEnv(t)
	writeLLMConfig(t, xdg, "providers:\n  openai:\n    api_key: fake-yaml-key\n")
	idx := NewAuthIndexFrom(context.Background(), catalogEnv, stubStore())

	got := idx.Lookup("openai")
	if got.Status() != "configured" || got.Source != KeySourceLLMConfig {
		t.Errorf("openai = %q via %q, want configured via %q", got.Status(), got.Source, KeySourceLLMConfig)
	}
	if !idx.Satisfied("openai") {
		t.Error("listing must keep openai rows: the llm.yaml key satisfies the precheck")
	}
	if got := idx.Lookup("openrouter"); got.Status() != "missing" || got.Source != "" {
		t.Errorf("openrouter = %q via %q; the openai file key must not be lent to it", got.Status(), got.Source)
	}
}

// TestAuthIndex_SchemeKeySource: a key found through the scheme's own
// secret name reports that source.
func TestAuthIndex_SchemeKeySource(t *testing.T) {
	unsetKeyEnv(t)
	t.Setenv("LLM_API_KEY", "fake-llm-key") // outranked, as in the precheck
	idx := NewAuthIndexFrom(context.Background(), catalogEnv, stubStore("groq_api_key"))

	got := idx.Lookup("groq")
	if got.Source != KeySourceSecret || got.SecretKey != "groq_api_key" {
		t.Errorf("groq = %q / %q, want %q / groq_api_key", got.Source, got.SecretKey, KeySourceSecret)
	}
}

// TestAuthIndex_LocalSchemesNeedNoKey: the catalog lists LMSTUDIO_API_KEY
// for lmstudio, but a run never asks for one, so the listing must not
// hide a local server's models behind it.
func TestAuthIndex_LocalSchemesNeedNoKey(t *testing.T) {
	unsetKeyEnv(t)
	idx := NewAuthIndexFrom(context.Background(), catalogEnv, stubStore())

	for _, scheme := range localSchemes {
		got := idx.Lookup(scheme)
		if got.Status() != "available" || got.AuthType() != "local" {
			t.Errorf("%s = %q/%q, want available/local", scheme, got.Status(), got.AuthType())
		}
	}
	if !idx.Satisfied("lmstudio") {
		t.Error("lmstudio catalog rows must be reachable without a key")
	}
}

// TestAuthIndex_GoogleTakesEitherKey: kit reads GOOGLE_API_KEY, then
// GEMINI_API_KEY, for google, so either one alone is "configured", and
// the index names the one that resolved.
func TestAuthIndex_GoogleTakesEitherKey(t *testing.T) {
	unsetKeyEnv(t)
	idx := NewAuthIndexFrom(context.Background(), catalogEnv, stubStore("gemini_api_key"))
	if got := idx.Lookup("google"); got.Status() != "configured" || got.SecretKey != "gemini_api_key" {
		t.Errorf("google = %q / %q, want configured / gemini_api_key", got.Status(), got.SecretKey)
	}

	idx = NewAuthIndexFrom(context.Background(), catalogEnv, stubStore())
	if got := idx.Lookup("google"); got.Status() != "missing" || got.SecretKey != "google_api_key" {
		t.Errorf("google = %q / %q, want missing / google_api_key (the first name)", got.Status(), got.SecretKey)
	}
}

// TestAuthIndex_BlankNameIsSkipped: kit skips a blank variable and
// takes the next name, so the source the index names is the one that
// answered, not the first one exported.
func TestAuthIndex_BlankNameIsSkipped(t *testing.T) {
	unsetKeyEnv(t)
	t.Setenv("GOOGLE_API_KEY", "  ")
	t.Setenv("GEMINI_API_KEY", "fake-gemini")
	idx := NewAuthIndexFrom(context.Background(), catalogEnv, nil)
	got := idx.Lookup("google")
	if got.Status() != "configured" || got.Source != KeySourceSecret || got.SecretKey != "gemini_api_key" {
		t.Errorf("google = %q / %q / %q, want configured / %q / gemini_api_key", got.Status(), got.Source, got.SecretKey, KeySourceSecret)
	}
}

// TestAuthIndex_GoogleAndGeminiShareConfigBlocks: google and gemini name
// one provider, and kit reads an llm.yaml block by either name for
// both: the scheme's own block first, else the other name's. The index
// reports what a run on each scheme does, key and source alike.
func TestAuthIndex_GoogleAndGeminiShareConfigBlocks(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		// want is the key a run on each scheme sends.
		want map[string]string
	}{
		{
			name: "gemini block only",
			yaml: "providers:\n  gemini:\n    api_key: fake-yaml-gemini\n",
			want: map[string]string{"gemini": "fake-yaml-gemini", "google": "fake-yaml-gemini"},
		},
		{
			name: "google block only",
			yaml: "providers:\n  google:\n    api_key: fake-yaml-google\n",
			want: map[string]string{"gemini": "fake-yaml-google", "google": "fake-yaml-google"},
		},
		{
			name: "both blocks: each scheme's own wins",
			yaml: "providers:\n  google:\n    api_key: fake-yaml-google\n  gemini:\n    api_key: fake-yaml-gemini\n",
			want: map[string]string{"gemini": "fake-yaml-gemini", "google": "fake-yaml-google"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			xdg := unsetKeyEnv(t)
			writeLLMConfig(t, xdg, tc.yaml)
			idx := NewAuthIndexFrom(context.Background(), catalogEnv, stubStore())
			for _, scheme := range []string{"gemini", "google"} {
				got := idx.Lookup(scheme)
				if got.Status() != "configured" || got.Provider != scheme || got.Source != KeySourceLLMConfig {
					t.Errorf("%s = %q (provider %q, source %q), want configured from llm.yaml", scheme, got.Status(), got.Provider, got.Source)
				}
				uri, err := applyKey(context.Background(), stubStore(), scheme+"://some-model")
				if err != nil {
					t.Fatalf("%s run: %v", scheme, err)
				}
				if k := parseOrFatal(t, uri).Params["api_key"]; k != tc.want[scheme] {
					t.Errorf("%s run sends %q, want %q", scheme, k, tc.want[scheme])
				}
			}
		})
	}
}
