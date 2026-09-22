package commands

import (
	"strings"
	"testing"
)

// TestRootUse_NoPositionalPlaceholder pins the usage-line shape.
//
// fang renders the root usage line as `<name> [command] <other args>
// [--flags]`: it injects [command] ahead of any bracketed token in Use
// and offers no way to express alternatives. So a Use carrying a
// positional placeholder — "foo [prompt]" — printed
// `foo [command] [prompt] [--flags]`, which reads as "pass a command and
// then a prompt". The root actually accepts EITHER a subcommand OR one
// bare prompt, and neither opens the REPL.
//
// Keeping Use free of bracketed positionals is the only lever that
// changes that line, so assert it directly.
func TestRootUse_NoPositionalPlaceholder(t *testing.T) {
	root := New("test")

	if got := root.Cmd.Use; got != "foo" {
		t.Fatalf("root Use = %q; want %q", got, "foo")
	}
	if strings.ContainsAny(root.Cmd.Use, "[]") {
		t.Fatalf("root Use %q must carry no bracketed positional: fang "+
			"renders it after the injected [command], reproducing the "+
			"misleading `foo [command] [prompt]` line", root.Cmd.Use)
	}
}

// TestRootExample_CoversEveryInvocationMode locks the EXAMPLES block to
// the three real modes. It is the only surface left that can show them,
// since the usage line cannot render alternatives.
func TestRootExample_CoversEveryInvocationMode(t *testing.T) {
	root := New("test")

	lines := make([]string, 0, 3)
	for _, l := range strings.Split(root.Cmd.Example, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) != 3 {
		t.Fatalf("root Example = %d non-empty lines; want 3 (prompt, "+
			"subcommand, bare REPL): %q", len(lines), root.Cmd.Example)
	}

	// Bare prompt: a quoted positional, no subcommand.
	if !strings.Contains(lines[0], `"`) {
		t.Errorf("example 1 %q must demo a quoted bare prompt", lines[0])
	}
	// Subcommand: a real registered command, unquoted.
	if !strings.HasPrefix(lines[1], "foo model") {
		t.Errorf("example 2 %q must demo a subcommand", lines[1])
	}
	// REPL: the bare binary, nothing else.
	if lines[2] != "foo" {
		t.Errorf("example 3 = %q; want bare %q for the no-args REPL",
			lines[2], "foo")
	}
}

// TestInstanceFlag_HiddenButParseable covers the no-op-advertised-as-real
// defect. --instance is wired through to config.Secrets.Service but foo
// is a single-instance build, so it selects nothing. It must not appear
// in --help, yet must keep parsing so existing scripts do not break on
// an unknown flag.
func TestInstanceFlag_HiddenButParseable(t *testing.T) {
	root := New("test")

	f := root.Cmd.PersistentFlags().Lookup("instance")
	if f == nil {
		t.Fatal("--instance must stay registered: removing it breaks " +
			"scripts already passing it")
	}
	if !f.Hidden {
		t.Error("--instance must be hidden from --help: it has no " +
			"backend to select in a single-instance build")
	}
	if strings.Contains(strings.ToLower(f.Usage), "no-op") {
		t.Errorf("--instance usage %q should describe the reservation, "+
			"not leak the no-op admission into help", f.Usage)
	}
}

// TestProfileFlag_VisibleAndHonest guards the other half of the audit.
// --profile is NOT a no-op: it namespaces secret lookups via
// config.Secrets.Service. That only binds on the keyring backend
// (foo defaults to env), so it stays visible with help text that says so.
func TestProfileFlag_VisibleAndHonest(t *testing.T) {
	root := New("test")

	f := root.Cmd.PersistentFlags().Lookup("profile")
	if f == nil {
		t.Fatal("--profile must stay registered")
	}
	if f.Hidden {
		t.Error("--profile does real work (scopes Secrets.Service); " +
			"it must stay visible")
	}
	if !strings.Contains(strings.ToLower(f.Usage), "keyring") {
		t.Errorf("--profile usage %q must name the backend it binds on, "+
			"otherwise it reads as universally effective", f.Usage)
	}
}
