package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"hop.top/foo/internal/embed"
	kitcli "hop.top/kit/go/console/cli"
	"hop.top/kit/go/console/output"
	"hop.top/kit/go/core/upgrade"
)

// dryRunCase is one write or destructive leaf run under --dry-run.
// setup runs for real first, outside the snapshot, to give the leaf
// something to act on.
type dryRunCase struct {
	path    []string
	args    []string
	setup   func(t *testing.T, dir string)
	effects int
}

// dryRunCases covers every leaf kit's tier policy opts into --dry-run.
// TestDryRun_EveryHonoringLeafCovered fails when a leaf is missing.
var dryRunCases = []dryRunCase{
	{path: []string{"pattern", "create"}, args: []string{"terse", "be terse"}, effects: 1},
	{
		path:    []string{"pattern", "import"},
		args:    []string{"src.md", "imported"},
		setup:   func(t *testing.T, dir string) { writeFile(t, filepath.Join(dir, "src.md"), "be kind") },
		effects: 1,
	},
	{
		path:    []string{"pattern", "delete"},
		args:    []string{"doomed"},
		setup:   func(t *testing.T, _ string) { mustRun(t, "pattern", "create", "doomed", "x") },
		effects: 1,
	},
	{path: []string{"model", "default"}, args: []string{"gpt-4o"}, effects: 1},
	{path: []string{"schema", "create"}, args: []string{"person", "name, age int"}, effects: 1},
	{
		path:    []string{"schema", "create"},
		args:    []string{"fromfile", "--file", "s.json"},
		setup:   func(t *testing.T, dir string) { writeFile(t, filepath.Join(dir, "s.json"), `{"type":"object"}`) },
		effects: 1,
	},
	{
		path:    []string{"schema", "delete"},
		args:    []string{"doomed"},
		setup:   func(t *testing.T, _ string) { mustRun(t, "schema", "create", "doomed", "name, age int") },
		effects: 1,
	},
	{
		path:    []string{"fragment", "create"},
		args:    []string{"notes", "notes.txt"},
		setup:   func(t *testing.T, dir string) { writeFile(t, filepath.Join(dir, "notes.txt"), "remember") },
		effects: 1,
	},
	{
		path: []string{"fragment", "delete"},
		args: []string{"doomed"},
		setup: func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "doomed.txt"), "bye")
			mustRun(t, "fragment", "create", "doomed", filepath.Join(dir, "doomed.txt"))
		},
		effects: 1,
	},
	{path: []string{"embed", "add"}, args: []string{"hello world"}, effects: 1},
	{
		path:    []string{"embed", "file"},
		args:    []string{"--file", "doc.txt", "--collection", "docs"},
		setup:   func(t *testing.T, dir string) { writeFile(t, filepath.Join(dir, "doc.txt"), "one short chunk") },
		effects: 1,
	},
	{path: []string{"embed", "collection", "delete"}, args: []string{"empty"}, effects: 0},
	{
		path:    []string{"embed", "collection", "delete"},
		args:    []string{"docs"},
		setup:   func(t *testing.T, _ string) { seedEmbedding(t, "docs") },
		effects: 1,
	},
	{
		path: []string{"upgrade"},
		setup: func(t *testing.T, _ string) {
			feed, _ := releaseFeed(t, 0)
			prev := upgradeSource
			upgradeSource = upgrade.WithReleaseURL(feed.URL)
			t.Cleanup(func() { upgradeSource = prev })
			// The release check refreshes kit's check cache, as every
			// update check does. Prime it so the snapshot sees only
			// what the dry run itself would write.
			if r := newUpgradeChecker().Check(context.Background()); r.Err != nil {
				t.Fatal(r.Err)
			}
		},
		effects: 1,
	},
	// kit's own alias leaves preview: the plan names the alias.
	{path: []string{"alias", "add"}, args: []string{"ml", "model list"}, effects: 1},
	{
		path:    []string{"alias", "delete"},
		args:    []string{"ml"},
		setup:   func(t *testing.T, _ string) { mustRun(t, "alias", "add", "ml", "model list") },
		effects: 1,
	},
}

// dryRunOptedOut are the write leaves that refuse --dry-run: running
// one must fail and change nothing.
var dryRunOptedOut = []dryRunCase{
	{path: []string{"tool", "install"}, args: []string{"--dir", "links"}},
	{path: []string{"tool", "uninstall"}, args: []string{"--dir", "links"}},
}

// Under --dry-run every write or destructive leaf prints the plan of
// what it would do and leaves HOME, the XDG tree and the working
// directory exactly as they were. A destructive leaf runs without the
// confirmation it would otherwise need, so it must not act.
func TestDryRun_WriteLeavesChangeNothing(t *testing.T) {
	for _, tc := range dryRunCases {
		name := strings.Join(append(append([]string(nil), tc.path...), tc.args...), " ")
		t.Run(name, func(t *testing.T) {
			dir := dryRunEnv(t)
			if tc.setup != nil {
				tc.setup(t, dir)
			}
			before := snapshotTree(t, dryRunRoots(dir))

			args := append(append(append([]string(nil), tc.path...), tc.args...), "--dry-run", "--format", "json")
			stdout, stderr, err := runFooExecute(t, args...)
			if err != nil {
				t.Fatalf("foo %s: %v\nstderr: %s", strings.Join(args, " "), err, stderr)
			}
			var plan kitcli.Plan
			if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
				t.Fatalf("stdout is not a plan (%v): %q", err, stdout)
			}
			if want := "foo " + strings.Join(tc.path, " "); plan.Command != want {
				t.Errorf("plan command %q; want %q", plan.Command, want)
			}
			if len(plan.Effects) != tc.effects {
				t.Errorf("plan effects %+v; want %d", plan.Effects, tc.effects)
			}
			if after := snapshotTree(t, dryRunRoots(dir)); !sameTree(before, after) {
				t.Errorf("dry run changed the filesystem:\n%s", treeDiff(before, after))
			}
		})
	}
}

