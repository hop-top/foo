package llmxrr

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	xrr "hop.top/xrr"
	xhttp "hop.top/xrr/adapters/http"
)

const chatPath = "/v1/chat/completions"

// provider is a local stand-in for a model provider: it answers every
// request with status and body, and counts the requests it saw.
type provider struct {
	*httptest.Server
	hits atomic.Int64
}

func newProvider(t *testing.T, status int, body string) *provider {
	t.Helper()
	p := &provider{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.hits.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(p.Close)
	return p
}

// closedURL is the base URL of a port nothing listens on, so a request
// to it fails in the transport, before any HTTP status exists.
func closedURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return "http://" + addr
}

// recorder is a record-mode transport the way the e2e seam builds it:
// replay first, record only the misses.
func recorder(dir string, onEx func(Exchange)) *Transport {
	return &Transport{
		Session:    xrr.NewSession(xrr.ModeRecord, xrr.NewFileCassette(dir)),
		Replay:     xrr.NewSession(xrr.ModeReplay, xrr.NewFileCassette(dir)),
		Next:       http.DefaultTransport,
		OnExchange: onEx,
	}
}

func replayer(dir string) *Transport {
	return &Transport{Session: xrr.NewSession(xrr.ModeReplay, xrr.NewFileCassette(dir))}
}

func post(t *testing.T, tr *Transport, base, body string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+chatPath+"?key=AIzaSECRETSECRETSECRETSECRET", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return tr.RoundTrip(req)
}

