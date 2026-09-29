package commands

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hop.top/kit/go/console/output"
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
	Reason   string `json:"reason"`
}

// exitCodeOf is the process exit status main would pick for err.
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ce interface{ AsCLIError() *output.Error }
	if errors.As(err, &ce) {
		if e := ce.AsCLIError(); e != nil && e.ExitCode != 0 {
			return e.ExitCode
		}
	}
	return 1
}

// check reports what a tool call would do with the path: the gate's
// verdict, not kit's raw decision, so an uncovered path in strict mode
// is denied rather than "unknown".
func TestScopeCheck_UsesFooPolicy(t *testing.T) {
	root := scopeEnv(t)
	writeScopeYAML(t, "allow:\n  - path: \""+root+"/p/**\"\n    ops: [read]\n")
	env := toolTestEnv{}

	for _, tc := range []struct {
		path, op, decision, reason string
		exit                       int
	}{
		{root + "/p/sub", "read", "allowed", "", 0},
		{root + "/p/sub", "write", "denied", "no scope allow rule covers write", 1},
		{root + "/p/.env", "read", "denied", "deny rule", 1}, // secret deny list
		{root + "/outside", "read", "denied", "no scope allow rule covers read", 1},
	} {
		stdout, _, _, err := runFooArgs(t, env, "scope", "check", tc.path, "--op", tc.op, "--format=json")
		if got := exitCodeOf(err); got != tc.exit {
			t.Fatalf("check %s %s: exit %d (%v); want %d", tc.path, tc.op, got, err, tc.exit)
		}
		var row scopeCheckRow
		if err := json.Unmarshal([]byte(stdout), &row); err != nil {
			t.Fatalf("stdout not JSON (%v): %q", err, stdout)
		}
		if row.Decision != tc.decision || row.Op != tc.op || !strings.Contains(row.Reason, tc.reason) {
			t.Errorf("check %s %s = %+v; want %s (%s)", tc.path, tc.op, row, tc.decision, tc.reason)
		}
	}
}

// In prompt and warn modes, a path a deny rule matches or no rule
// covers does not read as allowed: check reports what the gate does
// with it, and exits with a code of its own.
func TestScopeCheck_ModesMatchTheGate(t *testing.T) {
	for _, tc := range []struct {
		mode                   string
		covered, secret, other string
		exit                   int // for secret and other
	}{
		{"strict", "allowed", "denied", "denied", 1},
		{"prompt", "allowed", "prompt", "prompt", 8},
		{"warn", "allowed", "warn", "warn", 9},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			root := scopeEnv(t)
			writeScopeYAML(t, "mode: "+tc.mode+"\nallow:\n  - \""+root+"/p/**\"\n")
			for _, c := range []struct {
				path, decision string
				exit           int
			}{
				{root + "/p/sub", tc.covered, 0},
				{root + "/p/.env", tc.secret, tc.exit},
				{root + "/outside", tc.other, tc.exit},
			} {
				stdout, _, _, err := runFooArgs(t, toolTestEnv{}, "scope", "check", c.path, "--format=json")
				if got := exitCodeOf(err); got != c.exit {
					t.Fatalf("check %s: exit %d (%v); want %d", c.path, got, err, c.exit)
				}
				var row scopeCheckRow
				if err := json.Unmarshal([]byte(stdout), &row); err != nil {
					t.Fatalf("stdout not JSON (%v): %q", err, stdout)
				}
				if row.Decision != c.decision {
					t.Errorf("check %s = %q; want %q", c.path, row.Decision, c.decision)
				}
			}
		})
	}
}

