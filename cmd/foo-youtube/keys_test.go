package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	kitllm "hop.top/kit/go/ai/llm"
	"hop.top/kit/go/console/output"
)

// Key resolution is kit's (llm.ApplyAPIKey); these tests pin what the
// sidecar hands it and how it words kit's answer. Every test runs under
// isolateLLMEnv: throwaway HOME/XDG, no provider variables, and no
// request leaves the machine (base_url points at a local httptest
// server).

// wantUnauthorized asserts err is the sidecar's missing-key envelope:
// exit 4, CodeUnauthorized, naming envVar and the FOO_YOUTUBE_MODEL
// escape hatch.
func wantUnauthorized(t *testing.T, err error, envVar string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a missing-key error")
	}
	var ee *exitError
	if !asExitError(err, &ee) {
		t.Fatalf("error %T (%v) is not an *exitError; exit code would be wrong", err, err)
	}
	if ee.cli.ExitCode != exitUnauthorized {
		t.Errorf("exit code = %d, want %d", ee.cli.ExitCode, exitUnauthorized)
	}
	if got := exitCodeFor(err); got != exitUnauthorized {
		t.Errorf("process exit code = %d, want %d", got, exitUnauthorized)
	}
	if ee.cli.Code != output.CodeUnauthorized {
		t.Errorf("error code = %q, want %q", ee.cli.Code, output.CodeUnauthorized)
	}
	if !strings.Contains(ee.cli.Message, "export "+envVar+"=") {
		t.Errorf("message must name %s to export, got %q", envVar, ee.cli.Message)
	}
	if !strings.Contains(ee.cli.Message, "FOO_YOUTUBE_MODEL") {
		t.Errorf("message must keep the FOO_YOUTUBE_MODEL hint, got %q", ee.cli.Message)
	}
}

