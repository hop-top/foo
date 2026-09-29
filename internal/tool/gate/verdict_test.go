package gate_test

import (
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
