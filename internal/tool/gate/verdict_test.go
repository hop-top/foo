package gate_test

import (
	"fmt"
	"strings"
	"testing"

	"hop.top/foo/internal/tool/gate"
	"hop.top/kit/go/core/scope"
)

// Classify is the gate's own per-(path, op) verdict: a deny rule or an
// uncovered path is denied in strict mode, asked about in prompt mode
// and allowed with a warning in warn mode.
func TestScopeClassify_ModeMatrix(t *testing.T) {
	for _, tc := range []struct {
		mode                     string
		covered, secret, outside gate.Verdict
	}{
		{"strict", gate.VerdictAllow, gate.VerdictDeny, gate.VerdictDeny},
		{"prompt", gate.VerdictAllow, gate.VerdictPrompt, gate.VerdictPrompt},
		{"warn", gate.VerdictAllow, gate.VerdictWarn, gate.VerdictWarn},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			e := newFS(t)
			e.file(t, "p/a", "x")
			e.file(t, "p/.env", "x")
			e.file(t, "elsewhere/b", "x")
			e.scopeYAML(t, "mode: "+tc.mode+"\nallow:\n  - \"{root}/p/**\"\n")
			sc, err := gate.LoadScope("foo")
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range []struct {
				path   string
				want   gate.Verdict
				reason string
			}{
				{e.p("p/a"), tc.covered, ""},
				{e.p("p/.env"), tc.secret, "deny rule"},
				{e.p("elsewhere/b"), tc.outside, "no scope"},
			} {
				got, reason := sc.Classify(c.path, scope.Read)
				if got != c.want {
					t.Errorf("Classify(%s) = %s; want %s", c.path, got, c.want)
				}
				if !strings.Contains(reason, c.reason) {
					t.Errorf("Classify(%s) reason %q lacks %q", c.path, reason, c.reason)
				}
			}
		})
	}
}

// Without scope.yaml the gate denies every call, whatever the rules.
func TestScopeClassify_NotConfiguredDenies(t *testing.T) {
	e := newFS(t)
	e.file(t, "p/a", "x")
	sc, err := gate.LoadScope("foo")
	if err != nil {
		t.Fatal(err)
	}
	got, reason := sc.Classify(e.p("p/a"), scope.Read)
	if got != gate.VerdictDeny || !strings.Contains(reason, "no scope policy") {
		t.Fatalf("Classify = %s (%q); want denied, no scope policy", got, reason)
	}
}

func TestVerdictString(t *testing.T) {
	for v, want := range map[gate.Verdict]string{
		gate.VerdictAllow:  "allowed",
		gate.VerdictWarn:   "warn",
		gate.VerdictPrompt: "prompt",
		gate.VerdictDeny:   "denied",
	} {
		if v.String() != want {
			t.Errorf("%d.String() = %q; want %q", int(v), v.String(), want)
		}
	}
}

// warnModeTree is a warn-mode scope covering only p/, with n files
// under elsewhere/ that no rule covers.
func warnModeTree(t *testing.T, n int) *fsEnv {
	t.Helper()
	e := newFS(t)
	e.file(t, "p/a", "x")
	for i := range n {
		e.file(t, fmt.Sprintf("elsewhere/f%02d", i), "x")
	}
	e.scopeYAML(t, "mode: warn\nallow:\n  - \"{root}/p/**\"\n")
	return e
}

