package commands

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"hop.top/foo/internal/fragment"
	"hop.top/foo/internal/workspace"
)

func fragmentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fragment",
		Short: "Manage reusable prompt fragments",
		Aliases: []string{"frag"},
	}

	cmd.AddCommand(fragmentListCmd())
	cmd.AddCommand(fragmentSetCmd())
	cmd.AddCommand(fragmentShowCmd())
	cmd.AddCommand(fragmentRemoveCmd())

	return cmd
}

func newFragmentManager() (*fragment.Manager, error) {
	adapter, err := workspace.NewWSMAdapter(context.Background(), "")
	if err != nil {
		return nil, fmt.Errorf("init workspace: %w", err)
	}
	return fragment.NewManager(adapter, "default", "default"), nil
}

func fragmentListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all fragment aliases",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			mgr, err := newFragmentManager()
			if err != nil {
				return err
			}

			entries, err := mgr.List(ctx)
			if err != nil {
				return err
			}

			if len(entries) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No fragments found.")
				return nil
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%-20s %-10s %s\n", "ALIAS", "SOURCE", "ARTIFACT")
			fmt.Fprintf(out, "%-20s %-10s %s\n", "-----", "------", "--------")
			for _, e := range entries {
				artPrefix := e.Entry.ArtifactID
				if len(artPrefix) > 12 {
					artPrefix = artPrefix[:12]
				}
				fmt.Fprintf(out, "%-20s %-10s %s\n",
					e.Alias, e.Entry.Source, artPrefix)
			}

			return nil
		},
	}
}

func fragmentSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <alias> [source]",
		Short: "Set a fragment from file, URL, or stdin",
		Long: `Set a fragment alias. Source can be:
  - A file path
  - A URL (http:// or https://)
  - Omitted to read from stdin`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			mgr, err := newFragmentManager()
			if err != nil {
				return err
			}

			alias := args[0]

			if len(args) == 2 {
				// Source provided: file or URL
				if err := mgr.SetFromSource(ctx, alias, args[1]); err != nil {
					return err
				}
			} else {
				// Read from stdin
				if f, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(int(f.Fd())) {
					return fmt.Errorf("no source provided and stdin is a terminal; pipe content or provide a source argument")
				}
				if err := mgr.SetFromReader(ctx, alias, cmd.InOrStdin()); err != nil {
					return err
				}
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Fragment %q set\n", alias)
			return nil
		},
	}
}

func fragmentShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <alias>",
		Short: "Print fragment content to stdout",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			mgr, err := newFragmentManager()
			if err != nil {
				return err
			}

			content, err := mgr.Content(ctx, args[0])
			if err != nil {
				return err
			}

			fmt.Fprint(cmd.OutOrStdout(), string(content))
			return nil
		},
	}
}

func fragmentRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <alias>",
		Short: "Remove a fragment alias",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			mgr, err := newFragmentManager()
			if err != nil {
				return err
			}

			if err := mgr.RemoveWithIndex(ctx, args[0]); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Fragment %q removed\n", args[0])
			return nil
		},
	}
}
