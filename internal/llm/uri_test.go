package llm

import (
	"testing"

	kitllm "hop.top/kit/go/ai/llm"
)

// TestSchemeForModel_BareIDs pins the existing prefix-detection
// behavior so the URI handling added alongside it cannot regress the
// common case.
func TestSchemeForModel_BareIDs(t *testing.T) {
	cases := []struct {
		model      string
		wantScheme string
		wantEnv    string
	}{
		{"gpt-4o", "openai", "OPENAI_API_KEY"},
		{"o1", "openai", "OPENAI_API_KEY"},
		{"claude-3-5-sonnet-latest", "anthropic", "ANTHROPIC_API_KEY"},
		{"gemini-2.0-flash", "google", "GOOGLE_API_KEY"},
		{"llama3.2", "ollama", ""},
		{"router-mf:0.5", "routellm", ""},
		// Unknown prefix keeps assuming an OpenAI-compatible endpoint.
		{"qwen3.6-colibri", "openai", "OPENAI_API_KEY"},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			scheme, envVar := schemeForModel(tc.model)
			if scheme != tc.wantScheme || envVar != tc.wantEnv {
				t.Fatalf("schemeForModel(%q) = (%q, %q), want (%q, %q)",
					tc.model, scheme, envVar, tc.wantScheme, tc.wantEnv)
			}
		})
	}
}

// TestModelIsURI distinguishes a scheme-qualified URI from a bare model
// id. The ":" in "router-mf:0.5" is a threshold separator, not a scheme
// delimiter, so only "://" counts.
func TestModelIsURI(t *testing.T) {
	uris := []string{
		"openai://qwen3.6-colibri",
		"openai://qwen3.6-colibri?base_url=http://127.0.0.1:8800/v1",
		"ollama://llama3.2",
	}
	for _, u := range uris {
		if !modelIsURI(u) {
			t.Errorf("modelIsURI(%q) = false, want true", u)
		}
	}
	bare := []string{
		"gpt-4o",
		"router-mf:0.5",
		"qwen3.6-colibri",
		"qwen3.6-colibri?base_url=http://127.0.0.1:8800/v1",
	}
	for _, b := range bare {
		if modelIsURI(b) {
			t.Errorf("modelIsURI(%q) = true, want false", b)
		}
	}
}

// TestNewClient_URIModel_NotDoubleWrapped is the regression guard for
// the reported defect: a URI passed to -m was re-wrapped by buildClient
// into "openai://openai://<model>?api_key=...", which put the entire URI
// in the request's model field and dropped the api_key.
//
// The corrupt path returns no error — kit's Resolve accepts the mangled
// URI and only the wire shows the damage — so this asserts on the URI
// that gets built, which is what determines the request's model field.
func TestNewClient_URIModel_NotDoubleWrapped(t *testing.T) {
	t.Setenv("FOO_MODEL", "")
	t.Setenv("OPENAI_API_KEY", "test-key")

	const (
		wantModel = "qwen3.6-colibri"
		wantBase  = "http://127.0.0.1:1"
	)
	uri := "openai://" + wantModel + "?base_url=" + wantBase

	got, err := resolvedURIForModel(uri)
	if err != nil {
		t.Fatalf("resolvedURIForModel(%q): %v", uri, err)
	}

	parsed, err := kitllm.ParseURI(got)
	if err != nil {
		t.Fatalf("ParseURI(%q): %v", got, err)
	}
	if parsed.Model != wantModel {
		t.Errorf("model = %q, want %q (URI was re-wrapped: %q)",
			parsed.Model, wantModel, got)
	}
	if parsed.Scheme != "openai" {
		t.Errorf("scheme = %q, want %q", parsed.Scheme, "openai")
	}
	// The caller-supplied base_url must survive; losing it sends the
	// request to api.openai.com instead of the configured endpoint.
	if parsed.Params["base_url"] != wantBase {
		t.Errorf("base_url = %q, want %q", parsed.Params["base_url"], wantBase)
	}
}

// TestResolvedURIForModel_BareID confirms the bare-id path still
// assembles scheme, model and api_key as before.
func TestResolvedURIForModel_BareID(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")

	got, err := resolvedURIForModel("gpt-4o")
	if err != nil {
		t.Fatalf("resolvedURIForModel: %v", err)
	}
	parsed, err := kitllm.ParseURI(got)
	if err != nil {
		t.Fatalf("ParseURI(%q): %v", got, err)
	}
	if parsed.Scheme != "openai" || parsed.Model != "gpt-4o" {
		t.Errorf("got scheme=%q model=%q, want openai/gpt-4o", parsed.Scheme, parsed.Model)
	}
	if parsed.Params["api_key"] != "test-key" {
		t.Errorf("api_key = %q, want %q", parsed.Params["api_key"], "test-key")
	}
}
