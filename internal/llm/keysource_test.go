package llm

import (
	"testing"
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

	got, err := buildURI("openrouter", "openai/gpt-4.1-nano", envVarForScheme("openrouter"))
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

	got := fallbackURIs("ollama://llama3.2")
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
