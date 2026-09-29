package gate_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"hop.top/foo/internal/tool/gate"
	"hop.top/kit/go/ai/toolspec/policy"
	"hop.top/kit/go/core/scope"
)

func writeCall(e *fsEnv) gate.Request {
	return gate.Request{
		Tool:       "mkdir",
		SideEffect: "write",
		Paths: []gate.PathArg{
			{Param: "path", Values: []string{e.p("w/new")}, Op: scope.Write, Target: gate.Dirent},
		},
		Argv: func(c map[string][]string) []string {
			return append([]string{"/bin/mkdir", "--"}, c["path"]...)
		},
	}
}

// refuseConfirm fails the test when a question is asked.
type refuseConfirm struct{ t *testing.T }

func (r refuseConfirm) Confirm(q string) (bool, error) {
	r.t.Fatalf("unexpected prompt: %s", q)
	return false, nil
}

func TestPolicy_WritePrompts(t *testing.T) {
	e := writableTree(t)

	// No way to ask → declined, with the reason.
	var out bytes.Buffer
	g, _ := newGate(t, e.root, gate.WithConfirmer(noTTY(&out)))
	ge := mustRefuse(t, g, writeCall(e), gate.KindDeclined)
	if !strings.Contains(ge.Message, "terminal") {
		t.Fatalf("message %q should say no terminal", ge.Message)
	}

	// Answered yes → allowed; the question shows the canonical argv.
	out.Reset()
	g, _ = newGate(t, e.root, gate.WithConfirmer(prompter("y\n", &out)))
	mustAllow(t, g, writeCall(e))
	if !strings.Contains(out.String(), "/bin/mkdir -- "+e.p("w/new")) {
		t.Fatalf("prompt %q should show the canonical argv", out.String())
	}

	// Answered no → declined.
	g, _ = newGate(t, e.root, gate.WithConfirmer(prompter("n\n", &bytes.Buffer{})))
	mustRefuse(t, g, writeCall(e), gate.KindDeclined)
}

func TestPolicy_DestructivePrompts(t *testing.T) {
	e := writableTree(t)
	e.file(t, "w/x", "x")
	c := &confirmer{answers: []bool{false}}
	g, _ := newGate(t, e.root, gate.WithConfirmer(c))
	mustRefuse(t, g, rmCall(e.p("w/x")), gate.KindDeclined)
	if len(c.asked) != 1 {
		t.Fatalf("asked %d questions; want 1", len(c.asked))
	}
}

func TestPolicy_ReadRunsWithoutPrompt(t *testing.T) {
	e := writableTree(t)
	g, _ := newGate(t, e.root, gate.WithConfirmer(refuseConfirm{t}))
	mustAllow(t, g, readCall("cat", e.p("r/a")))
}

func TestPolicy_UnknownSideEffectPrompts(t *testing.T) {
	e := writableTree(t)
	c := &confirmer{answers: []bool{true}}
	g, _ := newGate(t, e.root, gate.WithConfirmer(c))
	req := readCall("plugin", e.p("r/a"))
	req.SideEffect = "unknown"
	mustAllow(t, g, req)
	if len(c.asked) != 1 {
		t.Fatalf("asked %d; want 1 (fail-safe prompt)", len(c.asked))
	}
}

func TestApproveAll_PromptsEvenForAutoAllow(t *testing.T) {
	e := writableTree(t)
	var out bytes.Buffer
	g, _ := newGate(t, e.root, gate.WithApproveAll(true), gate.WithConfirmer(prompter("y\n", &out)))
	req := readCall("cat", e.p("r/a"))
	req.Argv = func(c map[string][]string) []string { return append([]string{"/bin/cat", "--"}, c["path"]...) }
	mustAllow(t, g, req)
	if !strings.Contains(out.String(), "/bin/cat -- "+e.p("r/a")) {
		t.Fatalf("prompt %q should show the canonical argv", out.String())
	}
}

