package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hop.top/kit/go/core/netpolicy"
)

// runFooExecute drives the root the way main does: through kit's
// Root.Execute, so the PersistentPreRunE chain kit composes (chdir,
// progress, dry-run tier, offline marker) runs ahead of foo's own
// runtime init.
func runFooExecute(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	r := New("test")
	var out, errOut bytes.Buffer
	r.Cmd.SetOut(&out)
	r.Cmd.SetErr(&errOut)
	r.Cmd.SetIn(strings.NewReader(""))
	r.SetArgs(args)
	err = r.Execute(context.Background())
	return out.String(), errOut.String(), err
}

// -C names a directory that does not exist: kit's chdir hook refuses
// it before any command body runs.
func TestChdir_MissingDirectoryFails(t *testing.T) {
	newToolTestEnv(t)
	t.Chdir(t.TempDir())
	missing := filepath.Join(t.TempDir(), "nope")

	for _, args := range [][]string{
		{"-C", missing, "tool", "list"},
		{"tool", "list", "--chdir", missing},
	} {
		_, _, err := runFooExecute(t, args...)
		if err == nil {
			t.Fatalf("foo %s succeeded; want kit's chdir refusal", strings.Join(args, " "))
		}
		if !strings.Contains(err.Error(), "cannot chdir") {
			t.Errorf("foo %s: err %v; want kit's chdir error", strings.Join(args, " "), err)
		}
	}
}

// -C changes directory before foo loads its config, so the project
// .foo.yaml in the target directory is the one that applies.
func TestChdir_ProjectConfigFollowsTarget(t *testing.T) {
	newToolTestEnv(t)
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, ".foo.yaml"), []byte("model: chdir-probe-model\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())

	for _, args := range [][]string{
		{"-C", project, "model", "current", "--format", "json"},
		{"model", "current", "--format", "json", "--chdir", project},
	} {
		stdout, stderr, err := runFooExecute(t, args...)
		if err != nil {
			t.Fatalf("foo %s: %v\nstderr: %s", strings.Join(args, " "), err, stderr)
		}
		var got modelStatus
		if err := json.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatalf("stdout %q is not JSON: %v", stdout, err)
		}
		if got.Current != "chdir-probe-model" {
			t.Errorf("foo %s: current model %q; want the target's project config", strings.Join(args, " "), got.Current)
		}
	}
}

// --offline reaches the command context through kit's own chain, in
// either flag position: a remote model call is refused.
func TestOffline_MarkedByKitChain(t *testing.T) {
	for _, args := range [][]string{
		{"--offline", "-m", "gpt-4o", "--no-stream", "hi"},
		{"-m", "gpt-4o", "--no-stream", "hi", "--offline"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			newToolTestEnv(t)
			t.Setenv("OPENAI_API_KEY", "sk-test")
			t.Setenv("LLM_BASE_URL", remoteBaseURL)

			_, _, err := runFooExecute(t, args...)
			if !errors.Is(err, netpolicy.ErrOffline) {
				t.Fatalf("err = %v; want kit's offline refusal", err)
			}
		})
	}
}

// The root's --dry-run is kit's global flag: one flag object, so the
// value set in either position reaches the root's prompt preview.
func TestDryRun_RootIsKitGlobal(t *testing.T) {
	newToolTestEnv(t)
	r := New("test")
	kit := r.Cmd.PersistentFlags().Lookup("dry-run")
	if kit == nil {
		t.Fatal("kit's --dry-run is not on the root")
	}
	if local := r.Cmd.Flags().Lookup("dry-run"); local != nil && local != kit {
		t.Fatalf("root declares its own --dry-run (%q), shadowing kit's", local.Usage)
	}

	for _, args := range [][]string{
		{"--dry-run", "hi"},
		{"hi", "--dry-run"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			newToolTestEnv(t)
			t.Setenv("OPENAI_API_KEY", "sk-test")
			t.Setenv("LLM_BASE_URL", remoteBaseURL)

			stdout, stderr, err := runFooExecute(t, args...)
			if err != nil {
				t.Fatalf("execute: %v\nstderr: %s", err, stderr)
			}
			if strings.TrimSpace(stdout) != "hi" {
				t.Errorf("stdout %q; want the assembled prompt", stdout)
			}
		})
	}
}

// A root dry run leaves no trace on disk: no first-run pool seed, no
// workspace, nothing in the working directory.
func TestDryRun_RootWritesNothing(t *testing.T) {
	dir := dryRunEnv(t)
	before := snapshotTree(t, dryRunRoots(dir))
	if _, stderr, err := runFooExecute(t, "--dry-run", "hi"); err != nil {
		t.Fatalf("execute: %v\nstderr: %s", err, stderr)
	}
	if after := snapshotTree(t, dryRunRoots(dir)); !sameTree(before, after) {
		t.Errorf("dry run changed the filesystem:\n%s", treeDiff(before, after))
	}
}

// The REPL has no batch boundary to preview: kit refuses --dry-run on
// `foo repl`, and the bare root refuses it before opening the REPL.
func TestDryRun_REPLRefused(t *testing.T) {
	newToolTestEnv(t)
	_, _, err := runFooExecute(t, "repl", "--dry-run")
	if !isDryRunRefusal(err) {
		t.Errorf("foo repl --dry-run: err %v; want kit's interactive refusal (USAGE, exit 2)", err)
	}

	r := New("test")
	if err := r.Cmd.ParseFlags([]string{"--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if err := runREPL(r.Cmd); !isDryRunRefusal(err) {
		t.Errorf("bare REPL under --dry-run: err %v; want the same refusal as kit's (USAGE, exit 2)", err)
	}
}
