package commands

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
	"github.com/spf13/cobra"
	"hop.top/foo/internal/schema"
	"hop.top/kit/xdg"
)

func schemaCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schema",
		Short: "Manage JSON schemas",
	}

	cmd.AddCommand(schemaListCmd())
	cmd.AddCommand(schemaShowCmd())
	cmd.AddCommand(schemaSetCmd())
	cmd.AddCommand(schemaRemoveCmd())
	cmd.AddCommand(schemaDSLCmd())

	return cmd
}

func schemaListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List stored schemas",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openSchemaStore()
			if err != nil {
				return err
			}

			names, err := store.List()
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if len(names) == 0 {
				fmt.Fprintln(out, "No schemas stored")
				return nil
			}

			for _, name := range names {
				fmt.Fprintln(out, name)
			}
			return nil
		},
	}
}

func schemaShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Show a stored schema",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openSchemaStore()
			if err != nil {
				return err
			}

			sc, err := store.Get(args[0])
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			b, _ := json.MarshalIndent(sc.Schema, "", "  ")
			fmt.Fprintln(out, string(b))

			if sc.DSL != "" {
				fmt.Fprintf(out, "\nDSL: %s\n", sc.DSL)
			}
			return nil
		},
	}
}

func schemaSetCmd() *cobra.Command {
	var filePath string

	cmd := &cobra.Command{
		Use:   "set <name> [dsl]",
		Short: "Create or update a schema from DSL or JSON file",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			store, err := openSchemaStore()
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()

			if filePath != "" {
				data, err := os.ReadFile(filePath)
				if err != nil {
					return fmt.Errorf("read file: %w", err)
				}

				sc, err := store.SetJSON(name, json.RawMessage(data))
				if err != nil {
					return err
				}

				b, _ := json.MarshalIndent(sc.Schema, "", "  ")
				fmt.Fprintf(out, "Schema %q set from %s\n%s\n",
					name, filePath, string(b))
				return nil
			}

			if len(args) < 2 {
				return fmt.Errorf("provide DSL string or --file")
			}

			sc, err := store.Set(name, args[1])
			if err != nil {
				return err
			}

			b, _ := json.MarshalIndent(sc.Schema, "", "  ")
			fmt.Fprintf(out, "Schema %q set\n%s\n", name, string(b))
			return nil
		},
	}

	cmd.Flags().StringVar(&filePath, "file", "", "JSON schema file")
	return cmd
}

func schemaRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a stored schema",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openSchemaStore()
			if err != nil {
				return err
			}

			if err := store.Remove(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Schema %q removed\n", args[0])
			return nil
		},
	}
}

func schemaDSLCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dsl <shorthand>",
		Short: "Preview a DSL without storing",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			output, err := schema.CompileDSLJSON(args[0])
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), output)
			return nil
		},
	}
}

func openSchemaStore() (*schema.Store, error) {
	stateDir, err := xdg.StateDir("foo")
	if err != nil {
		return nil, fmt.Errorf("state dir: %w", err)
	}
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, err
	}

	dbPath := filepath.Join(stateDir, "schemas.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	return schema.NewStore(db)
}
