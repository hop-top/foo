// Model catalog listing. `foo model default <id>` wants a model id the
// CLI otherwise gives no way to discover: `foo provider list` prints
// compiled-in schemes only, never a model id, so the discovery step was
// a hand-rolled curl of some provider's /v1/models.
//
// This file owns the "where do candidate models come from" question.
// CatalogSource is the seam: today the only implementation is the aim
// registry (models.dev), but the command layer talks to the interface,
// so a live-endpoint source can be added without the command changing
// shape. ListModels is the one entry point the command layer calls.

package llm

import (
	"context"
	"fmt"
	"sort"

	"hop.top/aim"
	kitllm "hop.top/kit/go/ai/llm"
)

// DefaultListLimit caps the default `foo model list` view.
//
// The catalog holds ~7900 models across ~222 providers; a full dump is
// not a list, it is a denial of service on the reader's terminal. 20 is
// one screenful on a standard terminal and, under the ranking below,
// spans every provider foo can reach at least twice.
const DefaultListLimit = 20

// ModelSource identifies where a [ModelEntry] came from. It travels on
// every row so a caller can answer "why is this model in my list"
// without re-deriving it, and so a listing merged from two sources
// stays attributable per row.
type ModelSource string

const (
	// SourceCatalog is the aim registry (models.dev), the static
	// cross-provider catalog. Rows describe models that exist, not
	// models the local credentials can necessarily reach.
	SourceCatalog ModelSource = "catalog"
)

// ModelEntry is one listable model, decoupled from aim.Model so a
// second source is not forced to fake aim's wire shape. Fields are the
// subset every conceivable source can supply; anything aim-specific
// that a future source cannot produce stays optional (zero value =
// unknown).
type ModelEntry struct {
	// Source records the origin of this row.
	Source ModelSource
	// Provider is the provider id ("anthropic", "openai").
	Provider string
	// ID is the model id to hand to `foo model default`.
	ID string
	// Name is the human display name, when the source has one.
	Name string
	// Context is the context window in tokens; 0 means unknown.
	Context int
	// ToolCall reports tool-calling support.
	ToolCall bool
	// Reasoning reports chain-of-thought/reasoning support.
	Reasoning bool
	// InputCost is USD per 1M input tokens; 0 means free or unknown.
	InputCost float64
	// OutputCost is USD per 1M output tokens.
	OutputCost float64
	// Released is the model's release date (YYYY-MM-DD) when the
	// source publishes one. Lexicographic compare is a valid date
	// compare for that layout, so ranking never parses it.
	Released string
	// Routable reports whether Provider matches a provider scheme
	// compiled into this build of foo — i.e. whether foo has code
	// that knows how to speak to it. The catalog lists every provider
	// models.dev knows; only a minority are wired.
	//
	// Routable is a property of the binary, not of the machine. It
	// says nothing about credentials: foo is built with a google
	// adapter whether or not a GOOGLE_API_KEY exists anywhere. The
	// credential half of "can I call this" is [AuthIndex], and the
	// conjunction of the two is what [Reachable] means — see
	// [FilterReachable] for why the two stayed separate fields.
	Routable bool
}

// Reachable reports whether foo could actually complete a call to this
// model right now: it has an adapter for the provider *and* the
// provider's credential requirement is satisfied.
//
// The two conditions were one field until credentials entered the
// picture, and collapsing them back would lose information both callers
// need. Ranking sinks non-Routable rows but must still show them (a
// model foo cannot speak to is still a fact about the catalog, and
// linking a new adapter is a different fix from exporting a key), while
// the default view hides rows that are not Reachable. A single boolean
// cannot drive both.
//
// A nil index means "credentials unknown", which degrades to the
// pre-credential behaviour rather than hiding everything.
func (e ModelEntry) Reachable(auth *AuthIndex) bool {
	return e.Routable && auth.Satisfied(e.Provider)
}

// FilterReachable keeps only the entries foo could call right now, and
// reports how many it dropped.
//
// Dropping rather than sinking is the deliberate difference from [Rank].
// Rank answers "what should be at the top", where a row foo cannot reach
// is still worth a slot near the bottom. This answers "what is worth
// showing at all", where 7000-odd rows the user provably cannot call are
// not a list, they are noise with a handful of signal buried in it. The
// hidden count travels back so the caller can say so — a filtered
// listing that does not disclose its own filtering reads as an
// incomplete catalog.
func FilterReachable(entries []ModelEntry, auth *AuthIndex) (kept []ModelEntry, hidden int) {
	kept = make([]ModelEntry, 0, len(entries))
	for _, e := range entries {
		if e.Reachable(auth) {
			kept = append(kept, e)
		}
	}
	return kept, len(entries) - len(kept)
}

// CatalogSource produces listable models. The command layer depends on
// this interface rather than on aim directly, so a live `/v1/models`
// source slots in beside the catalog without touching the command.
//
// Implementations return rows in a stable, deterministic order:
// truncation at a display limit is only meaningful against a stable
// sort, and tests compare whole slices.
type CatalogSource interface {
	// ListModels returns every model the source knows about.
	ListModels(ctx context.Context) ([]ModelEntry, error)
}

// aimCatalog adapts an aim registry to CatalogSource.
type aimCatalog struct{ reg *aim.Registry }

