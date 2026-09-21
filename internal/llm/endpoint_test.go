package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ollamaBody is a real /v1/models response shape, captured from a local
// ollama. Every OpenAI-compatible server answers with this envelope.
const ollamaBody = `{"object":"list","data":[` +
	`{"id":"qwen2.5:7b-instruct","object":"model","created":1787191418,"owned_by":"library"},` +
	`{"id":"llama3.2:3b","object":"model","created":1787184512,"owned_by":"library"}]}`

// serveModels stands up an httptest server answering /v1/models with the
// given body, and returns the base URL foo would be configured with
// (including the /v1 prefix, as the docs specify).
func serveModels(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1"
}

// TestEndpointCatalog_ListsServerInventory is the headline behavior: the
// rows come from the server, not from models.dev.
func TestEndpointCatalog_ListsServerInventory(t *testing.T) {
	base := serveModels(t, ollamaBody)

	got, err := NewEndpointCatalog(base, nil).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(got), got)
	}
	// Sorted by id, so the order is pinned regardless of what the
	// server returned.
	if got[0].ID != "llama3.2:3b" || got[1].ID != "qwen2.5:7b-instruct" {
		t.Errorf("ids = %q, %q; want llama3.2:3b, qwen2.5:7b-instruct", got[0].ID, got[1].ID)
	}
}

// TestEndpointCatalog_RowsAreMarkedAsEndpointSourced guards the
// provenance contract: an endpoint row must never be mistaken for a
// catalog row, because its empty metadata fields mean "not reported"
// rather than "zero".
func TestEndpointCatalog_RowsAreMarkedAsEndpointSourced(t *testing.T) {
	base := serveModels(t, ollamaBody)

	got, err := NewEndpointCatalog(base, nil).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	for _, e := range got {
		if e.Source != SourceEndpoint {
			t.Errorf("%s: Source = %q, want %q", e.ID, e.Source, SourceEndpoint)
		}
		if e.Source == SourceCatalog {
			t.Errorf("%s: endpoint row claims to be a catalog row", e.ID)
		}
		// An id that came from a server that just answered is
		// reachable by construction.
		if !e.Reachable {
			t.Errorf("%s: Reachable = false, want true for a live endpoint row", e.ID)
		}
	}
}

// TestEndpointCatalog_DoesNotInventMetadata pins the deliberate absence.
// /v1/models carries no cost, context window or capability data, and
// fabricating any of it would make the catalog-only filter rejection
// pointless.
func TestEndpointCatalog_DoesNotInventMetadata(t *testing.T) {
	base := serveModels(t, ollamaBody)

	got, err := NewEndpointCatalog(base, nil).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	for _, e := range got {
		if e.Context != 0 || e.InputCost != 0 || e.OutputCost != 0 {
			t.Errorf("%s: invented metadata: context=%d in=%v out=%v",
				e.ID, e.Context, e.InputCost, e.OutputCost)
		}
		if e.ToolCall || e.Reasoning {
			t.Errorf("%s: invented capability flags", e.ID)
		}
		if e.Released != "" {
			t.Errorf("%s: invented release date %q", e.ID, e.Released)
		}
	}
}

// TestEndpointCatalog_UnreachableIsErrorNamingURL is the failure mode
// that matters most. A down ssh tunnel is the common case; surfacing it
// as an empty list would read as "this server has no models", which is
// the opposite of the truth and sends the user debugging the wrong
// thing.
func TestEndpointCatalog_UnreachableIsErrorNamingURL(t *testing.T) {
	// A server that is immediately closed gives a guaranteed-dead
	// address without guessing at a free port.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL + "/v1"
	srv.Close()

	got, err := NewEndpointCatalog(base, nil).ListModels(context.Background())
	if err == nil {
		t.Fatalf("unreachable endpoint returned no error; got %d entries", len(got))
	}
	if got != nil {
		t.Errorf("unreachable endpoint returned %d entries alongside the error", len(got))
	}
	if !strings.Contains(err.Error(), base) {
		t.Errorf("error does not name the endpoint URL\n error: %v\n want substring: %s", err, base)
	}
	// Go's *url.Error already embeds the URL, so the assertion above
	// passes even if foo drops its own mention of it — verified by
	// mutation. Asserting foo's framing too is what actually pins the
	// requirement: the message must say which endpoint foo was
	// listing, not merely quote a transport error that happens to
	// contain a URL.
	if !strings.Contains(err.Error(), "list models from "+endpointModelsURL(base)) {
		t.Errorf("error does not attribute the failure to foo's endpoint listing\n error: %v", err)
	}
}

