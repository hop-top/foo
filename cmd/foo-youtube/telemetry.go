// Progress and telemetry shared by the sidecar's fetch paths. Every
// event goes to the kit progress.Reporter wired into cmd.Context() by
// kit/cli, which renders to stderr (human lines or JSONL) and is
// silenced by --quiet. Stdout stays reserved for the markdown payload.
package main

import (
	"context"
	"fmt"
	"time"

	"hop.top/kit/go/console/progress"
)

// Phase names are lowercase nouns per the kit progress contract. The
// same three names are used by foo-scrape so both sidecars render an
// identical progress shape.
const (
	phaseFetch = "fetch"
	phaseCache = "cache"
	phaseDone  = "done"
)

// Extra keys carry telemetry the Event struct has no field for. kit's
// Human reporter does not render Extra, so anything that must be
// visible on a human line is also folded into Item by emitDone.
const (
	extraSource  = "source"
	extraTokens  = "est_tokens"
	extraElapsed = "elapsed"
)

// Source values distinguish a cache hit from a network/subprocess
// fetch. They ride in Extra[extraSource] and, for the human line, in
// the event's Item.
const (
	sourceCached  = "cache"
	sourceFetched = "fetched"
)

// started is the process-wide start of the current extraction, set by
// run() so emitDone can report elapsed time. Kept as a package var to
// match the existing ytCache/eventBus idiom in this single-file sidecar.
var started = time.Now()

// emitFetchStart announces work beginning on item.
func emitFetchStart(ctx context.Context, item string) {
	progress.FromContext(ctx).Emit(ctx, progress.Event{
		Phase: phaseFetch,
		Item:  item,
	})
}

// emitCacheOutcome reports whether the payload came from the cache or
// the wire. A hit uses phaseCache, a miss stays on phaseFetch, so the
// two are distinguishable from the phase alone as well as from Extra.
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

// emitDone reports the terminal event with elapsed time, the markdown
// size and an estimated token count. Because kit's Human reporter
// renders Item but not Extra, the elapsed/token figures are also
// spelled into Item so they show on a human line; JSONL consumers read
// the structured Extra instead.
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
// markdown. Four characters per token is the usual rule of thumb for
// English prose; this is an estimate for operator telemetry, never an
// input to billing or truncation.
func estimateTokens(s string) int { return len(s) / 4 }
