package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"hop.top/foo/internal/testutil"
)

// builtinShadowNames are foo-<name> binaries whose <name> a built-in
// already owns: commands foo registers itself (config, tool), one kit
// mounts (status), and the two cobra adds on Execute (help, completion).
var builtinShadowNames = []string{"config", "tool", "status", "help", "completion"}

// newShadowPluginEnv extends the plugin env with a foo-<name> binary for
// every builtinShadowNames entry. Each writes its name to the ext-info
// log when interrogated and prints "<name> ran" when dispatched.
func newShadowPluginEnv(t *testing.T) toolTestEnv {
	t.Helper()
	env := newPluginTestEnv(t)
	bin := filepath.Dir(env.demoPath)
	for _, name := range builtinShadowNames {
		p := filepath.Join(bin, "foo-"+name)
		testutil.WriteScript(t, p, subcommandPluginScript(name))
	}
	return env
}

// extInfoNames lists the binaries interrogated for --ext-info, by name.
func (e toolTestEnv) extInfoNames(t *testing.T) map[string]int {
	t.Helper()
	data, err := os.ReadFile(e.extInfoLog)
	if err != nil {
		return nil
	}
	seen := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line != "" {
			seen[line]++
		}
	}
	return seen
}

// TestExtPlugins_BuiltinNamesNotRegistered: a foo-<builtin> binary on
// PATH must not become a second cobra command of the same name. Before
// the fix it did, unannotated, and kit's validator then refused every
// invocation of foo, --help included.
func TestExtPlugins_BuiltinNamesNotRegistered(t *testing.T) {
	env := newShadowPluginEnv(t)

	r := New("test")

	count := map[string]int{}
	for _, c := range r.Cmd.Commands() {
		count[c.Name()]++
		if c.GroupID == "plugins" {
			for _, name := range builtinShadowNames {
				if c.Name() == name {
					t.Errorf("foo-%s registered as a plugin subcommand; the built-in owns %q", name, name)
				}
			}
		}
	}
	for name, n := range count {
		if n > 1 {
			t.Errorf("%d commands named %q; want 1", n, name)
		}
	}
	for _, name := range []string{"hello", "toolbox"} {
		if count[name] != 1 {
			t.Errorf("plugin foo-%s registered %d times; want 1", name, count[name])
		}
	}
	for _, name := range builtinShadowNames {
		if n := env.extInfoNames(t)[name]; n != 0 {
			t.Errorf("New() interrogated shadowed foo-%s %d times; want 0", name, n)
		}
	}
	if err := r.Validate(); err != nil {
		t.Errorf("Validate with foo-<builtin> binaries on PATH: %v", err)
	}
}

// TestExtPlugins_BuiltinWinsDispatch: `foo <builtin>` runs the built-in,
// never the same-named binary, and help still lists the real plugins.
func TestExtPlugins_BuiltinWinsDispatch(t *testing.T) {
	env := newShadowPluginEnv(t)

	cases := [][]string{
		{"config", "path"},
		{"tool", "list", "--offline", "--format=json"},
		{"completion", "bash"},
		{"help", "config"},
	}
	for _, args := range cases {
		stdout, stderr, _, err := runFooArgs(t, env, args...)
		if err != nil {
			t.Errorf("foo %s: %v\nstderr: %s", strings.Join(args, " "), err, stderr)
		}
		if strings.Contains(stdout+stderr, " ran:") {
			t.Errorf("foo %s dispatched a plugin binary:\n%s%s", strings.Join(args, " "), stdout, stderr)
		}
	}

	stdout, _, _, err := runFooArgs(t, env, "hello", "a")
	if err != nil || !strings.Contains(stdout, "hello ran: a") {
		t.Errorf("foo hello: err=%v stdout=%q; want plugin output", err, stdout)
	}

	stdout, _, _, err = runFooArgs(t, env, "--help")
	if err != nil {
		t.Fatalf("--help: %v", err)
	}
	for _, name := range builtinShadowNames {
		if strings.Contains(stdout, name+" plugin for tests") {
			t.Errorf("root help lists shadowed foo-%s:\n%s", name, stdout)
		}
	}
	if !strings.Contains(stdout, "hello plugin for tests") {
		t.Errorf("root help missing foo-hello:\n%s", stdout)
	}
	for _, name := range builtinShadowNames {
		if n := env.extInfoNames(t)[name]; n != 0 {
			t.Errorf("shadowed foo-%s interrogated %d times; want 0", name, n)
		}
	}
}

// TestRegisterExtPlugins_BuiltinAliasReserved: a built-in's alias is as
// taken as its name, so foo-<alias> is dropped too.
func TestRegisterExtPlugins_BuiltinAliasReserved(t *testing.T) {
	env := newPluginTestEnv(t)
	bin := filepath.Dir(env.demoPath)
	testutil.WriteScript(t, filepath.Join(bin, "foo-cfg"), subcommandPluginScript("cfg"))

	root := &cobra.Command{Use: "foo"}
	builtin := &cobra.Command{Use: "config", Aliases: []string{"cfg"}, Run: func(*cobra.Command, []string) {}}
	root.AddCommand(builtin)

	registerExtPlugins(root)

	for _, c := range root.Commands() {
		if c != builtin && (c.Name() == "cfg" || c.Name() == "config") {
			t.Errorf("foo-%s registered alongside built-in config (alias cfg)", c.Name())
		}
	}
	if found, _, err := root.Find([]string{"cfg"}); err != nil || found != builtin {
		t.Errorf("foo cfg resolves to %v (err %v); want the built-in", found, err)
	}
	if n := env.extInfoNames(t)["cfg"]; n != 0 {
		t.Errorf("shadowed foo-cfg interrogated %d times; want 0", n)
	}
	if hello, _, err := root.Find([]string{"hello"}); err != nil || hello == root {
		t.Errorf("foo-hello not registered: %v", err)
	}
}
