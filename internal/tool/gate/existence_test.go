package gate_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"hop.top/foo/internal/tool/gate"
	"hop.top/foo/internal/tool/shim"
	"hop.top/kit/go/core/scope"
)

// existenceTree grants read+write under p only. out holds one of each
// kind of entry the model might probe outside the grant.
func existenceTree(t *testing.T, mode string) *fsEnv {
	t.Helper()
	e := newFS(t)
	e.file(t, "p/a", "a")
	e.file(t, "p/b", "b")
	e.mkdir(t, "p/d")
	e.link(t, "loop", "p/loop")
	e.file(t, "out/f", "f")
	e.file(t, "out/d/1", "1") // two entries: over the walk cap below
	e.file(t, "out/d/2", "2")
	e.link(t, e.p("out/nowhere"), "out/dl")
	e.file(t, "elsewhere/f", "e")
	e.link(t, e.p("elsewhere/f"), "out/l")
	e.link(t, e.p("elsewhere"), "out/ld")
	e.link(t, "loop", "out/loop")
	// Links outside the grant into it: what they point to is read in a
	// directory the scope does not grant, so they must answer like a
	// missing sibling, not reach the grant.
	e.link(t, e.p("p"), "out/lp")
	e.link(t, e.p("p/a"), "out/lpa")
	e.link(t, e.p("p/d"), "out/lpd")
	// Aliases of places the shim root guard protects: $HOME, /, an entry
	// directly under / and a top-level link's target (macOS /tmp ->
	// /private/tmp). Outside the grant they must answer like the rest.
	t.Setenv("HOME", e.mkdir(t, "home"))
	e.link(t, e.p("home"), "out/hl")
	e.link(t, "/", "out/rl")
	e.link(t, "/usr", "out/ul")
	e.link(t, topLinkTarget(), "out/tl")
	e.file(t, "out/locked/x", "x")
	locked := e.p("out/locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	e.scopeYAML(t, "mode: "+mode+"\nallow:\n  - \"{root}/p/**\"\n")
	return e
}

// outsideProbes are paths outside the grant, relative to the root: an
// existing file and directory (a tree too large to walk), missing
// paths, links (dangling, to elsewhere, looping, to $HOME, /, /usr and
// a top-level link's target), a path under a file (ENOTDIR), an
// unsearchable directory (EACCES when walked) and a path under it, and
// links into the grant (to its root, a file, a directory, and paths
// under the root link, existing or not).
var outsideProbes = []string{
	"out/f", "out/d", "out/m", "out/md/x",
	"out/dl", "out/l", "out/ld/x", "out/loop",
	"out/hl", "out/rl", "out/ul", "out/tl",
	"out/f/x", "out/locked", "out/locked/x", "out/dl/x",
	"out/lp", "out/lpa", "out/lpd", "out/lp/x", "out/lp/a", "out/lp/../p/a",
}

// topLinkTarget is what a top-level link points to when the target is
// not itself directly under / (macOS /tmp -> /private/tmp); "/" when
// no such link exists.
func topLinkTarget() string {
	for _, top := range []string{"/tmp", "/var", "/etc", "/bin", "/lib", "/sbin"} {
		fi, err := os.Lstat(top)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			continue
		}
		if target, err := filepath.EvalSymlinks(top); err == nil && filepath.Dir(target) != "/" {
			return target
		}
	}
	return "/"
}

// climbProbes leave a directory outside the grant with ".." and come
// back into it: read lexically, each is p/a, which the scope allows;
// whether it resolves depends on what exists outside.
var climbProbes = []string{
	"out/d/../../p/a", "out/md/../../p/a", "out/f/../../p/a",
	"out/loop/../../p/a", "out/dl/../../p/a", "out/l/../../p/a",
	"out/locked/x/../../../p/a", "out/ld/x/../../../p/a",
}

// probeCall builds one kind of call on the probed path.
type probeCall struct {
	name  string
	write bool // needs approval from the policy table
	req   func(e *fsEnv, path string) gate.Request
}

func probeCalls() []probeCall {
	one := func(tool, se string, pa gate.PathArg) func(*fsEnv, string) gate.Request {
		return func(_ *fsEnv, path string) gate.Request {
			pa.Values = []string{path}
			return gate.Request{Tool: tool, SideEffect: se, Paths: []gate.PathArg{pa}}
		}
	}
	return []probeCall{
		{"cat", false, one("cat", "read", gate.PathArg{Param: "path", Op: scope.Read, MustExist: true})},
		{"grep -r", false, one("grep", "read", gate.PathArg{Param: "path", Op: scope.Read, Recursion: gate.FilterBefore})},
		{"find", false, one("find", "read", gate.PathArg{Param: "path", Op: scope.Read, MustExist: true, Recursion: gate.FilterAfter})},
		{"rm", true, one("rm", "destructive", gate.PathArg{Param: "path", Op: scope.Write, Target: gate.Dirent, MustExist: true})},
		{"rm -r", true, one("rm", "destructive", gate.PathArg{Param: "path", Op: scope.Write, Target: gate.Dirent, MustExist: true, Recursion: gate.AllOrNothing})},
		{"mkdir -p", true, one("mkdir", "write", gate.PathArg{Param: "path", Op: scope.Write, Target: gate.Dirent, Parents: true})},
		{"cp dst", true, func(e *fsEnv, path string) gate.Request { return cpCall(e.p("p/a"), path) }},
		{"cp 2 srcs dst", true, func(e *fsEnv, path string) gate.Request { return cpCall(e.p("p/a"), path, e.p("p/b")) }},
		{"cp -R dst", true, func(e *fsEnv, path string) gate.Request {
			req := cpCall(e.p("p/d"), path)
			req.Paths[0].Recursion = gate.AllOrNothing
			req.Paths[1].Recursion = gate.AllOrNothing
			return req
		}},
		{"mv src", true, func(e *fsEnv, path string) gate.Request {
			return gate.Request{Tool: "mv", SideEffect: "write", Paths: []gate.PathArg{
				{Param: "src", Values: []string{path}, Op: scope.Read | scope.Write, Target: gate.Dirent, MustExist: true, Recursion: gate.AllOrNothing},
				{Param: "dst", Values: []string{e.p("p/d")}, Op: scope.Write, Target: gate.Dirent, IntoDir: true, Recursion: gate.AllOrNothing},
			}}
		}},
	}
}

