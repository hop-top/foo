// `foo model list` — the discovery half of `foo model default`.
//
// Before this command the only way to learn a model id foo would accept
// was to curl a provider's /v1/models by hand: `foo provider list`
// prints compiled-in schemes and never a model id. This lists the aim
// catalog (models.dev), which aim itself caches.

package commands

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"hop.top/foo/internal/llm"
	kitcli "hop.top/kit/go/console/cli"
)

// modelListLimit backs --limit, the display knob: how much of the
// result to show. The content filters below decide what is in the
// result in the first place, and are applied before truncation.
var modelListLimit int

// modelListFilterFlags holds the filter flag values. Capability flags
// are plain bools here and only become a tristate pointer in
// [modelListFilter], gated on whether cobra saw the flag — see there
// for why a *bool cannot be bound directly.
type modelListFilterFlags struct {
	provider         string
	family           string
	input            []string
	output           []string
	query            string
	toolCall         bool
	reasoning        bool
	openWeights      bool
	structuredOutput bool
}

// modelListFlags backs the filter flags on `foo model list`.
var modelListFlags modelListFilterFlags

// modelCatalogSource is the row producer `foo model list` reads from.
// nil means "the aim catalog through foo's shared registry". It is a
// package var so a test can substitute a fixture source without a
// network fetch, and so a second source can be selected here rather
// than by rewriting RunE.
var modelCatalogSource llm.CatalogSource

func modelListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List models from the model catalog",
		Long: `List models known to the aim catalog (models.dev), newest first.

The catalog spans thousands of models across hundreds of providers, so
the default view is truncated. Models are ordered to put the ones this
build of foo can actually route to first, rotating across providers so
the first screenful is each provider's current flagship rather than one
aggregator's back catalogue.

Pass an id from the ID column to ` + "`foo model default`" + ` to make it the
default. Raise --limit (or --limit=0 for everything) to see more; pipe
through --format json for machine consumption.

Filters narrow the catalog before truncation, and combine with AND.
Capability filters are three-state: omit the flag for no filtering,
pass --reasoning for models that have it, --reasoning=false for models
that do not. --input and --output are repeatable and require every
listed modality. --query takes a catalog expression such as
"provider:openai reasoning:true"; where it names the same thing as an
explicit flag, the flag wins.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runModelList(cmd.Context(), cmd, modelListLimit,
				modelListFilter(cmd, modelListFlags))
		},
	}
	cmd.Flags().IntVar(&modelListLimit, "limit", llm.DefaultListLimit,
		"maximum models to list (0 for no limit)")

	f := cmd.Flags()
	f.StringVar(&modelListFlags.provider, "provider", "",
		"only models from this provider id (exact match)")
	f.StringVar(&modelListFlags.family, "family", "",
		"only models in this family (exact match)")
	f.StringArrayVar(&modelListFlags.input, "input", nil,
		"only models accepting this input modality (repeatable)")
	f.StringArrayVar(&modelListFlags.output, "output", nil,
		"only models producing this output modality (repeatable)")
	f.BoolVar(&modelListFlags.toolCall, "tool-call", false,
		"only models with (--tool-call) or without (--tool-call=false) tool calling")
	f.BoolVar(&modelListFlags.reasoning, "reasoning", false,
		"only models with (--reasoning) or without (--reasoning=false) reasoning")
	f.BoolVar(&modelListFlags.openWeights, "open-weights", false,
		"only models with (--open-weights) or without (--open-weights=false) open weights")
	f.BoolVar(&modelListFlags.structuredOutput, "structured-output", false,
		"only models with (--structured-output) or without (--structured-output=false) structured output")
	f.StringVar(&modelListFlags.query, "query", "",
		`catalog query expression, e.g. "provider:openai reasoning:true"`)

	kitcli.SetSideEffect(cmd, kitcli.SideEffectRead)
	return cmd
}

// tristate returns a pointer to the flag's value, or nil when the flag
// was never given.
//
// This is the whole reason the capability flags are not bound straight
// to a *bool. aim.Filter's capability fields are tristates where nil
// means "do not filter"; binding a bool and always taking its address
// would make the field non-nil on every invocation, so a bare `foo
// model list` would silently filter to models whose capability is
// false — dropping most of the catalog with nothing on screen to
// explain it. Consulting Changed is what keeps "unset" distinct from
// the explicit "=false" the flag's own default would otherwise forge.
func tristate(cmd *cobra.Command, name string, val bool) *bool {
	if !cmd.Flags().Changed(name) {
		return nil
	}
	return &val
}