// test exits with the most restrictive verdict among its paths:
// denied, then prompt, then warn.
func TestScopeTest_ExitIsMostRestrictiveVerdict(t *testing.T) {
	root := scopeEnv(t)
	for _, tc := range []struct {
		mode  string
		paths []string
		exit  int
	}{
		{"warn", []string{root + "/p/sub", root + "/outside"}, 9},
		{"prompt", []string{root + "/p/sub", root + "/outside"}, 8},
		{"prompt", []string{root + "/p/sub"}, 0},
	} {
		writeScopeYAML(t, "mode: "+tc.mode+"\nallow:\n  - \""+root+"/p/**\"\n")
		stdout, _, _, err := runFooArgs(t, toolTestEnv{}, append([]string{"scope", "test", "--format=json"}, tc.paths...)...)
		if got := exitCodeOf(err); got != tc.exit {
			t.Fatalf("%s test %q: exit %d (%v); want %d", tc.mode, tc.paths, got, err, tc.exit)
		}
		var rows []scopeCheckRow
		if err := json.Unmarshal([]byte(stdout), &rows); err != nil || len(rows) != len(tc.paths) {
			t.Fatalf("stdout not %d JSON rows (%v): %q", len(tc.paths), err, stdout)
		}
	}
}

// A scope.yaml that does not load is a config error, not a denial:
// scripts must be able to tell the two apart.
func TestScopeCheck_BrokenConfigIsNotDenied(t *testing.T) {
	root := scopeEnv(t)
	writeScopeYAML(t, "mode: sometimes\nallow:\n  - \""+root+"/p/**\"\n")
	for _, args := range [][]string{
		{"scope", "check", root + "/p/sub"},
		{"scope", "test", root + "/p/sub"},
	} {
		_, _, _, err := runFooArgs(t, toolTestEnv{}, args...)
		if got := exitCodeOf(err); got != 2 {
			t.Fatalf("%q: exit %d (%v); want 2", args, got, err)
		}
		if !strings.Contains(err.Error(), "scope.yaml") && !strings.Contains(err.Error(), "sometimes") {
			t.Errorf("%q: error %q should explain the config problem", args, err)
		}
	}
}

