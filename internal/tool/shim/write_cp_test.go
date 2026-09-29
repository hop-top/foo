package shim

import (
	"strings"
	"testing"

	"hop.top/foo/internal/tool/gate"
	"hop.top/kit/go/core/scope"
)

// Every test here runs the built-in cp spec on the pinned system cp
// (BSD on macOS, GNU coreutils or busybox on Linux) inside a temp tree.

func TestCP_CopiesThroughCanonicalPaths(t *testing.T) {
	b := newWriteBox(t)
	b.file(t, "w/a", "A")
	l := wTool(t, "cp")

	res := mustCall(t, b.eng, l, `{"src":["a"],"dst":"b"}`)

	wWantOK(t, res)
	wWantArgv(t, l, res, b.p("w/a"), b.p("w/b"))
	if indexOf(res.Argv, "-n") < 0 {
		t.Errorf("argv %q: no-clobber -n missing", res.Argv)
	}
	if got := wRead(t, b.p("w/b")); got != "A" {
		t.Errorf("w/b = %q", got)
	}
	req := b.gate.last
	if req.SideEffect != "write" {
		t.Errorf("side effect %q; want write", req.SideEffect)
	}
	src, dst := req.Paths[0], req.Paths[1]
	if src.Op != scope.Read || src.Target != gate.Follow || !src.MustExist || src.Recursion != gate.NoRecursion {
		t.Errorf("src request %+v", src)
	}
	if dst.Op != scope.Write || dst.Target != gate.Dirent || !dst.IntoDir || dst.Recursion != gate.NoRecursion {
		t.Errorf("dst request %+v", dst)
	}
}

func TestCP_NoClobberUnlessOverwriteEscalates(t *testing.T) {
	b := newWriteBox(t)
	b.file(t, "w/a", "new")
	b.file(t, "w/b", "old")
	l := wTool(t, "cp")

	_, err := call(t, b.eng, l, `{"src":["a"],"dst":"b"}`)
	if ge := wantKind(t, err, gate.KindInvalidArgs); ge.Param != "dst" || ge.Path != b.p("w/b") {
		t.Errorf("refusal %+v; want dst %s", ge, b.p("w/b"))
	}
	if got := wRead(t, b.p("w/b")); got != "old" {
		t.Fatalf("refused call replaced w/b: %q", got)
	}

	res := mustCall(t, b.eng, l, `{"src":["a"],"dst":"b","overwrite":true}`)
	wWantOK(t, res)
	if b.gate.last.SideEffect != "destructive" {
		t.Errorf("overwrite side effect %q; want destructive", b.gate.last.SideEffect)
	}
	if indexOf(res.Argv, "-n") >= 0 {
		t.Errorf("overwrite argv %q still has -n", res.Argv)
	}
	if got := wRead(t, b.p("w/b")); got != "new" {
		t.Errorf("w/b = %q; want replaced", got)
	}
}

func TestCP_IntoExistingDirMapsEachSource(t *testing.T) {
	b := newWriteBox(t)
	b.file(t, "r/a", "A")
	b.file(t, "w/b", "B")
	b.dir(t, "w/d")
	b.file(t, "w/d/b", "keep")
	l := wTool(t, "cp")

	res := mustCall(t, b.eng, l, `{"src":["`+b.p("r/a")+`"],"dst":"d"}`)
	wWantOK(t, res)
	wWantArgv(t, l, res, b.p("r/a"), b.p("w/d"))
	if got := wRead(t, b.p("w/d/a")); got != "A" {
		t.Errorf("w/d/a = %q", got)
	}
	if res.Paths[1].Resolved != b.p("w/d/a") {
		t.Errorf("dst report %+v; want the mapped path", res.Paths[1])
	}

	// A second source whose mapped entry exists: the whole call is
	// refused, nothing copied.
	b.file(t, "r/c", "C")
	_, err := call(t, b.eng, l, `{"src":["`+b.p("r/c")+`","b"],"dst":"d"}`)
	if ge := wantKind(t, err, gate.KindInvalidArgs); ge.Path != b.p("w/d/b") {
		t.Errorf("refusal %+v; want mapped %s", ge, b.p("w/d/b"))
	}
	if wExists(b.p("w/d/c")) || wRead(t, b.p("w/d/b")) != "keep" {
		t.Error("refused multi-source copy changed w/d")
	}

	// Several sources need an existing directory.
	_, err = call(t, b.eng, l, `{"src":["b","`+b.p("r/c")+`"],"dst":"nowhere"}`)
	wantKind(t, err, gate.KindInvalidArgs)
	if wExists(b.p("w/nowhere")) {
		t.Error("refused call created w/nowhere")
	}
}

