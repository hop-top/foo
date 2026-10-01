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
	"hop.top/foo/internal/tool/shim"
	kitcli "hop.top/kit/go/console/cli"
	"hop.top/kit/go/console/output"
)

func toolCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tool",
		Short: "Inspect tools available to -T",
		Long: `Inspect the tools a prompt can enable with -T/--tool: foo's
built-in tools, the OS commands foo runs from tool specs, and every
foo-tool-<name> executable on $PATH. Install links so other hosts can
run foo's spec tools as foo-tool-<name> plugins.`,
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List tools available to -T",
		Long: `List every tool -T/--tool accepts, with where it comes from and
the description the model sees.

SOURCE is "builtin" for tools compiled into foo and for OS commands run
from foo's own tool specs; "system:<path>" or "user:<path>" for a spec
read from /etc/xdg/foo/tools or foo's config dir (marked "(overrides
builtin)" when it replaces one of foo's); or the absolute path of the
foo-tool-<name> binary found on $PATH. PARAMS is true when the tool
declares arguments for the model to fill in. SIDE-EFFECT is what a spec
tool, or a plugin that declares foo_tool annotations, does (read,
write, destructive); "unknown" for a plugin that declares none. PATHS
lists the path arguments foo checks, with their operations (src:r
dst:w); "ungated" for a plugin that declares none: foo cannot check
paths a plugin does not declare.

STATUS is "active" for every tool -T accepts. A name has one owner:
foo's built-ins, then specs, then $PATH plugins; a later claimant is
listed as "shadowed" and never runs. A spec that fails to load is left
out with a warning and still owns its name.

A binary's name, description and parameters come from its --ext-info
output, so listing runs each foo-tool-* binary once with --ext-info
(links to foo itself and shadowed names are skipped). A binary whose
"parameters" is not a JSON Schema object of type "object", or whose
"foo_tool" annotations foo cannot enforce, is left out, with a warning
on stderr; -T warns only when it names that tool.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ts, err := discoverTools(nil, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			tools := ts.registry.List()
			rows := make([]toolRow, 0, len(tools)+len(ts.shadowed))
			for _, t := range tools {
				rows = append(rows, toolRowOf(t))
			}
			return renderData(cmd, append(rows, ts.shadowed...))
		},
	}
	kitcli.SetSideEffect(listCmd, kitcli.SideEffectRead)
	cmd.AddCommand(listCmd, toolInstallCmd(), toolUninstallCmd())

	return cmd
}

// selectedTools resolves -T into the registry the dispatcher runs with,
// or nil when -T was not given. It is the single discovery pass for a
// prompt run: validation and dispatch share its result, because every
// pass execs each foo-tool-* binary.
// Plugins skipped during discovery are reported on warn. Shim tools
// ask their approval questions through p, as do other tools under
// --tools-approve.
func selectedTools(warn io.Writer, p *tool.Prompter) (*tool.Registry, error) {
	if len(toolNames) == 0 {
		return nil, nil
	}
	return buildRegistry(toolNames, warn, shimAccess{confirm: p, approveAll: toolsApprove})
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
// A name owned by a spec that failed to load reports the spec's error.
func enrichUnknownTools(err error, invalid map[string]*shim.Invalid) error {
	var unknown *tool.UnknownToolError
	if !errors.As(err, &unknown) {
		return err
	}
	for _, n := range unknown.Unknown {
		if inv, ok := invalid[n]; ok {
			return output.NotFoundError(fmt.Sprintf("tool %q is unavailable: %v (run `foo tool list` for details)", n, inv))
		}
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
	SideEffect  string `json:"side_effect" yaml:"side_effect" table:"SIDE-EFFECT,priority=5"`
	Paths       string `json:"paths" yaml:"paths" table:"PATHS,priority=4"`
	Status      string `json:"status" yaml:"status" table:"STATUS,priority=6"`
}

const (
	statusActive      = "active"
	statusShadowed    = "shadowed"
	sideEffectUnknown = "unknown"
	pathsUngated      = "ungated"
)

// toolRowOf describes a registered tool.
func toolRowOf(t tool.Tool) toolRow {
	row := toolRow{
		Name:        t.Name(),
		Source:      tool.SourceOf(t),
		Description: t.Description(),
		Params:      tool.DeclaresParameters(t),
		Status:      statusActive,
	}
	switch v := t.(type) {
	case *shim.Tool:
		row.SideEffect, row.Paths = v.SideEffect(), v.PathSummary()
	case *tool.ExternalTool:
		row.SideEffect, row.Paths = sideEffectUnknown, pathsUngated
		if v.Gated() {
			row.SideEffect, row.Paths = v.SideEffect(), v.PathSummary()
		}
	default:
		row.SideEffect = string(kitcli.SideEffectRead)
	}
	return row
}

func toolInstallCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Link foo-tool-<name> plugins to foo for other hosts",
		Long: `Create a foo-tool-<name> symlink to the foo binary for every spec
tool (foo's own and valid user or system specs), so hosts that speak the
external plugin protocol can run them. Run through a link, foo checks
the arguments and paths exactly as it does for -T, and treats any
approval prompt as a denial.

-T never needs these links: foo runs its spec tools in-process. Links
go to --dir, default foo's bin home (off $PATH unless you add it).
Existing files and links that do not point at foo are left alone and
reported "skipped". Links foo made for specs that no longer exist are
removed. Running install again changes nothing.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, err := shimInstaller(dir)
			if err != nil {
				return err
			}
			cat := shim.Load(shim.DefaultLoadOptions())
			for _, inv := range cat.Invalid {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "[foo] warning: skipping %v\n", inv)
			}
			res, err := in.Install(cat.Specs)
			if err != nil {
				return err
			}
			if !onPath(in.Dir) {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "[foo] note: %s is not on $PATH; add it for other hosts to find the links\n", in.Dir)
			}
			return renderData(cmd, linkRows(res))
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "directory for the links (default: foo's bin home)")
	kitcli.SetSideEffect(cmd, kitcli.SideEffectWriteLocal)
	kitcli.SetIdempotency(cmd, kitcli.IdempotencyYes)
	// The link installer has no preview mode: kit refuses --dry-run
	// here rather than letting it through to a real install.
	kitcli.OptOutDryRun(cmd)
	return cmd
}

func toolUninstallCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the foo-tool-<name> links install created",
		Long: `Remove the foo-tool-<name> links in --dir (default foo's bin home)
that point at foo. Files and links that point anywhere else are never
touched. Running uninstall again changes nothing.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, err := shimInstaller(dir)
			if err != nil {
				return err
			}
			res, err := in.Uninstall()
			if err != nil {
				return err
			}
			return renderData(cmd, linkRows(res))
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "directory holding the links (default: foo's bin home)")
	kitcli.SetSideEffect(cmd, kitcli.SideEffectWriteLocal)
	kitcli.SetIdempotency(cmd, kitcli.IdempotencyYes)
	// No preview mode in the link installer; see toolInstallCmd.
	kitcli.OptOutDryRun(cmd)
	return cmd
}

// linkRows keeps an empty result a JSON array, not null.
func linkRows(res []shim.LinkResult) []shim.LinkResult {
	if res == nil {
		return []shim.LinkResult{}
	}
	return res
}
