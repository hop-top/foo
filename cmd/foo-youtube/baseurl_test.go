package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// The configured endpoint (llm.yaml providers.<scheme>.base_url,
// LLM_BASE_URL) must reach the provider whichever way the model is
// named: a bare id or a URI-form model. This binary runs one model and
// no fallbacks, so the model's own scheme is the primary scheme and
// LLM_BASE_URL always applies to it. Precedence, highest first:
//
//  1. ?base_url= on the model (or a host-form URI): never replaced
//  2. LLM_BASE_URL
//  3. llm.yaml providers.<scheme>.base_url
//  4. nothing: the adapter's default
//
// Every test runs under isolateLLMEnv, and TestMain sends any request
// that escapes a local stub to a closed proxy port.

// TestModelURI_ConfiguredBaseURL pins the precedence for both model
// forms on the URI handed to kit.
func TestModelURI_ConfiguredBaseURL(t *testing.T) {
	const (
		yamlURL = "http://127.0.0.1:9/yaml"
		envURL  = "http://127.0.0.1:9/env"
		callURL = "http://127.0.0.1:9/call"
	)
	yaml := "providers:\n" +
		"  openrouter:\n    base_url: " + yamlURL + "\n" +
		"  openai:\n    base_url: " + yamlURL + "\n"

	tests := []struct {
		name  string
		model string
		yaml  string
		env   string
		want  string
	}{
		{
			name:  "uri form takes llm.yaml base_url",
			model: "openrouter://openai/gpt-4.1-nano",
			yaml:  yaml,
			want:  "openrouter://openai/gpt-4.1-nano?base_url=" + yamlURL + "&api_key=or-key",
		},
		{
			name:  "uri form takes LLM_BASE_URL over llm.yaml",
			model: "openrouter://openai/gpt-4.1-nano",
			yaml:  yaml,
			env:   envURL,
			want:  "openrouter://openai/gpt-4.1-nano?base_url=" + envURL + "&api_key=or-key",
		},
		{
			name:  "uri form takes LLM_BASE_URL with no llm.yaml",
			model: "openrouter://openai/gpt-4.1-nano",
			env:   envURL,
			want:  "openrouter://openai/gpt-4.1-nano?base_url=" + envURL + "&api_key=or-key",
		},
		{
			name:  "uri form explicit base_url outranks env and file",
			model: "openrouter://openai/gpt-4.1-nano?base_url=" + callURL,
			yaml:  yaml,
			env:   envURL,
			want:  "openrouter://openai/gpt-4.1-nano?base_url=" + callURL + "&api_key=or-key",
		},
		{
			name:  "uri form ignores another scheme's llm.yaml block",
			model: "openrouter://openai/gpt-4.1-nano",
			yaml:  "providers:\n  openai:\n    base_url: " + yamlURL + "\n",
			want:  "openrouter://openai/gpt-4.1-nano?api_key=or-key",
		},
		{
			name:  "uri form with nothing configured keeps the adapter default",
			model: "openrouter://openai/gpt-4.1-nano",
			want:  "openrouter://openai/gpt-4.1-nano?api_key=or-key",
		},
		{
			name:  "host-form uri names its own endpoint",
			model: "openai://127.0.0.1:9/gpt-4o",
			yaml:  yaml,
			env:   envURL,
			want:  "openai://127.0.0.1:9/gpt-4o?api_key=sk-key",
		},
		{
			name:  "bare id takes llm.yaml base_url",
			model: "gpt-4o",
			yaml:  yaml,
			want:  "openai://gpt-4o?base_url=" + yamlURL + "&api_key=sk-key",
		},
		{
			name:  "bare id takes LLM_BASE_URL over llm.yaml",
			model: "gpt-4o",
			yaml:  yaml,
			env:   envURL,
			want:  "openai://gpt-4o?base_url=" + envURL + "&api_key=sk-key",
		},
		{
			name:  "bare id explicit base_url outranks env and file",
			model: "gpt-4o?base_url=" + callURL,
			yaml:  yaml,
			env:   envURL,
			want:  "openai://gpt-4o?base_url=" + callURL + "&api_key=sk-key",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := isolateLLMEnv(t)
			t.Setenv("OPENROUTER_API_KEY", "or-key")
			t.Setenv("OPENAI_API_KEY", "sk-key")
			if tt.yaml != "" {
				writeLLMYAML(t, cfg, tt.yaml)
			}
			if tt.env != "" {
				t.Setenv("LLM_BASE_URL", tt.env)
			}
			got, err := modelURI(context.Background(), tt.model)
			if err != nil {
				t.Fatalf("modelURI(%q): %v", tt.model, err)
			}
			if got != tt.want {
				t.Errorf("modelURI(%q)\n got %q\nwant %q", tt.model, got, tt.want)
			}
		})
	}
}

