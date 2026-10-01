package llmxrr

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// committedCassettes is every testdata/cassettes dir in the module.
func committedCassettes(t *testing.T) []string {
	t.Helper()
	root := filepath.Join("..", "..")
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.IsDir() && d.Name() == "cassettes" && filepath.Base(filepath.Dir(path)) == "testdata" {
			dirs = append(dirs, path)
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) == 0 {
		t.Fatal("found no testdata/cassettes dir: the walk no longer reaches the module root")
	}
	return dirs
}

// Every committed cassette, whichever suite owns it, is scanned on a
// plain go test: a suite that records but never scans its own dir, or
// a replay suite that only runs under a build tag, cannot let a key or
// a stored failure slip in.
func TestCommittedCassettes(t *testing.T) {
	for _, dir := range committedCassettes(t) {
		if err := CheckNoSecrets(dir); err != nil {
			t.Error(err)
		}
		if err := CheckNoFailures(dir); err != nil {
			t.Error(err)
		}
	}
}

func TestCheckNoFailures(t *testing.T) {
	write := func(t *testing.T, name, body string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	ok := "xrr: \"1\"\nadapter: http\npayload:\n    status: 200\n"
	if err := CheckNoFailures(write(t, "http-a.resp.yaml", ok)); err != nil {
		t.Fatalf("2xx flagged: %v", err)
	}
	for name, body := range map[string]string{
		"status":   "xrr: \"1\"\nadapter: http\npayload:\n    status: 400\n",
		"envelope": "xrr: \"1\"\nadapter: http\nerror: 'dial tcp: connection refused'\npayload: {}\n",
	} {
		err := CheckNoFailures(write(t, "http-a.resp.yaml", body))
		if err == nil || !strings.Contains(err.Error(), "http-a.resp.yaml") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	exec := "xrr: \"1\"\nadapter: exec\nerror: exit status 1\npayload: {}\n"
	if err := CheckNoFailures(write(t, "exec-a.resp.yaml", exec)); err != nil {
		t.Errorf("exec failure flagged: %v", err)
	}
}
