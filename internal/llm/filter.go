// Catalog filtering for `foo model list`.
//
// This file owns the "which subset of the catalog does the caller
// want" question, kept apart from catalog.go's "where do rows come
// from". The split matters because filtering cannot be done on
// []ModelEntry: that row type deliberately carries only the fields
// every conceivable source can supply, so it has no Family,
// OpenWeights, StructuredOutput or modality fields to test. Those live
// on aim.Model and are discarded by entryFromAim.
//
// So filters are pushed down to the source rather than applied to its
// output. Filter is foo's source-neutral predicate; aim-backed sources
// translate it to an aim.Filter and let the registry do the matching,
// which is also what makes the counts here identical to `aim query`.

package llm

import (
	"context"
	"fmt"

	"hop.top/aim"
)

// Filter narrows a catalog listing. The zero value selects everything.
//
// Capability fields are *bool tristates, mirroring aim.Filter: nil
// means "do not filter on this", non-nil means "must equal". A plain
// bool would be wrong here — false is a meaningful request ("models
// without reasoning"), and is not the same as "unspecified".
type Filter struct {
	// Provider matches aim's provider id exactly ("openai").
	Provider string
	// Family matches aim's model family exactly ("gpt-4").
	Family string
	// Input requires every listed input modality ("text", "image").
	Input []string
	// Output requires every listed output modality.
	Output []string
	// ToolCall filters on tool-calling support; nil = no filter.
	ToolCall *bool
	// Reasoning filters on reasoning support; nil = no filter.
	Reasoning *bool
	// OpenWeights filters on open-weights availability; nil = no filter.
	OpenWeights *bool
	// StructuredOutput filters on structured-output support; nil = no
	// filter.
	StructuredOutput *bool
	// Query is a raw aim DSL expression (`provider:openai
	// reasoning:true`), parsed by aim.ParseQuery. See [Filter.aimFilter]
	// for how it composes with the explicit fields above.
	Query string
}

// IsZero reports whether f constrains nothing, so a caller can skip
// building a filtered source and keep the unfiltered path byte-identical.
func (f Filter) IsZero() bool {
	return f.Provider == "" && f.Family == "" &&
		len(f.Input) == 0 && len(f.Output) == 0 &&
		f.ToolCall == nil && f.Reasoning == nil &&
		f.OpenWeights == nil && f.StructuredOutput == nil &&
		f.Query == ""
}

// aimFilter lowers f onto an aim.Filter.
//
// Composition rule: --query is parsed first and the explicit fields are
// layered on top, so an explicit flag wins over the same key inside the
// query string. Rationale: the flags are the typed, discoverable,
// shell-completable surface and the query is the escape hatch; when a
// user passes both, the one they spelled out as a flag is the more
// specific intent. Everything else in the query that the flags do not
// mention is preserved, so the two compose rather than one erasing the
// other — `--query 'reasoning:true' --provider openai` means both.
//
// Modalities are the one field that merges rather than overrides: aim
// treats Input/Output as subset containment (all listed modalities must
// be present), so appending --input onto a query's `in:` narrows the
// result, consistent with every other filter being an AND.
//
// A parse error is returned verbatim from aim; it already names the
// offending key, and re-wording it would only make it harder to match
// against aim's own documentation.
func (f Filter) aimFilter() (aim.Filter, error) {
	var out aim.Filter
	if f.Query != "" {
		parsed, err := aim.ParseQuery(f.Query)
		if err != nil {
			return aim.Filter{}, err
		}
		out = parsed
	}

	if f.Provider != "" {
		out.Provider = f.Provider
	}
	if f.Family != "" {
		out.Family = f.Family
	}
	// Append, don't replace: subset semantics make this an AND.
	out.Input = append(out.Input, f.Input...)
	out.Output = append(out.Output, f.Output...)
	if f.ToolCall != nil {
		out.ToolCall = f.ToolCall
	}
	if f.Reasoning != nil {
		out.Reasoning = f.Reasoning
	}
	if f.OpenWeights != nil {
		out.OpenWeights = f.OpenWeights
	}
	if f.StructuredOutput != nil {
		out.StructuredOutput = f.StructuredOutput
	}
	return out, nil
}

// filteredAimCatalog is an aim-backed CatalogSource that asks the
// registry for a subset. It is a distinct type from aimCatalog rather
// than a field on it so the unfiltered path keeps its exact previous
// behaviour, and so a nil/zero filter never reaches aim at all.
type filteredAimCatalog struct {
	reg    *aim.Registry
	filter Filter
}

// NewFilteredAimCatalog returns a CatalogSource backed by an aim
// registry, restricted to models matching f. A nil registry means
// foo's process-wide default, as with [NewAimCatalog].
//
// A zero filter yields the same rows as [NewAimCatalog]; callers that
// know their filter is empty should prefer that, but passing a zero
// filter here is not an error.
func NewFilteredAimCatalog(reg *aim.Registry, f Filter) CatalogSource {
	if reg == nil {
		reg = ensureRegistry()
	}
	return filteredAimCatalog{reg: reg, filter: f}
}

// ListModels reads the matching slice of the catalog. Filtering happens
// inside aim, so fields absent from ModelEntry (family, open weights,
// modalities) are still filterable.
func (c filteredAimCatalog) ListModels(ctx context.Context) ([]ModelEntry, error) {
	af, err := c.filter.aimFilter()
	if err != nil {
		return nil, err
	}
	models, err := c.reg.Models(ctx, af)
	if err != nil {
		return nil, fmt.Errorf("foo: read model catalog: %w", err)
	}
	out := make([]ModelEntry, 0, len(models))
	for _, m := range models {
		out = append(out, entryFromAim(m))
	}
	return out, nil
}