// NewAimCatalog returns a CatalogSource backed by an aim registry.
// A nil registry means "use foo's process-wide default", the same
// registry the pool picker borrows — so the catalog and the picker
// share one cache rather than each fetching models.dev. aim owns the
// caching (XDG cache dir, 24h TTL); foo adds none of its own.
func NewAimCatalog(reg *aim.Registry) CatalogSource {
	if reg == nil {
		reg = ensureRegistry()
	}
	return aimCatalog{reg: reg}
}

// ListModels reads the whole catalog. aim.Models already sorts by
// (provider, id), which is the order foo wants; the sort is re-asserted
// in tests rather than re-applied here.
func (c aimCatalog) ListModels(ctx context.Context) ([]ModelEntry, error) {
	models, err := c.reg.Models(ctx, aim.Filter{})
	if err != nil {
		return nil, fmt.Errorf("foo: read model catalog: %w", err)
	}
	out := make([]ModelEntry, 0, len(models))
	for _, m := range models {
		out = append(out, entryFromAim(m))
	}
	return out, nil
}

// entryFromAim projects an aim.Model onto foo's source-neutral row.
func entryFromAim(m aim.Model) ModelEntry {
	e := ModelEntry{
		Source:    SourceCatalog,
		Provider:  m.Provider,
		ID:        m.ID,
		Name:      m.Name,
		Context:   m.Limit.Context,
		ToolCall:  m.ToolCall,
		Reasoning: m.Reasoning,
		Released:  m.ReleaseDate,
		Routable:  routableProviders()[m.Provider],
	}
	// Cost is a pointer in aim: many open-weight entries omit it
	// entirely, and nil must not be conflated with explicit zero at
	// the dereference.
	if m.Cost != nil {
		e.InputCost = m.Cost.Input
		e.OutputCost = m.Cost.Output
	}
	return e
}

// routableProviders is the set of provider ids foo has a compiled-in
// adapter for. Derived from kit's registered scheme list rather than
// hardcoded, so a build that links a new adapter widens the default
// view without this file changing.
//
// Not every kit scheme is a models.dev provider id ("routellm" and
// "gemini" are foo/kit-side aliases with no catalog entry); a scheme
// absent from the catalog simply never matches, which is correct.
func routableProviders() map[string]bool {
	out := make(map[string]bool)
	for _, s := range kitllm.Schemes() {
		out[s] = true
	}
	return out
}

// Rank orders entries for the default view and reports how many of them
// are worth showing before truncation.
//
// Two problems shape it. First, only ~7% of catalog providers are ones
// foo links an adapter for, so unroutable rows are sunk below routable
// ones rather than dropped — a user who asks for more still sees them,
// and a live endpoint's rows are routable by construction. Second,
// within the routable set one aggregator (openrouter) supplies the
// majority of rows and a flat recency sort buries every first-party
// model beneath it. So routable rows are
// interleaved round-robin across providers: each provider contributes
// its newest model, then its second-newest, and so on. The first
// screenful is therefore the current flagship of each provider foo
// speaks, which is what someone running `foo model list` to feed `foo
// model default` is looking for.
//
// Ranking deliberately does not consult credentials. Sinking and hiding
// are separate jobs on separate axes: [FilterReachable] decides which
// rows survive at all, Rank decides what order the survivors appear in,
// and with --all in play the unroutable rows it sinks are exactly the
// ones the reader asked to see.
//
// The order is total and deterministic: ties inside a provider break on
// id, and provider rotation order is alphabetical.
func Rank(entries []ModelEntry) []ModelEntry {
	groups := make(map[string][]ModelEntry)
	var routable, unroutable []string
	for _, e := range entries {
		if _, seen := groups[e.Provider]; !seen {
			if e.Routable {
				routable = append(routable, e.Provider)
			} else {
				unroutable = append(unroutable, e.Provider)
			}
		}
		groups[e.Provider] = append(groups[e.Provider], e)
	}
	sort.Strings(routable)
	sort.Strings(unroutable)
	for _, p := range append(append([]string{}, routable...), unroutable...) {
		g := groups[p]
		sort.SliceStable(g, func(i, j int) bool {
			if g[i].Released != g[j].Released {
				return g[i].Released > g[j].Released
			}
			return g[i].ID < g[j].ID
		})
		groups[p] = g
	}

	out := make([]ModelEntry, 0, len(entries))
	// Routable providers rotate first and exhaust before any
	// unroutable row appears, so a truncated view never spends a
	// slot on a model foo has no adapter for.
	for _, tier := range [][]string{routable, unroutable} {
		for round := 0; ; round++ {
			added := false
			for _, p := range tier {
				if round < len(groups[p]) {
					out = append(out, groups[p][round])
					added = true
				}
			}
			if !added {
				break
			}
		}
	}
	return out
}

// Truncate returns the first limit entries and the number omitted. A
// limit of zero or less means "no limit".
func Truncate(entries []ModelEntry, limit int) (shown []ModelEntry, omitted int) {
	if limit <= 0 || len(entries) <= limit {
		return entries, 0
	}
	return entries[:limit], len(entries) - limit
}

// ListModels reads every model from src, ranked for display. It exists
// so the command layer has one call site to name regardless of which
// source is wired, and so truncation (a display concern) stays with the
// caller that knows the requested limit.
func ListModels(ctx context.Context, src CatalogSource) ([]ModelEntry, error) {
	if src == nil {
		src = NewAimCatalog(nil)
	}
	entries, err := src.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	return Rank(entries), nil
}
