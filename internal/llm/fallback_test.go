package llm

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	kitllm "hop.top/kit/go/ai/llm"
)

// captureWarnings routes slog to a buffer for the test and clears the
// once-per-run memory of dropped fallbacks, so each test sees its own
// warnings from a clean slate.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	resetDroppedFallbacks()
	t.Cleanup(func() {
		slog.SetDefault(prev)
		resetDroppedFallbacks()
	})
	return &buf
}

func resetDroppedFallbacks() {
	droppedFallbacksMu.Lock()
	droppedFallbacks = map[string]struct{}{}
	droppedFallbacksMu.Unlock()
}

func parseOrFatal(t *testing.T, uri string) kitllm.URI {
	t.Helper()
	parsed, err := kitllm.ParseURI(uri)
	if err != nil {
		t.Fatalf("ParseURI(%q): %v", uri, err)
	}
	return parsed
}

// TestFallbackURIs_InjectsSchemeKey is the reported defect: fallback
// entries reached kit without api_key, so an openrouter:// fallback sent
// an empty Authorization header. Each entry gets its own scheme's key,
// never OPENAI_API_KEY.
func TestFallbackURIs_InjectsSchemeKey(t *testing.T) {
	for scheme, envVar := range keyedSchemes {
		t.Run(scheme, func(t *testing.T) {
			clearProviderKeys(t)
			captureWarnings(t)
			t.Setenv(envVar, "fake-"+scheme+"-key")
			if envVar != "OPENAI_API_KEY" {
				t.Setenv("OPENAI_API_KEY", "fake-openai-key")
			}
			t.Setenv("LLM_FALLBACK", scheme+"://vendor/fb-model")

			got := fallbackURIs(context.Background(), nil, "ollama://llama3.2")
			if len(got) != 1 {
				t.Fatalf("fallbackURIs = %q, want 1 entry", got)
			}
			parsed := parseOrFatal(t, got[0])
			if parsed.Scheme != scheme || parsed.Model != "vendor/fb-model" {
				t.Errorf("got %s / %s, want %s / vendor/fb-model", parsed.Scheme, parsed.Model, scheme)
			}
			if want := "fake-" + scheme + "-key"; parsed.Params["api_key"] != want {
				t.Errorf("api_key = %q, want %q (from %s)", parsed.Params["api_key"], want, envVar)
			}
		})
	}
}

// TestFallbackURIs_ConfigFileList: the llm.yaml `fallback:` list takes
// the same path as LLM_FALLBACK.
func TestFallbackURIs_ConfigFileList(t *testing.T) {
	clearProviderKeys(t)
	captureWarnings(t)
	t.Setenv("OPENROUTER_API_KEY", "fake-or-key")
	t.Setenv("ANTHROPIC_API_KEY", "fake-ant-key")
	writeLLMYAML(t, "fallback:\n  - openrouter://openai/gpt-4.1-nano\n  - anthropic://claude-3-haiku-20240307\n")

	got := fallbackURIs(context.Background(), nil, "ollama://llama3.2")
	if len(got) != 2 {
		t.Fatalf("fallbackURIs = %q, want 2 entries", got)
	}
	for i, want := range []string{"fake-or-key", "fake-ant-key"} {
		if k := parseOrFatal(t, got[i]).Params["api_key"]; k != want {
			t.Errorf("fallback[%d] api_key = %q, want %q", i, k, want)
		}
	}
}

// TestFallbackURIs_MissingKeyDropsEntry: a fallback whose key is absent
// is dropped with one warning naming the variable; the rest of the chain
// survives. The warning is not repeated when a second client is built in
// the same process.
func TestFallbackURIs_MissingKeyDropsEntry(t *testing.T) {
	clearProviderKeys(t)
	warnings := captureWarnings(t)
	t.Setenv("OPENAI_API_KEY", "fake-openai-key")
	t.Setenv("LLM_FALLBACK", "openrouter://openai/gpt-4.1-nano,openai://gpt-4o-mini")

	got := fallbackURIs(context.Background(), nil, "ollama://llama3.2")
	if len(got) != 1 || parseOrFatal(t, got[0]).Scheme != "openai" {
		t.Fatalf("fallbackURIs = %q, want only the openai entry", got)
	}
	if k := parseOrFatal(t, got[0]).Params["api_key"]; k != "fake-openai-key" {
		t.Errorf("openai fallback api_key = %q, want fake-openai-key", k)
	}

	_ = fallbackURIs(context.Background(), nil, "ollama://llama3.2")

	out := warnings.String()
	if n := strings.Count(out, "llm.fallback.dropped"); n != 1 {
		t.Errorf("want exactly one fallback warning, got %d:\n%s", n, out)
	}
	if !strings.Contains(out, "missing=OPENROUTER_API_KEY") {
		t.Errorf("warning does not name OPENROUTER_API_KEY:\n%s", out)
	}
	if !strings.Contains(out, "openrouter://openai/gpt-4.1-nano") {
		t.Errorf("warning does not name the dropped fallback:\n%s", out)
	}
	if strings.Contains(out, "fake-openai-key") {
		t.Errorf("warning leaks a key:\n%s", out)
	}
}

// TestNewClient_MissingFallbackKeyKeepsPrimary: a fallback without a key
// never fails the run; the primary client still builds.
func TestNewClient_MissingFallbackKeyKeepsPrimary(t *testing.T) {
	clearProviderKeys(t)
	warnings := captureWarnings(t)
	t.Setenv("OPENAI_API_KEY", "fake-openai-key")
	t.Setenv("LLM_FALLBACK", "openrouter://openai/gpt-4.1-nano")

	client, err := NewClient(context.Background(), ClientOpts{Model: "openai://gpt-4o-mini?base_url=http://127.0.0.1:1/v1"})
	if err != nil {
		t.Fatalf("NewClient failed on a fallback's missing key: %v", err)
	}
	if client == nil {
		t.Fatal("NewClient returned nil client")
	}
	if !strings.Contains(warnings.String(), "OPENROUTER_API_KEY") {
		t.Errorf("no warning for the dropped fallback:\n%s", warnings.String())
	}
}

// TestFallbackURIs_ExplicitAndLocalUntouched: a fallback carrying its own
// api_key, and a local runtime, pass through verbatim.
func TestFallbackURIs_ExplicitAndLocalUntouched(t *testing.T) {
	clearProviderKeys(t)
	warnings := captureWarnings(t)
	t.Setenv("OPENROUTER_API_KEY", "fake-env-key")
	want := []string{
		"openrouter://openai/gpt-4.1-nano?api_key=fake-explicit",
		"ollama://llama3.2",
		"routellm://mf:0.5",
	}
	t.Setenv("LLM_FALLBACK", strings.Join(want, ","))

	got := fallbackURIs(context.Background(), nil, "ollama://llama3.2")
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("fallbacks rewritten:\n got  %q\n want %q", got, want)
	}
	if warnings.Len() != 0 {
		t.Errorf("unexpected warnings:\n%s", warnings.String())
	}
}
