// Catalog cache provenance — where the rows came from and how old they
// are.
//
// foo does not cache the models.dev catalog. aim already does, and does
// it thoroughly: 24h TTL, ETag/304 revalidation, atomic writes, a
// lockfile for concurrent processes, stale-on-error, corrupt-file
// recovery. Measured cold-vs-warm on this machine is 456ms against
// 53ms, with the payload at ~3.1MB under $XDG_CACHE_HOME/hop/aim.
// Reimplementing any of that here would be a second cache racing the
// first over the same bytes.
//
// What was missing was not caching, it was disclosure: a listing that
// silently served a day-old snapshot looked exactly like one that had
// just fetched. aim.Cache.Meta exists for precisely this — its doc
// comment says callers use it to attach provenance to rendered output —
// so this file reads it and shapes it for display. Nothing here fetches
// or writes.

package llm

import (
	"context"
	"fmt"
	"time"

	"hop.top/aim"
)

// CatalogProvenance describes the state of aim's catalog cache at the
// moment a listing was rendered.
//
// It is foo's own type rather than a re-export of aim.Meta because the
// two answer different questions. aim.Meta is the on-disk record;
// this is what a reader needs to decide whether to trust the list,
// which means an age, a freshness verdict, and a human phrase — none of
// which aim computes, and all of which depend on when the render
// happened rather than on when the file was written.
type CatalogProvenance struct {
	// Fetched reports whether the catalog has ever been fetched. A
	// non-Present aim.Meta means first run — no cache file exists
	// yet — and every other field here stays zero.
	Fetched bool
	// FetchedAt is when aim last fetched or revalidated the catalog.
	FetchedAt time.Time
	// Age is how long ago that was, as of the call to
	// [ReadCatalogProvenance].
	Age time.Duration
	// TTL is the freshness window aim recorded with the entry.
	TTL time.Duration
	// Stale reports whether Age has passed TTL, i.e. whether the next
	// read will trigger a refetch. It is not "the data is wrong":
	// aim revalidates with an ETag and a 304 leaves the same bytes in
	// place.
	Stale bool
}

// ReadCatalogProvenance reads aim's cache metadata through reg, or
// through foo's shared registry when reg is nil.
//
// This never fetches. aim.Cache.Meta is a read of meta.json and returns
// a zero Meta on any error — missing file, unreadable dir, malformed
// JSON — which this maps to Fetched=false. A listing must render even
// when its provenance cannot be determined, so there is no error
// return: "we do not know when this was fetched" is itself the useful
// thing to display.
func ReadCatalogProvenance(reg *aim.Registry) CatalogProvenance {
	if reg == nil {
		reg = ensureRegistry()
	}
	return catalogProvenanceFrom(reg.Cache().Meta(), time.Now())
}

// catalogProvenanceFrom projects an aim.Meta onto foo's view as of now.
// Split out so tests pin the age arithmetic against a fixed clock
// instead of racing a real one.
func catalogProvenanceFrom(m aim.Meta, now time.Time) CatalogProvenance {
	if !m.Present {
		return CatalogProvenance{}
	}
	p := CatalogProvenance{
		Fetched:   true,
		FetchedAt: m.LastFetch,
		Age:       now.Sub(m.LastFetch),
		TTL:       m.TTL,
	}
	// A zero TTL in the metadata means aim wrote no window, not "expires
	// immediately"; with nothing to compare against, staleness is
	// unknown and the honest answer is "not stale".
	if p.TTL > 0 {
		p.Stale = p.Age >= p.TTL
	}
	return p
}

// Describe returns the one-line phrase shown beneath a listing.
//
// Three states, because they call for three different reactions. Never
// fetched is the first run and explains a slow command rather than a
// stale list. Fresh states the age so the reader can judge it. Stale
// adds the hint, because a reader who cares about the age is exactly
// the one who wants to know --refresh exists.
func (p CatalogProvenance) Describe() string {
	if !p.Fetched {
		return "catalog: fetched just now (no cache before this run)"
	}
	if p.Stale {
		return fmt.Sprintf("catalog: cached %s ago, past its %s window; pass --refresh to refetch",
			humanizeAge(p.Age), humanizeAge(p.TTL))
	}
	return fmt.Sprintf("catalog: cached %s ago", humanizeAge(p.Age))
}

// humanizeAge renders a duration at one significant unit.
//
// time.Duration.String() prints "3h12m9.432s", which is precision no
// reader of a provenance line wants and which changes on every
// invocation, making it untestable against a golden output. One unit,
// rounded down, is what "cached 3h ago" means.
func humanizeAge(d time.Duration) string {
	switch {
	case d < 0:
		// A clock that moved backwards, or metadata written by a
		// machine ahead of this one. "in the future" is wrong to
		// round to a negative age, and 0 reads as fresh, which is the
		// safe direction.
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	}
}

// RefreshCatalog forces aim to refetch the catalog, bypassing its TTL.
//
// This is the catalog half of --refresh. It is a separate call from
// listing because aim's Refresh returns the provider map and foo wants
// the ranked rows — running it first and then listing normally means
// the listing reads the freshly written cache, and means a refresh
// failure is reported before any rows are rendered.
//
// A nil registry means foo's shared one. Errors are returned rather
// than swallowed: the user asked for fresh data by name, so silently
// serving the stale copy would be answering a different question.
func RefreshCatalog(ctx context.Context, reg *aim.Registry) error {
	if reg == nil {
		reg = ensureRegistry()
	}
	if _, err := reg.Cache().Refresh(ctx, true); err != nil {
		return fmt.Errorf("foo: refresh model catalog: %w", err)
	}
	return nil
}
