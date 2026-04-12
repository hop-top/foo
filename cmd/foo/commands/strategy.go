package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	"hop.top/foo/internal/strategy"
)

func strategyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "strategy",
		Short: "Manage prompt strategies",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List available strategies",
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := strategy.NewManager()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			for _, s := range mgr.List() {
				desc := s.Description
				if desc == "" {
					desc = "(no description)"
				}
				fmt.Fprintf(out, "%-15s %s\n", s.Name, desc)
			}
			return nil
		},
	})

	return cmd
}
