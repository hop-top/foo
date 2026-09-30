package llm

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSeedDefaultPool_CleanHome writes the full seed when no llm.yaml
// exists. Verifies the file lands at the expected XDG path and the
// payload contains the three tiers' canonical aliases.
func TestSeedDefaultPool_CleanHome(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	wrote, err := SeedDefaultPool()
	if err != nil {
		t.Fatalf("SeedDefaultPool: %v", err)
	}
	if !wrote {
		t.Fatal("expected wrote=true on clean home")
	}

	path := filepath.Join(tmp, "hop", "llm.yaml")
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read seeded file: %v", readErr)
	}
	body := string(data)
	for _, want := range []string{
		"cheap-openai", "cheap-anthropic", "cheap-google",
		"balanced-openai", "balanced-anthropic", "balanced-google",
		"premium-openai", "premium-anthropic",
		"pool:",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("seed missing %q:\n%s", want, body)
		}
	}
	// Google premium tier is intentionally omitted from the seed (no
	// publicly-released ultra-class 2.0 model). Guard against accidental
	// re-introduction with a stale/wrong ID.
	for _, unwanted := range []string{"gemini-2.0-ultra", "premium-google"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("seed contains %q which is not a verified GA model:\n%s", unwanted, body)
		}
	}
}

// TestSeedDefaultPool_Idempotent confirms a second call after a
// successful seed is a no-op (returns wrote=false, file mtime
// unchanged at the byte level).
func TestSeedDefaultPool_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	wrote1, err := SeedDefaultPool()
	if err != nil || !wrote1 {
		t.Fatalf("first seed: wrote=%v err=%v", wrote1, err)
	}
	path := filepath.Join(tmp, "hop", "llm.yaml")
	before, _ := os.ReadFile(path)

	wrote2, err := SeedDefaultPool()
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if wrote2 {
		t.Fatal("expected wrote=false on second seed")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Errorf("file mutated on second seed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// plantLLMYAML plants an operator-authored llm.yaml under a throwaway
// XDG_CONFIG_HOME and returns its path.
func plantLLMYAML(t *testing.T, body string) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	if err := os.MkdirAll(filepath.Join(tmp, "hop"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(tmp, "hop", "llm.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write llm.yaml: %v", err)
	}
	return path
}

// assertUntouched fails unless path still holds want byte-for-byte,
// with mode 0600 (a replaced file comes back 0644).
func assertUntouched(t *testing.T, path, want string) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600 (file was replaced)", fi.Mode().Perm())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != want {
		t.Errorf("llm.yaml modified:\nwant:\n%s\ngot:\n%s", want, got)
	}
}

// An existing llm.yaml is the operator's file: no pool block is not an
// invitation to append one. Covers the shapes that used to be edited —
// providers only, empty, comments only, a bare document marker.
func TestSeedDefaultPool_ExistingFileNeverModified(t *testing.T) {
	for name, body := range map[string]string{
		"providers-only": "default: anthropic://claude-3-5-sonnet-latest\n" +
			"providers:\n  anthropic:\n    api_key: sk-ant-existing\n",
		"no-trailing-newline": "providers:\n  openai:\n    base_url: http://127.0.0.1:9/v1",
		"empty":               "",
		"comments-only":       "# pool comes later\n",
		"document-marker":     "---\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := plantLLMYAML(t, body)

			wrote, err := SeedDefaultPool()
			if err != nil {
				t.Fatalf("SeedDefaultPool: %v", err)
			}
			if wrote {
				t.Error("wrote=true for an existing llm.yaml")
			}
			assertUntouched(t, path, body)
		})
	}
}

