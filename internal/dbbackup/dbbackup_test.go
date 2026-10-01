package dbbackup

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// newDB writes a SQLite file at path holding one table, with version
// recorded in its header.
func newDB(t *testing.T, path string, version int) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t (x TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		t.Fatal(err)
	}
}

// backups lists the .bak files in dir, sorted.
func backups(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".bak") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func TestBeforeMigrate_NoFileNoBackup(t *testing.T) {
	dir := t.TempDir()
	got, err := BeforeMigrate(filepath.Join(dir, "s.db"), 1)
	if err != nil || got != "" {
		t.Fatalf("BeforeMigrate = %q, %v; want no backup", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, Dir)); !os.IsNotExist(err) {
		t.Fatalf("backup dir created for a missing db: %v", err)
	}
}

// TestBeforeMigrate_CurrentNoBackup: a store already at the target
// revision has nothing to migrate, so opening it again and again
// leaves no copies behind.
func TestBeforeMigrate_CurrentNoBackup(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "s.db")
	newDB(t, db, 3)
	for i := 0; i < 3; i++ {
		got, err := BeforeMigrate(db, 3)
		if err != nil || got != "" {
			t.Fatalf("open %d: BeforeMigrate = %q, %v; want no backup", i, got, err)
		}
	}
	if b := backups(t, filepath.Join(dir, Dir)); len(b) != 0 {
		t.Fatalf("backups of an up-to-date store: %v", b)
	}
}

// TestBeforeMigrate_NewerNoBackup: a file stamped by a newer foo is
// not migrated (stores never lower the stamp), so no copy either.
func TestBeforeMigrate_NewerNoBackup(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "s.db")
	newDB(t, db, 5)
	got, err := BeforeMigrate(db, 2)
	if err != nil || got != "" {
		t.Fatalf("BeforeMigrate = %q, %v; want no backup", got, err)
	}
}

func TestBeforeMigrate_PendingOneBackup(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "s.db")
	newDB(t, db, 0)
	got, err := BeforeMigrate(db, 1)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(got) != filepath.Join(dir, Dir) {
		t.Fatalf("backup %q not under %s", got, Dir)
	}
	if !strings.HasPrefix(filepath.Base(got), "s.pre-v1.") {
		t.Fatalf("backup %q not labeled with the target revision", got)
	}
	if b := backups(t, filepath.Join(dir, Dir)); len(b) != 1 {
		t.Fatalf("backups = %v, want exactly one", b)
	}
	// The copy is the pre-migration file: still a db at the old revision.
	if v, err := Version(got); err != nil || v != 0 {
		t.Fatalf("backup revision = %d, %v; want 0", v, err)
	}
}

// TestBeforeMigrate_UnreadableBacksUp: a file whose revision cannot be
// read gets a copy; doubt resolves toward keeping the data.
func TestBeforeMigrate_UnreadableBacksUp(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "s.db")
	if err := os.WriteFile(db, []byte("not a sqlite database, just bytes......"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := BeforeMigrate(db, 1)
	if err != nil || got == "" {
		t.Fatalf("BeforeMigrate = %q, %v; want a backup", got, err)
	}
}

// TestBeforeMigrate_Retention: after a backup the store keeps only its
// newest Keep copies; other stores' copies and unrelated files stay.
func TestBeforeMigrate_Retention(t *testing.T) {
	dir := t.TempDir()
	bdir := filepath.Join(dir, Dir)
	if err := os.MkdirAll(bdir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Clutter left by earlier foo builds: one copy per open.
	var old []string
	for i := 0; i < Keep+4; i++ {
		name := fmt.Sprintf("s.pre-v1.20250101-0000%02d.bak", i)
		old = append(old, name)
		if err := os.WriteFile(filepath.Join(bdir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	keepers := []string{
		"other.pre-v1.20240101-000000.bak", // another store's copy
		"s.db.bak",                         // not foo's naming
		"s.pre-vX.20240101-000000.bak",     // malformed revision
		"notes.txt",
	}
	for _, n := range keepers {
		if err := os.WriteFile(filepath.Join(bdir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	db := filepath.Join(dir, "s.db")
	newDB(t, db, 0)
	got, err := BeforeMigrate(db, 1)
	if err != nil {
		t.Fatal(err)
	}

	var mine []string
	for _, n := range backups(t, bdir) {
		if strings.HasPrefix(n, "s.pre-v1.") {
			mine = append(mine, n)
		}
	}
	if len(mine) != Keep {
		t.Fatalf("store backups = %d (%v), want %d", len(mine), mine, Keep)
	}
	want := append([]string{}, old[len(old)-(Keep-1):]...)
	want = append(want, filepath.Base(got))
	sort.Strings(want)
	if strings.Join(mine, ",") != strings.Join(want, ",") {
		t.Fatalf("kept %v, want newest %v", mine, want)
	}
	for _, n := range keepers {
		if _, err := os.Stat(filepath.Join(bdir, n)); err != nil {
			t.Errorf("unrelated %s removed: %v", n, err)
		}
	}
}

// TestPrune_OrdersByTimestampNotRevision: pre-v10 sorts before pre-v9
// as text; age decides what goes, not the label.
func TestPrune_OrdersByTimestampNotRevision(t *testing.T) {
	bdir := t.TempDir()
	names := []string{
		"s.pre-v10.20250103-000000.bak",
		"s.pre-v9.20250102-000000.bak",
		"s.pre-v2.20250101-000000.bak",
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(bdir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := Prune(bdir, "s.db", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || filepath.Base(removed[0]) != "s.pre-v2.20250101-000000.bak" {
		t.Fatalf("removed %v, want only the oldest", removed)
	}
}

func TestPrune_MissingDir(t *testing.T) {
	removed, err := Prune(filepath.Join(t.TempDir(), "absent"), "s.db", 1)
	if err != nil || len(removed) != 0 {
		t.Fatalf("Prune = %v, %v; want nothing", removed, err)
	}
}

func TestVersion(t *testing.T) {
	db := filepath.Join(t.TempDir(), "s.db")
	newDB(t, db, 7)
	v, err := Version(db)
	if err != nil || v != 7 {
		t.Fatalf("Version = %d, %v; want 7", v, err)
	}
	// Reading never writes: no journal or WAL sidecar left behind.
	entries, _ := os.ReadDir(filepath.Dir(db))
	if len(entries) != 1 {
		t.Fatalf("read left files: %v", entries)
	}
}
