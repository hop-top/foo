package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"hop.top/kit/go/console/progress"
	"hop.top/kit/go/storage/kv"
	_ "hop.top/kit/go/storage/kv/sqlite"
)

// recorder is a progress.Reporter that keeps every event for assertions.
type recorder struct {
	mu     sync.Mutex
	events []progress.Event
}

func (r *recorder) Emit(_ context.Context, e progress.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) all() []progress.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]progress.Event(nil), r.events...)
}

func (r *recorder) phases() []string {
	var out []string
	for _, e := range r.all() {
		out = append(out, e.Phase)
	}
	return out
}

func (r *recorder) find(phase string) (progress.Event, bool) {
	for _, e := range r.all() {
		if e.Phase == phase {
			return e, true
		}
	}
	return progress.Event{}, false
}

const scrapePage = `<html><head><title>Hello</title></head>` +
	`<body><article><p>Some body text for the article.</p></article></body></html>`

// scrapeCmd builds a cobra command carrying ctx and a stdout buffer,
// mirroring how kit invokes RunE.
func scrapeCmd(ctx context.Context, out *strings.Builder) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	cmd.SetOut(out)
	cmd.SetErr(out)
	return cmd
}

// TestScrape_PhasesAndTelemetry pins the scrape sidecar's phase sequence
// and the telemetry fields on the terminal event. Shape must match
// foo-youtube's.
func TestScrape_PhasesAndTelemetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(scrapePage))
	}))
	t.Cleanup(srv.Close)

	rec := &recorder{}
	ctx := progress.WithReporter(context.Background(), rec)
	var out strings.Builder

	if err := scrape(scrapeCmd(ctx, &out), srv.URL, "readability", false); err != nil {
		t.Fatalf("scrape: %v", err)
	}

	wantPhases := []string{phaseFetch, phaseFetch, phaseDone}
	if got := rec.phases(); !equalStrings(got, wantPhases) {
		t.Fatalf("phases = %v, want %v", got, wantPhases)
	}

	done, ok := rec.find(phaseDone)
	if !ok {
		t.Fatal("no done event")
	}
	if done.Bytes <= 0 {
		t.Errorf("done bytes = %d, want the markdown size", done.Bytes)
	}
	if done.OK == nil || !*done.OK {
		t.Errorf("done OK = %v, want true", done.OK)
	}
	if _, ok := done.Extra[extraTokens]; !ok {
		t.Errorf("Extra[%q] missing; Extra = %v", extraTokens, done.Extra)
	}
	if _, ok := done.Extra[extraElapsed]; !ok {
		t.Errorf("Extra[%q] missing; Extra = %v", extraElapsed, done.Extra)
	}
}

// TestScrape_CacheHitVsMiss is the scrape-side hit/miss distinction:
// with a cache wired, the first fetch is a miss and the second a hit.
func TestScrape_CacheHitVsMiss(t *testing.T) {
	var origin int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		origin++
		_, _ = w.Write([]byte(scrapePage))
	}))
	t.Cleanup(srv.Close)

	path := filepath.Join(t.TempDir(), "scrape.db")
	t.Setenv("FOO_SCRAPE_CACHE", path)

	want := []string{sourceFetched, sourceCached}
	var got []string

	for i := 0; i < 2; i++ {
		rec := &recorder{}
		ctx := progress.WithReporter(context.Background(), rec)
		var out strings.Builder
		if err := scrape(scrapeCmd(ctx, &out), srv.URL, "readability", false); err != nil {
			t.Fatalf("scrape %d: %v", i, err)
		}
		e, ok := rec.find(phaseFetch)
		if !ok {
			t.Fatalf("run %d: no fetch-outcome event; phases = %v", i, rec.phases())
		}
		// The outcome event is the SECOND fetch-phase event; find it by
		// the source marker it must carry.
		var src string
		for _, ev := range rec.all() {
			if s, k := ev.Extra[extraSource].(string); k {
				src = s
			}
		}
		if src == "" {
			t.Fatalf("run %d: no %q in any Extra; events = %+v (first=%+v)",
				i, extraSource, rec.all(), e)
		}
		got = append(got, src)
	}

	if !equalStrings(got, want) {
		t.Errorf("sources = %v, want %v (origin hits = %d)", got, want, origin)
	}
	if origin != 1 {
		t.Errorf("origin requests = %d, want 1 (second must be served from cache)", origin)
	}
}

// TestObservedStore_ReportsHit proves the kv wrapper used to observe
// httpcache's internal hit/miss decision reports a hit only when the
// underlying store actually returned a value.
func TestObservedStore_ReportsHit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "obs.db")
	base, err := kv.Open(kv.Config{Backend: "sqlite", Path: path})
	if err != nil {
		t.Fatalf("kv.Open: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	ttl, ok := base.(kv.TTLStore)
	if !ok {
		t.Fatal("sqlite store is not a TTLStore")
	}

	obs := newObservedStore(ttl)
	ctx := context.Background()

	if _, _, err := obs.Get(ctx, "absent"); err != nil {
		t.Fatalf("Get absent: %v", err)
	}
	if obs.hit() {
		t.Error("hit() = true after a miss, want false")
	}

	if err := obs.Put(ctx, "present", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, _, err := obs.Get(ctx, "present"); err != nil {
		t.Fatalf("Get present: %v", err)
	}
	if !obs.hit() {
		t.Error("hit() = false after a successful Get, want true")
	}
}

// TestEstimateTokens_ScalesWithLength keeps the scrape-side estimator
// identical in shape to the youtube one.
func TestEstimateTokens_ScalesWithLength(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"four chars is one token", "abcd", 1},
		{"forty chars is ten tokens", strings.Repeat("a", 40), 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := estimateTokens(tc.in); got != tc.want {
				t.Errorf("estimateTokens(%d chars) = %d, want %d", len(tc.in), got, tc.want)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