// TestModelURI_AliasConfigBlock: a provider's llm.yaml block may sit
// under any of its names. A scheme without a block of its own takes
// the block of the provider it names (gemini reads providers.google,
// fireworks-ai reads providers.fireworks), base_url and api_key alike;
// its own block wins.
func TestModelURI_AliasConfigBlock(t *testing.T) {
	const googleURL, geminiURL, fwURL = "http://127.0.0.1:9/google", "http://127.0.0.1:9/gemini", "http://127.0.0.1:9/fw"
	tests := []struct {
		name, model, yaml, want string
	}{
		{
			name:  "gemini reads providers.google",
			model: "gemini://gemini-2.0-flash",
			yaml:  "providers:\n  google:\n    base_url: " + googleURL + "\n    api_key: g-yaml\n",
			want:  "gemini://gemini-2.0-flash?base_url=" + googleURL + "&api_key=g-yaml",
		},
		{
			name:  "own block wins",
			model: "gemini://gemini-2.0-flash",
			yaml:  "providers:\n  google:\n    base_url: " + googleURL + "\n  gemini:\n    base_url: " + geminiURL + "\n    api_key: gm-yaml\n",
			want:  "gemini://gemini-2.0-flash?base_url=" + geminiURL + "&api_key=gm-yaml",
		},
		{
			name:  "fireworks-ai reads providers.fireworks",
			model: "fireworks-ai://accounts/fireworks/models/m",
			yaml:  "providers:\n  fireworks:\n    base_url: " + fwURL + "\n    api_key: fw-yaml\n",
			want:  "fireworks-ai://accounts/fireworks/models/m?base_url=" + fwURL + "&api_key=fw-yaml",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := isolateLLMEnv(t)
			writeLLMYAML(t, cfg, tt.yaml)
			got, err := modelURI(context.Background(), tt.model)
			if err != nil {
				t.Fatalf("modelURI(%q): %v", tt.model, err)
			}
			if got != tt.want {
				t.Errorf("modelURI(%q)\n got %q\nwant %q", tt.model, got, tt.want)
			}
		})
	}
}

// TestModelURI_BlankAPIKeyResolved: a blank api_key on the model is no
// key; it is dropped and the scheme's own key sent instead.
func TestModelURI_BlankAPIKeyResolved(t *testing.T) {
	isolateLLMEnv(t)
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	got, err := modelURI(context.Background(), "openrouter://openai/gpt-4.1-nano?api_key=")
	if err != nil {
		t.Fatalf("modelURI: %v", err)
	}
	if want := "openrouter://openai/gpt-4.1-nano?api_key=or-key"; got != want {
		t.Errorf("modelURI\n got %q\nwant %q", got, want)
	}
}

