package llm

import (
	"context"
	"testing"

	kitllm "hop.top/kit/go/ai/llm"
)

// Key precedence, highest first, for a keyed scheme:
//
//  1. ?api_key= on the URI (the caller's explicit choice)
//  2. the scheme's own key: secret store, then its env var
//  3. LLM_API_KEY (kit's universal key)
//
// Kit's Resolve reads the key from the URI alone, so whatever foo
// appends is exactly what kit sends; these tests assert on that URI.

// TestLLMAPIKey_SatisfiesPrecheck: LLM_API_KEY alone satisfies the
// precheck on every path that builds a keyed URI, and is the key sent.
func TestLLMAPIKey_SatisfiesPrecheck(t *testing.T) {
	for _, model := range []string{
		"gpt-4o-mini",                      // bare id, recognised prefix
		"claude-3-haiku-20240307",          // bare id, anthropic
		"qwen3.6-colibri",                  // bare id, guessed openai arm
		"openrouter://openai/gpt-4.1-nano", // URI form
		"groq://llama-3.1-8b-instant",      // URI form
	} {
		t.Run(model, func(t *testing.T) {
			clearProviderKeys(t)
			t.Setenv("LLM_API_KEY", "fake-llm-key")

			got, err := resolvedURIForModel(model)
			if err != nil {
				t.Fatalf("resolvedURIForModel(%q) with LLM_API_KEY set: %v", model, err)
			}
			if k := parseOrFatal(t, got).Params["api_key"]; k != "fake-llm-key" {
				t.Errorf("api_key = %q, want fake-llm-key (LLM_API_KEY)", k)
			}
		})
	}
}

// TestLLMAPIKey_PickerPath covers the pool pick, where the scheme comes
// from the registry.
func TestLLMAPIKey_PickerPath(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("LLM_API_KEY", "fake-llm-key")

	got, err := buildURI(context.Background(), nil, "openrouter", "openai/gpt-4.1-nano", envVarForScheme("openrouter"))
	if err != nil {
		t.Fatalf("buildURI with LLM_API_KEY set: %v", err)
	}
	if k := parseOrFatal(t, got).Params["api_key"]; k != "fake-llm-key" {
		t.Errorf("api_key = %q, want fake-llm-key", k)
	}
}

// TestLLMAPIKey_SchemeKeyWins: LLM_API_KEY is a fallback, not an
// override. A scheme's own key outranks it (kit's SecretFor order, and
// the google adapter's GEMINI_API_KEY > LLM_API_KEY).
func TestLLMAPIKey_SchemeKeyWins(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("LLM_API_KEY", "fake-llm-key")
	t.Setenv("OPENROUTER_API_KEY", "fake-or-key")

	got, err := resolvedURIForModel("openrouter://openai/gpt-4.1-nano")
	if err != nil {
		t.Fatalf("resolvedURIForModel: %v", err)
	}
	if k := parseOrFatal(t, got).Params["api_key"]; k != "fake-or-key" {
		t.Errorf("api_key = %q, want fake-or-key (scheme key outranks LLM_API_KEY)", k)
	}
}

// TestLLMAPIKey_ExplicitURIKeyWins: ?api_key= on the URI is sent as
// written.
func TestLLMAPIKey_ExplicitURIKeyWins(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("LLM_API_KEY", "fake-llm-key")

	const uri = "openrouter://openai/gpt-4.1-nano?api_key=fake-explicit"
	got, err := resolvedURIForModel(uri)
	if err != nil {
		t.Fatalf("resolvedURIForModel: %v", err)
	}
	if got != uri {
		t.Errorf("URI rewritten:\n got  %q\n want %q", got, uri)
	}
}

// TestLLMAPIKey_LocalSchemesUntouched: local runtimes take no key; a
// universal key is not sprayed onto them.
func TestLLMAPIKey_LocalSchemesUntouched(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("LLM_API_KEY", "fake-llm-key")
	for _, uri := range []string{"ollama://llama3.2", "routellm://mf:0.5"} {
		got, err := resolvedURIForModel(uri)
		if err != nil {
			t.Errorf("resolvedURIForModel(%q): %v", uri, err)
			continue
		}
		if got != uri {
			t.Errorf("local URI rewritten: got %q, want %q", got, uri)
		}
	}
}

// TestLLMAPIKey_AppliesToFallbacks: kit applies LLM_API_KEY to every URI
// it resolves config for, fallbacks included, so a fallback without its
// own key takes LLM_API_KEY instead of being dropped.
func TestLLMAPIKey_AppliesToFallbacks(t *testing.T) {
	clearProviderKeys(t)
	warnings := captureWarnings(t)
	t.Setenv("LLM_API_KEY", "fake-llm-key")
	t.Setenv("ANTHROPIC_API_KEY", "fake-ant-key")
	t.Setenv("LLM_FALLBACK", "openrouter://openai/gpt-4.1-nano,anthropic://claude-3-haiku-20240307")

	got := fallbackURIs(context.Background(), nil, "ollama://llama3.2")
	if len(got) != 2 {
		t.Fatalf("fallbackURIs = %q, want 2 entries", got)
	}
	for i, want := range []string{"fake-llm-key", "fake-ant-key"} {
		if k := parseOrFatal(t, got[i]).Params["api_key"]; k != want {
			t.Errorf("fallback[%d] api_key = %q, want %q", i, k, want)
		}
	}
	if warnings.Len() != 0 {
		t.Errorf("unexpected warnings:\n%s", warnings.String())
	}
}