// modelBody is the refusal exactly as the model receives it, with the
// path the model supplied (and the forms derived from it alone: cleaned,
// its parent) replaced by placeholders.
func modelBody(t *testing.T, ge *gate.Error, supplied string) string {
	t.Helper()
	if ge == nil {
		return "<allowed>"
	}
	data, err := json.Marshal(shim.NewErrorBody(ge))
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	clean := filepath.Clean(supplied)
	subs := map[string]string{supplied: "<P>", clean: "<P>", filepath.Dir(clean): "<P/..>"}
	keys := make([]string, 0, len(subs))
	for k := range subs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		out = strings.ReplaceAll(out, `"`+k+`"`, `"`+subs[k]+`"`)
		out = strings.ReplaceAll(out, k+" ", subs[k]+" ")
	}
	return out
}

// A path the scope does not grant gets the same answer whether it
// exists or not, whatever it is: the model must not be able to probe
// the filesystem outside the grant. Only the path it supplied differs.
func TestExistence_OutsideGrantAnswersAlike(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the unsearchable directory")
	}
	for _, mode := range []string{"strict", "prompt", "warn"} {
		for _, call := range probeCalls() {
			if mode == "warn" && !call.write {
				continue // warn mode runs reads outside the grant by design
			}
			for _, set := range [][]string{outsideProbes, climbProbes} {
				t.Run(mode+"/"+call.name+"/"+set[0], func(t *testing.T) {
					e := existenceTree(t, mode)
					g, _ := newGate(t, e.root, gate.WithConfirmer(noTTY(&bytes.Buffer{})), gate.WithMaxWalk(1))
					var first, firstBody string
					for _, rel := range set {
						path := e.root + "/" + rel // not e.p: Join would clean ".."
						_, ge := authorize(t, g, call.req(e, path))
						body := modelBody(t, ge, path)
						if ge == nil || (ge.Kind != gate.KindDenied && ge.Kind != gate.KindDeclined) {
							t.Errorf("%s: got %s; want denied or declined", rel, body)
						}
						if first == "" {
							first, firstBody = rel, body
							continue
						}
						if body != firstBody {
							t.Errorf("answers differ:\n  %-18s %s\n  %-18s %s", first, firstBody, rel, body)
						}
					}
				})
			}
		}
	}
}

// Paths the scope grants still report what is wrong with them.
func TestExistence_InsideGrantReportsErrors(t *testing.T) {
	e := existenceTree(t, "strict")
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))

	cat := func(p string) gate.Request {
		req := readCall("cat", p)
		req.Paths[0].MustExist = true
		return req
	}
	ge := mustRefuse(t, g, cat(e.p("p/missing")), gate.KindNotFound)
	if ge.Path != e.p("p/missing") {
		t.Errorf("not_found path = %q", ge.Path)
	}
	mustRefuse(t, g, rmCall(e.p("p/missing")), gate.KindNotFound)
	mustRefuse(t, g, cat(e.p("p/loop")), gate.KindInvalidArgs)
	mustRefuse(t, g, cat(e.p("p/nothing")+"/../a"), gate.KindInvalidArgs)
	mustRefuse(t, g, cpCall(e.p("p/a"), e.p("p/new"), e.p("p/b")), gate.KindInvalidArgs)
	grant := mustAllow(t, g, cpCall(e.p("p/a"), e.p("p/d")))
	wantCanonical(t, grant, "dst", e.p("p/d/a"))
	grant = mustAllow(t, g, cat(e.p("p/d")+"/../a"))
	wantCanonical(t, grant, "path", e.p("p/a"))

	// Under a file: kit cannot check it, so it is refused, saying why.
	ge = mustRefuse(t, g, cat(e.p("p/a/x")), gate.KindDenied)
	if !strings.Contains(ge.Message, "not a directory") {
		t.Errorf("message %q should say why the path cannot be checked", ge.Message)
	}
}

// In prompt mode the question is asked before anything about the path
// is reported; once approved, a missing path is not_found.
func TestExistence_PromptAsksBeforeReporting(t *testing.T) {
	e := existenceTree(t, "prompt")
	for _, tc := range []struct {
		rel  string
		kind gate.Kind
	}{{"out/f", ""}, {"out/m", gate.KindNotFound}, {"out/md/../f", gate.KindInvalidArgs}} {
		c := &confirmer{answers: []bool{true}}
		g, _ := newGate(t, e.root, gate.WithConfirmer(c))
		req := readCall("cat", e.root+"/"+tc.rel)
		req.Paths[0].MustExist = true
		_, ge := authorize(t, g, req)
		if len(c.asked) != 1 {
			t.Errorf("%s: asked %d questions; want 1", tc.rel, len(c.asked))
		}
		switch {
		case tc.kind == "" && ge != nil:
			t.Errorf("%s: approved call refused: %v", tc.rel, ge)
		case tc.kind != "" && (ge == nil || ge.Kind != tc.kind):
			t.Errorf("%s: got %v; want %s", tc.rel, ge, tc.kind)
		}
	}
}