// modelListFilter assembles the filter from the parsed flags. It reads
// Changed off cmd rather than trusting the bound values, so the
// unset/true/false distinction survives.
func modelListFilter(cmd *cobra.Command, flags modelListFilterFlags) llm.Filter {
	return llm.Filter{
		Provider:         flags.provider,
		Family:           flags.family,
		Input:            flags.input,
		Output:           flags.output,
		Query:            flags.query,
		ToolCall:         tristate(cmd, "tool-call", flags.toolCall),
		Reasoning:        tristate(cmd, "reasoning", flags.reasoning),
		OpenWeights:      tristate(cmd, "open-weights", flags.openWeights),
		StructuredOutput: tristate(cmd, "structured-output", flags.structuredOutput),
	}
}

// modelFilteredSource builds a filtered row producer. It is a package
// var for the same reason modelCatalogSource is one: a test needs to
// observe the filter the command assembled without reaching the
// network. Production wiring is the aim-backed source.
//
// Filtering is pushed down into the source rather than applied to its
// rows because ModelEntry carries only a subset of the filterable
// fields — it has no family, open-weights, structured-output or
// modality — so a post-hoc pass over []ModelEntry could honour three of
// the nine filters and would have to silently ignore the rest.
var modelFilteredSource = func(f llm.Filter) llm.CatalogSource {
	return llm.NewFilteredAimCatalog(nil, f)
}

// modelListSource picks the row producer for one invocation.
//
// An explicitly injected modelCatalogSource always wins, so a test
// fixture is never silently swapped out for a network-backed source.
// Otherwise an empty filter keeps the plain catalog — byte-identical to
// the unfiltered path — and a non-empty one gets the filtered source.
func modelListSource(filter llm.Filter) llm.CatalogSource {
	if modelCatalogSource != nil || filter.IsZero() {
		return modelCatalogSource
	}
	return modelFilteredSource(filter)
}

// runModelList is split out of RunE so tests drive the whole path —
// source read, ranking, truncation, render, hint — against a fixture
// source and a captured writer.
func runModelList(ctx context.Context, cmd *cobra.Command, limit int, filter llm.Filter) error {
	entries, err := llm.ListModels(ctx, modelListSource(filter))
	if err != nil {
		return err
	}
	shown, omitted := llm.Truncate(entries, limit)

	rows := make([]modelRow, 0, len(shown))
	for _, e := range shown {
		rows = append(rows, modelRowFromEntry(e))
	}
	if err := renderData(cmd, rows); err != nil {
		return err
	}

	// The hint goes to stderr so it never contaminates `--format json`
	// piped into a parser, and is suppressed when nothing was cut.
	if omitted > 0 {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
			"%d more model(s) not shown; raise --limit or pass --limit=0 for all\n",
			omitted)
	}
	return nil
}

// modelRowFromEntry projects a catalog entry onto the wire/table row.
func modelRowFromEntry(e llm.ModelEntry) modelRow {
	return modelRow{
		Provider:  e.Provider,
		ID:        e.ID,
		Context:   e.Context,
		ToolCall:  e.ToolCall,
		Reasoning: e.Reasoning,
		Released:  e.Released,
		Source:    string(e.Source),
	}
}

// modelRow is one row of `foo model list`.
//
// Table columns are the four a person picking a model needs — who
// serves it, what to type, how much context, whether tools work — plus
// reasoning and release date, which is what distinguishes near-
// identical ids within one provider. Source is carried on the
// structured formats but kept off the table: with a single source
// wired every row would repeat the same value, and it earns a column
// only once a listing can mix origins.
type modelRow struct {
	Provider  string `json:"provider" yaml:"provider" table:"PROVIDER,priority=9"`
	ID        string `json:"id" yaml:"id" table:"ID,priority=8"`
	Context   int    `json:"context" yaml:"context" table:"CONTEXT,priority=7"`
	ToolCall  bool   `json:"tool_call" yaml:"tool_call" table:"TOOLS,priority=6"`
	Reasoning bool   `json:"reasoning" yaml:"reasoning" table:"REASONING,priority=5"`
	Released  string `json:"released,omitempty" yaml:"released,omitempty" table:"RELEASED,priority=4"`
	Source    string `json:"source" yaml:"source"`
}
