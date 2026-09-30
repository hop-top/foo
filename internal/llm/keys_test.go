package llm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"hop.top/aim"
	kitllm "hop.top/kit/go/ai/llm"
	"hop.top/kit/go/console/output"
)

// keyedSchemes is every hosted scheme foo links an adapter for, with the
// env var its provider documents. The OpenAI-compatible gateways are the
// point: each used to borrow OPENAI_API_KEY, so an OpenRouter key had to
// sit where a real OpenAI key belongs.
var keyedSchemes = map[string]string{
	"openai":     "OPENAI_API_KEY",
	"anthropic":  "ANTHROPIC_API_KEY",
	"google":     "GOOGLE_API_KEY",
	"gemini":     "GOOGLE_API_KEY",
	"openrouter": "OPENROUTER_API_KEY",
	"groq":       "GROQ_API_KEY",
	"xai":        "XAI_API_KEY",
	"together":   "TOGETHER_API_KEY",
	"fireworks":  "FIREWORKS_API_KEY",
	"deepseek":   "DEEPSEEK_API_KEY",
	"mistral":    "MISTRAL_API_KEY",
}

// localSchemes need no credential; foo must neither precheck nor inject.
var localSchemes = []string{"ollama", "lmstudio", "routellm"}

// clearProviderKeys empties every provider key variable so a developer's
// real environment cannot satisfy a test by accident.
func clearProviderKeys(t *testing.T) {
	t.Helper()
	for _, v := range keyedSchemes {
		t.Setenv(v, "")
	}
	t.Setenv("LLM_BASE_URL", "")
	t.Setenv("LLM_FALLBACK", "")
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func TestEnvVarForScheme_PerProvider(t *testing.T) {
	for scheme, want := range keyedSchemes {
		if got := envVarForScheme(scheme); got != want {
			t.Errorf("envVarForScheme(%q) = %q, want %q", scheme, got, want)
		}
	}
	for _, scheme := range localSchemes {
		if got := envVarForScheme(scheme); got != "" {
			t.Errorf("envVarForScheme(%q) = %q, want no key (local)", scheme, got)
		}
	}
}

// TestEnvVarForScheme_NeverLendsOpenAIKey pins the fallback decision: a
// scheme that is not openai never resolves to OPENAI_API_KEY, known or
// not. Sending an OpenAI key to another host leaks it and cannot
// authenticate there anyway.
func TestEnvVarForScheme_NeverLendsOpenAIKey(t *testing.T) {
	for _, scheme := range []string{"openrouter", "groq", "together", "no-such-scheme"} {
		if got := envVarForScheme(scheme); got == "OPENAI_API_KEY" {
			t.Errorf("envVarForScheme(%q) = OPENAI_API_KEY; a non-openai host must not receive the OpenAI key", scheme)
		}
	}
}

// TestEnvVarForScheme_CoversEveryKitScheme forces a decision for each
// scheme kit registers. Without an entry a new adapter would get no key
// at all, and the first sign would be a provider 401.
func TestEnvVarForScheme_CoversEveryKitScheme(t *testing.T) {
	for _, scheme := range kitllm.Schemes() {
		if _, ok := schemeKeyEnv[scheme]; !ok {
			t.Errorf("kit scheme %q has no entry in schemeKeyEnv; add its key env var (or \"\" for a local runtime)", scheme)
		}
	}
}

// TestSchemeForModel_GuessedArmKeepsOpenAIKey: a bare id with no known
// prefix is sent to the openai scheme, so OPENAI_API_KEY is the right
// key for it — including an OpenRouter-shaped "vendor/model" id.
func TestSchemeForModel_GuessedArmKeepsOpenAIKey(t *testing.T) {
	for _, model := range []string{"openai/gpt-4.1-nano", "qwen3.6-colibri"} {
		scheme, envVar, guessed := schemeForModel(model)
		if scheme != "openai" || envVar != "OPENAI_API_KEY" || !guessed {
			t.Errorf("schemeForModel(%q) = (%q, %q, %v), want (openai, OPENAI_API_KEY, true)",
				model, scheme, envVar, guessed)
		}
	}
}

// TestResolveURI_URIForm_InjectsSchemeKey is the reported defect: a
// URI-form --model reached kit with no api_key, so kit sent no
// Authorization header and OpenRouter answered 401.
func TestResolveURI_URIForm_InjectsSchemeKey(t *testing.T) {
	for scheme, envVar := range keyedSchemes {
		t.Run(scheme, func(t *testing.T) {
			clearProviderKeys(t)
			t.Setenv(envVar, "fake-"+scheme+"-key")
			if envVar != "OPENAI_API_KEY" {
				// Present but wrong: must not be the one injected.
				t.Setenv("OPENAI_API_KEY", "fake-openai-key")
			}

			got, err := resolvedURIForModel(scheme + "://vendor/some-model")
			if err != nil {
				t.Fatalf("resolvedURIForModel: %v", err)
			}
			parsed, err := kitllm.ParseURI(got)
			if err != nil {
				t.Fatalf("ParseURI(%q): %v", got, err)
			}
			if parsed.Scheme != scheme || parsed.Model != "vendor/some-model" {
				t.Errorf("got scheme=%q model=%q, want %s / vendor/some-model", parsed.Scheme, parsed.Model, scheme)
			}
			if want := "fake-" + scheme + "-key"; parsed.Params["api_key"] != want {
				t.Errorf("api_key = %q, want %q (from %s)", parsed.Params["api_key"], want, envVar)
			}
		})
	}
}

// TestResolveURI_URIForm_InjectAppendsToExistingQuery: a URI that already
// carries params gets "&api_key=", not a second "?" that kit would fold
// into the preceding value.
func TestResolveURI_URIForm_InjectAppendsToExistingQuery(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("OPENROUTER_API_KEY", "fake-or-key")

	const base = "http://127.0.0.1:1/v1"
	got, err := resolvedURIForModel("openrouter://openai/gpt-4.1-nano?base_url=" + base)
	if err != nil {
		t.Fatalf("resolvedURIForModel: %v", err)
	}
	if strings.Count(got, "?") != 1 {
		t.Errorf("URI %q has %d '?', want 1", got, strings.Count(got, "?"))
	}
	parsed, err := kitllm.ParseURI(got)
	if err != nil {
		t.Fatalf("ParseURI(%q): %v", got, err)
	}
	if parsed.Params["base_url"] != base {
		t.Errorf("base_url = %q, want %q", parsed.Params["base_url"], base)
	}
	if parsed.Params["api_key"] != "fake-or-key" {
		t.Errorf("api_key = %q, want fake-or-key", parsed.Params["api_key"])
	}
}

// TestResolveURI_URIForm_MissingKey: the URI path fails the way the
// bare-id path does, naming the scheme's own variable — even when
// OPENAI_API_KEY is set.
func TestResolveURI_URIForm_MissingKey(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("OPENAI_API_KEY", "fake-openai-key")

	_, err := resolvedURIForModel("openrouter://openai/gpt-4.1-nano")
	assertMissingKeyError(t, err, "OPENROUTER_API_KEY", "openrouter")
}

// TestResolveURI_URIForm_ExplicitAPIKeyUntouched: a caller-supplied
// api_key outranks the environment and the URI passes through verbatim.
func TestResolveURI_URIForm_ExplicitAPIKeyUntouched(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("OPENROUTER_API_KEY", "fake-env-key")

	const uri = "openrouter://openai/gpt-4.1-nano?api_key=fake-explicit&base_url=http://127.0.0.1:1/v1"
	got, err := resolvedURIForModel(uri)
	if err != nil {
		t.Fatalf("resolvedURIForModel: %v", err)
	}
	if got != uri {
		t.Errorf("URI rewritten:\n got  %q\n want %q", got, uri)
	}
}

// TestResolveURI_URIForm_LocalSchemesUntouched: local runtimes take no
// key, so no precheck fails and nothing is appended.
func TestResolveURI_URIForm_LocalSchemesUntouched(t *testing.T) {
	clearProviderKeys(t)
	for _, uri := range []string{
		"ollama://llama3.2",
		"lmstudio://qwen2.5-7b?base_url=http://127.0.0.1:1234/v1",
		"routellm://mf:0.5",
	} {
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

// TestBuildURI_PickerSchemeUsesOwnKey covers the pool-picker path, where
// the scheme comes from the registry and envVarForScheme names the key.
func TestBuildURI_PickerSchemeUsesOwnKey(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("OPENAI_API_KEY", "fake-openai-key")
	t.Setenv("OPENROUTER_API_KEY", "fake-or-key")

	got, err := buildURI(context.Background(), nil, "openrouter", "openai/gpt-4.1-nano", envVarForScheme("openrouter"))
	if err != nil {
		t.Fatalf("buildURI: %v", err)
	}
	parsed, err := kitllm.ParseURI(got)
	if err != nil {
		t.Fatalf("ParseURI(%q): %v", got, err)
	}
	if parsed.Params["api_key"] != "fake-or-key" {
		t.Errorf("api_key = %q, want fake-or-key (OPENROUTER_API_KEY)", parsed.Params["api_key"])
	}
}

// TestNewClient_PickerPath_OpenRouterNamesItsKey drives NewClient through
// the pool picker with an openrouter entry: the precheck must name
// OPENROUTER_API_KEY, and a set OPENAI_API_KEY must not satisfy it.
func TestNewClient_PickerPath_OpenRouterNamesItsKey(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("OPENAI_API_KEY", "fake-openai-key")
	t.Setenv("LLM_POOL_DISABLE", "")
	writeLLMYAML(t, "pool:\n  - scheme: openrouter\n    model: openai/gpt-4.1-nano\n")

	reg := newFixtureRegistry(t, aim.Model{
		Provider: "openrouter", ID: "openai/gpt-4.1-nano", Name: "gpt-4.1-nano",
		Cost:  &aim.Cost{Input: 0.1, Output: 0.4},
		Limit: aim.Limits{Context: 128000},
	})

	_, err := NewClient(context.Background(), ClientOpts{Registry: reg, Budget: kitllm.BudgetCheap})
	assertMissingKeyError(t, err, "OPENROUTER_API_KEY", "openrouter")
}

func assertMissingKeyError(t *testing.T, err error, envVar, scheme string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected missing-%s error, got nil", envVar)
	}
	var outErr *output.Error
	if !errors.As(err, &outErr) || outErr.Code != output.UnauthorizedError("").Code {
		t.Errorf("error is not an UnauthorizedError: %T %v", err, err)
	}
	msg := err.Error()
	for _, want := range []string{"missing " + envVar, "provider " + scheme, "export " + envVar + "="} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q: %s", want, msg)
		}
	}
	if envVar != "OPENAI_API_KEY" && strings.Contains(msg, "OPENAI_API_KEY") {
		t.Errorf("error names OPENAI_API_KEY for a %s model: %s", scheme, msg)
	}
}
