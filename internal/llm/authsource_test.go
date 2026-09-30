package llm

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// keyEnvVars is every variable that can satisfy a key check, catalog
// alternatives included, so a test starts from a machine with no key
// anywhere.
var keyEnvVars = []string{
	"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GOOGLE_API_KEY",
	"GOOGLE_GENERATIVE_AI_API_KEY", "GEMINI_API_KEY", "OPENROUTER_API_KEY",
	"GROQ_API_KEY", "XAI_API_KEY", "TOGETHER_API_KEY", "FIREWORKS_API_KEY",
	"DEEPSEEK_API_KEY", "MISTRAL_API_KEY", "LMSTUDIO_API_KEY",
	"LLM_API_KEY",
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
// otherwise.
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
// model on scheme — the verdict the index must reproduce.
func precheckPasses(scheme string) bool {
	envVar := envVarForScheme(scheme)
	if envVar == "" {
		return true
	}
	_, err := buildURI(context.Background(), nil, scheme, "some-model", envVar)
	return err == nil
}

// TestAuthIndex_AgreesWithPrecheck is the invariant: for every scheme
// foo links an adapter for, `provider show` and the model-list filter
// report "has a key" exactly when a run's precheck would pass — under
// each source the precheck honours and under none.
func TestAuthIndex_AgreesWithPrecheck(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T, xdg string){
		"no key":            func(*testing.T, string) {},
		"LLM_API_KEY":       func(t *testing.T, _ string) { t.Setenv("LLM_API_KEY", "fake-llm-key") },
		"scheme env var":    func(t *testing.T, _ string) { t.Setenv("GROQ_API_KEY", "fake-groq-key") },
		"catalog alt only":  func(t *testing.T, _ string) { t.Setenv("GEMINI_API_KEY", "fake-gemini-key") },
		"local catalog key": func(t *testing.T, _ string) { t.Setenv("LMSTUDIO_API_KEY", "fake-lms-key") },
		"llm.yaml": func(t *testing.T, xdg string) {
			writeLLMConfig(t, xdg, "providers:\n  openai:\n    api_key: fake-yaml-key\n  gemini:\n    api_key: fake-yaml-gemini\n  fireworks:\n    api_key: fake-yaml-fw\n")
		},
	} {
		t.Run(name, func(t *testing.T) {
			xdg := unsetKeyEnv(t)
			setup(t, xdg)
			idx := NewAuthIndexFrom(context.Background(), catalogEnv, stubStore())

			for scheme := range schemeKeyEnv {
				want := precheckPasses(scheme)
				if got := idx.LookupScheme(scheme).Satisfied(); got != want {
					t.Errorf("LookupScheme(%q).Satisfied() = %v, precheck passes = %v", scheme, got, want)
				}
			}
			// The listing holds catalog ids, not schemes.
			for id, scheme := range map[string]string{
				"openai": "openai", "google": "google", "groq": "groq",
				"fireworks-ai": "fireworks", "togetherai": "together", "lmstudio": "lmstudio",
			} {
				want := precheckPasses(scheme)
				if got := idx.Satisfied(id); got != want {
					t.Errorf("Satisfied(%q) = %v, precheck for %s passes = %v", id, got, scheme, want)
				}
			}
		})
	}
}

// TestAuthIndex_LLMAPIKeySatisfiesKeyedSchemes: LLM_API_KEY alone makes
// every keyed scheme usable at run time, so neither surface may call one
// missing, and the source says which key it was.
func TestAuthIndex_LLMAPIKeySatisfiesKeyedSchemes(t *testing.T) {
	unsetKeyEnv(t)
	t.Setenv("LLM_API_KEY", "fake-llm-key")
	idx := NewAuthIndexFrom(context.Background(), catalogEnv, stubStore())

	got := idx.LookupScheme("openai")
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

	got := idx.LookupScheme("openai")
	if got.Status() != "configured" || got.Source != KeySourceLLMConfig {
		t.Errorf("openai = %q via %q, want configured via %q", got.Status(), got.Source, KeySourceLLMConfig)
	}
	if !idx.Satisfied("openai") {
		t.Error("listing must keep openai rows: the llm.yaml key satisfies the precheck")
	}
	if got := idx.LookupScheme("openrouter"); got.Status() != "missing" || got.Source != "" {
		t.Errorf("openrouter = %q via %q; the openai file key must not be lent to it", got.Status(), got.Source)
	}
}

// TestAuthIndex_SchemeKeySource: a key found through the scheme's own
// secret name reports that source.
func TestAuthIndex_SchemeKeySource(t *testing.T) {
	unsetKeyEnv(t)
	t.Setenv("LLM_API_KEY", "fake-llm-key") // outranked, as in the precheck
	idx := NewAuthIndexFrom(context.Background(), catalogEnv, stubStore("groq_api_key"))

	got := idx.LookupScheme("groq")
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
		got := idx.LookupScheme(scheme)
		if got.Status() != "available" || got.AuthType() != "local" {
			t.Errorf("%s = %q/%q, want available/local", scheme, got.Status(), got.AuthType())
		}
	}
	if !idx.Satisfied("lmstudio") {
		t.Error("lmstudio catalog rows must be reachable without a key")
	}
}

// TestAuthIndex_GoogleNeedsThePrecheckKey: a run reads GOOGLE_API_KEY for
// google, not the catalog's other spellings, so GEMINI_API_KEY alone is
// not "configured".
func TestAuthIndex_GoogleNeedsThePrecheckKey(t *testing.T) {
	unsetKeyEnv(t)
	idx := NewAuthIndexFrom(context.Background(), catalogEnv, stubStore("gemini_api_key"))
	if got := idx.Lookup("google"); got.Status() != "missing" || got.SecretKey != "google_api_key" {
		t.Errorf("google = %q / %q, want missing / google_api_key", got.Status(), got.SecretKey)
	}

	idx = NewAuthIndexFrom(context.Background(), catalogEnv, stubStore("google_api_key"))
	if got := idx.Lookup("google"); got.Status() != "configured" {
		t.Errorf("google status = %q, want configured", got.Status())
	}
}

// TestAuthIndex_GeminiReadsItsOwnConfigBlock: a gemini:// run reads
// providers.gemini, so `provider show gemini` must too — not the google
// record it borrows the catalog requirement from.
func TestAuthIndex_GeminiReadsItsOwnConfigBlock(t *testing.T) {
	xdg := unsetKeyEnv(t)
	writeLLMConfig(t, xdg, "providers:\n  gemini:\n    api_key: fake-yaml-gemini\n")
	idx := NewAuthIndexFrom(context.Background(), catalogEnv, stubStore())

	if got := idx.LookupScheme("gemini"); got.Status() != "configured" || got.Provider != "gemini" {
		t.Errorf("gemini = %q (provider %q), want configured under gemini", got.Status(), got.Provider)
	}
	if got := idx.LookupScheme("google"); got.Status() != "missing" {
		t.Errorf("google = %q, want missing: providers.gemini is not providers.google", got.Status())
	}
}
