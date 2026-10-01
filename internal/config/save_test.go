package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// userFile is a hand-written user config: comments, blank lines,
// 4-space indent, a quoted value and a key foo does not know. Saving a
// setting must leave every byte of it alone except the setting's own.
const userFile = `# my foo config
model: old-model   # pinned for now

# looks
accent: "#123456"
patterns_path: /home/me/patterns

secrets:
    backend: keyring    # work laptop
    service: work
budget: cheap
x_unknown: [a, b]
`

// isolateUser points XDG_CONFIG_HOME at a temp dir, clears foo's env
// layer, moves into an empty project dir, and returns the user config
// path. body, when non-empty, is written there first.
func isolateUser(t *testing.T, body string) string {
	t.Helper()
	for _, v := range []string{"FOO_MODEL", "FOO_PATTERNS_PATH", "FOO_ACCENT", "FOO_BUDGET",
		"FOO_SECRETS_BACKEND", "FOO_SECRETS_PREFIX", "FOO_SECRETS_SERVICE"} {
		t.Setenv(v, "")
		_ = os.Unsetenv(v)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "foo", "config.yaml")
	if body != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func decode(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return m
}

// TestSetUser_OnlyTheKeyChanges is the defect: saving the default model
// wrote the whole merged config, so project-file, env and -c values
// landed in the user file and its own layout was lost. Every other
// byte must survive, and no other layer's value may appear.
func TestSetUser_OnlyTheKeyChanges(t *testing.T) {
	path := isolateUser(t, userFile)
	if err := os.WriteFile(".foo.yaml", []byte("accent: \"#PROJECT\"\nbudget: premium\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FOO_PATTERNS_PATH", "/from/env")
	t.Setenv("FOO_SECRETS_BACKEND", "env")
	if _, err := Load(LoadOptions{Overrides: map[string]any{"secrets": map[string]any{"prefix": "FROM_C_"}}}); err != nil {
		t.Fatal(err)
	}

	if err := SetUser("model", "gpt-4o"); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(userFile, "model: old-model ", "model: gpt-4o ", 1)
	if got := readFile(t, path); got != want {
		t.Errorf("user file =\n%s\nwant\n%s", got, want)
	}
}

// TestSetUser_AppendsMissingKey: a user file without the key gains one
// line at the end; the rest is untouched.
func TestSetUser_AppendsMissingKey(t *testing.T) {
	body := strings.Replace(userFile, "model: old-model   # pinned for now\n", "", 1)
	path := isolateUser(t, body)
	if err := SetUser("model", "gpt-4o"); err != nil {
		t.Fatal(err)
	}
	if got, want := readFile(t, path), body+"model: gpt-4o\n"; got != want {
		t.Errorf("user file =\n%s\nwant\n%s", got, want)
	}
}

// TestSetUser_AppendsAfterUnterminatedLine: a file whose last line has
// no newline still gets the key on a line of its own.
func TestSetUser_AppendsAfterUnterminatedLine(t *testing.T) {
	path := isolateUser(t, "budget: cheap")
	if err := SetUser("model", "gpt-4o"); err != nil {
		t.Fatal(err)
	}
	if got, want := readFile(t, path), "budget: cheap\nmodel: gpt-4o\n"; got != want {
		t.Errorf("user file = %q, want %q", got, want)
	}
}

// TestSetUser_CreatesFileWithOnlyTheKey: no user file yet. The new one
// holds the key and nothing from any other layer or the defaults.
func TestSetUser_CreatesFileWithOnlyTheKey(t *testing.T) {
	path := isolateUser(t, "")
	if err := os.WriteFile(".foo.yaml", []byte("accent: \"#PROJECT\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FOO_BUDGET", "premium")
	if _, err := Load(LoadOptions{}); err != nil {
		t.Fatal(err)
	}

	if err := SetUser("model", "gpt-4o"); err != nil {
		t.Fatal(err)
	}
	got := decode(t, readFile(t, path))
	if len(got) != 1 || got["model"] != "gpt-4o" {
		t.Errorf("new user file = %v, want only model: gpt-4o", got)
	}
}

// TestSetUser_CommentOnlyFile: a file holding only comments keeps them
// and gains the key.
func TestSetUser_CommentOnlyFile(t *testing.T) {
	path := isolateUser(t, "# nothing yet\n")
	if err := SetUser("model", "gpt-4o"); err != nil {
		t.Fatal(err)
	}
	if got, want := readFile(t, path), "# nothing yet\nmodel: gpt-4o\n"; got != want {
		t.Errorf("user file = %q, want %q", got, want)
	}
}

// TestSetUser_QuotesWhenNeeded: a value YAML would read as another type
// is written so it reads back as the string given.
func TestSetUser_QuotesWhenNeeded(t *testing.T) {
	for _, v := range []string{"yes", "1.5", "null", "a: b", "#hash", "it's"} {
		t.Run(v, func(t *testing.T) {
			path := isolateUser(t, userFile)
			if err := SetUser("model", v); err != nil {
				t.Fatal(err)
			}
			body := readFile(t, path)
			if got := decode(t, body)["model"]; got != v {
				t.Errorf("model reads back as %#v, want %q (file:\n%s)", got, v, body)
			}
			rest := decode(t, body)
			want := decode(t, userFile)
			delete(rest, "model")
			delete(want, "model")
			if !yamlEqual(t, rest, want) {
				t.Errorf("other keys changed: %v, want %v", rest, want)
			}
		})
	}
}

// TestSetUser_ReplacesQuotedValue: a quoted current value is replaced
// whole, comment kept.
func TestSetUser_ReplacesQuotedValue(t *testing.T) {
	for _, line := range []string{
		`model: "old \" model"  # c`,
		`model: 'it''s old'  # c`,
	} {
		t.Run(line, func(t *testing.T) {
			path := isolateUser(t, "budget: cheap\n"+line+"\naccent: x\n")
			if err := SetUser("model", "gpt-4o"); err != nil {
				t.Fatal(err)
			}
			if got, want := readFile(t, path), "budget: cheap\nmodel: gpt-4o  # c\naccent: x\n"; got != want {
				t.Errorf("user file = %q, want %q", got, want)
			}
		})
	}
}

// TestSetUser_UnspliceableValueKeepsOtherKeys: a value the line edit
// cannot replace in place (a block scalar) still saves, and every
// other key and value survives.
func TestSetUser_UnspliceableValueKeepsOtherKeys(t *testing.T) {
	rest := strings.Replace(userFile, "model: old-model   # pinned for now\n", "", 1)
	for name, head := range map[string]string{
		"block scalar":           "model: |\n  old\n  model\n",
		"continued plain scalar": "model: old\n  model\n",
		"continued quoted":       "model: \"old\n  model\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			body := head + rest
			path := isolateUser(t, body)
			if err := SetUser("model", "gpt-4o"); err != nil {
				t.Fatal(err)
			}
			got := decode(t, readFile(t, path))
			want := decode(t, body)
			want["model"] = "gpt-4o"
			if !yamlEqual(t, got, want) {
				t.Errorf("user file = %v, want %v", got, want)
			}
		})
	}
}

// TestSetUser_MalformedFileUntouched: a user file foo cannot parse is
// reported, not overwritten.
func TestSetUser_MalformedFileUntouched(t *testing.T) {
	body := "model: [unclosed\naccent: x\n"
	path := isolateUser(t, body)
	if err := SetUser("model", "gpt-4o"); err == nil {
		t.Error("malformed user file saved over without error")
	}
	if got := readFile(t, path); got != body {
		t.Errorf("malformed user file rewritten: %q", got)
	}
}

// TestSetUser_KeepsModeAndSymlink: an existing file keeps its mode, and
// a symlinked user config (a dotfiles checkout) is written through,
// not replaced.
func TestSetUser_KeepsModeAndSymlink(t *testing.T) {
	path := isolateUser(t, "")
	target := filepath.Join(t.TempDir(), "dotfiles-config.yaml")
	if err := os.WriteFile(target, []byte(userFile), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := SetUser("model", "gpt-4o"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(path); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("user config no longer a symlink (err %v)", err)
	}
	if fi, err := os.Stat(target); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("target mode = %v (err %v), want 0600 kept", fi.Mode().Perm(), err)
	}
	if got := decode(t, readFile(t, target))["model"]; got != "gpt-4o" {
		t.Errorf("target model = %v, want gpt-4o", got)
	}
}

// yamlEqual compares two decoded documents by re-encoding them.
func yamlEqual(t *testing.T, a, b map[string]any) bool {
	t.Helper()
	x, err := yaml.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	y, err := yaml.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return string(x) == string(y)
}
