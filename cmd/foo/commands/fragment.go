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
	}

	cmd.AddCommand(fragmentListCmd())
	cmd.AddCommand(fragmentCreateCmd())
	cmd.AddCommand(fragmentShowCmd())
	cmd.AddCommand(fragmentDeleteCmd())

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
		Short: "List fragment aliases",
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
				return nil
			}
			rows := make([]fragmentRow, 0, len(entries))
			for _, entry := range entries {
				rows = append(rows, fragmentRow{
					Alias:    entry.Alias,
					Source:   entry.Entry.Source,
					Artifact: shortID(entry.Entry.ArtifactID),
				})
			}
			return renderData(cmd, rows)
		},
	}
}

func fragmentCreateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create <alias> [source]",
		Short: "Create or replace a fragment from file, URL, or stdin",
		Long: `Create or replace a fragment alias. Source can be:
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

			publishEvent(ctx, "foo.fragment.created", map[string]any{"alias": alias})
			fmt.Fprintf(cmd.OutOrStdout(), "fragment %q saved\n", alias)
			return nil
		},
	}
}

func fragmentShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <alias>",
		Short: "Show one fragment",
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
			return renderData(cmd, fragmentView{Alias: args[0], Content: string(content)})
		},
	}
}

func fragmentDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <alias>",
		Short: "Delete a fragment alias",
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

			publishEvent(ctx, "foo.fragment.deleted", map[string]any{"alias": args[0]})
			fmt.Fprintf(cmd.OutOrStdout(), "fragment %q deleted\n", args[0])
			return nil
		},
	}
}

type fragmentRow struct {
	Alias    string `json:"alias" yaml:"alias" table:"ALIAS,priority=9"`
	Source   string `json:"source" yaml:"source" table:"SOURCE,priority=8"`
	Artifact string `json:"artifact" yaml:"artifact" table:"ARTIFACT,priority=7"`
}

type fragmentView struct {
	Alias   string `json:"alias" yaml:"alias" table:"ALIAS,priority=9"`
	Content string `json:"content" yaml:"content"`
}

func shortID(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}
