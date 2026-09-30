package gate_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hop.top/foo/internal/tool/gate"
	"hop.top/kit/go/core/scope"
)

// The policy row is picked by the tool's declared network, not always
// network none: kit's table prompts for an egress read and denies an
// egress destructive call, while foo's overlay keeps local writes
// prompting. No foo layer auto-allows a write on any network.
func TestPolicy_NetworkSelectsRow(t *testing.T) {
	for _, tc := range []struct {
		effect, network string
		kind            gate.Kind // "" = allowed
		asks            int
		reason          string // in the question or the refusal
	}{
		{"read", "", "", 0, ""},
		{"read", "none", "", 0, ""},
		{"read", "local-only", "", 0, ""},
		{"read", "egress", "", 1, "egress read may exfiltrate"},
		{"write", "none", "", 1, "local write"},
		{"write", "local-only", "", 1, "shared infrastructure"},
		{"write", "egress", "", 1, "remote service"},
		{"destructive", "none", "", 1, "irreversible local mutation"},
		{"destructive", "egress", gate.KindPolicy, 0, "irreversible remote mutation"},
	} {
		t.Run(tc.effect+"/"+tc.network, func(t *testing.T) {
			e := writableTree(t)
			c := yes()
			g, _ := newGate(t, e.root, gate.WithConfirmer(c))
			req := netCall(e, tc.effect, tc.network)
			if tc.kind != "" {
				ge := mustRefuse(t, g, req, tc.kind)
				if !strings.Contains(ge.Message, tc.reason) || !strings.Contains(ge.Message, tc.network) {
					t.Errorf("refusal %q should name the network and carry %q", ge.Message, tc.reason)
				}
			} else {
				mustAllow(t, g, req)
			}
			if len(c.asked) != tc.asks {
				t.Fatalf("asked %d; want %d: %q", len(c.asked), tc.asks, c.asked)
			}
			if tc.asks > 0 && !strings.Contains(c.asked[0], tc.reason) {
				t.Errorf("question %q should carry %q", c.asked[0], tc.reason)
			}
			if tc.asks > 0 && tc.network != "none" && !strings.Contains(c.asked[0], "network "+tc.network) {
				t.Errorf("question %q should name network %s", c.asked[0], tc.network)
			}
		})
	}
}

// A tool-policy.yaml row for an egress side effect applies to a tool
// that declares egress, and leaves network none alone.
func TestPolicy_UserRowForNetwork(t *testing.T) {
	e := writableTree(t)
	dir := filepath.Join(e.cfg, "foo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `rules:
  - {side_effect: read, network: egress, action: auto-allow, reason: "trusted mirror"}
  - {side_effect: write, network: egress, action: deny, reason: "no remote writes"}
`
	if err := os.WriteFile(filepath.Join(dir, gate.PolicyFileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	g, _ := newGate(t, e.root, gate.WithConfirmer(refuseConfirm{t}))
	mustAllow(t, g, netCall(e, "read", "egress"))
	ge := mustRefuse(t, g, netCall(e, "write", "egress"), gate.KindPolicy)
	if !strings.Contains(ge.Message, "no remote writes") {
		t.Errorf("refusal %q should carry the user rule's reason", ge.Message)
	}
	// Network none keeps foo's overlay: the local write still asks.
	c := &confirmer{answers: []bool{true}}
	g, _ = newGate(t, e.root, gate.WithConfirmer(c))
	mustAllow(t, g, netCall(e, "write", "none"))
	if len(c.asked) != 1 {
		t.Errorf("local write asked %d; want 1 (overlay)", len(c.asked))
	}
}

func netCall(e *fsEnv, effect, network string) gate.Request {
	op, rel, target := scope.Read, "r/a", gate.Follow
	if effect != "read" {
		op, rel, target = scope.Write, "w/new", gate.Dirent
	}
	return gate.Request{
		Tool:       "nettool",
		SideEffect: effect,
		Network:    network,
		Paths:      []gate.PathArg{{Param: "path", Values: []string{e.p(rel)}, Op: op, Target: target}},
	}
}
