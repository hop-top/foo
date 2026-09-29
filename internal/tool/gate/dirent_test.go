package gate_test

import (
	"testing"

	"hop.top/foo/internal/tool/gate"
	"hop.top/kit/go/core/scope"
)

// writableTree grants read+write under /w and read under /r.
func writableTree(t *testing.T) *fsEnv {
	t.Helper()
	e := newFS(t)
	e.mkdir(t, "w")
	e.file(t, "r/a", "a")
	e.mkdir(t, "outside")
	e.scopeYAML(t, `mode: strict
allow:
  - "{root}/w/**"
  - path: "{root}/r/**"
    ops: [read]
`)
	return e
}

func rmCall(values ...string) gate.Request {
	return gate.Request{Tool: "rm", SideEffect: "destructive", Paths: []gate.PathArg{
		{Param: "path", Values: values, Op: scope.Write, Target: gate.Dirent, MustExist: true},
	}}
}

// rm acts on the link, not its target.
func TestDirent_LinkInsideAllowedDirNamesTheLink(t *testing.T) {
	e := writableTree(t)
	e.file(t, "w/target", "t")
	e.link(t, e.p("w/target"), "w/link")
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))
	grant := mustAllow(t, g, rmCall(e.p("w/link")))
	wantCanonical(t, grant, "path", e.p("w/link"))
}

// A link inside a denied dir pointing at an allowed target: the entry
// check alone follows to the allowed target; the parent write check
// denies it.
func TestDirent_LinkInDeniedDirToAllowedTargetDenied(t *testing.T) {
	e := writableTree(t)
	e.file(t, "w/target", "t")
	e.link(t, e.p("w/target"), "outside/link")
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))
	ge := mustRefuse(t, g, rmCall(e.p("outside/link")), gate.KindDenied)
	if ge.Path != e.p("outside") || ge.Op != scope.Write {
		t.Fatalf("denied %s %v; want parent %s write", ge.Path, ge.Op, e.p("outside"))
	}
}

// The parent of a dirent is resolved; the final link is kept.
func TestDirent_ParentResolvedThroughLinks(t *testing.T) {
	e := writableTree(t)
	e.file(t, "w/real/f", "f")
	e.link(t, e.p("w/real"), "w/alias")
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))
	grant := mustAllow(t, g, rmCall(e.p("w/alias/f")))
	wantCanonical(t, grant, "path", e.p("w/real/f"))
}

// A dangling link in an allowed dir pointing outside: writing through
// it (cp dst) would create the target outside. kit cannot follow a
// dangling link, so the gate checks the target itself.
func TestDirent_DanglingLinkTargetChecked(t *testing.T) {
	e := writableTree(t)
	e.link(t, e.p("outside/new"), "w/dangling")
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))
	ge := mustRefuse(t, g, cpCall(e.p("r/a"), e.p("w/dangling")), gate.KindDenied)
	if ge.Param != "dst" || ge.Path != e.p("outside/new") {
		t.Fatalf("denied %s %s; want dst %s", ge.Param, ge.Path, e.p("outside/new"))
	}
}

// Removing the granted root needs write on its parent.
func TestDirent_GrantRootNotRemovable(t *testing.T) {
	e := writableTree(t)
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))
	ge := mustRefuse(t, g, rmCall(e.p("w")), gate.KindDenied)
	if ge.Path != e.root {
		t.Fatalf("denied path = %q; want parent %q", ge.Path, e.root)
	}
}

func TestDirent_MustExistUsesLstat(t *testing.T) {
	e := writableTree(t)
	e.link(t, e.p("w/nothing"), "w/dangling")
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))
	grant := mustAllow(t, g, rmCall(e.p("w/dangling")))
	wantCanonical(t, grant, "path", e.p("w/dangling"))

	mustRefuse(t, g, rmCall(e.p("w/missing")), gate.KindNotFound)
}

func TestIntoDir_ExistingDirMapsToBase(t *testing.T) {
	e := writableTree(t)
	e.mkdir(t, "w/dir")
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))

	grant := mustAllow(t, g, cpCall(e.p("r/a"), e.p("w/dir")))
	wantCanonical(t, grant, "dst", e.p("w/dir/a"))

	// Not an existing dir: the path itself.
	grant = mustAllow(t, g, cpCall(e.p("r/a"), e.p("w/new")))
	wantCanonical(t, grant, "dst", e.p("w/new"))
}

func TestIntoDir_MappedPathChecked(t *testing.T) {
	e := writableTree(t)
	e.mkdir(t, "w/dir")
	e.scopeYAML(t, `allow:
  - "{root}/w/**"
  - path: "{root}/r/**"
    ops: [read]
deny:
  - path: "{root}/w/dir/a"
    ops: [write]
`)
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))
	ge := mustRefuse(t, g, cpCall(e.p("r/a"), e.p("w/dir")), gate.KindDenied)
	if ge.Param != "dst" || ge.Path != e.p("w/dir/a") {
		t.Fatalf("denied %s %s; want dst %s", ge.Param, ge.Path, e.p("w/dir/a"))
	}
}

func TestIntoDir_LinkToDirMapsUnderTarget(t *testing.T) {
	e := writableTree(t)
	e.mkdir(t, "w/real")
	e.link(t, e.p("w/real"), "w/alias")
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))
	grant := mustAllow(t, g, cpCall(e.p("r/a"), e.p("w/alias")))
	wantCanonical(t, grant, "dst", e.p("w/real/a"))
}

func TestIntoDir_ManySourcesNeedExistingDir(t *testing.T) {
	e := writableTree(t)
	e.file(t, "r/b", "b")
	e.mkdir(t, "w/dir")
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))

	grant := mustAllow(t, g, cpCall(e.p("r/a"), e.p("w/dir"), e.p("r/b")))
	wantCanonical(t, grant, "dst", e.p("w/dir/a"), e.p("w/dir/b"))

	ge := mustRefuse(t, g, cpCall(e.p("r/a"), e.p("w/new"), e.p("r/b")), gate.KindInvalidArgs)
	if ge.Param != "dst" {
		t.Fatalf("param = %q; want dst", ge.Param)
	}
}

func mkdirCall(parents bool, values ...string) gate.Request {
	return gate.Request{Tool: "mkdir", SideEffect: "write", Paths: []gate.PathArg{
		{Param: "path", Values: values, Op: scope.Write, Target: gate.Dirent, Parents: parents},
	}}
}

func TestParents_EveryMissingAncestorChecked(t *testing.T) {
	e := writableTree(t)
	e.scopeYAML(t, `allow:
  - "{root}/w/**"
deny:
  - path: "{root}/w/a"
    ops: [write]
`)
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))

	ge := mustRefuse(t, g, mkdirCall(true, e.p("w/a/b/c")), gate.KindDenied)
	if ge.Path != e.p("w/a") {
		t.Fatalf("denied path = %q; want ancestor %q", ge.Path, e.p("w/a"))
	}

	grant := mustAllow(t, g, mkdirCall(true, e.p("w/x/y/z")))
	wantCanonical(t, grant, "path", e.p("w/x/y/z"))
}
