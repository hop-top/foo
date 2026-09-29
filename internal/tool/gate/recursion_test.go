package gate_test

import (
	"fmt"
	"strings"
	"testing"

	"hop.top/foo/internal/tool/gate"
	"hop.top/kit/go/core/scope"
)

// projectTree: /p readable and writable, with secrets inside, and a
// link pointing outside.
func projectTree(t *testing.T) *fsEnv {
	t.Helper()
	e := newFS(t)
	e.file(t, "p/a.go", "a")
	e.file(t, "p/.env", "TOKEN=x")
	e.file(t, "p/sub/b.go", "b")
	e.file(t, "p/sub/.env", "TOKEN=y")
	e.file(t, "outside/o.go", "o")
	e.link(t, e.p("outside/o.go"), "p/out.go")
	e.scopeYAML(t, `allow:
  - "{root}/p/**"
`)
	return e
}

func grepCall(values ...string) gate.Request {
	return gate.Request{Tool: "grep", SideEffect: "read", Paths: []gate.PathArg{
		{Param: "path", Values: values, Op: scope.Read, Recursion: gate.FilterBefore},
	}}
}

func TestFilterBefore_SkipsDeniedAndLinks(t *testing.T) {
	e := projectTree(t)
	g, _ := newGate(t, e.root)
	grant := mustAllow(t, g, grepCall(e.p("p")))
	wantCanonical(t, grant, "path", e.p("p"))
	got := strings.Join(grant.Files["path"], "\n")
	want := strings.Join([]string{e.p("p/a.go"), e.p("p/sub/b.go")}, "\n")
	if got != want {
		t.Fatalf("Files = %q; want %q", got, want)
	}
	if grant.Filtered != 2 {
		t.Fatalf("Filtered = %d; want 2 (.env files)", grant.Filtered)
	}
}

func TestFilterBefore_FileRoot(t *testing.T) {
	e := projectTree(t)
	g, _ := newGate(t, e.root)
	grant := mustAllow(t, g, grepCall(e.p("p/a.go")))
	if got := grant.Files["path"]; len(got) != 1 || got[0] != e.p("p/a.go") {
		t.Fatalf("Files = %q", got)
	}
}

func TestFilterBefore_DeniedRoot(t *testing.T) {
	e := projectTree(t)
	g, _ := newGate(t, e.root)
	mustRefuse(t, g, grepCall(e.p("outside")), gate.KindDenied)
}

func TestFilterBefore_WalkCap(t *testing.T) {
	e := projectTree(t)
	for i := range 5 {
		e.file(t, fmt.Sprintf("p/many/f%d", i), "x")
	}
	g, _ := newGate(t, e.root, gate.WithMaxWalk(4))
	ge := mustRefuse(t, g, grepCall(e.p("p")), gate.KindInvalidArgs)
	if !strings.Contains(ge.Message, "4") {
		t.Fatalf("message %q should name the cap", ge.Message)
	}
}

func TestFilterAfter_AllowChecksEachEntry(t *testing.T) {
	e := projectTree(t)
	g, _ := newGate(t, e.root)
	req := gate.Request{Tool: "find", SideEffect: "read", Paths: []gate.PathArg{
		{Param: "path", Values: []string{e.p("p")}, Op: scope.Read, Recursion: gate.FilterAfter},
	}}
	grant := mustAllow(t, g, req)
	if grant.Allow == nil {
		t.Fatal("Allow is nil for a FilterAfter call")
	}
	for path, want := range map[string]bool{
		e.p("p/a.go"):       true,
		e.p("p/sub"):        true,
		e.p("p/.env"):       false,
		e.p("p/sub/.env"):   false,
		e.p("p/out.go"):     false, // link to a denied target
		e.p("outside/o.go"): false,
		"relative/path":     false,
	} {
		if got := grant.Allow(path); got != want {
			t.Errorf("Allow(%s) = %v; want %v", path, got, want)
		}
	}
}

func TestFilterAfter_NilWithoutFilterAfterArg(t *testing.T) {
	e := projectTree(t)
	g, _ := newGate(t, e.root)
	if grant := mustAllow(t, g, readCall("ls", e.p("p"))); grant.Allow != nil {
		t.Fatal("Allow should be nil without a FilterAfter argument")
	}
}

