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
	"hop.top/kit/go/core/xdg"
)

func schemaCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schema",
		Short: "Manage JSON schemas",
	}

	cmd.AddCommand(schemaListCmd())
	cmd.AddCommand(schemaShowCmd())
	cmd.AddCommand(schemaCreateCmd())
	cmd.AddCommand(schemaDeleteCmd())
	cmd.AddCommand(schemaCompileCmd())

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

			if len(names) == 0 {
				return nil
			}
			rows := make([]schemaRow, 0, len(names))
			for _, name := range names {
				rows = append(rows, schemaRow{Name: name})
			}
			return renderData(cmd, rows)
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

			return renderData(cmd, schemaView{Name: sc.Name, DSL: sc.DSL, Schema: sc.Schema})
		},
	}
}

func schemaCreateCmd() *cobra.Command {
	var filePath string

	cmd := &cobra.Command{
		Use:   "create <name> [dsl]",
		Short: "Create or replace a schema from DSL or JSON",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			store, err := openSchemaStore()
			if err != nil {
				return err
			}

			if filePath != "" {
				data, err := os.ReadFile(filePath)
				if err != nil {
					return fmt.Errorf("read file: %w", err)
				}

				if _, err := store.SetJSON(name, json.RawMessage(data)); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "schema %q saved from %s\n", name, filePath)
				return nil
			}

			if len(args) < 2 {
				return fmt.Errorf("provide DSL string or --file")
			}

			if _, err := store.Set(name, args[1]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "schema %q saved\n", name)
			return nil
		},
	}

	cmd.Flags().StringVar(&filePath, "file", "", "JSON schema file")
	return cmd
}

func schemaDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a schema",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openSchemaStore()
			if err != nil {
				return err
			}

			if err := store.Remove(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "schema %q deleted\n", args[0])
			return nil
		},
	}
}

func schemaCompileCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "compile <shorthand>",
		Short: "Compile a DSL without storing it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			compiled, err := schema.CompileDSL(args[0])
			if err != nil {
				return err
			}
			return renderData(cmd, schemaView{Name: "", DSL: args[0], Schema: compiled})
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

type schemaRow struct {
	Name string `json:"name" yaml:"name" table:"NAME,priority=9"`
}

type schemaView struct {
	Name   string         `json:"name,omitempty" yaml:"name,omitempty" table:"NAME,priority=9"`
	DSL    string         `json:"dsl,omitempty" yaml:"dsl,omitempty"`
	Schema map[string]any `json:"schema" yaml:"schema"`
}