func TestPolicyDeny_BeatsApproveAll(t *testing.T) {
	e := writableTree(t)
	tbl := policy.Merge(policy.Default(), policy.Table{Rules: []policy.Rule{
		{SideEffect: policy.SideEffectRead, Network: policy.NetworkNone, Action: policy.ActionDeny, Reason: "reads off"},
	}})
	g, _ := newGate(t, e.root, gate.WithPolicy(tbl), gate.WithApproveAll(true), gate.WithConfirmer(refuseConfirm{t}))
	ge := mustRefuse(t, g, readCall("cat", e.p("r/a")), gate.KindPolicy)
	if !strings.Contains(ge.Message, "reads off") {
		t.Fatalf("message %q should carry the rule reason", ge.Message)
	}
}

func TestStrictDeny_BeatsApproveAll(t *testing.T) {
	e := writableTree(t)
	g, _ := newGate(t, e.root, gate.WithApproveAll(true), gate.WithConfirmer(refuseConfirm{t}))
	mustRefuse(t, g, readCall("cat", e.p("outside")), gate.KindDenied)
}

func promptModeTree(t *testing.T) *fsEnv {
	t.Helper()
	e := newFS(t)
	e.file(t, "p/a", "a")
	e.file(t, "p/.env", "TOKEN=x")
	e.file(t, "elsewhere/b", "b")
	e.scopeYAML(t, `mode: prompt
allow:
  - "{root}/p/**"
`)
	return e
}

func TestScopePromptMode(t *testing.T) {
	e := promptModeTree(t)

	var out bytes.Buffer
	g, _ := newGate(t, e.root, gate.WithConfirmer(prompter("y\n", &out)))
	mustAllow(t, g, readCall("cat", e.p("p/.env")))
	if !strings.Contains(out.String(), e.p("p/.env")) || !strings.Contains(out.String(), "read") {
		t.Fatalf("prompt %q should name path and op", out.String())
	}

	g, _ = newGate(t, e.root, gate.WithConfirmer(prompter("n\n", &bytes.Buffer{})))
	mustRefuse(t, g, readCall("cat", e.p("p/.env")), gate.KindDeclined)

	// No terminal: denied, not merely declined, with the reason.
	g, _ = newGate(t, e.root, gate.WithConfirmer(noTTY(&bytes.Buffer{})))
	ge := mustRefuse(t, g, readCall("cat", e.p("p/.env")), gate.KindDenied)
	if !strings.Contains(ge.Message, "terminal") {
		t.Fatalf("message %q should say no terminal", ge.Message)
	}

	// kit semantics: Unknown is allowed outside strict mode.
	g, _ = newGate(t, e.root, gate.WithConfirmer(refuseConfirm{t}))
	mustAllow(t, g, readCall("cat", e.p("elsewhere/b")))
}

func TestScopeWarnMode(t *testing.T) {
	e := newFS(t)
	e.file(t, "p/.env", "TOKEN=x")
	e.scopeYAML(t, `mode: warn
allow:
  - "{root}/p/**"
`)
	g, logs := newGate(t, e.root, gate.WithConfirmer(refuseConfirm{t}))
	mustAllow(t, g, readCall("cat", e.p("p/.env")))
	if !strings.Contains(logs.String(), "WARN") || !strings.Contains(logs.String(), e.p("p/.env")) {
		t.Fatalf("warn mode should log the path: %q", logs.String())
	}
}

// Scope prompt, policy prompt and --tools-approve merge into one
// question.
func TestPrompt_OneQuestionPerCall(t *testing.T) {
	e := promptModeTree(t)
	c := &confirmer{answers: []bool{true}}
	g, _ := newGate(t, e.root, gate.WithConfirmer(c), gate.WithApproveAll(true))
	req := gate.Request{Tool: "sed", SideEffect: "destructive", Paths: []gate.PathArg{
		{Param: "path", Values: []string{e.p("p/.env")}, Op: scope.Read | scope.Write},
	}}
	mustAllow(t, g, req)
	if len(c.asked) != 1 {
		t.Fatalf("asked %d questions; want 1", len(c.asked))
	}
	q := c.asked[0]
	for _, want := range []string{"scope", "policy", "--tools-approve"} {
		if !strings.Contains(q, want) {
			t.Errorf("question lacks %q: %s", want, q)
		}
	}
}