func TestCP_DestinationOutsideWriteScopeDenied(t *testing.T) {
	b := newWriteBox(t)
	b.file(t, "w/a", "A")
	l := wTool(t, "cp")

	for _, dst := range []string{b.p("r/a"), b.p("out/a")} {
		_, err := call(t, b.eng, l, `{"src":["a"],"dst":"`+dst+`"}`)
		if ge := wantKind(t, err, gate.KindDenied); ge.Param != "dst" {
			t.Errorf("dst %s: denied on %q; want dst", dst, ge.Param)
		}
		if wExists(dst) {
			t.Errorf("denied copy created %s", dst)
		}
	}
	// A link in the writable dir pointing outside: dirent keeps the
	// link, and its target is checked too.
	b.link(t, b.p("out/target"), "w/esc")
	_, err := call(t, b.eng, l, `{"src":["a"],"dst":"esc","overwrite":true}`)
	wantKind(t, err, gate.KindDenied)
	if wExists(b.p("out/target")) {
		t.Error("copy wrote through a link to outside the scope")
	}
}

func TestCP_RecursiveAllOrNothing(t *testing.T) {
	b := newWriteBox(t)
	b.file(t, "w/t/a", "A")
	b.file(t, "w/t/sub/b", "B")
	b.link(t, "a", "w/t/rel") // link inside the tree, to a sibling
	l := wTool(t, "cp")

	// Directory without recursive: cp itself refuses (a result).
	res := mustCall(t, b.eng, l, `{"src":["t"],"dst":"u"}`)
	if res.OK || wExists(b.p("w/u")) {
		t.Fatalf("non-recursive dir copy: exit %d, u exists %v", res.ExitCode, wExists(b.p("w/u")))
	}

	res = mustCall(t, b.eng, l, `{"src":["t"],"dst":"u","recursive":true}`)
	wWantOK(t, res)
	wWantArgv(t, l, res, b.p("w/t"), b.p("w/u"))
	if i := indexOf(res.Argv, "-R"); i < 0 || indexOf(res.Argv, "-P") < 0 {
		t.Errorf("argv %q; want -R -P", res.Argv)
	}
	if wRead(t, b.p("w/u/sub/b")) != "B" || !wIsLink(b.p("w/u/rel")) {
		t.Errorf("tree not copied with links as links")
	}
	req := b.gate.last
	if req.Paths[0].Recursion != gate.AllOrNothing || req.Paths[1].Recursion != gate.AllOrNothing {
		t.Errorf("recursive request %+v", req.Paths)
	}

	// One source entry the scope denies (read): nothing is copied.
	b.gate.deny = append(b.gate.deny, scopeRule{root: b.p("w/t/sub/b"), ops: scope.Read})
	_, err := call(t, b.eng, l, `{"src":["t"],"dst":"v","recursive":true}`)
	if ge := wantKind(t, err, gate.KindDenied); ge.Param != "src" || !strings.Contains(ge.Message, b.p("w/t/sub/b")) {
		t.Errorf("refusal %+v; want src naming w/t/sub/b", ge)
	}
	if wExists(b.p("w/v")) {
		t.Fatal("denied tree copy created w/v")
	}
	b.gate.deny = b.gate.deny[:len(b.gate.deny)-1]

	// One mapped destination entry the scope denies (write): nothing is
	// copied either.
	b.dir(t, "w/d")
	b.gate.deny = append(b.gate.deny, scopeRule{root: b.p("w/d/t/sub"), ops: scope.Write})
	_, err = call(t, b.eng, l, `{"src":["t"],"dst":"d","recursive":true}`)
	if ge := wantKind(t, err, gate.KindDenied); ge.Param != "dst" {
		t.Errorf("refusal %+v; want dst", ge)
	}
	if wExists(b.p("w/d/t")) {
		t.Fatal("denied tree copy created w/d/t")
	}

	// A link inside the tree pointing outside the scope: its target
	// is checked (fail-closed), so the tree is refused.
	b.gate.deny = b.gate.deny[:len(b.gate.deny)-1]
	b.file(t, "out/secret", "S")
	b.link(t, b.p("out/secret"), "w/t/esc")
	_, err = call(t, b.eng, l, `{"src":["t"],"dst":"x","recursive":true}`)
	wantKind(t, err, gate.KindDenied)
	if wExists(b.p("w/x")) {
		t.Fatal("tree with an escaping link was copied")
	}
}

func TestCP_ArgvNeverCarriesRawValues(t *testing.T) {
	b := newWriteBox(t)
	b.file(t, "w/-R", "flag-named")
	b.dir(t, "w/sub")
	l := wTool(t, "cp")

	res := mustCall(t, b.eng, l, `{"src":["-R"],"dst":"./sub/../copy"}`)
	wWantOK(t, res)
	wWantArgv(t, l, res, b.p("w/-R"), b.p("w/copy"))
	if wRead(t, b.p("w/copy")) != "flag-named" {
		t.Error("flag-named file not copied as a file")
	}
	for _, a := range res.Argv {
		if a == "-R" || strings.Contains(a, "..") {
			t.Fatalf("argv %q carries a raw value", res.Argv)
		}
	}
}
