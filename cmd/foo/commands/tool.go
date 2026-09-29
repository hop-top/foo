// `foo tool list` — the discovery half of -T/--tool.
//
// -T selects tools by name from the registry buildRegistry assembles:
// foo's builtins plus every foo-tool-<name> executable on $PATH. Before
// this command the only way to learn a valid name was to read the
// source or scan $PATH by hand.

package commands

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"hop.top/foo/internal/suggest"
	"hop.top/foo/internal/tool"
	kitcli "hop.top/kit/go/console/cli"
	"hop.top/kit/go/console/output"
)

func toolCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tool",
		Short: "Inspect tools available to -T",
		Long: `Inspect the tools a prompt can enable with -T/--tool: foo's
built-in tools plus every foo-tool-<name> executable on $PATH.`,
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List tools available to -T",
		Long: `List every tool -T/--tool accepts, with where it comes from and
the description the model sees.

SOURCE is "builtin" for tools compiled into foo, or the absolute path of
the foo-tool-<name> binary found on $PATH. PARAMS is true when the
tool declares arguments for the model to fill in. A binary's name,
description and parameters come from its --ext-info output, so
listing runs each foo-tool-* binary once with --ext-info. A binary
whose "parameters" is not a JSON Schema object of type "object" is
left out, with a warning on stderr; -T warns only when it names that
tool.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			registry, err := buildRegistry(nil, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			tools := registry.List()
			rows := make([]toolRow, 0, len(tools))
			for _, t := range tools {
				rows = append(rows, toolRow{
					Name:        t.Name(),
					Source:      tool.SourceOf(t),
					Description: t.Description(),
					Params:      tool.DeclaresParameters(t),
				})
			}
			return renderData(cmd, rows)
		},
	}
	kitcli.SetSideEffect(listCmd, kitcli.SideEffectRead)
	cmd.AddCommand(listCmd)

	return cmd
}

// selectedTools resolves -T into the registry the dispatcher runs with,
// or nil when -T was not given. It is the single discovery pass for a
// prompt run: validation and dispatch share its result, because every
// pass execs each foo-tool-* binary.
// Plugins skipped during discovery are reported on warn.
func selectedTools(warn io.Writer) (*tool.Registry, error) {
	if len(toolNames) == 0 {
		return nil, nil
	}
	return buildRegistry(toolNames, warn)
}

// warnSkippedTool reports a foo-tool-* binary left out of the
// registry. With names set (a -T run) only a skip of a selected tool
// is reported, so an unrelated broken plugin does not add a warning to
// every prompt; `foo tool list` (no names) reports them all.
func warnSkippedTool(warn io.Writer, names []string, err error) {
	var invalid *tool.InvalidParametersError
	if len(names) > 0 && errors.As(err, &invalid) && !slices.Contains(names, invalid.Name) {
		return
	}
	_, _ = fmt.Fprintf(warn, "[foo] warning: skipping tool plugin %v\n", err)
}

// enrichUnknownTools turns a registry selection failure into the
// not-found error the user sees: the bad names, a closest match for a
// single near miss, every valid name, and a pointer at `foo tool list`.
// Exit code 3, matching unknown --pattern and --schema values.
func enrichUnknownTools(err error) error {
	var unknown *tool.UnknownToolError
	if !errors.As(err, &unknown) {
		return err
	}
	var b strings.Builder
	b.WriteString(unknown.Summary())
	guess := ""
	if len(unknown.Unknown) == 1 {
		guess = suggest.Closest(unknown.Unknown[0], unknown.Available, 2)
	}
	if guess != "" {
		fmt.Fprintf(&b, "; did you mean %q? Available tools: ", guess)
	} else {
		b.WriteString("; available tools: ")
	}
	fmt.Fprintf(&b, "%s (run `foo tool list` for details)", strings.Join(unknown.Available, ", "))
	return output.NotFoundError(b.String())
}

type toolRow struct {
	Name        string `json:"name" yaml:"name" table:"NAME,priority=9"`
	Source      string `json:"source" yaml:"source" table:"SOURCE,priority=7"`
	Description string `json:"description" yaml:"description" table:"DESCRIPTION,priority=8"`
	Params      bool   `json:"params" yaml:"params" table:"PARAMS,priority=6"`
}