func TestNoScopeFile_DeniesEveryCall(t *testing.T) {
	e := newFS(t)
	e.file(t, "p/a", "a")
	g, _ := newGate(t, e.root, gate.WithApproveAll(true), gate.WithConfirmer(refuseConfirm{t}))
	ge := mustRefuse(t, g, readCall("cat", e.p("p/a")), gate.KindDenied)
	want := filepath.Join(e.cfg, "foo", "scope.yaml")
	if !strings.Contains(ge.Message, want) || !strings.Contains(ge.Message, "foo scope") {
		t.Fatalf("message %q should name %s and `foo scope`", ge.Message, want)
	}
}

func TestNoScopeFile_MacOSPathWithoutXDG(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS config dir")
	}
	e := newFS(t)
	if err := os.Unsetenv("XDG_CONFIG_HOME"); err != nil {
		t.Fatal(err)
	}
	sc, err := gate.LoadScope("foo")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(e.home, "Library", "Application Support", "foo", "scope.yaml")
	if sc.UserFile != want {
		t.Fatalf("UserFile = %q; want %q", sc.UserFile, want)
	}
	if sc.Configured() {
		t.Fatal("no scope.yaml exists; Configured() = true")
	}
}

func TestLoadScope_AddsSecretDenyList(t *testing.T) {
	e := newFS(t)
	e.file(t, "p/.env", "x")
	e.scopeYAML(t, `allow:
  - "{root}/p/**"
`)
	sc, err := gate.LoadScope("foo")
	if err != nil {
		t.Fatal(err)
	}
	if !sc.Configured() || len(sc.Files) != 1 {
		t.Fatalf("Files = %v; want the user scope.yaml", sc.Files)
	}
	for _, op := range []scope.Op{scope.Read, scope.Write, scope.Exec} {
		if dec, _ := sc.Policy.Check(scope.Path(e.p("p/.env")), op); dec != scope.Denied {
			t.Errorf("%v on .env = %v; want Denied", op, dec)
		}
	}
}

func TestLoadScope_ParseError(t *testing.T) {
	e := newFS(t)
	e.scopeYAML(t, "mode: sometimes\n")
	if _, err := gate.LoadScope("foo"); err == nil {
		t.Fatal("want parse error")
	}
}

func TestLoadPolicy_WritePromptOverlay(t *testing.T) {
	newFS(t)
	tbl, err := gate.LoadPolicy("foo")
	if err != nil {
		t.Fatal(err)
	}
	for se, want := range map[policy.SideEffect]policy.Action{
		policy.SideEffectRead:        policy.ActionAutoAllow,
		policy.SideEffectWrite:       policy.ActionPrompt,
		policy.SideEffectDestructive: policy.ActionPrompt,
	} {
		if got := tbl.Resolve(se, policy.NetworkNone).Action; got != want {
			t.Errorf("%s → %s; want %s", se, got, want)
		}
	}
}

// The user's tool-policy.yaml overlays foo's overlay.
func TestLoadPolicy_UserOverlayWins(t *testing.T) {
	e := newFS(t)
	e.file(t, "cfg/foo/tool-policy.yaml", `rules:
  - side_effect: write
    network: none
    action: auto-allow
    reason: "trusted"
`)
	tbl, err := gate.LoadPolicy("foo")
	if err != nil {
		t.Fatal(err)
	}
	if got := tbl.Resolve(policy.SideEffectWrite, policy.NetworkNone).Action; got != policy.ActionAutoAllow {
		t.Fatalf("write → %s; want auto-allow from the user overlay", got)
	}
}

func TestLoadPolicy_BadUserOverlay(t *testing.T) {
	e := newFS(t)
	e.file(t, "cfg/foo/tool-policy.yaml", "rules: [\n")
	if _, err := gate.LoadPolicy("foo"); err == nil {
		t.Fatal("want parse error")
	}
}

func TestNew_RequiresAbsoluteCwd(t *testing.T) {
	if _, err := gate.New(gate.WithCwd("rel")); err == nil {
		t.Fatal("want error for relative cwd")
	}
}

func TestAuthorize_ContextCanceled(t *testing.T) {
	e := projectTree(t)
	g, _ := newGate(t, e.root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := g.Authorize(ctx, grepCall(e.p("p")))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v; want context.Canceled", err)
	}
}
