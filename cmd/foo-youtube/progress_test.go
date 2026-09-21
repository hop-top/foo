package main

import (
	"context"
	"strings"
	"sync"
	"testing"

	"hop.top/kit/go/console/progress"
)

// recorder is a progress.Reporter that keeps every event for assertions.
// Safe for concurrent use so it can stand in anywhere kit's own
// reporters do.
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

// phases returns the ordered phase names, for terse table assertions.
func (r *recorder) phases() []string {
	var out []string
	for _, e := range r.all() {
		out = append(out, e.Phase)
	}
	return out
}

// find returns the first event whose phase matches.
func (r *recorder) find(phase string) (progress.Event, bool) {
	for _, e := range r.all() {
		if e.Phase == phase {
			return e, true
		}
	}
	return progress.Event{}, false
}

// TestRunYTDLP_EmitsCacheHitAndMiss is the core hit/miss distinction:
// the first call for an argv must report a fetch (miss), the second the
// same argv must report a cache read.
func TestRunYTDLP_EmitsCacheHitAndMiss(t *testing.T) {
	tests := []struct {
		name       string
		runs       int
		wantPhases []string
		wantSource []string
	}{
		{
			name: "miss then hit",
			runs: 2,
			// Each call emits a fetch start plus its outcome, so two
			// runs are four events: start, miss, start, hit.
			wantPhases: []string{phaseFetch, phaseFetch, phaseFetch, phaseCache},
			wantSource: []string{sourceFetched, sourceCached},
		},
		{
			name:       "single run is a miss only",
			runs:       1,
			wantPhases: []string{phaseFetch, phaseFetch},
			wantSource: []string{sourceFetched},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prev := ytRunner
			ytRunner = func(_ context.Context, _ []string) ([]byte, error) {
				return []byte(`{"title":"X"}`), nil
			}
			t.Cleanup(func() { ytRunner = prev })

			ytCache = newCacheStore(t)
			t.Cleanup(func() { ytCache = nil })

			rec := &recorder{}
			ctx := progress.WithReporter(context.Background(), rec)
			args := []string{"--dump-json", "https://youtu.be/" + tc.name}

			for i := 0; i < tc.runs; i++ {
				if _, err := runYTDLP(ctx, args); err != nil {
					t.Fatalf("run %d: %v", i, err)
				}
			}

			if got := rec.phases(); !equalStrings(got, tc.wantPhases) {
				t.Errorf("phases = %v, want %v", got, tc.wantPhases)
			}

			var sources []string
			for _, e := range rec.all() {
				if s, ok := e.Extra[extraSource].(string); ok {
					sources = append(sources, s)
				}
			}
			if !equalStrings(sources, tc.wantSource) {
				t.Errorf("sources = %v, want %v", sources, tc.wantSource)
			}
		})
	}
}

// TestRunYTDLP_CacheEventCarriesBytes proves the cache/fetch outcome
// event reports how many bytes it handled.
func TestRunYTDLP_CacheEventCarriesBytes(t *testing.T) {
	payload := []byte(`{"title":"a bit of payload here"}`)
	prev := ytRunner
	ytRunner = func(_ context.Context, _ []string) ([]byte, error) { return payload, nil }
	t.Cleanup(func() { ytRunner = prev })

	ytCache = newCacheStore(t)
	t.Cleanup(func() { ytCache = nil })

	rec := &recorder{}
	ctx := progress.WithReporter(context.Background(), rec)
	args := []string{"--dump-json", "https://youtu.be/bytes"}

	if _, err := runYTDLP(ctx, args); err != nil {
		t.Fatalf("run1: %v", err)
	}
	if _, err := runYTDLP(ctx, args); err != nil {
		t.Fatalf("run2: %v", err)
	}

	miss, ok := rec.find(phaseFetch)
	if !ok {
		t.Fatalf("no %q event; got %v", phaseFetch, rec.phases())
	}
	_ = miss
	hit, ok := rec.find(phaseCache)
	if !ok {
		t.Fatalf("no %q event; got %v", phaseCache, rec.phases())
	}
	if hit.Bytes != int64(len(payload)) {
		t.Errorf("cache-hit bytes = %d, want %d", hit.Bytes, len(payload))
	}
}

// TestEmitDone_CarriesElapsedBytesTokens pins the terminal event's
// telemetry: bytes of markdown, an estimated token count in Extra, and
// an elapsed duration.
func TestEmitDone_CarriesElapsedBytesTokens(t *testing.T) {
	rec := &recorder{}
	ctx := progress.WithReporter(context.Background(), rec)

	markdown := strings.Repeat("token ", 100) // 600 bytes
	emitDone(ctx, "https://youtu.be/x", int64(len(markdown)), estimateTokens(markdown))

	e, ok := rec.find(phaseDone)
	if !ok {
		t.Fatalf("no %q event; got %v", phaseDone, rec.phases())
	}
	if e.Bytes != int64(len(markdown)) {
		t.Errorf("bytes = %d, want %d", e.Bytes, len(markdown))
	}
	if e.OK == nil || !*e.OK {
		t.Errorf("OK = %v, want true", e.OK)
	}
	tok, ok := e.Extra[extraTokens]
	if !ok {
		t.Fatalf("Extra[%q] missing; Extra = %v", extraTokens, e.Extra)
	}
	n, ok := tok.(int)
	if !ok {
		t.Fatalf("Extra[%q] = %T, want int", extraTokens, tok)
	}
	if n <= 0 {
		t.Errorf("est_tokens = %d, want > 0", n)
	}
	if _, ok := e.Extra[extraElapsed]; !ok {
		t.Errorf("Extra[%q] missing; Extra = %v", extraElapsed, e.Extra)
	}
}

// TestEstimateTokens_ScalesWithLength keeps the estimator honest: more
// text must mean more tokens, and empty text zero.
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