// TestModelURI_URIFormGetsSchemeKey is the reported defect: a URI-form
// model was handed through with no key, so the provider answered 401.
// Each scheme takes its own variable, an alias takes its provider's.
func TestModelURI_URIFormGetsSchemeKey(t *testing.T) {
	tests := []struct {
		model, envVar, want string
	}{
		{"openrouter://openai/gpt-4.1-nano", "OPENROUTER_API_KEY", "openrouter://openai/gpt-4.1-nano?api_key=fake-key"},
		{"groq://llama-3.3-70b-versatile", "GROQ_API_KEY", "groq://llama-3.3-70b-versatile?api_key=fake-key"},
		{"anthropic://claude-3-5-haiku-latest", "ANTHROPIC_API_KEY", "anthropic://claude-3-5-haiku-latest?api_key=fake-key"},
		// aim's curated alias reaches the fireworks adapter and its key.
		{"fireworks-ai://accounts/fireworks/models/x", "FIREWORKS_API_KEY", "fireworks-ai://accounts/fireworks/models/x?api_key=fake-key"},
		// A base_url param already on the URI keeps its place.
		{"openrouter://m?base_url=http://127.0.0.1:9/v1", "OPENROUTER_API_KEY", "openrouter://m?base_url=http://127.0.0.1:9/v1&api_key=fake-key"},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			isolateLLMEnv(t)
			t.Setenv(tt.envVar, "fake-key")

			got, err := modelURI(context.Background(), tt.model)
			if err != nil {
				t.Fatalf("modelURI: %v", err)
			}
			if got != tt.want {
				t.Errorf("modelURI(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

// TestModelURI_URIFormNeverBorrowsOpenAIKey: an OpenRouter model must
// not be sent the user's OpenAI key; with only OPENAI_API_KEY set it is
// a missing OPENROUTER_API_KEY.
func TestModelURI_URIFormNeverBorrowsOpenAIKey(t *testing.T) {
	isolateLLMEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-openai-secret")

	got, err := modelURI(context.Background(), "openrouter://openai/gpt-4.1-nano")
	wantUnauthorized(t, err, "OPENROUTER_API_KEY")
	assertNoSecret(t, got, "sk-openai-secret")
}

// TestModelURI_MissingKeyNamesFirstVariable: the hint names the
// highest-precedence variable kit consulted, for URI-form and bare ids
// alike (google reads GOOGLE_API_KEY before GEMINI_API_KEY).
func TestModelURI_MissingKeyNamesFirstVariable(t *testing.T) {
	tests := []struct{ model, envVar string }{
		{"openrouter://openai/gpt-4.1-nano", "OPENROUTER_API_KEY"},
		{"gpt-4o", "OPENAI_API_KEY"},
		{"qwen3-coder", "OPENAI_API_KEY"},
		{"claude-3-5-sonnet-latest", "ANTHROPIC_API_KEY"},
		{"gemini-2.0-flash", "GOOGLE_API_KEY"},
		{"together://meta-llama/x", "TOGETHER_API_KEY"},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			isolateLLMEnv(t)
			_, err := modelURI(context.Background(), tt.model)
			wantUnauthorized(t, err, tt.envVar)
		})
	}
}

// TestModelURI_LLMAPIKeyFallback: the universal LLM_API_KEY answers for
// any provider that requires a key and has none of its own — and is
// never lent to a local runtime.
func TestModelURI_LLMAPIKeyFallback(t *testing.T) {
	isolateLLMEnv(t)
	t.Setenv("LLM_API_KEY", "universal-key")

	for model, want := range map[string]string{
		"gpt-4o":                           "openai://gpt-4o?api_key=universal-key",
		"qwen3-coder":                      "openai://qwen3-coder?api_key=universal-key",
		"openrouter://openai/gpt-4.1-nano": "openrouter://openai/gpt-4.1-nano?api_key=universal-key",
		"llama3":                           "ollama://llama3",
		"router-mf:0.5":                    "routellm://mf:0.5",
	} {
		got, err := modelURI(context.Background(), model)
		if err != nil {
			t.Errorf("modelURI(%q): %v", model, err)
			continue
		}
		if got != want {
			t.Errorf("modelURI(%q) = %q, want %q", model, got, want)
		}
	}

	// A provider's own variable outranks the universal one.
	t.Setenv("OPENROUTER_API_KEY", "own-key")
	got, err := modelURI(context.Background(), "openrouter://m")
	if err != nil {
		t.Fatalf("modelURI: %v", err)
	}
	if got != "openrouter://m?api_key=own-key" {
		t.Errorf("modelURI = %q; OPENROUTER_API_KEY must outrank LLM_API_KEY", got)
	}
}

// TestModelURI_LLMYamlKey: llm.yaml's providers.<scheme>.api_key_env
// and api_key outrank the scheme's own variable.
func TestModelURI_LLMYamlKey(t *testing.T) {
	cfg := isolateLLMEnv(t)
	writeLLMYAML(t, cfg, "providers:\n  openrouter:\n    api_key_env: MY_OR_KEY\n  anthropic:\n    api_key: yaml-literal\n")
	t.Setenv("MY_OR_KEY", "yaml-env-key")
	t.Setenv("OPENROUTER_API_KEY", "own-key")
	t.Setenv("ANTHROPIC_API_KEY", "own-key")

	for model, want := range map[string]string{
		"openrouter://openai/gpt-4.1-nano": "openrouter://openai/gpt-4.1-nano?api_key=yaml-env-key",
		"claude-3-5-sonnet-latest":         "anthropic://claude-3-5-sonnet-latest?api_key=yaml-literal",
	} {
		got, err := modelURI(context.Background(), model)
		if err != nil {
			t.Errorf("modelURI(%q): %v", model, err)
			continue
		}
		if got != want {
			t.Errorf("modelURI(%q) = %q, want %q", model, got, want)
		}
	}
}

// TestModelURI_ExplicitKeyOutranks: an api_key the user put on the URI
// is theirs; nothing replaces or duplicates it.
func TestModelURI_ExplicitKeyOutranks(t *testing.T) {
	isolateLLMEnv(t)
	t.Setenv("OPENROUTER_API_KEY", "own-key")
	t.Setenv("LLM_API_KEY", "universal-key")

	const uri = "openrouter://m?api_key=given"
	got, err := modelURI(context.Background(), uri)
	if err != nil {
		t.Fatalf("modelURI: %v", err)
	}
	if got != uri {
		t.Errorf("modelURI = %q, want %q unchanged", got, uri)
	}
}

// TestModelURI_CatalogProviderKey: a provider known only from the aim
// catalog (no adapter registers it by name) takes the catalog's key
// variable, read from the on-disk cache kit consults.
func TestModelURI_CatalogProviderKey(t *testing.T) {
	isolateLLMEnv(t)
	seedAimCatalog(t, `{"digitalocean":{"id":"digitalocean","name":"DigitalOcean",`+
		`"npm":"@ai-sdk/openai-compatible","api":"https://inference.do-ai.run/v1",`+
		`"env":["DIGITALOCEAN_ACCESS_TOKEN"],"models":{}}}`)

	_, err := modelURI(context.Background(), "digitalocean://llama3.3-70b-instruct")
	wantUnauthorized(t, err, "DIGITALOCEAN_ACCESS_TOKEN")

	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "fake-do")
	got, err := modelURI(context.Background(), "digitalocean://llama3.3-70b-instruct")
	if err != nil {
		t.Fatalf("modelURI: %v", err)
	}
	if got != "digitalocean://llama3.3-70b-instruct?api_key=fake-do" {
		t.Errorf("modelURI = %q", got)
	}
}

// TestModelURI_OtherErrorsPassThrough: a failure that is not a missing
// key keeps kit's own error (not exit 4), and names no key value.
func TestModelURI_OtherErrorsPassThrough(t *testing.T) {
	isolateLLMEnv(t)
	// A key a provider URI cannot carry intact.
	t.Setenv("OPENROUTER_API_KEY", "sk&secret-part")

	_, err := modelURI(context.Background(), "openrouter://m")
	if err == nil {
		t.Fatal("expected kit's refusal of an unencodable key")
	}
	var ee *exitError
	if asExitError(err, &ee) {
		t.Errorf("a non-missing-key error must pass through, got envelope exit %d: %v", ee.cli.ExitCode, err)
	}
	if errors.Is(err, kitllm.ErrMissingKey) {
		t.Errorf("error must not read as a missing key: %v", err)
	}
	assertNoSecret(t, err.Error(), "sk&secret-part", "secret-part")
}

// TestAnswer_URIFormModelSendsKey drives production answer() end to end
// with a URI-form model: the scheme's key must reach the provider in the
// Authorization header.
func TestAnswer_URIFormModelSendsKey(t *testing.T) {
	isolateLLMEnv(t)
	stub := newOpenAIStub(t, "ok")
	t.Setenv("OPENROUTER_API_KEY", "or-live-test")
	t.Setenv("OPENAI_API_KEY", "sk-openai-must-not-leak")

	model := "openrouter://openai/gpt-4.1-nano?base_url=" + stub.srv.URL + "/v1"
	if _, err := answer(context.Background(), model, "hello"); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if n := stub.calls.Load(); n != 1 {
		t.Fatalf("stub received %d request(s), want 1", n)
	}
	auth := stub.auth.Load()
	if auth == nil || *auth != "Bearer or-live-test" {
		t.Errorf("Authorization did not carry OPENROUTER_API_KEY (got %d bytes)", len(deref(auth)))
	}
	if !strings.Contains(stub.lastBody(), `"openai/gpt-4.1-nano"`) {
		t.Errorf("model id lost on the wire: %s", stub.lastBody())
	}
}

// TestRun_MissingKeyExitsUnauthorizedWithoutRequest goes through run()
// with the real answer seam: a URI-form model with no key must stop with
// exit 4 before any request is made, and print nothing.
func TestRun_MissingKeyExitsUnauthorizedWithoutRequest(t *testing.T) {
	withFullYTDLP(t)
	clearPromptEnv(t)
	isolateLLMEnv(t)
	stub := newOpenAIStub(t, "must not be reached")
	t.Setenv("FOO_YOUTUBE_MODEL", "openrouter://openai/gpt-4.1-nano?base_url="+stub.srv.URL+"/v1")

	var out bytes.Buffer
	err := run(promptCmd(context.Background(), &out),
		[]string{"https://youtu.be/VID123", "what did they say?"},
		runOpts{metadata: true, transcript: true})

	wantUnauthorized(t, err, "OPENROUTER_API_KEY")
	if n := stub.calls.Load(); n != 0 {
		t.Errorf("stub received %d request(s); a missing key must fail before the request", n)
	}
	if out.Len() != 0 {
		t.Errorf("nothing may be printed on a failed prompt, got %q", out.String())
	}
}

// TestNoFooInternalImports guards the documented invariant: this
// sidecar imports zero foo internal packages, so it ships and versions
// independently of the host. Provider key logic belongs in kit.
func TestNoFooInternalImports(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	cmd := exec.Command(goBin, "list", "-buildvcs=false", "-deps", "-f", "{{.ImportPath}}", ".")
	// The package-wide throwaway HOME would send go to an empty module
	// cache; list with the caller's environment, offline.
	cmd.Env = append(append([]string{}, testCallerEnv...), "GOPROXY=off")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, stderr.String())
	}
	var deps int
	for _, pkg := range strings.Fields(string(out)) {
		deps++
		if pkg == "hop.top/foo/internal" || strings.HasPrefix(pkg, "hop.top/foo/internal/") {
			t.Errorf("foo-youtube imports %s; it must import zero foo internal packages", pkg)
		}
	}
	if deps == 0 {
		t.Fatal("go list -deps listed nothing; the guard would pass vacuously")
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// writeLLMYAML writes kit's llm.yaml under xdgConfig.
func writeLLMYAML(t *testing.T, xdgConfig, body string) {
	t.Helper()
	dir := filepath.Join(xdgConfig, "hop")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "llm.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// seedAimCatalog writes payload as aim's cached models.dev catalog in
// the test's XDG cache, where kit's default registry reads it.
func seedAimCatalog(t *testing.T, payload string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("XDG_CACHE_HOME"), "hop", "aim")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models-dev.json"), []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
}
