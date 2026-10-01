package commands

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// storeStateDir points XDG_STATE_HOME at a fresh temp dir and returns
// foo's state dir under it.
func storeStateDir(t *testing.T) string {
	t.Helper()
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", state)
	return filepath.Join(state, "foo")
}

// storeBackups lists the store's pre-migration copies in .dbs.
func storeBackups(t *testing.T, stateDir, base string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(stateDir, ".dbs"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), base+".pre-v") && strings.HasSuffix(e.Name(), ".bak") {
			out = append(out, e.Name())
		}
	}
	return out
}

// legacyStore writes a store file as foo builds before the revision
// stamp left it: the table present, user_version 0.
func legacyStore(t *testing.T, path, ddl string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
}

var storeOpeners = []struct {
	name string
	base string
	ddl  string
	open func() error
}{
	{
		name: "schema", base: "schemas",
		ddl: `CREATE TABLE schemas (name TEXT PRIMARY KEY, dsl TEXT NOT NULL DEFAULT '',
			schema TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		open: func() error { _, err := openSchemaStore(); return err },
	},
	{
		name: "embed", base: "embeddings",
		ddl: `CREATE TABLE embeddings (id TEXT PRIMARY KEY, collection TEXT NOT NULL,
			content_hash TEXT NOT NULL, vector BLOB NOT NULL, metadata TEXT,
			created_at TEXT NOT NULL, UNIQUE(collection, content_hash))`,
		open: func() error { _, err := openEmbedStore(); return err },
	},
}

// TestStoreOpen_NoBackupWithoutMigration: every `foo schema list` or
// `foo embed search` used to leave a fresh .dbs/*.bak because the
// backup ran on each open. An up-to-date store opens without a copy.
func TestStoreOpen_NoBackupWithoutMigration(t *testing.T) {
	for _, s := range storeOpeners {
		t.Run(s.name, func(t *testing.T) {
			stateDir := storeStateDir(t)
			for i := 0; i < 4; i++ {
				if err := s.open(); err != nil {
					t.Fatalf("open %d: %v", i, err)
				}
			}
			if b := storeBackups(t, stateDir, s.base); len(b) != 0 {
				t.Fatalf("backups after repeated opens of a current store: %v", b)
			}
		})
	}
}

// TestStoreOpen_LegacyStoreBackedUpOnce: a store written before the
// revision stamp has a migration pending (the stamp itself); the first
// open copies it once, later opens do not.
func TestStoreOpen_LegacyStoreBackedUpOnce(t *testing.T) {
	for _, s := range storeOpeners {
		t.Run(s.name, func(t *testing.T) {
			stateDir := storeStateDir(t)
			legacyStore(t, filepath.Join(stateDir, s.base+".db"), s.ddl)
			for i := 0; i < 3; i++ {
				if err := s.open(); err != nil {
					t.Fatalf("open %d: %v", i, err)
				}
			}
			if b := storeBackups(t, stateDir, s.base); len(b) != 1 {
				t.Fatalf("backups = %v, want exactly one", b)
			}
		})
	}
}
