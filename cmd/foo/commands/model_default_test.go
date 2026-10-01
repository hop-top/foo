package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// modelDefaultUserFile is a hand-written user config: comments, blank
// lines, 4-space indent, a key foo does not know.
const modelDefaultUserFile = `# my foo config
model: old-model   # pinned for now

# looks
accent: "#123456"

secrets:
    backend: env    # default store
budget: cheap
x_unknown: [a, b]
`

// TestModelDefault_WritesOnlyModel is the defect: `foo model default`
// saved the whole merged config, so a project .foo.yaml, FOO_* env,
// -c overrides and --profile were written into the user file, which
// also lost its comments and layout. Only the model line may change.
func TestModelDefault_WritesOnlyModel(t *testing.T) {
	storeEnv(t, "")
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "foo", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(modelDefaultUserFile), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	if err := os.WriteFile(".foo.yaml", []byte("accent: \"#PROJECT\"\npatterns_path: /from/project\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FOO_BUDGET", "premium")

	r := New("test")
	var out, errOut bytes.Buffer
	r.Cmd.SetOut(&out)
	r.Cmd.SetErr(&errOut)
	r.Cmd.SetIn(strings.NewReader(""))
	r.Cmd.SetArgs([]string{"-c", "secrets.prefix=FROM_C_", "--profile", "work", "model", "default", "gpt-4o"})
	if err := r.Cmd.Execute(); err != nil {
		t.Fatalf("model default: %v (stderr %s)", err, errOut.String())
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(modelDefaultUserFile, "model: old-model ", "model: gpt-4o ", 1)
	if string(got) != want {
		t.Errorf("user file =\n%s\nwant\n%s", got, want)
	}
	if !strings.Contains(out.String(), `default model set to "gpt-4o"`) {
		t.Errorf("stdout = %q", out.String())
	}
}

// TestModelDefault_WarnsWhenOverridden: a higher layer that still sets
// model means the saved default does not take effect here; say so on
// stderr, naming nothing secret, and still save.
func TestModelDefault_WarnsWhenOverridden(t *testing.T) {
	storeEnv(t, "")
	t.Chdir(t.TempDir())
	t.Setenv("FOO_MODEL", "env-model")

	r := New("test")
	var out, errOut bytes.Buffer
	r.Cmd.SetOut(&out)
	r.Cmd.SetErr(&errOut)
	r.Cmd.SetIn(strings.NewReader(""))
	r.Cmd.SetArgs([]string{"model", "default", "gpt-4o"})
	if err := r.Cmd.Execute(); err != nil {
		t.Fatalf("model default: %v", err)
	}
	if !strings.Contains(errOut.String(), `"env-model"`) {
		t.Errorf("stderr = %q, want a note that env-model still wins", errOut.String())
	}
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "foo", "config.yaml")
	if got, _ := os.ReadFile(path); string(got) != "model: gpt-4o\n" {
		t.Errorf("user file = %q, want only the model", got)
	}
}
