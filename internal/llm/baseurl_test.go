package llm

import (
	"os"
	"path/filepath"
	"testing"

	kitllm "hop.top/kit/go/ai/llm"
)

// writeLLMYAML points XDG_CONFIG_HOME at a temp dir holding the given
// hop/llm.yaml, so kit's LoadConfig reads it instead of the developer's
// real config.
func writeLLMYAML(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	hop := filepath.Join(dir, "hop")
	if err := os.MkdirAll(hop, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hop, "llm.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write llm.yaml: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", dir)
}

// TestResolvedURI_BaseURLFromConfigFile covers the documented
// `providers.<scheme>.base_url` key, which was ignored: buildClient
// assembled a URI and handed it to kitllm.Resolve, which reads the URI
// alone and never consults llm.yaml. Requests went to api.openai.com no
// matter what the file said.
func TestResolvedURI_BaseURLFromConfigFile(t *testing.T) {
	t.Setenv("LLM_BASE_URL", "")
	t.Setenv("OPENAI_API_KEY", "test-key")
	writeLLMYAML(t, `
providers:
  openai:
    base_url: http://127.0.0.1:9001/v1
`)

	got, err := resolvedURIForModel("gpt-4o")
	if err != nil {
		t.Fatalf("resolvedURIForModel: %v", err)
	}
	parsed, err := kitllm.ParseURI(got)
	if err != nil {
		t.Fatalf("ParseURI(%q): %v", got, err)
	}
	if parsed.Params["base_url"] != "http://127.0.0.1:9001/v1" {
		t.Fatalf("base_url = %q, want the llm.yaml value (uri=%q)",
			parsed.Params["base_url"], got)
	}
}

// TestResolvedURI_BaseURLFromEnv covers the documented LLM_BASE_URL
// variable, ignored for the same reason.
func TestResolvedURI_BaseURLFromEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("LLM_BASE_URL", "http://127.0.0.1:9002/v1")
	writeLLMYAML(t, "")

	got, err := resolvedURIForModel("gpt-4o")
	if err != nil {
		t.Fatalf("resolvedURIForModel: %v", err)
	}
	parsed, err := kitllm.ParseURI(got)
	if err != nil {
		t.Fatalf("ParseURI(%q): %v", got, err)
	}
	if parsed.Params["base_url"] != "http://127.0.0.1:9002/v1" {
		t.Fatalf("base_url = %q, want the LLM_BASE_URL value (uri=%q)",
			parsed.Params["base_url"], got)
	}
}

// TestResolvedURI_EnvBeatsConfigFile pins the documented precedence:
// env overrides the file.
func TestResolvedURI_EnvBeatsConfigFile(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("LLM_BASE_URL", "http://127.0.0.1:9002/v1")
	writeLLMYAML(t, `
providers:
  openai:
    base_url: http://127.0.0.1:9001/v1
`)

	got, err := resolvedURIForModel("gpt-4o")
	if err != nil {
		t.Fatalf("resolvedURIForModel: %v", err)
	}
	parsed, _ := kitllm.ParseURI(got)
	if parsed.Params["base_url"] != "http://127.0.0.1:9002/v1" {
		t.Fatalf("base_url = %q, want env to beat the config file", parsed.Params["base_url"])
	}
}

// TestResolvedURI_ExplicitParamWins keeps the one lever that already
// worked authoritative: a base_url written on the --model value must
// beat both the file and the env.
func TestResolvedURI_ExplicitParamWins(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("LLM_BASE_URL", "http://127.0.0.1:9002/v1")
	writeLLMYAML(t, `
providers:
  openai:
    base_url: http://127.0.0.1:9001/v1
`)

	const explicit = "http://127.0.0.1:9003/v1"
	got, err := resolvedURIForModel("qwen3.6-colibri?base_url=" + explicit)
	if err != nil {
		t.Fatalf("resolvedURIForModel: %v", err)
	}
	parsed, err := kitllm.ParseURI(got)
	if err != nil {
		t.Fatalf("ParseURI(%q): %v", got, err)
	}
	if parsed.Params["base_url"] != explicit {
		t.Fatalf("base_url = %q, want the explicit param %q", parsed.Params["base_url"], explicit)
	}
}

// TestResolvedURI_NoBaseURLConfigured leaves the URI untouched when
// nothing configures an endpoint, so the provider default applies.
func TestResolvedURI_NoBaseURLConfigured(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("LLM_BASE_URL", "")
	writeLLMYAML(t, "")

	got, err := resolvedURIForModel("gpt-4o")
	if err != nil {
		t.Fatalf("resolvedURIForModel: %v", err)
	}
	parsed, _ := kitllm.ParseURI(got)
	if bu := parsed.Params["base_url"]; bu != "" {
		t.Fatalf("base_url = %q, want empty when nothing is configured", bu)
	}
}
