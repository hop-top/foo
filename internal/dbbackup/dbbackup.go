// Package dbbackup takes the pre-migration backups of foo's SQLite
// stores, only when a migration is pending, and bounds how many it
// keeps.
//
// Each store stamps the schema revision it migrates to into the file
// header (PRAGMA user_version). A store whose recorded revision is
// below the revision the running binary migrates to has a migration
// pending: BeforeMigrate copies it into the hidden .dbs directory
// beside it (kit's sqlstore.BackupBeforeMigrate does the copy) and
// then deletes that store's oldest copies beyond Keep. A store at or
// above the target revision opens without a copy.
package dbbackup

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"hop.top/kit/go/storage/sqlstore"
)

// Dir is the backup directory created beside each store file.
const Dir = ".dbs"

// Keep is how many pre-migration backups each store retains; older
// ones are deleted after each new backup.
const Keep = 5

// tsLayout is the timestamp kit's sqlstore.BackupBeforeMigrate puts in
// backup names: <name>.pre-v<revision>.<20060102-150405>.bak.
const tsLayout = "20060102-150405"

// BeforeMigrate backs dbPath up into <dir of dbPath>/.dbs/ when its
// recorded schema revision is below target, then prunes that store's
// backups to the newest Keep. It returns the new backup's path, or ""
// when nothing was copied: no file yet, or already at (or past)
// target. A file whose revision cannot be read is copied: doubt
// resolves toward keeping the data.
func BeforeMigrate(dbPath string, target int) (string, error) {
	if _, err := os.Stat(dbPath); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	if v, err := Version(dbPath); err == nil && v >= target {
		return "", nil
	}

	dir := filepath.Join(filepath.Dir(dbPath), Dir)
	path, err := sqlstore.BackupBeforeMigrate(dbPath, target, sqlstore.WithBackupDir(dir))
	if err != nil {
		return "", err
	}
	if _, err := Prune(dir, dbPath, Keep); err != nil {
		return path, fmt.Errorf("prune backups: %w", err)
	}
	return path, nil
}

// Version reads the schema revision recorded in dbPath's header. The
// file is opened read-only and never created.
func Version(dbPath string) (int, error) {
	dsn := (&url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	// A file that is not a database fails here ("file is not a
	// database") rather than reading as revision 0.
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

// Prune deletes all but the newest keep backups of dbPath's store in
// dir, oldest first by the timestamp in their names, and returns the
// paths it removed. Files not named like that store's backups are
// never touched. A missing dir is not an error.
func Prune(dir, dbPath string, keep int) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	base := filepath.Base(dbPath)
	prefix := strings.TrimSuffix(base, filepath.Ext(base)) + ".pre-v"
	type backup struct {
		name string
		rev  int
		ts   time.Time
	}
	var found []backup
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".bak") {
			continue
		}
		revStr, tsStr, ok := strings.Cut(strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".bak"), ".")
		if !ok {
			continue
		}
		rev, err := strconv.Atoi(revStr)
		if err != nil {
			continue
		}
		ts, err := time.Parse(tsLayout, tsStr)
		if err != nil {
			continue
		}
		found = append(found, backup{name: name, rev: rev, ts: ts})
	}
	if len(found) <= keep {
		return nil, nil
	}

	sort.Slice(found, func(i, j int) bool {
		if !found[i].ts.Equal(found[j].ts) {
			return found[i].ts.After(found[j].ts)
		}
		return found[i].rev > found[j].rev
	})
	var removed []string
	for _, b := range found[max(keep, 0):] {
		p := filepath.Join(dir, b.name)
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return removed, err
		}
		removed = append(removed, p)
	}
	return removed, nil
}