func files(t *testing.T, dir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func readAll(t *testing.T, r *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRecord_PersistsSuccess(t *testing.T) {
	dir := t.TempDir()
	p := newProvider(t, http.StatusOK, `{"ok":true}`)

	resp, err := post(t, recorder(dir, nil), p.URL, `{"q":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, resp); resp.StatusCode != 200 || got != `{"ok":true}` {
		t.Fatalf("record reply = %d %s", resp.StatusCode, got)
	}
	if n := len(files(t, dir)); n != 2 {
		t.Fatalf("cassette files = %d, want 2 (req + resp)", n)
	}

	resp, err = post(t, replayer(dir), closedURL(t), `{"q":1}`)
	if err != nil {
		t.Fatalf("replay of a recorded success: %v", err)
	}
	if got := readAll(t, resp); got != `{"ok":true}` {
		t.Fatalf("replay reply = %s", got)
	}
}

// A failed exchange is never a recording: record mode refuses it
// loudly, names the status and path, keeps secrets out of the message,
// and writes nothing.
func TestRecord_RefusesFailures(t *testing.T) {
	const secretBody = `{"error":{"type":"invalid_request_error","message":"Incorrect API key provided: sk-proj-ABCDEFGHIJKLMNOP"}}`
	cases := []struct {
		name string
		base func(t *testing.T) (url string, hits func() int64)
		want []string
	}{
		{"400", func(t *testing.T) (string, func() int64) {
			p := newProvider(t, http.StatusBadRequest, secretBody)
			return p.URL, p.hits.Load
		}, []string{"400", chatPath, "invalid_request_error"}},
		{"500", func(t *testing.T) (string, func() int64) {
			p := newProvider(t, http.StatusInternalServerError, `{"error":"boom"}`)
			return p.URL, p.hits.Load
		}, []string{"500", chatPath}},
		{"transport error", func(t *testing.T) (string, func() int64) {
			return closedURL(t), func() int64 { return 1 }
		}, []string{chatPath, "refused"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			base, hits := c.base(t)
			var exs []Exchange
			tr := recorder(dir, func(ex Exchange) { exs = append(exs, ex) })

			resp, err := post(t, tr, base, `{"q":1}`)
			if err == nil {
				t.Fatalf("recording a failure succeeded: status %d", resp.StatusCode)
			}
			if !errors.Is(err, ErrRefused) {
				t.Errorf("err = %v, want ErrRefused", err)
			}
			msg := err.Error()
			for _, w := range append(c.want, "refusing to record") {
				if !strings.Contains(msg, w) {
					t.Errorf("error %q does not name %q", msg, w)
				}
			}
			for _, bad := range []string{"AIzaSECRET", "sk-proj-ABCDEFGH", "key="} {
				if strings.Contains(msg, bad) {
					t.Errorf("error %q leaks %q", msg, bad)
				}
			}
			if got := files(t, dir); len(got) != 0 {
				t.Fatalf("failure was written to the cassette: %v", got)
			}
			if hits() != 1 {
				t.Errorf("provider hits = %d, want 1", hits())
			}
			if tr.Refused() != 1 || len(exs) != 1 || !exs[0].Refused || exs[0].Error == "" {
				t.Errorf("refused = %d, exchanges = %+v", tr.Refused(), exs)
			}
		})
	}
}

// After one refusal the recording is void: further misses fail with
// the same error and never reach the provider, so an SDK retrying the
// refused request does not spend live calls.
func TestRecord_RefusalStopsLiveCalls(t *testing.T) {
	dir := t.TempDir()
	p := newProvider(t, http.StatusBadRequest, `{"error":"bad"}`)
	tr := recorder(dir, nil)

	_, first := post(t, tr, p.URL, `{"q":1}`)
	_, again := post(t, tr, p.URL, `{"q":1}`)
	_, other := post(t, tr, p.URL, `{"q":2}`)
	if first == nil || again == nil || other == nil {
		t.Fatalf("errs = %v / %v / %v", first, again, other)
	}
	if again.Error() != first.Error() || !errors.Is(other, ErrRefused) {
		t.Errorf("later errors = %q / %q, want the first refusal %q", again, other, first)
	}
	if p.hits.Load() != 1 {
		t.Fatalf("provider hits = %d, want 1", p.hits.Load())
	}
}

// Passthrough never writes a cassette, so it hands a failure back as is.
func TestPassthrough_KeepsFailures(t *testing.T) {
	p := newProvider(t, http.StatusTooManyRequests, `{"error":"slow down"}`)
	tr := &Transport{Session: xrr.NewSession(xrr.ModePassthrough, nil), Next: http.DefaultTransport}
	resp, err := post(t, tr, p.URL, `{"q":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

// storeFailure writes a failed exchange the way xrr's record mode did
// before llmxrr refused them: a non-2xx payload, or an error envelope.
func storeFailure(t *testing.T, dir, body string, status int, recErr error) {
	t.Helper()
	tr := &Transport{}
	req := &xhttp.Request{Method: http.MethodPost, URL: chatPath, Body: tr.canonical([]byte(body))}
	sess := xrr.NewSession(xrr.ModeRecord, xrr.NewFileCassette(dir))
	_, _ = sess.Record(t.Context(), xhttp.NewAdapter(), req, func() (xrr.Response, error) {
		if recErr != nil {
			return nil, recErr
		}
		return &xhttp.Response{Status: status, Body: `{"error":"stored"}`}, nil
	})
	if len(files(t, dir)) != 2 {
		t.Fatalf("stored failure not on disk: %v", files(t, dir))
	}
}

var storedFailures = []struct {
	name   string
	status int
	err    error
}{
	{"status 400", http.StatusBadRequest, nil},
	{"transport error", 0, errors.New(`Post "http://x/v1/chat/completions": unsupported protocol scheme ""`)},
}

// -update heals a cassette that holds a failure: the stored failure
// counts as missing, is sent again, and the success replaces it.
func TestRecord_ReRecordsStoredFailure(t *testing.T) {
	for _, sf := range storedFailures {
		t.Run(sf.name, func(t *testing.T) {
			dir := t.TempDir()
			storeFailure(t, dir, `{"q":1}`, sf.status, sf.err)
			p := newProvider(t, http.StatusOK, `{"ok":"healed"}`)

			resp, err := post(t, recorder(dir, nil), p.URL, `{"q":1}`)
			if err != nil {
				t.Fatalf("re-record: %v", err)
			}
			if got := readAll(t, resp); got != `{"ok":"healed"}` {
				t.Fatalf("re-record reply = %s", got)
			}
			if p.hits.Load() != 1 {
				t.Fatalf("provider hits = %d, want 1: the stored failure was replayed", p.hits.Load())
			}
			resp, err = post(t, replayer(dir), closedURL(t), `{"q":1}`)
			if err != nil {
				t.Fatalf("replay after re-record: %v", err)
			}
			if got := readAll(t, resp); got != `{"ok":"healed"}` {
				t.Fatalf("replay after re-record = %s", got)
			}
			for _, f := range files(t, dir) {
				data, _ := os.ReadFile(f)
				if strings.Contains(string(data), "stored") || strings.Contains(string(data), "\nerror:") {
					t.Errorf("%s still holds the failure:\n%s", f, data)
				}
			}
		})
	}
}

// A stored failure that cannot be re-recorded stays refused: it is not
// replayed to foo as if the provider had said it.
func TestRecord_StoredFailureStillFailingIsRefused(t *testing.T) {
	dir := t.TempDir()
	storeFailure(t, dir, `{"q":1}`, http.StatusBadRequest, nil)
	p := newProvider(t, http.StatusBadRequest, `{"error":"still bad"}`)
	_, err := post(t, recorder(dir, nil), p.URL, `{"q":1}`)
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v, want ErrRefused", err)
	}
	if p.hits.Load() != 1 {
		t.Fatalf("provider hits = %d, want 1", p.hits.Load())
	}
}

// Plain replay of a stored failure fails loudly and says to re-record,
// rather than handing foo a provider error nobody sent this run.
func TestReplay_StoredFailureFailsLoudly(t *testing.T) {
	for _, sf := range storedFailures {
		t.Run(sf.name, func(t *testing.T) {
			dir := t.TempDir()
			storeFailure(t, dir, `{"q":1}`, sf.status, sf.err)
			var exs []Exchange
			tr := replayer(dir)
			tr.OnExchange = func(ex Exchange) { exs = append(exs, ex) }
			resp, err := post(t, tr, closedURL(t), `{"q":1}`)
			if err == nil {
				t.Fatalf("replayed a stored failure as a response: status %d", resp.StatusCode)
			}
			if !errors.Is(err, ErrStoredFailure) {
				t.Errorf("err = %v, want ErrStoredFailure", err)
			}
			for _, w := range []string{"failed exchange", "re-record", chatPath} {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not say %q", err, w)
				}
			}
			if len(exs) != 1 || exs[0].Error == "" {
				t.Errorf("exchanges = %+v", exs)
			}

			// A fallback request foo sends after the failure has no
			// recording; its error still leads with the cause.
			_, err = post(t, tr, closedURL(t), `{"q":"fallback"}`)
			if !errors.Is(err, xrr.ErrCassetteMiss) || !errors.Is(err, ErrStoredFailure) {
				t.Fatalf("fallback err = %v, want the stored failure and the miss", err)
			}
			if !strings.HasPrefix(err.Error(), ErrStoredFailure.Error()) {
				t.Errorf("fallback err %q does not lead with the stored failure", err)
			}
		})
	}
}

func TestCheckRecordable(t *testing.T) {
	if err := CheckRecordable(http.MethodGet, "/v1/models", 204, nil, nil); err != nil {
		t.Fatalf("2xx refused: %v", err)
	}
	for _, s := range []int{199, 301, 404, 503} {
		if err := CheckRecordable(http.MethodGet, "/v1/models", s, nil, nil); !errors.Is(err, ErrRefused) {
			t.Errorf("status %d: err = %v", s, err)
		}
	}
	// A Next that wraps errors the way http.Client does puts the full
	// URL in the message; the refusal names the cassette path instead.
	ue := &url.Error{Op: "Post", URL: "https://api.example.com/v1/models?key=zz", Err: errors.New("dial tcp: connection refused")}
	err := CheckRecordable(http.MethodGet, "/v1/models", 0, nil, ue)
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "api.example.com") || strings.Contains(err.Error(), "zz") {
		t.Errorf("error %q carries the request URL", err)
	}
}