// A recursive walk over uncovered files logs one warning for the call,
// with the count and the first few paths, not one line per file.
func TestScopeWarnMode_WalkLogsOneWarningPerCall(t *testing.T) {
	e := warnModeTree(t, 8)
	g, logs := newGate(t, e.root, gate.WithConfirmer(refuseConfirm{t}))
	grant := mustAllow(t, g, gate.Request{Tool: "grep", SideEffect: "read", Paths: []gate.PathArg{
		{Param: "path", Values: []string{e.p("elsewhere")}, Op: scope.Read, Recursion: gate.FilterBefore},
	}})
	if len(grant.Files["path"]) != 8 {
		t.Fatalf("granted %d files; want 8", len(grant.Files["path"]))
	}
	out := logs.String()
	if n := strings.Count(out, "WARN"); n != 1 {
		t.Fatalf("logged %d warnings; want 1:\n%s", n, out)
	}
	// root + 8 files = 9 paths; the first few are named, the rest counted.
	for _, want := range []string{"count=9", e.p("elsewhere"), "and 4 more"} {
		if !strings.Contains(out, want) {
			t.Errorf("warning lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, e.p("elsewhere/f07")) {
		t.Errorf("warning should not list every path:\n%s", out)
	}
}

// A single warn-mode path still names its op and reason.
func TestScopeWarnMode_SinglePathNamesReason(t *testing.T) {
	e := warnModeTree(t, 1)
	g, logs := newGate(t, e.root, gate.WithConfirmer(refuseConfirm{t}))
	mustAllow(t, g, readCall("cat", e.p("elsewhere/f00")))
	out := logs.String()
	if strings.Count(out, "WARN") != 1 {
		t.Fatalf("want one warning:\n%s", out)
	}
	for _, want := range []string{e.p("elsewhere/f00"), "count=1", "op=read", "no scope rule covers this path"} {
		if !strings.Contains(out, want) {
			t.Errorf("warning lacks %q:\n%s", want, out)
		}
	}
}

// find's output entries are checked one by one after the call ran,
// with no end-of-call signal: the filter logs the first warn-mode entry
// once, not every entry.
func TestScopeWarnMode_OutputFilterLogsOnce(t *testing.T) {
	e := warnModeTree(t, 0)
	for _, d := range []string{"p/x", "p/y", "p/z"} {
		e.file(t, d+"/.env", "x")
	}
	g, logs := newGate(t, e.root, gate.WithConfirmer(refuseConfirm{t}))
	grant := mustAllow(t, g, gate.Request{Tool: "find", SideEffect: "read", Paths: []gate.PathArg{
		{Param: "path", Values: []string{e.p("p")}, Op: scope.Read, Recursion: gate.FilterAfter},
	}})
	if strings.Contains(logs.String(), "WARN") {
		t.Fatalf("covered root should not warn: %q", logs.String())
	}
	for _, d := range []string{"p/x", "p/y", "p/z"} {
		if !grant.Allow(e.p(d + "/.env")) {
			t.Fatalf("warn mode should allow %s/.env", d)
		}
	}
	out := logs.String()
	if n := strings.Count(out, "WARN"); n != 1 {
		t.Fatalf("output filter logged %d warnings; want 1:\n%s", n, out)
	}
	if !strings.Contains(out, e.p("p/x/.env")) || !strings.Contains(out, "deny rule") {
		t.Errorf("warning should name the first entry and its reason:\n%s", out)
	}
}

// Entries under a root the call already warned about, for the same
// reason, add no warning: find over an uncovered tree logs one line.
func TestScopeWarnMode_FindUnderWarnedRootLogsOnce(t *testing.T) {
	e := warnModeTree(t, 5)
	g, logs := newGate(t, e.root, gate.WithConfirmer(refuseConfirm{t}))
	grant := mustAllow(t, g, gate.Request{Tool: "find", SideEffect: "read", Paths: []gate.PathArg{
		{Param: "path", Values: []string{e.p("elsewhere")}, Op: scope.Read, Recursion: gate.FilterAfter},
	}})
	for _, p := range []string{"elsewhere", "elsewhere/f00", "elsewhere/f01", "elsewhere/f04"} {
		if !grant.Allow(e.p(p)) {
			t.Fatalf("warn mode should allow %s", p)
		}
	}
	out := logs.String()
	if n := strings.Count(out, "WARN"); n != 1 {
		t.Fatalf("logged %d warnings for one call; want 1:\n%s", n, out)
	}
}