// TestEndpointCatalog_HTTPErrorIsErrorNamingURL covers a server that
// answers but refuses — an auth-gated gateway, or a base_url pointing at
// a path the server does not mount.
func TestEndpointCatalog_HTTPErrorIsErrorNamingURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	base := srv.URL + "/v1"

	_, err := NewEndpointCatalog(base, nil).ListModels(context.Background())
	if err == nil {
		t.Fatal("401 from endpoint returned no error")
	}
	if !strings.Contains(err.Error(), base) {
		t.Errorf("error does not name the URL: %v", err)
	}
	if !strings.Contains(err.Error(), "Unauthorized") {
		t.Errorf("error does not report the status: %v", err)
	}
}

// TestEndpointCatalog_NonJSONIsErrorNamingURL covers a base_url aimed at
// a web UI or a proxy error page: 200 with HTML.
func TestEndpointCatalog_NonJSONIsErrorNamingURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html><body>login</body></html>"))
	}))
	t.Cleanup(srv.Close)
	base := srv.URL + "/v1"

	_, err := NewEndpointCatalog(base, nil).ListModels(context.Background())
	if err == nil {
		t.Fatal("HTML body returned no error")
	}
	if !strings.Contains(err.Error(), base) {
		t.Errorf("error does not name the URL: %v", err)
	}
}

// TestEndpointCatalog_EmptyInventoryIsNotAnError is the other half of
// the unreachable contract: a server that genuinely serves nothing must
// return an empty list, not an error. Conflating the two in either
// direction loses the distinction.
func TestEndpointCatalog_EmptyInventoryIsNotAnError(t *testing.T) {
	base := serveModels(t, `{"object":"list","data":[]}`)

	got, err := NewEndpointCatalog(base, nil).ListModels(context.Background())
	if err != nil {
		t.Fatalf("empty inventory should not error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d entries, want 0", len(got))
	}
}

