// Progress and telemetry for the scrape sidecar. Mirrors foo-youtube's
// telemetry.go so both sidecars emit an identically shaped sequence:
// fetch start → cache outcome → done. Events go to the kit
// progress.Reporter wired into cmd.Context() by kit/cli, which renders
// to stderr and is silenced by --quiet; stdout stays reserved for the
// markdown payload.
package main

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"hop.top/kit/go/console/progress"
	"hop.top/kit/go/storage/kv"
)

// Phase names, Extra keys and source values are kept identical to
// foo-youtube's so a consumer of either sidecar's JSONL sees one
// vocabulary.
const (
	phaseFetch = "fetch"
	phaseCache = "cache"
	phaseDone  = "done"
)

const (
	extraSource  = "source"
	extraTokens  = "est_tokens"
	extraElapsed = "elapsed"
)

const (
	sourceCached  = "cache"
	sourceFetched = "fetched"
)

// started is the start of the current scrape, set by scrape() so the
// done event can report elapsed time.
var started = time.Now()

func emitFetchStart(ctx context.Context, item string) {
	progress.FromContext(ctx).Emit(ctx, progress.Event{
		Phase: phaseFetch,
		Item:  item,
	})
}

func emitCacheOutcome(ctx context.Context, item string, hit bool, n int) {
	phase, source := phaseFetch, sourceFetched
	if hit {
		phase, source = phaseCache, sourceCached
	}
	progress.FromContext(ctx).Emit(ctx, progress.Event{
		Phase: phase,
		Item:  fmt.Sprintf("%s (%s)", item, source),
		Bytes: int64(n),
		Extra: map[string]any{extraSource: source},
	})
}

// emitDone reports the terminal event. kit's Human reporter renders
// Item but not Extra, so the elapsed/token figures are spelled into
// Item for the human line as well as carried structurally in Extra.
func emitDone(ctx context.Context, item string, n int64, tokens int) {
	elapsed := time.Since(started).Round(time.Millisecond)
	ok := true
	progress.FromContext(ctx).Emit(ctx, progress.Event{
		Phase: phaseDone,
		Item:  fmt.Sprintf("%s in %s, ~%d tokens", item, elapsed, tokens),
		Bytes: n,
		OK:    &ok,
		Extra: map[string]any{
			extraTokens:  tokens,
			extraElapsed: elapsed.String(),
		},
	})
}

// estimateTokens approximates an LLM token count for the produced
// markdown; four characters per token is the usual English rule of
// thumb. Operator telemetry only, never an input to billing.
func estimateTokens(s string) int { return len(s) / 4 }

// observedStore wraps a kv.TTLStore to make httpcache's internal
// hit/miss decision observable from the outside.
//
// kit's httpcache.Transport exposes no hook, callback or response
// marker for whether a request was served from cache — the decision
// lives entirely inside RoundTrip. But the transport reaches its
// verdict through exactly one call: store.Get. A Get that returns
// ok==true is the hit. Wrapping the store therefore observes the
// decision without a kit change and without duplicating the
// transport's keying or cacheability rules.
//
// One nuance this cannot see: httpcache treats a stored entry that
// fails to decode as a miss, which this wrapper would have already
// counted as a hit. A corrupt entry is the only case, and it
// self-corrects on the refetch that follows.
type observedStore struct {
	kv.TTLStore
	hits atomic.Int64
}

// newObservedStore wraps inner so hit() reports whether a Get has
// found a value since the wrapper was made.
func newObservedStore(inner kv.TTLStore) *observedStore {
	return &observedStore{TTLStore: inner}
}

// Get records a hit when the underlying store returns a value.
func (s *observedStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	raw, ok, err := s.TTLStore.Get(ctx, key)
	if ok && err == nil {
		s.hits.Add(1)
	}
	return raw, ok, err
}

// hit reports whether any Get served a value.
func (s *observedStore) hit() bool { return s.hits.Load() > 0 }
