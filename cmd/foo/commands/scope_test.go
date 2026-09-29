package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	kitscope "hop.top/kit/go/console/cli/scope"
)

// scopeEnv isolates a run and returns a canonical tree root holding
// p/ (readable), p/.env and outside/.
func scopeEnv(t *testing.T) string {
	t.Helper()
	newToolTestEnv(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"p/sub", "outside"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "p/.env"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeScopeYAML(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "foo", "scope.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

type scopeCheckRow struct {
	Path     string `json:"path"`
	Op       string `json:"op"`
	Decision string `json:"decision"`
}

func TestScopeCheck_UsesFooPolicy(t *testing.T) {
	root := scopeEnv(t)
	writeScopeYAML(t, "allow:\n  - path: \""+root+"/p/**\"\n    ops: [read]\n")
	env := toolTestEnv{}

	for _, tc := range []struct {
		path, op, decision string
		denied             bool
	}{
		{root + "/p/sub", "read", "allowed", false},
		{root + "/p/sub", "write", "unknown", true},
		{root + "/p/.env", "read", "denied", true}, // secret deny list
		{root + "/outside", "read", "unknown", true},
	} {
		stdout, _, _, err := runFooArgs(t, env, "scope", "check", tc.path, "--op", tc.op, "--format=json")
		if kitscope.IsDeniedExit(err) != tc.denied {
			t.Fatalf("check %s %s: err = %v; want denied=%v", tc.path, tc.op, err, tc.denied)
		}
		if !tc.denied && err != nil {
			t.Fatalf("check %s: %v", tc.path, err)
		}
		var row scopeCheckRow
		if err := json.Unmarshal([]byte(stdout), &row); err != nil {
			t.Fatalf("stdout not JSON (%v): %q", err, stdout)
		}
		if row.Decision != tc.decision || row.Op != tc.op {
			t.Errorf("check %s %s = %+v; want %s", tc.path, tc.op, row, tc.decision)
		}
	}
}

// check resolves paths as the gate does: a link's `..` climbs from the
// link target, where kit alone would clean it lexically.
func TestScopeCheck_ResolvesLikeTheGate(t *testing.T) {
	root := scopeEnv(t)
	writeScopeYAML(t, "allow:\n  - \""+root+"/p/**\"\n")
	if err := os.MkdirAll(filepath.Join(root, "outside/deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside/deep"), filepath.Join(root, "p/link")); err != nil {
		t.Fatal(err)
	}
	stdout, _, _, err := runFooArgs(t, toolTestEnv{}, "scope", "check", root+"/p/link/..", "--format=json")
	if !kitscope.IsDeniedExit(err) {
		t.Fatalf("err = %v; want denied", err)
	}
	var row scopeCheckRow
	if err := json.Unmarshal([]byte(stdout), &row); err != nil {
		t.Fatalf("stdout not JSON (%v): %q", err, stdout)
	}
	if row.Path != filepath.Join(root, "outside") {
		t.Fatalf("checked %q; want physical %q", row.Path, filepath.Join(root, "outside"))
	}
}

func TestScopeShow_NoScopeFileNamesPath(t *testing.T) {
	scopeEnv(t)
	stdout, stderr, _, err := runFooArgs(t, toolTestEnv{}, "scope", "show")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	want := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "foo", "scope.yaml")
	if !strings.Contains(stderr, want) {
		t.Fatalf("stderr %q should name %s", stderr, want)
	}
	if !strings.Contains(stdout, "MODE: strict") || !strings.Contains(stdout, "**/.env") {
		t.Fatalf("show should list the secret deny list: %q", stdout)
	}
}

func TestScopeShow_ListsRules(t *testing.T) {
	root := scopeEnv(t)
	writeScopeYAML(t, "mode: prompt\nallow:\n  - \""+root+"/p/**\"\n")
	stdout, stderr, _, err := runFooArgs(t, toolTestEnv{}, "scope", "show")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if strings.Contains(stderr, "no scope policy") {
		t.Fatalf("configured policy reported missing: %q", stderr)
	}
	if !strings.Contains(stdout, "MODE: prompt") || !strings.Contains(stdout, root+"/p/**") {
		t.Fatalf("show output %q lacks mode or rule", stdout)
	}
}

func TestScopeTest_AnyDeniedExits1(t *testing.T) {
	root := scopeEnv(t)
	writeScopeYAML(t, "allow:\n  - \""+root+"/p/**\"\n")
	_, _, _, err := runFooArgs(t, toolTestEnv{}, "scope", "test", root+"/p/sub", root+"/outside")
	if !kitscope.IsDeniedExit(err) {
		t.Fatalf("err = %v; want denied exit", err)
	}
	if _, _, _, err := runFooArgs(t, toolTestEnv{}, "scope", "test", root+"/p/sub"); err != nil {
		t.Fatalf("all allowed: %v", err)
	}
}

func TestScope_ToolFlagRejected(t *testing.T) {
	scopeEnv(t)
	_, _, _, err := runFooArgs(t, toolTestEnv{}, "scope", "show", "--tool", "other")
	if err == nil || !strings.Contains(err.Error(), "--tool") {
		t.Fatalf("err = %v; want --tool rejected", err)
	}
}

// kit's show reads the format from the global viper, not the flag.
func TestScopeShow_JSON(t *testing.T) {
	root := scopeEnv(t)
	writeScopeYAML(t, "allow:\n  - \""+root+"/p/**\"\n")
	stdout, _, _, err := runFooArgs(t, toolTestEnv{}, "scope", "show", "--format=json")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	var out struct {
		Mode  string `json:"mode"`
		Rules []struct {
			Verdict string `json:"verdict"`
			Pattern string `json:"pattern"`
		} `json:"rules"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("stdout not JSON (%v): %q", err, stdout)
	}
	if out.Mode != "strict" || len(out.Rules) < 2 {
		t.Fatalf("show json = %+v", out)
	}
}
