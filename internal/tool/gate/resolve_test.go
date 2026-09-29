package gate_test

import (
	"path/filepath"
	"strings"
	"testing"

	"hop.top/foo/internal/tool/gate"
	"hop.top/kit/go/core/scope"
)

// readOnlyTree is the plan's acceptance setup: `allow: /path/to/**`
// (read) in strict mode, plus foo's secret deny list.
func readOnlyTree(t *testing.T) *fsEnv {
	t.Helper()
	e := newFS(t)
	e.mkdir(t, "p/to/sub")
	e.file(t, "p/to/a", "a")
	e.mkdir(t, "outside/deep")
	e.file(t, "outside/secret", "s")
	e.scopeYAML(t, `mode: strict
allow:
  - path: "{root}/p/to/**"
    ops: [read]
`)
	return e
}

func TestResolve_AllowedDirCoversItself(t *testing.T) {
	e := readOnlyTree(t)
	g, _ := newGate(t, e.root)
	grant := mustAllow(t, g, readCall("ls", e.p("p/to")))
	wantCanonical(t, grant, "path", e.p("p/to"))
}

func TestResolve_RootDenied(t *testing.T) {
	e := readOnlyTree(t)
	g, _ := newGate(t, e.root)
	ge := mustRefuse(t, g, readCall("ls", "/"), gate.KindDenied)
	if ge.Param != "path" || ge.Path != "/" || ge.Op != scope.Read {
		t.Fatalf("error names %s %s %v; want path / read", ge.Param, ge.Path, ge.Op)
	}
}

func TestResolve_DotDotOutOfGrantDenied(t *testing.T) {
	e := readOnlyTree(t)
	g, _ := newGate(t, e.root)
	ge := mustRefuse(t, g, readCall("ls", e.p("p/to")+"/../.."), gate.KindDenied)
	if ge.Path != e.root {
		t.Fatalf("denied path = %q; want physical %q", ge.Path, e.root)
	}
}

func TestResolve_LinkToOutsideDenied(t *testing.T) {
	e := readOnlyTree(t)
	e.link(t, "/etc", "p/to/link")
	g, _ := newGate(t, e.root)
	ge := mustRefuse(t, g, readCall("ls", e.p("p/to/link")), gate.KindDenied)
	etc, _ := filepath.EvalSymlinks("/etc")
	if ge.Path != etc {
		t.Fatalf("denied path = %q; want resolved %q", ge.Path, etc)
	}
}

// The escape kit's lexical `..` cleaning lets through: link → outside,
// then `..` climbs from the link's target, not from the link.
func TestResolve_LinkDotDotEscapeDenied(t *testing.T) {
	e := readOnlyTree(t)
	e.link(t, e.p("outside/deep"), "p/to/link")
	raw := e.p("p/to/link") + "/.."

	// Premise: kit alone allows this path (lexical Clean → /p/to).
	sc, err := gate.LoadScope("foo")
	if err != nil {
		t.Fatal(err)
	}
	if dec, _ := sc.Policy.Check(scope.Path(raw), scope.Read); dec != scope.Allowed {
		t.Fatalf("premise: kit Check(%s) = %v; want Allowed (lexical ..)", raw, dec)
	}

	g, _ := newGate(t, e.root)
	ge := mustRefuse(t, g, readCall("ls", raw), gate.KindDenied)
	if ge.Path != e.p("outside") {
		t.Fatalf("denied path = %q; want physical %q", ge.Path, e.p("outside"))
	}
}

func TestResolve_RelativeAgainstCapturedCwd(t *testing.T) {
	e := readOnlyTree(t)

	g, _ := newGate(t, e.p("p"))
	grant := mustAllow(t, g, readCall("ls", "to"))
	wantCanonical(t, grant, "path", e.p("p/to"))

	g, _ = newGate(t, e.root)
	mustRefuse(t, g, readCall("ls", "to"), gate.KindDenied)
}

func TestResolve_DashRIsAPath(t *testing.T) {
	e := readOnlyTree(t)

	g, _ := newGate(t, e.p("p/to"))
	grant := mustAllow(t, g, readCall("ls", "-R"))
	wantCanonical(t, grant, "path", e.p("p/to/-R"))

	g, _ = newGate(t, e.root)
	ge := mustRefuse(t, g, readCall("ls", "-R"), gate.KindDenied)
	if ge.Path != e.p("-R") {
		t.Fatalf("denied path = %q; want %q", ge.Path, e.p("-R"))
	}
}