func rmRecursive(values ...string) gate.Request {
	return gate.Request{Tool: "rm", SideEffect: "destructive", Paths: []gate.PathArg{
		{Param: "path", Values: values, Op: scope.Write, Target: gate.Dirent, MustExist: true, Recursion: gate.AllOrNothing},
	}}
}

func TestAllOrNothing_OneDeniedEntryDeniesCall(t *testing.T) {
	e := projectTree(t)
	e.mkdir(t, "p/clean/x")
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))

	ge := mustRefuse(t, g, rmRecursive(e.p("p/sub")), gate.KindDenied)
	if ge.Param != "path" || ge.Path != e.p("p/sub") {
		t.Fatalf("denied %s %s; want path %s", ge.Param, ge.Path, e.p("p/sub"))
	}
	if !strings.Contains(ge.Message, e.p("p/sub/.env")) {
		t.Fatalf("message %q should list the offender", ge.Message)
	}

	mustAllow(t, g, rmRecursive(e.p("p/clean")))
}

func TestAllOrNothing_ListsFirstFiveOffenders(t *testing.T) {
	e := projectTree(t)
	for i := range 7 {
		e.file(t, fmt.Sprintf("p/many/k%d.pem", i), "k")
	}
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))
	ge := mustRefuse(t, g, rmRecursive(e.p("p/many")), gate.KindDenied)
	for i := range 5 {
		if !strings.Contains(ge.Message, e.p(fmt.Sprintf("p/many/k%d.pem", i))) {
			t.Errorf("message lacks offender %d: %q", i, ge.Message)
		}
	}
	if strings.Contains(ge.Message, "k5.pem") || !strings.Contains(ge.Message, "2 more") {
		t.Errorf("message should cut at five and count the rest: %q", ge.Message)
	}
}

func TestAllOrNothing_LinkEntryToDeniedTargetDenied(t *testing.T) {
	e := projectTree(t)
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))
	e.mkdir(t, "p/tree")
	e.link(t, e.p("outside/o.go"), "p/tree/l")
	ge := mustRefuse(t, g, rmRecursive(e.p("p/tree")), gate.KindDenied)
	if !strings.Contains(ge.Message, e.p("p/tree/l")) {
		t.Fatalf("message %q should list the link", ge.Message)
	}
}

func TestAllOrNothing_WalkCap(t *testing.T) {
	e := projectTree(t)
	for i := range 5 {
		e.file(t, fmt.Sprintf("p/many/f%d", i), "x")
	}
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()), gate.WithMaxWalk(3))
	mustRefuse(t, g, rmRecursive(e.p("p/many")), gate.KindDenied)
}

// cp -R: every source entry is read-checked and mapped under the
// destination for a write check.
func TestAllOrNothing_CopyTreeMapsDestination(t *testing.T) {
	e := newFS(t)
	e.file(t, "r/tree/a", "a")
	e.file(t, "r/tree/sub/b", "b")
	e.mkdir(t, "w")
	e.scopeYAML(t, `allow:
  - path: "{root}/r/**"
    ops: [read]
  - "{root}/w/**"
deny:
  - path: "{root}/w/tree/sub/**"
    ops: [write]
`)
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))
	req := gate.Request{Tool: "cp", SideEffect: "write", Paths: []gate.PathArg{
		{Param: "src", Values: []string{e.p("r/tree")}, Op: scope.Read, MustExist: true, Recursion: gate.AllOrNothing},
		{Param: "dst", Values: []string{e.p("w")}, Op: scope.Write, Target: gate.Dirent, IntoDir: true, Recursion: gate.AllOrNothing},
	}}
	ge := mustRefuse(t, g, req, gate.KindDenied)
	if ge.Param != "dst" || !strings.Contains(ge.Message, e.p("w/tree/sub/b")) {
		t.Fatalf("denied %s %q; want dst naming %s", ge.Param, ge.Message, e.p("w/tree/sub/b"))
	}

	// Into a fresh destination name: mapped under it.
	req.Paths[1].Values = []string{e.p("w/copy")}
	mustAllow(t, g, req)
}