// TestAnswer_URIFormModelReachesLLMYamlBaseURL is the reported defect
// on the wire: a URI-form model with llm.yaml naming its scheme's
// endpoint must send the request there, not to the public endpoint.
func TestAnswer_URIFormModelReachesLLMYamlBaseURL(t *testing.T) {
	cfg := isolateLLMEnv(t)
	stub := newOpenAIStub(t, "ok")
	t.Setenv("OPENROUTER_API_KEY", "or-live-test")
	writeLLMYAML(t, cfg, "providers:\n  openrouter:\n    base_url: "+stub.srv.URL+"/v1\n")

	if _, err := answer(context.Background(), "openrouter://openai/gpt-4.1-nano", "hello"); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if n := stub.calls.Load(); n != 1 {
		t.Fatalf("configured endpoint received %d request(s), want 1", n)
	}
	if auth := stub.auth.Load(); auth == nil || *auth != "Bearer or-live-test" {
		t.Errorf("Authorization did not carry OPENROUTER_API_KEY (got %d bytes)", len(deref(auth)))
	}
}

// TestAnswer_URIFormModelReachesLLMBaseURL: LLM_BASE_URL is the same
// lever from the environment, and must steer a URI-form model too.
func TestAnswer_URIFormModelReachesLLMBaseURL(t *testing.T) {
	isolateLLMEnv(t)
	stub := newOpenAIStub(t, "ok")
	t.Setenv("OPENROUTER_API_KEY", "or-live-test")
	t.Setenv("LLM_BASE_URL", stub.srv.URL+"/v1")

	if _, err := answer(context.Background(), "openrouter://openai/gpt-4.1-nano", "hello"); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if n := stub.calls.Load(); n != 1 {
		t.Fatalf("LLM_BASE_URL endpoint received %d request(s), want 1", n)
	}
}

// TestRun_URIFormModelFromEnvReachesLLMYamlBaseURL goes through run()
// with the real answer seam: FOO_YOUTUBE_MODEL in URI form plus an
// llm.yaml endpoint answers from that endpoint.
func TestRun_URIFormModelFromEnvReachesLLMYamlBaseURL(t *testing.T) {
	withFullYTDLP(t)
	clearPromptEnv(t)
	cfg := isolateLLMEnv(t)
	stub := newOpenAIStub(t, "from the configured endpoint")
	t.Setenv("OPENROUTER_API_KEY", "or-live-test")
	t.Setenv("FOO_YOUTUBE_MODEL", "openrouter://openai/gpt-4.1-nano")
	writeLLMYAML(t, cfg, "providers:\n  openrouter:\n    base_url: "+stub.srv.URL+"/v1\n")

	var out bytes.Buffer
	err := run(promptCmd(context.Background(), &out),
		[]string{"https://youtu.be/VID123", "what did they say?"},
		runOpts{metadata: true, transcript: true})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if n := stub.calls.Load(); n != 1 {
		t.Fatalf("configured endpoint received %d request(s), want 1", n)
	}
	if !strings.Contains(out.String(), "from the configured endpoint") {
		t.Errorf("stdout = %q, want the configured endpoint's answer", out.String())
	}
}

// TestAnswer_ExplicitBaseURLOutranksLLMBaseURL: a base_url the caller
// spelled on the model is theirs; LLM_BASE_URL must not redirect it.
func TestAnswer_ExplicitBaseURLOutranksLLMBaseURL(t *testing.T) {
	isolateLLMEnv(t)
	explicit := newOpenAIStub(t, "ok")
	env := newOpenAIStub(t, "must not be reached")
	t.Setenv("OPENROUTER_API_KEY", "or-live-test")
	t.Setenv("LLM_BASE_URL", env.srv.URL+"/v1")

	model := "openrouter://openai/gpt-4.1-nano?base_url=" + explicit.srv.URL + "/v1"
	if _, err := answer(context.Background(), model, "hello"); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if n := explicit.calls.Load(); n != 1 {
		t.Errorf("explicit endpoint received %d request(s), want 1", n)
	}
	if n := env.calls.Load(); n != 0 {
		t.Errorf("LLM_BASE_URL endpoint received %d request(s), want 0", n)
	}
}
