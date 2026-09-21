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

// modelListLimit backs --limit. It is the one display knob this command
// owns; content filters (provider, tool-call, modality) are a separate
// surface and deliberately absent here.
var modelListLimit int

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
through --format json for machine consumption.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runModelList(cmd.Context(), cmd, modelListLimit)
		},
	}
	cmd.Flags().IntVar(&modelListLimit, "limit", llm.DefaultListLimit,
		"maximum models to list (0 for no limit)")
	kitcli.SetSideEffect(cmd, kitcli.SideEffectRead)
	return cmd
}

// runModelList is split out of RunE so tests drive the whole path —
// source read, ranking, truncation, render, hint — against a fixture
// source and a captured writer.
func runModelList(ctx context.Context, cmd *cobra.Command, limit int) error {
	entries, err := llm.ListModels(ctx, modelCatalogSource)
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