func TestResolve_TildeIsHomeOnly(t *testing.T) {
	e := newFS(t)
	e.file(t, "home/notes", "n")
	e.scopeYAML(t, `allow:
  - "~/**"
  - "{root}/cwd/**"
`)
	e.mkdir(t, "cwd")
	g, _ := newGate(t, e.p("cwd"))

	grant := mustAllow(t, g, readCall("cat", "~/notes", "~"))
	wantCanonical(t, grant, "path", e.p("home/notes"), e.home)

	// ~user and $VAR are literal names under cwd.
	grant = mustAllow(t, g, readCall("cat", "~root/x", "$HOME/x"))
	wantCanonical(t, grant, "path", e.p("cwd/~root/x"), e.p("cwd/$HOME/x"))
}

func TestResolve_MissingTail(t *testing.T) {
	e := newFS(t)
	e.mkdir(t, "w")
	e.mkdir(t, "outside")
	e.link(t, e.p("outside"), "w/out")
	e.scopeYAML(t, `allow:
  - "{root}/w/**"
`)
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))

	grant := mustAllow(t, g, readCall("cat", e.p("w/new/file")))
	wantCanonical(t, grant, "path", e.p("w/new/file"))

	// Missing tail under a link resolves through the link.
	ge := mustRefuse(t, g, readCall("cat", e.p("w/out/new")), gate.KindDenied)
	if ge.Path != e.p("outside/new") {
		t.Fatalf("denied path = %q; want %q", ge.Path, e.p("outside/new"))
	}

	// `..` after a missing component cannot be resolved physically.
	mustRefuse(t, g, readCall("cat", e.p("w/missing")+"/../x"), gate.KindInvalidArgs)
}

// A dangling final link names its target's location: a follow write
// would create the file there. kit alone checks the link path.
func TestResolve_DanglingLinkFollowsToTarget(t *testing.T) {
	e := newFS(t)
	e.mkdir(t, "w")
	e.mkdir(t, "outside")
	e.link(t, e.p("outside/new"), "w/dangling")
	e.scopeYAML(t, `allow:
  - "{root}/w/**"
`)
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))
	req := gate.Request{Tool: "tee", SideEffect: "write", Paths: []gate.PathArg{
		{Param: "path", Values: []string{e.p("w/dangling")}, Op: scope.Write},
	}}
	ge := mustRefuse(t, g, req, gate.KindDenied)
	if ge.Path != e.p("outside/new") {
		t.Fatalf("denied path = %q; want link target %q", ge.Path, e.p("outside/new"))
	}
}

func TestResolve_InvalidValues(t *testing.T) {
	e := readOnlyTree(t)
	g, _ := newGate(t, e.root)
	for _, v := range []string{"", "a\x00b"} {
		ge := mustRefuse(t, g, readCall("cat", v), gate.KindInvalidArgs)
		if ge.Param != "path" {
			t.Fatalf("param = %q; want path", ge.Param)
		}
	}
}

func TestResolve_MustExist(t *testing.T) {
	e := readOnlyTree(t)
	g, _ := newGate(t, e.root)
	req := readCall("cat", e.p("p/to/missing"))
	req.Paths[0].MustExist = true
	ge := mustRefuse(t, g, req, gate.KindNotFound)
	if ge.Path != e.p("p/to/missing") {
		t.Fatalf("path = %q", ge.Path)
	}
}

// One denied value denies the whole call (no partial `cat a b`).
func TestResolve_AnyDeniedValueDeniesCall(t *testing.T) {
	e := readOnlyTree(t)
	g, _ := newGate(t, e.root)
	ge := mustRefuse(t, g, readCall("cat", e.p("p/to/a"), e.p("outside/secret")), gate.KindDenied)
	if ge.Path != e.p("outside/secret") {
		t.Fatalf("denied path = %q", ge.Path)
	}
}

func TestResolve_SecretPathDenied(t *testing.T) {
	e := readOnlyTree(t)
	e.file(t, "p/to/.env", "TOKEN=x")
	g, _ := newGate(t, e.root)
	ge := mustRefuse(t, g, readCall("cat", e.p("p/to/.env")), gate.KindDenied)
	if !strings.Contains(ge.Message, "deny") {
		t.Fatalf("message %q should say a deny rule matched", ge.Message)
	}
}

