package embed

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hop.top/foo/internal/llm"
	"hop.top/kit/go/console/output"
	"hop.top/kit/go/core/netpolicy"
)

// isolateEndpoint is isolateKeys plus an empty LLM_BASE_URL and an
// OpenAI key, so only what a test sets decides the endpoint.
func isolateEndpoint(t *testing.T) {
	t.Helper()
	isolateKeys(t)
	t.Setenv("LLM_BASE_URL", "")
	_ = os.Unsetenv("LLM_BASE_URL")
	t.Setenv("OPENAI_API_KEY", "sk-endpoint-fake")
}

// writeLLMYAML writes hop/llm.yaml under the XDG_CONFIG_HOME
// isolateKeys set, the file kit and foo read providers.* from.
func writeLLMYAML(t *testing.T, body string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "hop")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "llm.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// recorder is a local OpenAI-compatible embeddings server that records
// the path of every request it serves.
type recorder struct {
	srv   *httptest.Server
	paths []string
}

func newRecorder(t *testing.T) *recorder {
	t.Helper()
	r := &recorder{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.paths = append(r.paths, req.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{0.1, 0.2}, "index": 0}},
		})
	}))
	t.Cleanup(r.srv.Close)
	return r
}

// embedOnce builds an embedder from the environment as it stands and
// embeds one text through it, without touching the url seam.
func embedOnce(t *testing.T) {
	t.Helper()
	e, err := NewOpenAIEmbedder()
	if err != nil {
		t.Fatalf("new embedder: %v", err)
	}
	if _, err := e.Embed(context.Background(), []string{"hello"}); err != nil {
		t.Fatalf("embed: %v", err)
	}
}

// TestOpenAIEmbedder_DefaultEndpoint: with nothing configured the
// embedder targets OpenAI's public embeddings endpoint.
func TestOpenAIEmbedder_DefaultEndpoint(t *testing.T) {
	isolateEndpoint(t)
	e, err := NewOpenAIEmbedder()
	if err != nil {
		t.Fatal(err)
	}
	if e.url != "https://api.openai.com/v1/embeddings" {
		t.Errorf("url = %q, want OpenAI's embeddings endpoint", e.url)
	}
}

// TestOpenAIEmbedder_FileBaseURL is the defect: llm.yaml
// providers.openai.base_url reached every run but never the embedder,
// which posted to api.openai.com regardless.
func TestOpenAIEmbedder_FileBaseURL(t *testing.T) {
	isolateEndpoint(t)
	rec := newRecorder(t)
	writeLLMYAML(t, "providers:\n  openai:\n    base_url: "+rec.srv.URL+"/v1\n")

	embedOnce(t)
	if len(rec.paths) != 1 || rec.paths[0] != "/v1/embeddings" {
		t.Errorf("paths = %v, want one POST to /v1/embeddings under the configured base", rec.paths)
	}
}

// TestOpenAIEmbedder_EnvBaseURL: LLM_BASE_URL reaches the embedder and
// outranks the file, as it does for an openai run.
func TestOpenAIEmbedder_EnvBaseURL(t *testing.T) {
	isolateEndpoint(t)
	rec := newRecorder(t)
	writeLLMYAML(t, "providers:\n  openai:\n    base_url: http://127.0.0.1:9/v1\n")
	t.Setenv("LLM_BASE_URL", rec.srv.URL+"/v1/")

	embedOnce(t)
	if len(rec.paths) != 1 || rec.paths[0] != "/v1/embeddings" {
		t.Errorf("paths = %v, want one POST to /v1/embeddings under LLM_BASE_URL", rec.paths)
	}
}

// TestOpenAIEmbedder_OtherSchemeBaseURLIgnored: another provider's
// block is not the embedder's endpoint.
func TestOpenAIEmbedder_OtherSchemeBaseURLIgnored(t *testing.T) {
	isolateEndpoint(t)
	writeLLMYAML(t, "providers:\n  anthropic:\n    base_url: http://127.0.0.1:9/v1\n")
	e, err := NewOpenAIEmbedder()
	if err != nil {
		t.Fatal(err)
	}
	if e.url != "https://api.openai.com/v1/embeddings" {
		t.Errorf("url = %q, want OpenAI's endpoint: anthropic's base_url is not openai's", e.url)
	}
}

// TestOpenAIEmbedder_OfflineRefusal: under --offline a remote
// embeddings endpoint is refused with the OFFLINE envelope a run gets,
// not a bare transport error, and nothing is sent.
func TestOpenAIEmbedder_OfflineRefusal(t *testing.T) {
	isolateEndpoint(t)
	e, err := NewOpenAIEmbedder()
	if err != nil {
		t.Fatal(err)
	}
	e.client = &http.Client{Transport: netpolicy.Guard(http.DefaultTransport)}
	ctx := netpolicy.WithOffline(context.Background(), true)

	_, err = e.Embed(ctx, []string{"hello"})
	var ce *output.Error
	if !errors.As(err, &ce) || ce.Code != llm.CodeOffline {
		t.Fatalf("err = %v, want the OFFLINE refusal", err)
	}
	if !strings.Contains(ce.Message, "api.openai.com") {
		t.Errorf("message = %q, want the refused host named", ce.Message)
	}
}
