package schema

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Schema is a named, stored JSON Schema compiled from DSL.
type Schema struct {
	Name      string         `json:"name"`
	DSL       string         `json:"dsl"`
	Schema    map[string]any `json:"schema"`
	CreatedAt string         `json:"created_at"`
	UpdatedAt string         `json:"updated_at"`
}

// Store persists named schemas in SQLite.
type Store struct {
	db *sql.DB
}

// NewStore creates a schema store backed by SQLite.
func NewStore(db *sql.DB) (*Store, error) {
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate schemas: %w", err)
	}
	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS schemas (
			name       TEXT PRIMARY KEY,
			dsl        TEXT NOT NULL DEFAULT '',
			schema     TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)
	`)
	return err
}

// Set stores a schema compiled from DSL under the given name.
func (s *Store) Set(name, dsl string) (*Schema, error) {
	compiled, err := CompileDSL(dsl)
	if err != nil {
		return nil, fmt.Errorf("compile dsl: %w", err)
	}

	return s.upsert(name, dsl, compiled)
}

// SetJSON stores a raw JSON Schema under the given name.
func (s *Store) SetJSON(name string, raw json.RawMessage) (*Schema, error) {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("invalid JSON schema: %w", err)
	}
	return s.upsert(name, "", obj)
}

func (s *Store) upsert(name, dsl string, schemaObj map[string]any) (*Schema, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	schemaJSON, err := json.Marshal(schemaObj)
	if err != nil {
		return nil, fmt.Errorf("marshal schema: %w", err)
	}

	_, err = s.db.Exec(`
		INSERT INTO schemas (name, dsl, schema, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			dsl = excluded.dsl,
			schema = excluded.schema,
			updated_at = excluded.updated_at
	`, name, dsl, string(schemaJSON), now, now)
	if err != nil {
		return nil, fmt.Errorf("store schema: %w", err)
	}

	return &Schema{
		Name:      name,
		DSL:       dsl,
		Schema:    schemaObj,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// Get retrieves a named schema.
func (s *Store) Get(name string) (*Schema, error) {
	var sc Schema
	var schemaJSON string

	err := s.db.QueryRow(`
		SELECT name, dsl, schema, created_at, updated_at
		FROM schemas WHERE name = ?
	`, name).Scan(&sc.Name, &sc.DSL, &schemaJSON, &sc.CreatedAt, &sc.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("schema %q not found", name)
	}
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal([]byte(schemaJSON), &sc.Schema); err != nil {
		return nil, fmt.Errorf("unmarshal schema: %w", err)
	}
	return &sc, nil
}

// Remove deletes a named schema.
func (s *Store) Remove(name string) error {
	res, err := s.db.Exec(`DELETE FROM schemas WHERE name = ?`, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("schema %q not found", name)
	}
	return nil
}

// List returns all stored schema names.
func (s *Store) List() ([]string, error) {
	rows, err := s.db.Query(`SELECT name FROM schemas ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}
