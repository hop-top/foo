package llm

import (
	"context"
	"flag"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	xrr "hop.top/xrr"
	xhttp "hop.top/xrr/adapters/http"
)

// cassetteDir holds a committed xrr http cassette capturing a real
// OpenAI-compatible /v1/models response, so the suite replays what a
// server actually returned rather than markup this repo wrote.
//
// The httptest tests in endpoint_test.go serve hand-written JSON: they
// prove the pipeline runs, never that it copes with a real server's
// envelope. Re-record with -update against a live endpoint.
const cassetteDir = "testdata/cassettes"

// liveEndpoint is the origin used when re-recording. Any
// OpenAI-compatible server works; the recorded response is what the
// suite replays afterwards, so CI never dials it.
const liveEndpoint = "http://127.0.0.1:11434/v1"

var updateCassettes = flag.Bool("update", false, "re-record xrr cassettes against a live endpoint")

// xrrTransport routes each request through an xrr session: replayed from
// the cassette by default, recorded against the live endpoint under
// -update.
type xrrTransport struct {
	sess  xrr.Session
	next  http.RoundTripper
	adapt *xhttp.Adapter

	// live counts round-trips that actually reached the network. In
	// replay it must stay zero; a non-zero count means the cassette
	// was bypassed and the test is silently exercising a live server,
	// which flakes the moment that server is down.
	live atomic.Int64

	// seam counts how often the injected client was requested. Zero
	// means the endpoint source never consulted the injection point,
	// so the fetch went out through some other client and nothing
	// here was actually replayed.
	seam atomic.Int64
}

func (t *xrrTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	req := &xhttp.Request{Method: r.Method, URL: r.URL.String()}

	resp, err := t.sess.Record(r.Context(), t.adapt, req, func() (xrr.Response, error) {
		// do() only runs in record mode; replay never calls it.
		t.live.Add(1)
		live, lerr := t.next.RoundTrip(r)
		if lerr != nil {
			return nil, lerr
		}
		defer live.Body.Close()
		body, rerr := io.ReadAll(live.Body)
		if rerr != nil {
			return nil, rerr
		}
		return &xhttp.Response{Status: live.StatusCode, Body: string(body)}, nil
	})
	if err != nil {
		return nil, err
	}

	status, body := http.StatusOK, ""
	switch v := resp.(type) {
	case *xhttp.Response:
		status, body = v.Status, v.Body
	case *xrr.RawResponse:
		// Replay hands back an untyped payload map, never the
		// adapter's typed response — pull the fields back out.
		if s, ok := v.Payload["status"].(int); ok {
			status = s
		}
		if s, ok := v.Payload["body"].(string); ok {
			body = s
		}
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
		Request:    r,
	}, nil
}

// newCassetteClient returns an http.Client backed by the cassette, plus
// the transport so the test can assert on the seam and live counters.
func newCassetteClient(t *testing.T) (*http.Client, *xrrTransport) {
	t.Helper()
	mode := xrr.ModeReplay
	if *updateCassettes {
		mode = xrr.ModeRecord
	}
	transport := &xrrTransport{
		sess:  xrr.NewSession(mode, xrr.NewFileCassette(cassetteDir)),
		next:  http.DefaultTransport,
		adapt: xhttp.NewAdapter(),
	}
	// Counting here is what proves the source used the injected
	// client rather than building its own.
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		transport.seam.Add(1)
		return transport.RoundTrip(r)
	})}, transport
}

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestEndpointCatalog_RealResponseFromCassette exercises the projection
// against a real recorded /v1/models response.
//
// Recorded from a live ollama; the same envelope is what llama.cpp,
// vLLM and colibri return, so one cassette covers the shape for all of
// them.
func TestEndpointCatalog_RealResponseFromCassette(t *testing.T) {
	client, transport := newCassetteClient(t)

	got, err := NewEndpointCatalog(liveEndpoint, client).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	// Both assertions are required. With only the seam check the test
	// passes while hitting the live network; with only the live check
	// it passes when the fetch bypassed the injected client entirely.
	if transport.seam.Load() == 0 {
		t.Error("ListModels never used the injected client; the fetch bypassed the seam")
	}
	if !*updateCassettes {
		if n := transport.live.Load(); n != 0 {
			t.Errorf("%d live round-trip(s) during replay; cassette was bypassed", n)
		}
	}

	if len(got) == 0 {
		t.Fatal("recorded response produced no models")
	}
	for _, e := range got {
		if e.ID == "" {
			t.Error("recorded response produced a row with no id")
		}
		if e.Source != SourceEndpoint {
			t.Errorf("%s: Source = %q, want %q", e.ID, e.Source, SourceEndpoint)
		}
		if !e.Reachable {
			t.Errorf("%s: Reachable = false on a live-endpoint row", e.ID)
		}
	}

	// A real server's ids are what the catalog cannot supply: the
	// point of the whole source. Ollama's carry a `:tag` suffix no
	// models.dev id uses, which is the recorded shape worth pinning.
	if !strings.Contains(got[0].ID, ":") {
		t.Logf("first recorded id %q has no tag suffix", got[0].ID)
	}
}
