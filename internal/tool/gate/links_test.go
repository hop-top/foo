package gate_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"hop.top/foo/internal/tool/gate"
	"hop.top/kit/go/core/scope"
)

// linkCalls are the calls a path through a link must keep working for,
// on a directory d holding the file a and the directory sub.
func linkCalls(d string) map[string]struct {
	req  gate.Request
	want []string // Canonical of the checked param, relative to d
} {
	mkdirP := gate.Request{Tool: "mkdir", SideEffect: "write", Paths: []gate.PathArg{
		{Param: "path", Values: []string{d + "/new/x"}, Op: scope.Write, Target: gate.Dirent, Parents: true},
	}}
	grep := readCall("grep", d)
	grep.Paths[0].Recursion = gate.FilterBefore
	find := readCall("find", d)
	find.Paths[0].Recursion = gate.FilterAfter
	mv := gate.Request{Tool: "mv", SideEffect: "write", Paths: []gate.PathArg{
		{Param: "src", Values: []string{d + "/a"}, Op: scope.Read | scope.Write, Target: gate.Dirent, MustExist: true},
		{Param: "dst", Values: []string{d + "/sub"}, Op: scope.Write, Target: gate.Dirent, IntoDir: true},
	}}
	return map[string]struct {
		req  gate.Request
		want []string
	}{
		"cat":      {readCall("cat", d+"/a"), []string{"a"}},
		"ls root":  {readCall("ls", d), []string{""}},
		"grep -r":  {grep, []string{""}},
		"find":     {find, []string{""}},
		"rm":       {rmCall(d + "/a"), []string{"a"}},
		"mkdir -p": {mkdirP, []string{"new/x"}},
		"cp dst":   {cpCall(d+"/a", d+"/sub"), []string{"sub/a"}},
		"cp new":   {cpCall(d+"/a", d+"/b"), []string{"b"}},
		"mv":       {mv, []string{"a"}},
	}
}

// runLinkCalls expects every linkCalls call through via to be allowed,
// on the physical paths under real.
func runLinkCalls(t *testing.T, e *fsEnv, via, real string) {
	t.Helper()
	for name, c := range linkCalls(via) {
		t.Run(name, func(t *testing.T) {
			g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))
			grant := mustAllow(t, g, c.req)
			param := c.req.Paths[0].Param
			if name == "cp dst" || name == "cp new" {
				param = "dst"
			}
			want := make([]string, len(c.want))
			for i, rel := range c.want {
				want[i] = filepath.Join(real, rel)
			}
			wantCanonical(t, grant, param, want...)
		})
	}
}

// linkTarget fills dir with the tree linkCalls acts on.
func linkTarget(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The documented macOS case: /tmp links to /private/tmp, and a rule
// written as /tmp/** must keep working for paths sent through /tmp,
// though the link sits in /, which no rule grants. Pinned so a rule
// refusing every path through an ungranted link cannot pass the suite.
func TestLinks_RuleWrittenThroughTopLevelLink(t *testing.T) {
	e := newFS(t)
	var dir, rule string
	if fi, err := os.Lstat("/tmp"); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		d, err := os.MkdirTemp("/tmp", "foo-gate-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(d) })
		dir, rule = d, "/tmp/**"
	} else if raw := t.TempDir(); raw != canonical(t, raw) {
		dir, rule = raw, raw+"/**" // e.g. /var/folders on macOS with TMPDIR set
	} else {
		t.Skip("no top-level link on the way to a temp dir here")
	}
	linkTarget(t, dir)
	e.scopeYAML(t, "mode: strict\nallow:\n  - \""+rule+"\"\n")
	runLinkCalls(t, e, dir, canonical(t, dir))
}

// A grant written through a link in an ungranted directory (~/code
// linking elsewhere) works through that link, as the user named it.
// Written as the link's target instead, the same path through the link
// is refused like a missing sibling.
func TestLinks_RuleWrittenThroughHomeLink(t *testing.T) {
	e := newFS(t)
	real := e.mkdir(t, "code-real")
	linkTarget(t, real)
	e.link(t, real, "home/code")

	e.scopeYAML(t, "mode: strict\nallow:\n  - \"~/code/**\"\n")
	for _, via := range []string{"~/code", e.p("home/code")} {
		t.Run(via, func(t *testing.T) { runLinkCalls(t, e, via, real) })
	}

	// The resolved path still meets deny rules.
	e.file(t, "code-real/secret", "s")
	e.scopeYAML(t, "mode: strict\nallow:\n  - \"~/code/**\"\ndeny:\n  - \"{root}/code-real/secret\"\n")
	g, _ := newGate(t, e.root)
	mustRefuse(t, g, readCall("cat", "~/code/secret"), gate.KindDenied)

	// Granted by its target's name only: the link is not an entry point,
	// and answers like a missing sibling. p holds what probeCalls copy
	// and move.
	e.file(t, "p/a", "a")
	e.file(t, "p/b", "b")
	e.mkdir(t, "p/d")
	for _, mode := range []string{"strict", "prompt"} {
		e.scopeYAML(t, "mode: "+mode+"\nallow:\n  - \"{root}/code-real/**\"\n  - \"{root}/p/**\"\n")
		for _, call := range probeCalls() {
			t.Run(mode+"/"+call.name, func(t *testing.T) {
				g, _ := newGate(t, e.root, gate.WithConfirmer(noTTY(&bytes.Buffer{})))
				bodies := map[string]string{}
				for _, rel := range []string{"home/code/a", "home/nocode/a"} {
					path := e.p(rel)
					_, ge := authorize(t, g, call.req(e, path))
					bodies[rel] = modelBody(t, ge, path)
					if ge == nil || ge.Kind != gate.KindDenied {
						t.Errorf("%s: got %s; want denied", rel, bodies[rel])
					}
				}
				if bodies["home/code/a"] != bodies["home/nocode/a"] {
					t.Errorf("answers differ:\n  link    %s\n  missing %s", bodies["home/code/a"], bodies["home/nocode/a"])
				}
			})
		}
	}
}

// Links inside the grant to places inside it keep working.
func TestLinks_InsideGrantToInsideGrant(t *testing.T) {
	e := existenceTree(t, "strict")
	real := e.p("p/d")
	linkTarget(t, real)
	e.link(t, real, "p/ld")
	runLinkCalls(t, e, e.p("p/ld"), real)
}

func canonical(t *testing.T, p string) string {
	t.Helper()
	c, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