func TestScopeCheck_BadOpIsUsage(t *testing.T) {
	root := scopeEnv(t)
	writeScopeYAML(t, "allow:\n  - \""+root+"/p/**\"\n")
	_, _, _, err := runFooArgs(t, toolTestEnv{}, "scope", "check", root+"/p/sub", "--op", "fly")
	if got := exitCodeOf(err); got != 2 {
		t.Fatalf("exit %d (%v); want 2", got, err)
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
	if exitCodeOf(err) != 1 {
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
	if exitCodeOf(err) != 1 {
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

// A secret-named directory hides its contents from check and test, not
// only its own entry; lookalike names stay allowed.
func TestScopeCheck_SecretDirContentsDenied(t *testing.T) {
	root := scopeEnv(t)
	for _, f := range []string{"p/secrets/key.txt", "p/secrets/nested/deep.txt", "p/credentials/c", "p/mysecrets.txt"} {
		path := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeScopeYAML(t, "allow:\n  - \""+root+"/p/**\"\n")
	env := toolTestEnv{}

	for _, tc := range []struct {
		rel, op, decision string
		exit              int
	}{
		{"p/secrets", "read", "denied", 1},
		{"p/secrets/key.txt", "read", "denied", 1},
		{"p/secrets/key.txt", "write", "denied", 1},
		{"p/secrets/key.txt", "exec", "denied", 1},
		{"p/secrets/nested/deep.txt", "read", "denied", 1},
		{"p/secrets/new.txt", "write", "denied", 1},
		{"p/credentials/c", "read", "denied", 1},
		{"p/mysecrets.txt", "read", "allowed", 0},
	} {
		stdout, _, _, err := runFooArgs(t, env, "scope", "check", filepath.Join(root, tc.rel), "--op", tc.op, "--format=json")
		if got := exitCodeOf(err); got != tc.exit {
			t.Fatalf("check %s %s: exit %d (%v); want %d", tc.rel, tc.op, got, err, tc.exit)
		}
		var row scopeCheckRow
		if err := json.Unmarshal([]byte(stdout), &row); err != nil {
			t.Fatalf("stdout not JSON (%v): %q", err, stdout)
		}
		if row.Decision != tc.decision {
			t.Errorf("check %s %s = %+v; want %s", tc.rel, tc.op, row, tc.decision)
		}
	}

	_, _, _, err := runFooArgs(t, env, "scope", "test", filepath.Join(root, "p/mysecrets.txt"), filepath.Join(root, "p/secrets/key.txt"))
	if exitCodeOf(err) != 1 {
		t.Fatalf("test with a file under secrets/: err = %v; want denied exit", err)
	}
}

// show lists the effective secret patterns, descendant forms included.
func TestScopeShow_ListsSecretDescendants(t *testing.T) {
	scopeEnv(t)
	stdout, _, _, err := runFooArgs(t, toolTestEnv{}, "scope", "show")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	for _, want := range []string{"**/secrets*\n", "**/secrets*/**", "**/credentials*/**", "**/.env/**"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("show output lacks %q:\n%s", want, stdout)
		}
	}
}

// A credential dir inside a granted tree is denied like the one under
// home, at any depth; lookalike names stay allowed.
func TestScopeCheck_CredentialDirsAnywhere(t *testing.T) {
	root := scopeEnv(t)
	for _, f := range []string{
		"p/.ssh/id", "p/sub/.aws/config", "p/.config/gcloud/adc.json", "p/.kube/config",
		"p/.netrc", "p/.sshx/f", "p/gcloud/f", "p/.npmrc",
	} {
		path := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeScopeYAML(t, "allow:\n  - \""+root+"/p/**\"\n")
	env := toolTestEnv{}

	for _, tc := range []struct {
		rel, op, decision string
		exit              int
	}{
		{"p/.ssh", "read", "denied", 1},
		{"p/.ssh/id", "read", "denied", 1},
		{"p/.ssh/id", "write", "denied", 1},
		{"p/.ssh/id", "exec", "denied", 1},
		{"p/.ssh/new", "write", "denied", 1},
		{"p/sub/.aws/config", "read", "denied", 1},
		{"p/.config/gcloud/adc.json", "read", "denied", 1},
		{"p/.kube/config", "read", "denied", 1},
		{"p/.netrc", "read", "denied", 1},
		{"p/.sshx/f", "read", "allowed", 0},
		{"p/gcloud/f", "read", "allowed", 0},
		{"p/.npmrc", "read", "allowed", 0},
	} {
		stdout, _, _, err := runFooArgs(t, env, "scope", "check", filepath.Join(root, tc.rel), "--op", tc.op, "--format=json")
		if got := exitCodeOf(err); got != tc.exit {
			t.Fatalf("check %s %s: exit %d (%v); want %d", tc.rel, tc.op, got, err, tc.exit)
		}
		var row scopeCheckRow
		if err := json.Unmarshal([]byte(stdout), &row); err != nil {
			t.Fatalf("stdout not JSON (%v): %q", err, stdout)
		}
		if row.Decision != tc.decision {
			t.Errorf("check %s %s = %+v; want %s", tc.rel, tc.op, row, tc.decision)
		}
	}
}

// show lists the anywhere forms of the credential dirs and files.
func TestScopeShow_ListsCredentialAnywhereForms(t *testing.T) {
	scopeEnv(t)
	stdout, _, _, err := runFooArgs(t, toolTestEnv{}, "scope", "show")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	for _, want := range []string{"**/.ssh\n", "**/.ssh/**", "**/.aws/**", "**/.config/gcloud/**", "**/.netrc\n"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("show output lacks %q:\n%s", want, stdout)
		}
	}
}

// checkRow runs `foo scope check` on path and returns its row and exit
// code.
func checkRow(t *testing.T, path string, extra ...string) (scopeCheckRow, int) {
	t.Helper()
	stdout, _, _, err := runFooArgs(t, toolTestEnv{}, append([]string{"scope", "check", path, "--format=json"}, extra...)...)
	var row scopeCheckRow
	if jerr := json.Unmarshal([]byte(stdout), &row); jerr != nil {
		t.Fatalf("check %s: stdout not JSON (%v; err %v): %q", path, jerr, err, stdout)
	}
	return row, exitCodeOf(err)
}

// check and test apply the gate's refusals on the value, not only its
// verdict on the resolved path: a `..` out of an ungranted directory,
// and a path through a link in an ungranted directory into the grant,
// are refused by a tool call, so check does not call them allowed. The
// mode decides the verdict, as for any path outside the grant.
func TestScopeCheck_GateRefusalsOnTheValue(t *testing.T) {
	for _, tc := range []struct {
		mode, decision string
		exit           int
	}{
		{"strict", "denied", 1},
		{"prompt", "prompt", 8},
		{"warn", "warn", 9},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			root := scopeEnv(t)
			if err := os.Symlink(filepath.Join(root, "p"), filepath.Join(root, "outside/link")); err != nil {
				t.Fatal(err)
			}
			writeScopeYAML(t, "mode: "+tc.mode+"\nallow:\n  - \""+root+"/p/**\"\n")

			for _, c := range []struct{ path, reason string }{
				{root + "/outside/../p/sub", `".."`},
				{root + "/outside/link/sub", "symlink"},
			} {
				row, exit := checkRow(t, c.path)
				if exit != tc.exit || row.Decision != tc.decision {
					t.Errorf("check %s = %s, exit %d; want %s, exit %d", c.path, row.Decision, exit, tc.decision, tc.exit)
				}
				if !strings.Contains(row.Reason, c.reason) {
					t.Errorf("check %s reason %q should mention %s", c.path, row.Reason, c.reason)
				}
				// The user's own terminal may see where the path lands.
				if row.Path != filepath.Join(root, "p/sub") {
					t.Errorf("check %s path %q; want %q", c.path, row.Path, filepath.Join(root, "p/sub"))
				}
			}

			// The same path named without the detour stays allowed.
			if row, exit := checkRow(t, root+"/p/sub"); exit != 0 || row.Decision != "allowed" {
				t.Errorf("check p/sub = %s, exit %d; want allowed", row.Decision, exit)
			}

			// test takes the most restrictive verdict of its paths.
			_, _, _, err := runFooArgs(t, toolTestEnv{}, "scope", "test", root+"/p/sub", root+"/outside/link/sub")
			if got := exitCodeOf(err); got != tc.exit {
				t.Errorf("test with a linked path: exit %d (%v); want %d", got, err, tc.exit)
			}
		})
	}
}

// A path through a link in an ungranted directory is allowed when an
// allow rule names it as written, through the link, as a tool call
// allows it.
func TestScopeCheck_RuleWrittenThroughLink(t *testing.T) {
	root := scopeEnv(t)
	if err := os.Symlink(filepath.Join(root, "p"), filepath.Join(root, "outside/link")); err != nil {
		t.Fatal(err)
	}
	home := os.Getenv("HOME")
	if err := os.Symlink(filepath.Join(root, "p"), filepath.Join(home, "code")); err != nil {
		t.Fatal(err)
	}
	writeScopeYAML(t, "allow:\n  - \""+root+"/outside/link/**\"\n  - \"~/code/**\"\n")

	for _, path := range []string{root + "/outside/link/sub", "~/code/sub", home + "/code/sub"} {
		row, exit := checkRow(t, path)
		if exit != 0 || row.Decision != "allowed" {
			t.Errorf("check %s = %+v, exit %d; want allowed", path, row, exit)
		}
	}

	// Rules as written still yield to deny rules on the target.
	if row, exit := checkRow(t, root+"/outside/link/.env"); exit != 1 || row.Decision != "denied" {
		t.Errorf("check link/.env = %+v, exit %d; want denied", row, exit)
	}
}

// A path that does not resolve is a usage error where the scope grants
// it, as a tool call fails on it there; outside the grant it is refused
// like any other path, whatever is there.
func TestScopeCheck_UnresolvableByPlace(t *testing.T) {
	root := scopeEnv(t)
	for _, l := range []string{"p/loop", "outside/loop"} {
		if err := os.Symlink("loop", filepath.Join(root, l)); err != nil {
			t.Fatal(err)
		}
	}
	writeScopeYAML(t, "allow:\n  - \""+root+"/p/**\"\n")
	_, _, _, err := runFooArgs(t, toolTestEnv{}, "scope", "check", root+"/p/loop")
	if got := exitCodeOf(err); got != 2 || !strings.Contains(err.Error(), "cannot resolve") {
		t.Errorf("check p/loop: exit %d (%v); want 2, cannot resolve", got, err)
	}
	if row, exit := checkRow(t, root+"/outside/loop"); exit != 1 || row.Decision != "denied" {
		t.Errorf("check outside/loop = %+v, exit %d; want denied", row, exit)
	}
}