// TestEndpointCatalog_HonorsContextCancellation proves the probe is
// bounded by the caller's context and cannot hang the command against a
// host that accepts the connection and never answers — the half-open
// tunnel case, where there is no connection refusal to fail fast on.
func TestEndpointCatalog_HonorsContextCancellation(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-block
	}))
	t.Cleanup(func() { close(block); srv.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	t.Cleanup(cancel)

	start := time.Now()
	_, err := NewEndpointCatalog(srv.URL+"/v1", nil).ListModels(ctx)
	if err == nil {
		t.Fatal("a hanging endpoint returned no error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("probe took %v; context deadline was not honored", elapsed)
	}
}

// TestEndpointCatalog_DefaultTimeoutIsSet guards the explicit bound on
// the default client, which is the only thing protecting a caller that
// passes context.Background().
func TestEndpointCatalog_DefaultTimeoutIsSet(t *testing.T) {
	src, ok := NewEndpointCatalog("http://127.0.0.1:1/v1", nil).(endpointCatalog)
	if !ok {
		t.Fatal("NewEndpointCatalog did not return an endpointCatalog")
	}
	if src.client.Timeout <= 0 {
		t.Error("default client has no timeout; a hung endpoint would block forever")
	}
}

// TestEndpointModelsURL covers base-URL joining, including the doubled
// path the local-endpoint how-to calls out as a common misconfiguration.
func TestEndpointModelsURL(t *testing.T) {
	cases := map[string]string{
		"http://h:1/v1":         "http://h:1/v1/models",
		"http://h:1/v1/":        "http://h:1/v1/models",
		"http://h:1/v1/models":  "http://h:1/v1/models",
		"  http://h:1/v1  ":     "http://h:1/v1/models",
		"http://h:1/openai/v1/": "http://h:1/openai/v1/models",
	}
	for in, want := range cases {
		if got := endpointModelsURL(in); got != want {
			t.Errorf("endpointModelsURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestEndpointProvider_FallsBackToHost pins the labelling rule: ollama
// reports "library" for every model, which tells a reader nothing, so
// the host — the fact that distinguishes two local servers in one
// listing — is used instead.
func TestEndpointProvider_FallsBackToHost(t *testing.T) {
	cases := []struct {
		ownedBy string
		base    string
		want    string
	}{
		{"library", "http://127.0.0.1:11434/v1", "127.0.0.1:11434"},
		{"", "http://127.0.0.1:8800/v1", "127.0.0.1:8800"},
		{"colibri", "http://127.0.0.1:8800/v1", "colibri"},
		{"library", "", "endpoint"},
	}
	for _, c := range cases {
		got := endpointProvider(endpointModel{OwnedBy: c.ownedBy}, c.base)
		if got != c.want {
			t.Errorf("endpointProvider(owned_by=%q, base=%q) = %q, want %q",
				c.ownedBy, c.base, got, c.want)
		}
	}
}

// TestEndpointCatalog_SkipsIdlessEntries guards against a row with no id
// reaching the list, where it would render as a blank line the user
// cannot pass to `foo model default`.
func TestEndpointCatalog_SkipsIdlessEntries(t *testing.T) {
	base := serveModels(t, `{"data":[{"id":""},{"id":"real"}]}`)

	got, err := NewEndpointCatalog(base, nil).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(got) != 1 || got[0].ID != "real" {
		t.Errorf("got %+v, want only the id-bearing row", got)
	}
}

// writeEndpointYAML points XDG_CONFIG_HOME at a temp hop/llm.yaml.
func writeEndpointYAML(t *testing.T, body string) {
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

// TestResolveConfiguredEndpoint_FromConfigFile covers the lowest rung of
// the documented ladder.
func TestResolveConfiguredEndpoint_FromConfigFile(t *testing.T) {
	t.Setenv("LLM_BASE_URL", "")
	writeEndpointYAML(t, `
providers:
  openai:
    base_url: http://127.0.0.1:9001/v1
`)
	if got := ResolveConfiguredEndpoint(); got != "http://127.0.0.1:9001/v1" {
		t.Errorf("ResolveConfiguredEndpoint() = %q, want the llm.yaml value", got)
	}
}

// TestResolveConfiguredEndpoint_EnvBeatsFile pins the precedence foo
// documents, and proves it is kit's ladder rather than a second
// implementation that could drift from the completion path.
func TestResolveConfiguredEndpoint_EnvBeatsFile(t *testing.T) {
	writeEndpointYAML(t, `
providers:
  openai:
    base_url: http://127.0.0.1:9001/v1
`)
	t.Setenv("LLM_BASE_URL", "http://127.0.0.1:9002/v1")
	if got := ResolveConfiguredEndpoint(); got != "http://127.0.0.1:9002/v1" {
		t.Errorf("ResolveConfiguredEndpoint() = %q, want LLM_BASE_URL to win", got)
	}
}

// TestResolveConfiguredEndpoint_UnsetIsEmpty is what makes the catalog
// the fallback: no configured endpoint must be an empty string, not an
// error, so `foo model list` keeps working with no llm.yaml at all.
func TestResolveConfiguredEndpoint_UnsetIsEmpty(t *testing.T) {
	t.Setenv("LLM_BASE_URL", "")
	writeEndpointYAML(t, "")
	if got := ResolveConfiguredEndpoint(); got != "" {
		t.Errorf("ResolveConfiguredEndpoint() = %q, want empty", got)
	}
}

// TestCatalogOnlyFlagError_NamesTheFlag pins the message contract: the
// error has to name the offending flag, because "unsupported
// combination" leaves the user guessing which of several flags to drop.
func TestCatalogOnlyFlagError_NamesTheFlag(t *testing.T) {
	err := &CatalogOnlyFlagError{Flag: "min-context"}
	if !strings.Contains(err.Error(), "--min-context") {
		t.Errorf("error does not name the flag: %v", err)
	}
	if !errors.Is(err, ErrEndpointFlagUnsupported) {
		t.Error("CatalogOnlyFlagError does not unwrap to ErrEndpointFlagUnsupported")
	}
}

// TestEndpointSource_IsDistinctFromCatalog is a guard against someone
// collapsing the two constants later: the whole provenance contract
// rests on them differing.
func TestEndpointSource_IsDistinctFromCatalog(t *testing.T) {
	if SourceEndpoint == SourceCatalog {
		t.Fatal("SourceEndpoint and SourceCatalog are the same value")
	}
}
