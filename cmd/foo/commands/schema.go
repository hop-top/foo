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
	kitcli "hop.top/kit/go/console/cli"
	"hop.top/kit/go/core/xdg"
	"hop.top/kit/go/storage/sqlstore"
)

// schemaSchemaVersion is the schema-store revision recorded in
// pre-migrate backup filenames. Bump when schema.NewStore's table
// layout changes.
const schemaSchemaVersion = 1

func schemaCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schema",
		Short: "Manage JSON schemas",
		Long: `Persist named JSON Schemas (and their source DSL) and use them
when prompting the model for structured output.`,
	}

	cmd.AddCommand(schemaListCmd())
	cmd.AddCommand(schemaShowCmd())
	cmd.AddCommand(schemaCreateCmd())
	cmd.AddCommand(schemaDeleteCmd())
	cmd.AddCommand(schemaCompileCmd())

	return cmd
}

func schemaListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List stored schemas",
		Long:  "List every schema persisted in the local schema store.",
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
	kitcli.SetSideEffect(cmd, kitcli.SideEffectRead)
	return cmd
}

func schemaShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Show a stored schema",
		Long:  "Render the source DSL and compiled JSON Schema for one stored schema.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openSchemaStore()
			if err != nil {
				return err
			}

			sc, err := store.Get(args[0])
			if err != nil {
				return enrichSchemaNotFound(args[0], err)
			}

			return renderData(cmd, schemaView{Name: sc.Name, DSL: sc.DSL, Schema: sc.Schema})
		},
	}
	kitcli.SetSideEffect(cmd, kitcli.SideEffectRead)
	return cmd
}

func schemaCreateCmd() *cobra.Command {
	var filePath string

	cmd := &cobra.Command{
		Use:   "create <name> [dsl]",
		Short: "Create or replace a schema from DSL or JSON",
		Long: `Create or replace a schema in the local store from inline DSL or
a JSON Schema file supplied via --file. The DSL is compiled before
persistence; the JSON path stores the schema verbatim.`,
		Args: cobra.RangeArgs(1, 2),
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
				publishEvent(cmd.Context(), "foo.knowledge.schema.created", map[string]any{"name": name, "source": filePath})
				fmt.Fprintf(cmd.OutOrStdout(), "schema %q saved from %s\n", name, filePath)
				return nil
			}

			if len(args) < 2 {
				return fmt.Errorf("provide DSL string or --file")
			}

			if _, err := store.Set(name, args[1]); err != nil {
				return err
			}
			publishEvent(cmd.Context(), "foo.knowledge.schema.created", map[string]any{"name": name})
			fmt.Fprintf(cmd.OutOrStdout(), "schema %q saved\n", name)
			return nil
		},
	}

	kitcli.SetSideEffect(cmd, kitcli.SideEffectWriteLocal)
	cmd.Flags().StringVar(&filePath, "file", "", "JSON schema file")
	return cmd
}

func schemaDeleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a schema",
		Long:  "Remove a schema from the local store. Local irreversible.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openSchemaStore()
			if err != nil {
				return err
			}

			if err := store.Remove(args[0]); err != nil {
				return enrichSchemaNotFound(args[0], err)
			}
			publishEvent(cmd.Context(), "foo.knowledge.schema.deleted", map[string]any{"name": args[0]})
			fmt.Fprintf(cmd.OutOrStdout(), "schema %q deleted\n", args[0])
			return nil
		},
	}
	kitcli.SetSideEffect(cmd, kitcli.SideEffectDestructiveLocal)
	return cmd
}

func schemaCompileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compile <shorthand>",
		Short: "Compile a DSL without storing it",
		Long:  "Compile a DSL shorthand into a JSON Schema and render it. No persistence.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			compiled, err := schema.CompileDSL(args[0])
			if err != nil {
				return err
			}
			publishEvent(cmd.Context(), "foo.knowledge.schema.compiled", map[string]any{"dsl": args[0]})
			return renderData(cmd, schemaView{Name: "", DSL: args[0], Schema: compiled})
		},
	}
	kitcli.SetSideEffect(cmd, kitcli.SideEffectRead)
	kitcli.SetIdempotency(cmd, kitcli.IdempotencyYes)
	return cmd
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

	// Back up the live DB into <stateDir>/.dbs/ before NewStore migrates.
	// No-op on first run; timestamped copy otherwise. Backups stay in a
	// hidden .dbs sibling, never beside the live DB.
	if _, err := sqlstore.BackupBeforeMigrate(dbPath, schemaSchemaVersion, sqlstore.WithBackupDir(filepath.Join(stateDir, ".dbs"))); err != nil {
		return nil, fmt.Errorf("backup schemas db: %w", err)
	}

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
