package schema

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func userVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestNewStore_StampsSchemaVersion: migrating records the revision in
// the file header, so the next open sees nothing pending and takes no
// pre-migration backup.
func TestNewStore_StampsSchemaVersion(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if SchemaVersion < 1 {
		t.Fatalf("SchemaVersion = %d; a stamp of 0 reads as never migrated", SchemaVersion)
	}
	if _, err := NewStore(db); err != nil {
		t.Fatal(err)
	}
	if v := userVersion(t, db); v != SchemaVersion {
		t.Fatalf("user_version = %d, want %d", v, SchemaVersion)
	}
}

// TestNewStore_KeepsNewerStamp: a file stamped by a newer foo is
// never relabeled down to this binary's revision.
func TestNewStore_KeepsNewerStamp(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(db); err != nil {
		t.Fatal(err)
	}
	if v := userVersion(t, db); v != 99 {
		t.Fatalf("user_version = %d, want the newer 99 kept", v)
	}
}
