package gate_test

import (
	"io"
	"strings"
	"testing"

	"hop.top/foo/internal/tool/gate"
	"hop.top/kit/go/core/scope"
)

// CheckPath answers what a read call on the same value gets, for every
// probe of the existence matrix and paths in the grant: denied in
// strict mode, asked about (denied with no terminal) in prompt mode,
// run with a warning in warn mode, and a value error where the call
// fails on the value itself.
func TestCheckPath_MatchesAuthorize(t *testing.T) {
	probes := []string{
		"p/a", "p/d", "p/m", "p/m/x", "p/loop", "p/a/x",
		"p/../out/f", "p/d/../a", "out/../p/a", "p/../p/a",
	}
	probes = append(append(probes, outsideProbes...), climbProbes...)
	// strictDenied: the probe's strict-mode call was denied; modes run
	// in order, so warn mode reads it.
	strictDenied := map[string]bool{}
	for _, mode := range []string{"strict", "prompt", "warn"} {
		t.Run(mode, func(t *testing.T) {
			e := existenceTree(t, mode)
			sc, err := gate.LoadScope("foo")
			if err != nil {
				t.Fatal(err)
			}
			for _, rel := range probes {
				raw := e.root + "/" + rel // not e.p: Join would clean ".."
				pv, perr := sc.CheckPath(e.root, raw, scope.Read)
				g, logs := newGate(t, e.root, gate.WithConfirmer(noTTY(io.Discard)))
				_, ge := authorize(t, g, readCall("cat", raw))

				var want gate.Verdict
				switch {
				case ge != nil && ge.Kind == gate.KindDenied && strings.Contains(ge.Message, "approval cannot be asked"):
					want = gate.VerdictPrompt
				case ge != nil && ge.Kind == gate.KindDenied:
					want = gate.VerdictDeny
				case mode == "warn" && (logs.Len() > 0 || ge != nil && strictDenied[rel]):
					// A value error after the warning ends the call
					// before it logs; strict mode denied the value.
					want = gate.VerdictWarn
				default:
					want = gate.VerdictAllow
				}
				if mode == "strict" {
					strictDenied[rel] = want == gate.VerdictDeny
				}
				if pv.Verdict != want {
					t.Errorf("%s: CheckPath = %s (%s); Authorize = %v", rel, pv.Verdict, pv.Reason, ge)
				}
				if want == gate.VerdictAllow && (ge == nil) != (perr == nil) {
					t.Errorf("%s: CheckPath error %v; Authorize %v", rel, perr, ge)
				}
			}
		})
	}
}

// The user is told why a value the rules would allow by its resolved
// path is refused, and where it resolves.
func TestCheckPath_ReasonAndTarget(t *testing.T) {
	e := existenceTree(t, "strict")
	sc, err := gate.LoadScope("foo")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ rel, path, reason string }{
		{"out/lpa", "p/a", "symlink in a directory no scope rule grants"},
		{"out/lp/a", "p/a", "symlink in a directory no scope rule grants"},
		{"out/../p/a", "p/a", `".." leaves a directory no scope rule grants`},
	} {
		pv, err := sc.CheckPath(e.root, e.root+"/"+tc.rel, scope.Read)
		if err != nil {
			t.Fatalf("%s: %v", tc.rel, err)
		}
		if pv.Verdict != gate.VerdictDeny || pv.Path != e.p(tc.path) || !strings.Contains(pv.Reason, tc.reason) {
			t.Errorf("%s = %+v; want denied at %s (%s)", tc.rel, pv, e.p(tc.path), tc.reason)
		}
	}

	// Relative values resolve against cwd, "~" against the home dir.
	e.link(t, e.p("p"), "home/code")
	for _, raw := range []string{"../out/lpa", "~/code/a"} {
		pv, err := sc.CheckPath(e.p("p"), raw, scope.Read)
		if err != nil || pv.Verdict != gate.VerdictDeny || pv.Path != e.p("p/a") {
			t.Errorf("%s = %+v, %v; want denied at %s", raw, pv, err, e.p("p/a"))
		}
	}
}