// TestConfigFileKey_SatisfiesPrecheck: llm.yaml providers.<scheme>.api_key
// is documented as a key source; it satisfies the precheck on every path
// and is the key that reaches kit.
func TestConfigFileKey_SatisfiesPrecheck(t *testing.T) {
	const yaml = "providers:\n  openai:\n    api_key: fake-file-openai\n  openrouter:\n    api_key: fake-file-or\n"
	for model, want := range map[string]string{
		"gpt-4o-mini":                      "fake-file-openai", // bare id
		"qwen3.6-colibri":                  "fake-file-openai", // guessed openai arm
		"openrouter://openai/gpt-4.1-nano": "fake-file-or",     // URI form
	} {
		t.Run(model, func(t *testing.T) {
			clearProviderKeys(t)
			writeLLMYAML(t, yaml)

			got, err := resolvedURIForModel(model)
			if err != nil {
				t.Fatalf("resolvedURIForModel(%q) with a config-file key: %v", model, err)
			}
			if k := parseOrFatal(t, got).Params["api_key"]; k != want {
				t.Errorf("api_key = %q, want %q (llm.yaml)", k, want)
			}
		})
	}
}

// TestConfigFileKey_PickerAndFallback: the pool pick and fallback
// entries read the same file key.
func TestConfigFileKey_PickerAndFallback(t *testing.T) {
	clearProviderKeys(t)
	warnings := captureWarnings(t)
	writeLLMYAML(t, "providers:\n  openrouter:\n    api_key: fake-file-or\nfallback:\n  - openrouter://openai/gpt-4.1-mini\n")

	got, err := buildURI(context.Background(), nil, "openrouter", "openai/gpt-4.1-nano", envVarForScheme("openrouter"))
	if err != nil {
		t.Fatalf("buildURI with a config-file key: %v", err)
	}
	if k := parseOrFatal(t, got).Params["api_key"]; k != "fake-file-or" {
		t.Errorf("picker api_key = %q, want fake-file-or", k)
	}

	fbs := fallbackURIs(context.Background(), nil, "ollama://llama3.2")
	if len(fbs) != 1 {
		t.Fatalf("fallbackURIs = %q, want 1 entry\n%s", fbs, warnings.String())
	}
	if k := parseOrFatal(t, fbs[0]).Params["api_key"]; k != "fake-file-or" {
		t.Errorf("fallback api_key = %q, want fake-file-or", k)
	}
}

// TestConfigFileKey_OtherSchemeNeverLent: a file key belongs to its own
// scheme. providers.openai.api_key must not satisfy an openrouter model.
func TestConfigFileKey_OtherSchemeNeverLent(t *testing.T) {
	clearProviderKeys(t)
	writeLLMYAML(t, "providers:\n  openai:\n    api_key: fake-file-openai\n")

	_, err := resolvedURIForModel("openrouter://openai/gpt-4.1-nano")
	assertMissingKeyError(t, err, "OPENROUTER_API_KEY", "openrouter")
}

// TestKeyPrecedence_Matrix pins the full order and checks the rows kit
// itself defines against kit. Highest first: URI ?api_key=, the
// scheme's own key (secret store / env), LLM_API_KEY, llm.yaml
// providers.<scheme>.api_key.
//
// Kit's Resolve reads the key from the URI's api_key param alone, so
// the param on the URI foo builds is exactly what kit sends. For the
// rows with no scheme key, kit's LoadConfig merge (file < URI <
// LLM_API_KEY) must pick the same key foo did.
func TestKeyPrecedence_Matrix(t *testing.T) {
	const model = "openrouter://openai/gpt-4.1-nano"
	cases := []struct {
		name                   string
		explicit, env, llm, fs string
		want                   string
	}{
		{name: "explicit beats all", explicit: "k-uri", env: "k-env", llm: "k-llm", fs: "k-file", want: "k-uri"},
		{name: "scheme env beats LLM_API_KEY and file", env: "k-env", llm: "k-llm", fs: "k-file", want: "k-env"},
		{name: "LLM_API_KEY beats file", llm: "k-llm", fs: "k-file", want: "k-llm"},
		{name: "file alone", fs: "k-file", want: "k-file"},
		{name: "scheme env alone", env: "k-env", want: "k-env"},
		{name: "LLM_API_KEY alone", llm: "k-llm", want: "k-llm"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearProviderKeys(t)
			yaml := ""
			if tc.fs != "" {
				yaml = "providers:\n  openrouter:\n    api_key: " + tc.fs + "\n"
			}
			writeLLMYAML(t, yaml)
			t.Setenv("OPENROUTER_API_KEY", tc.env)
			t.Setenv("LLM_API_KEY", tc.llm)
			uri := model
			if tc.explicit != "" {
				uri += "?api_key=" + tc.explicit
			}

			got, err := resolvedURIForModel(uri)
			if err != nil {
				t.Fatalf("resolvedURIForModel(%q): %v", uri, err)
			}
			sent := parseOrFatal(t, got).Params["api_key"]
			if sent != tc.want {
				t.Errorf("api_key sent = %q, want %q", sent, tc.want)
			}

			if tc.explicit == "" && tc.env == "" {
				cfg, err := kitllm.LoadConfig(model)
				if err != nil {
					t.Fatalf("kit LoadConfig: %v", err)
				}
				if cfg.Provider.APIKey != sent {
					t.Errorf("foo sent %q but kit's LoadConfig resolves %q", sent, cfg.Provider.APIKey)
				}
			}
		})
	}
}