// A leaf that opts out refuses --dry-run before its body runs.
func TestDryRun_OptedOutLeavesRefuse(t *testing.T) {
	for _, tc := range dryRunOptedOut {
		name := strings.Join(tc.path, " ")
		t.Run(name, func(t *testing.T) {
			dir := dryRunEnv(t)
			if tc.setup != nil {
				tc.setup(t, dir)
			}
			before := snapshotTree(t, dryRunRoots(dir))

			args := append(append(append([]string(nil), tc.path...), tc.args...), "--dry-run")
			_, _, err := runFooExecute(t, args...)
			if !isDryRunRefusal(err) {
				t.Errorf("foo %s: err %v; want kit's --dry-run refusal (USAGE, exit 2)", strings.Join(args, " "), err)
			}
			if after := snapshotTree(t, dryRunRoots(dir)); !sameTree(before, after) {
				t.Errorf("refused dry run changed the filesystem:\n%s", treeDiff(before, after))
			}
		})
	}
}

// Every leaf kit would let through under --dry-run has a case above,
// and every opted-out case really is opted out. A new write leaf fails
// here until it either honors --dry-run or opts out.
func TestDryRun_EveryHonoringLeafCovered(t *testing.T) {
	newToolTestEnv(t)
	r := New("test")

	covered := map[string]bool{}
	for _, tc := range dryRunCases {
		covered["foo "+strings.Join(tc.path, " ")] = true
	}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if !c.HasSubCommands() && kitcli.IsDryRunSupported(c) && !covered[c.CommandPath()] {
			t.Errorf("%s honors --dry-run per kit's tier policy but has no dry-run case", c.CommandPath())
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(r.Cmd)

	for _, tc := range dryRunOptedOut {
		c, _, err := r.Cmd.Find(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		if c.Annotations["kit/dry-run"] != "opted-out" {
			t.Errorf("%s is listed as opted out but is not", c.CommandPath())
		}
	}
}

// isDryRunRefusal reports whether err refuses --dry-run the way kit
// does: a USAGE error, exit 2, naming the flag. kit owns the wording.
func isDryRunRefusal(err error) bool {
	var oe *output.Error
	return errors.As(err, &oe) && oe.Code == output.CodeUsage && oe.ExitCode == 2 &&
		strings.Contains(oe.Message, "--dry-run")
}

// dryRunEnv isolates a run and makes a scratch working directory the
// cwd, returning it.
func dryRunEnv(t *testing.T) string {
	t.Helper()
	newToolTestEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("LLM_BASE_URL", remoteBaseURL)
	dir := t.TempDir()
	t.Chdir(dir)
	return dir
}

func dryRunRoots(cwd string) []string {
	roots := []string{cwd}
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		roots = append(roots, os.Getenv(k))
	}
	return roots
}

// seedEmbedding stores one row in collection, as embed add would
// after the model call.
func seedEmbedding(t *testing.T, collection string) {
	t.Helper()
	store, err := openEmbedStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(embed.Embedding{
		ID: "01SEED", Collection: collection, ContentHash: "seed",
		Vector: []float32{1, 0}, Metadata: map[string]string{"text": "seed"},
	}); err != nil {
		t.Fatal(err)
	}
}

func mustRun(t *testing.T, args ...string) {
	t.Helper()
	if _, stderr, err := runFooExecute(t, args...); err != nil {
		t.Fatalf("setup foo %s: %v\nstderr: %s", strings.Join(args, " "), err, stderr)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// snapshotTree maps every path under roots to its type and content
// hash (a link to its target).
func snapshotTree(t *testing.T, roots []string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			switch {
			case d.Type()&fs.ModeSymlink != 0:
				target, _ := os.Readlink(p)
				out[p] = "link:" + target
			case d.IsDir():
				out[p] = "dir"
			default:
				data, rerr := os.ReadFile(p)
				if rerr != nil {
					out[p] = "unreadable"
					return nil
				}
				sum := sha256.Sum256(data)
				out[p] = "file:" + hex.EncodeToString(sum[:])
			}
			return nil
		})
	}
	return out
}

func sameTree(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func treeDiff(before, after map[string]string) string {
	var lines []string
	for k, v := range after {
		if w, ok := before[k]; !ok {
			lines = append(lines, "+ "+k)
		} else if w != v {
			lines = append(lines, "~ "+k)
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			lines = append(lines, "- "+k)
		}
	}
	return strings.Join(lines, "\n")
}