// A file foo cannot read is still there; it is left alone.
func TestSeedDefaultPool_UnreadableFileLeftAlone(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads mode-0 files")
	}
	body := "providers: {}\n"
	path := plantLLMYAML(t, body)
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}

	wrote, err := SeedDefaultPool()
	if err != nil || wrote {
		t.Fatalf("SeedDefaultPool = (%v, %v), want (false, nil)", wrote, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	assertUntouched(t, path, body)
}

// llm.yaml as a symlink (dotfile managers do this) is the operator's
// file whether or not its target exists yet: a live link keeps its
// target's content, a dangling one stays a dangling link rather than
// being replaced by a regular file.
func TestSeedDefaultPool_SymlinkLeftAlone(t *testing.T) {
	t.Run("live", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "llm.yaml")
		body := "providers:\n  openai: {}\n"
		if err := os.WriteFile(target, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		tmp := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmp)
		link := filepath.Join(tmp, "hop", "llm.yaml")
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}

		if wrote, err := SeedDefaultPool(); err != nil || wrote {
			t.Fatalf("SeedDefaultPool = (%v, %v), want (false, nil)", wrote, err)
		}
		if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("llm.yaml no longer a symlink: %v %v", fi, err)
		}
		assertUntouched(t, target, body)
	})
	t.Run("dangling", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "not-yet", "llm.yaml")
		tmp := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmp)
		link := filepath.Join(tmp, "hop", "llm.yaml")
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}

		if wrote, err := SeedDefaultPool(); err != nil || wrote {
			t.Fatalf("SeedDefaultPool = (%v, %v), want (false, nil)", wrote, err)
		}
		fi, err := os.Lstat(link)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("dangling llm.yaml link replaced: %v %v", fi, err)
		}
		if got, _ := os.Readlink(link); got != target {
			t.Errorf("link target = %q, want %q", got, target)
		}
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Errorf("seed wrote through the link: stat target err = %v", err)
		}
	})
}

// The write itself never replaces a file: one that appears between the
// absence check and the write (a concurrent first run, the operator
// saving in an editor) wins.
func TestWriteNew_NeverReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "llm.yaml")
	body := "providers: {}\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	err := writeNew(path, []byte(defaultPoolYAML))
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("writeNew over an existing file: err = %v, want fs.ErrExist", err)
	}
	assertUntouched(t, path, body)
}

// TestSeedDefaultPool_ExistingPoolPreserved confirms an authored pool
// block is never overwritten — even an empty one expresses intent.
func TestSeedDefaultPool_ExistingPoolPreserved(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	if err := os.MkdirAll(filepath.Join(tmp, "hop"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	existing := `default: anthropic://claude-3-5-sonnet-latest
pool:
  - alias: solo
    scheme: openai
    model: gpt-4o
`
	path := filepath.Join(tmp, "hop", "llm.yaml")
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatalf("seed pre-existing: %v", err)
	}

	wrote, err := SeedDefaultPool()
	if err != nil {
		t.Fatalf("SeedDefaultPool: %v", err)
	}
	if wrote {
		t.Fatal("expected wrote=false when pool: already present")
	}

	data, _ := os.ReadFile(path)
	body := string(data)
	if strings.Contains(body, "cheap-openai") {
		t.Errorf("seed clobbered operator pool:\n%s", body)
	}
	if !strings.Contains(body, "alias: solo") {
		t.Errorf("operator alias lost:\n%s", body)
	}
}

// TestSeedDefaultPool_InvalidYAMLLeftAlone covers the "soft-fail on
// corrupt file" branch. SeedDefaultPool refuses to mutate a file it
// can't parse — the operator should fix the corruption first.
func TestSeedDefaultPool_InvalidYAMLLeftAlone(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	if err := os.MkdirAll(filepath.Join(tmp, "hop"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	corrupt := "this: is: not: valid yaml: ::: ::\n"
	path := filepath.Join(tmp, "hop", "llm.yaml")
	if err := os.WriteFile(path, []byte(corrupt), 0o644); err != nil {
		t.Fatalf("seed corrupt: %v", err)
	}

	wrote, err := SeedDefaultPool()
	if err != nil {
		t.Fatalf("err must be nil on soft fail: %v", err)
	}
	if wrote {
		t.Fatal("must not write when existing file is unparseable")
	}

	data, _ := os.ReadFile(path)
	if string(data) != corrupt {
		t.Errorf("file mutated despite parse failure:\n%s", string(data))
	}
}

func TestSeedPath_HonorsXDG(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	got, err := SeedPath()
	if err != nil {
		t.Fatalf("SeedPath: %v", err)
	}
	want := filepath.Join(tmp, "hop", "llm.yaml")
	if got != want {
		t.Errorf("SeedPath = %q, want %q", got, want)
	}
}
