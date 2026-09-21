package main

import (
	"context"
	"flag"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"hop.top/kit/go/console/progress"
	xrr "hop.top/xrr"
	xhttp "hop.top/xrr/adapters/http"
)

// cassetteDir holds committed xrr http cassettes capturing real pages,
// so the suite replays recorded markup instead of serving a hand-written
// fixture from httptest. Re-record with -update.
const cassetteDir = "testdata/cassettes"

var updateCassettes = flag.Bool("update", false, "re-record xrr cassettes against the live web")

// xrrTransport routes each request through an xrr session: replayed from
// the cassette by default, recorded against the live web under -update.
type xrrTransport struct {
	sess  xrr.Session
	next  http.RoundTripper
	adapt *xhttp.Adapter

	// live counts round-trips that actually reached the network. In
	// replay it must stay zero; a non-zero count means the cassette was
	// bypassed and the test is silently exercising the live web.
	live atomic.Int64

	// seam counts how often the injected client was requested. Zero
	// means scrape() never consulted the injection point at all, so the
	// fetch went out through some other client and nothing here was
	// actually replayed.
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
		// Replay hands back an untyped payload map, never the adapter's
		// typed response — pull the fields back out.
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

// withCassette swaps the scrape fetch seam for a cassette-backed client
// for the test's duration.
func withCassette(t *testing.T) *xrrTransport {
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
	prev := httpClientFor
	httpClientFor = func() (*http.Client, *observedStore) {
		transport.seam.Add(1)
		return &http.Client{Transport: transport}, nil
	}
	t.Cleanup(func() { httpClientFor = prev })
	return transport
}

// TestScrape_RealPageFromCassette exercises the readability extraction
// against real recorded markup.
//
// The httptest-based tests in progress_test.go serve markup this repo
// wrote, so they can only prove the pipeline runs — never that it copes
// with what the web actually returns. This one replays a recorded page,
// which is the case most likely to regress.
func TestScrape_RealPageFromCassette(t *testing.T) {
	transport := withCassette(t)

	var out strings.Builder
	rec := &recorder{}
	ctx := progress.WithReporter(context.Background(), rec)

	if err := scrape(scrapeCmd(ctx, &out), "https://example.com", "readability"); err != nil {
		t.Fatalf("scrape: %v", err)
	}

	// Replay must be served entirely from the cassette. Without this the
	// test still passes when the seam is bypassed — it just silently
	// fetches the live page instead.
	if transport.seam.Load() == 0 {
		t.Error("scrape() never used the injected client; the fetch bypassed the seam")
	}
	if !*updateCassettes {
		if n := transport.live.Load(); n != 0 {
			t.Errorf("%d live round-trip(s) during replay; cassette was bypassed", n)
		}
	}

	md := out.String()
	if !strings.Contains(md, "Example Domain") {
		t.Errorf("expected the page title in the markdown, got:\n%s", md)
	}
	// The extractor must carry body prose through, not just the heading.
	if !strings.Contains(md, "documentation examples") {
		t.Errorf("expected body prose in the markdown, got:\n%s", md)
	}
	// Telemetry must still reach the terminal event on the recorded path.
	if done, ok := rec.find(phaseDone); !ok {
		t.Error("no done event")
	} else if done.Bytes == 0 {
		t.Error("done event reported zero bytes")
	}
}
