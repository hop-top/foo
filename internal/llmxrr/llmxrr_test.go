package llmxrr

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	xrr "hop.top/xrr"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A recording made under one root replays under another: the cassette
// holds the placeholder, and the reply gets the current root back.
func TestTransport_SubstAcrossRoots(t *testing.T) {
	dir := t.TempDir()
	live := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"path":"/tmp/rec-root/a"}`))}, nil
	})
	send := func(mode xrr.Mode, root string) (string, *Transport) {
		tr := &Transport{
			Session: xrr.NewSession(mode, xrr.NewFileCassette(dir)),
			Next:    live,
			Subst:   []Subst{{Value: root, Placeholder: "{{root}}"}},
		}
		body := `{"messages":[{"role":"user","content":"ls ` + root + `/a"}]}`
		req, _ := http.NewRequest(http.MethodPost, "https://api.example.com/v1/chat/completions?key=SECRET", strings.NewReader(body))
		resp, err := tr.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := io.ReadAll(resp.Body)
		return string(out), tr
	}

	if got, _ := send(xrr.ModeRecord, "/tmp/rec-root"); got != `{"path":"/tmp/rec-root/a"}` {
		t.Fatalf("record reply = %s", got)
	}
	got, tr := send(xrr.ModeReplay, "/var/other")
	if got != `{"path":"/var/other/a"}` {
		t.Fatalf("replay reply = %s, want the current root", got)
	}
	if tr.Live() != 0 || tr.Seen() != 1 {
		t.Fatalf("replay live=%d seen=%d", tr.Live(), tr.Seen())
	}
	if err := CheckNoSecrets(dir, "SECRET-NOT-THERE"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(mustGlob(t, dir, "*.req.yaml"))
	for _, bad := range []string{"/tmp/rec-root", "SECRET", "api.example.com"} {
		if strings.Contains(string(data), bad) {
			t.Errorf("cassette holds %q:\n%s", bad, data)
		}
	}
}

func TestTransport_RefusesNonModelCalls(t *testing.T) {
	tr := &Transport{Session: xrr.NewSession(xrr.ModeReplay, xrr.NewFileCassette(t.TempDir()))}
	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/repos/x/y/releases/latest", nil)
	if _, err := tr.RoundTrip(req); err == nil || !strings.Contains(err.Error(), "only model-provider calls") {
		t.Fatalf("err = %v", err)
	}
	if tr.Seen() != 0 {
		t.Fatal("refused call counted as a model request")
	}
}

func TestTransport_ElideToolResultsKeepsLinkage(t *testing.T) {
	tr := &Transport{ElideToolResults: true}
	got := tr.canonical([]byte(`{"messages":[{"role":"tool","tool_call_id":"c1","content":"platform output"}]}`))
	if strings.Contains(got, "platform output") || !strings.Contains(got, `"tool_call_id": "c1"`) || !strings.Contains(got, ElidedToolResult) {
		t.Fatalf("canonical = %s", got)
	}
}

func TestRecordedURL_DropsHostAndKey(t *testing.T) {
	u, _ := url.Parse("https://generativelanguage.googleapis.com/v1beta/models/m:generateContent?key=AIzaXXX&alt=sse")
	if got := RecordedURL(u); got != "/v1beta/models/m:generateContent?alt=sse" {
		t.Fatalf("RecordedURL = %s", got)
	}
}

func TestCheckNoSecrets(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.yaml"), []byte("url: /x?key=abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckNoSecrets(dir); err == nil {
		t.Fatal("key= param not flagged")
	}
	if err := os.WriteFile(filepath.Join(dir, "a.yaml"), []byte("body: my-recording-value-123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckNoSecrets(dir, "my-recording-value-123"); err == nil {
		t.Fatal("recording key value not flagged")
	}
}

func mustGlob(t *testing.T, dir, pattern string) string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, pattern))
	if len(m) != 1 {
		t.Fatalf("glob %s: %v", pattern, m)
	}
	return m[0]
}