// Plan: `rm path=/path/to/x` on a read-only grant.
func TestResolve_WriteOnReadOnlyGrantDenied(t *testing.T) {
	e := readOnlyTree(t)
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))
	req := gate.Request{Tool: "rm", SideEffect: "destructive", Paths: []gate.PathArg{
		{Param: "path", Values: []string{e.p("p/to/a")}, Op: scope.Write, Target: gate.Dirent, MustExist: true},
	}}
	ge := mustRefuse(t, g, req, gate.KindDenied)
	if ge.Op != scope.Write {
		t.Fatalf("op = %v; want write", ge.Op)
	}
}

// Plan: `cp src=/path/to/a dst=/tmp/b` names dst.
func TestResolve_CopyOutOfGrantNamesDst(t *testing.T) {
	e := readOnlyTree(t)
	e.mkdir(t, "tmp")
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))
	ge := mustRefuse(t, g, cpCall(e.p("p/to/a"), e.p("tmp/b")), gate.KindDenied)
	if ge.Param != "dst" {
		t.Fatalf("param = %q; want dst", ge.Param)
	}
}

// mv's source leaves its directory: op write, so a read grant is not
// enough.
func TestResolve_MoveSourceNeedsWrite(t *testing.T) {
	e := readOnlyTree(t)
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))
	req := gate.Request{Tool: "mv", SideEffect: "write", Paths: []gate.PathArg{
		{Param: "src", Values: []string{e.p("p/to/a")}, Op: scope.Write, Target: gate.Dirent, MustExist: true},
		{Param: "dst", Values: []string{e.p("p/to/b")}, Op: scope.Write, Target: gate.Dirent, IntoDir: true},
	}}
	ge := mustRefuse(t, g, req, gate.KindDenied)
	if ge.Param != "src" {
		t.Fatalf("param = %q; want src", ge.Param)
	}
}

// A combined op is checked bit by bit: a read-only allow rule must not
// satisfy read|write.
func TestResolve_CombinedOpNeedsEveryBit(t *testing.T) {
	e := readOnlyTree(t)
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))
	req := gate.Request{Tool: "sed", SideEffect: "destructive", Paths: []gate.PathArg{
		{Param: "path", Values: []string{e.p("p/to/a")}, Op: scope.Read | scope.Write},
	}}
	ge := mustRefuse(t, g, req, gate.KindDenied)
	if ge.Op != scope.Write {
		t.Fatalf("op = %v; want write", ge.Op)
	}
}

// Existing paths resolve exactly as filepath.EvalSymlinks does.
func TestCanonical_MatchesEvalSymlinksForExistingPaths(t *testing.T) {
	e := newFS(t)
	e.mkdir(t, "a/b/c")
	e.link(t, "../../b", "a/b/c/up")
	e.link(t, e.p("a"), "abs")
	// Built by concatenation: filepath.Join would clean them first.
	for _, raw := range []string{
		e.root + "/a/b/c/up/c/up",
		e.root + "/abs/b/../b/c",
		e.root + "/a//b/./c/",
		e.root + "/abs/b/c/up/..",
		e.root + "/a/b/c/up/../b/c/up/..",
	} {
		want, err := filepath.EvalSymlinks(raw)
		if err != nil {
			t.Fatal(err)
		}
		got, err := gate.Canonical(e.root, raw)
		if err != nil {
			t.Fatalf("Canonical(%s): %v", raw, err)
		}
		if got != want {
			t.Errorf("Canonical(%s) = %s; want %s", raw, got, want)
		}
	}
}

func TestCanonical_SymlinkLoop(t *testing.T) {
	e := newFS(t)
	e.link(t, e.p("loop2"), "loop1")
	e.link(t, e.p("loop1"), "loop2")
	if _, err := gate.Canonical(e.root, "loop1/x"); err == nil {
		t.Fatal("want error for a symlink loop")
	}
}

func cpCall(src, dst string, srcs ...string) gate.Request {
	return gate.Request{Tool: "cp", SideEffect: "write", Paths: []gate.PathArg{
		{Param: "src", Values: append([]string{src}, srcs...), Op: scope.Read, MustExist: true},
		{Param: "dst", Values: []string{dst}, Op: scope.Write, Target: gate.Dirent, IntoDir: true},
	}}
}
