package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hop.top/foo/internal/testutil"
)

// subcommandPluginScript is a foo-<name> subcommand plugin. Each
// --ext-info interrogation appends its name to $FOO_TEST_EXTINFO_LOG,
// alongside the "x" lines foo-tool-demo writes, so a test can tell
// which binary discovery executed.
func subcommandPluginScript(name string) string {
	return `#!/bin/sh
if [ "$1" = "--ext-info" ]; then
  echo ` + name + ` >> "$FOO_TEST_EXTINFO_LOG"
  echo '{"name":"` + name + `","version":"0.1.0","description":"` + name + ` plugin for tests","capabilities":["discover"]}'
  exit 0
fi
echo "` + name + ` ran: $*"
`
}

// newPluginTestEnv extends the tool env with two subcommand plugins:
// foo-hello, and foo-toolbox, whose name shares "tool" with the tool
// prefix but is not foo-tool-*.
func newPluginTestEnv(t *testing.T) toolTestEnv {
	t.Helper()
	env := newToolTestEnv(t)
	bin := filepath.Dir(env.demoPath)
	for _, name := range []string{"hello", "toolbox"} {
		p := filepath.Join(bin, "foo-"+name)
		testutil.WriteScript(t, p, subcommandPluginScript(name))
	}
	return env
}

// toolExtInfoCalls counts foo-tool-demo interrogations only.
func (e toolTestEnv) toolExtInfoCalls(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(e.extInfoLog)
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if line == "x" {
			n++
		}
	}
	return n
}

// TestExtPlugins_ToolBinariesNotSubcommands: foo-tool-* binaries are
// LLM tools, not verbs. Subcommand discovery scans every foo-* binary,
// so without a filter foo-tool-demo became `foo tool-demo` and was
// exec'd for --ext-info on every New().
func TestExtPlugins_ToolBinariesNotSubcommands(t *testing.T) {
	env := newPluginTestEnv(t)

	r := New("test")

	plugins := map[string]string{}
	for _, c := range r.Cmd.Commands() {
		plugins[c.Name()] = c.GroupID
	}
	if _, ok := plugins["tool-demo"]; ok {
		t.Error("foo-tool-demo registered as subcommand `foo tool-demo`; tool plugins are not subcommands")
	}
	for _, name := range []string{"hello", "toolbox"} {
		if g, ok := plugins[name]; !ok || g != "plugins" {
			t.Errorf("subcommand plugin foo-%s: registered=%v group=%q; want registered in plugins group", name, ok, g)
		}
	}
	if got := env.toolExtInfoCalls(t); got != 0 {
		t.Errorf("New() interrogated foo-tool-demo %d times; want 0", got)
	}
}

// TestExtPlugins_HelpOmitsToolPlugins checks the user-visible surface:
// root help lists the subcommand plugins and never the tool plugin,
// and rendering it does not exec the tool plugin.
func TestExtPlugins_HelpOmitsToolPlugins(t *testing.T) {
	env := newPluginTestEnv(t)

	stdout, _, _, err := runFooArgs(t, env, "--help")
	if err != nil {
		t.Fatalf("--help: %v", err)
	}
	if strings.Contains(stdout, "tool-demo") {
		t.Errorf("root help lists tool plugin as `tool-demo`:\n%s", stdout)
	}
	for _, want := range []string{"hello plugin for tests", "toolbox plugin for tests"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("root help missing %q:\n%s", want, stdout)
		}
	}
	if got := env.toolExtInfoCalls(t); got != 0 {
		t.Errorf("--help interrogated foo-tool-demo %d times; want 0", got)
	}
}

// TestExtPlugins_ToolboxDispatches: the tool-prefix filter is exact on
// foo-tool-, so foo-toolbox still runs as `foo toolbox`.
func TestExtPlugins_ToolboxDispatches(t *testing.T) {
	env := newPluginTestEnv(t)

	stdout, _, _, err := runFooArgs(t, env, "toolbox", "a", "b")
	if err != nil {
		t.Fatalf("foo toolbox: %v", err)
	}
	if !strings.Contains(stdout, "toolbox ran: a b") {
		t.Errorf("foo toolbox stdout = %q; want plugin output with argv passthrough", stdout)
	}
}
