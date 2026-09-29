package shim

import (
	"os"
	"testing"

	"hop.top/foo/internal/tool/gate"
	"hop.top/kit/go/core/scope"
)

func TestMV_MovesThroughCanonicalPaths(t *testing.T) {
	b := newWriteBox(t)
	b.file(t, "w/a", "A")
	l := wTool(t, "mv")

	res := mustCall(t, b.eng, l, `{"src":["a"],"dst":"b"}`)

	wWantOK(t, res)
	wWantArgv(t, l, res, b.p("w/a"), b.p("w/b"))
	if wExists(b.p("w/a")) || wRead(t, b.p("w/b")) != "A" {
		t.Fatal("w/a not moved to w/b")
	}
	req := b.gate.last
	if req.SideEffect != "write" {
		t.Errorf("side effect %q; want write", req.SideEffect)
	}
	src, dst := req.Paths[0], req.Paths[1]
	if src.Op != scope.Write || src.Target != gate.Dirent || !src.MustExist || src.Recursion != gate.AllOrNothing {
		t.Errorf("src request %+v; want write dirent must-exist all-or-nothing", src)
	}
	if dst.Op != scope.Write || dst.Target != gate.Dirent || !dst.IntoDir || dst.Recursion != gate.AllOrNothing {
		t.Errorf("dst request %+v", dst)
	}
}

// Moving removes the source entry: a read-only grant on it is not
// enough.
func TestMV_SourceNeedsWrite(t *testing.T) {
	b := newWriteBox(t)
	b.file(t, "r/a", "A")
	l := wTool(t, "mv")

	_, err := call(t, b.eng, l, `{"src":["`+b.p("r/a")+`"],"dst":"a"}`)

	ge := wantKind(t, err, gate.KindDenied)
	if ge.Param != "src" || ge.Op != scope.Write {
		t.Errorf("denied %s op %v; want src write", ge.Param, ge.Op)
	}
	if !wExists(b.p("r/a")) || wExists(b.p("w/a")) {
		t.Fatal("denied move changed the tree")
	}

	// Writable parent, entry readable but not writable: still denied,
	// on the entry itself.
	b.file(t, "w/locked", "L")
	b.gate.deny = append(b.gate.deny, scopeRule{root: b.p("w/locked"), ops: scope.Write})
	_, err = call(t, b.eng, l, `{"src":["locked"],"dst":"moved"}`)
	ge = wantKind(t, err, gate.KindDenied)
	if ge.Param != "src" || ge.Path != b.p("w/locked") || ge.Op != scope.Write {
		t.Errorf("denied %s %s op %v; want src w/locked write", ge.Param, ge.Path, ge.Op)
	}
	if !wExists(b.p("w/locked")) || wExists(b.p("w/moved")) {
		t.Fatal("mv moved an entry it may not write")
	}
}

func TestMV_NoClobberUnlessOverwriteEscalates(t *testing.T) {
	b := newWriteBox(t)
	b.file(t, "w/a", "new")
	b.file(t, "w/b", "old")
	l := wTool(t, "mv")

	_, err := call(t, b.eng, l, `{"src":["a"],"dst":"b"}`)
	if ge := wantKind(t, err, gate.KindInvalidArgs); ge.Param != "dst" {
		t.Errorf("refusal %+v; want dst", ge)
	}
	if wRead(t, b.p("w/b")) != "old" || !wExists(b.p("w/a")) {
		t.Fatal("refused move changed the tree")
	}

	res := mustCall(t, b.eng, l, `{"src":["a"],"dst":"b","overwrite":true}`)
	wWantOK(t, res)
	if b.gate.last.SideEffect != "destructive" || indexOf(res.Argv, "-n") >= 0 {
		t.Errorf("overwrite: side effect %q argv %q", b.gate.last.SideEffect, res.Argv)
	}
	if wExists(b.p("w/a")) || wRead(t, b.p("w/b")) != "new" {
		t.Error("overwrite did not replace w/b")
	}
}

// A symlink source moves as the link; its target stays put.
func TestMV_MovesLinkNotTarget(t *testing.T) {
	b := newWriteBox(t)
	b.file(t, "w/target", "T")
	b.link(t, "target", "w/ln")
	b.dir(t, "w/d")
	l := wTool(t, "mv")

	res := mustCall(t, b.eng, l, `{"src":["ln"],"dst":"d"}`)
	wWantOK(t, res)
	wWantArgv(t, l, res, b.p("w/ln"), b.p("w/d"))
	if !wIsLink(b.p("w/d/ln")) || wExists(b.p("w/ln")) {
		t.Fatal("link not moved as a link")
	}
	if wRead(t, b.p("w/target")) != "T" {
		t.Error("link target changed")
	}
}

func TestMV_DirectoryTreeAllOrNothing(t *testing.T) {
	b := newWriteBox(t)
	b.file(t, "w/t/a", "A")
	b.file(t, "w/t/sub/b", "B")
	b.dir(t, "w/d")
	l := wTool(t, "mv")

	// One entry of the source tree is not writable: nothing moves.
	b.gate.deny = append(b.gate.deny, scopeRule{root: b.p("w/t/sub/b"), ops: scope.Write})
	_, err := call(t, b.eng, l, `{"src":["t"],"dst":"d"}`)
	if ge := wantKind(t, err, gate.KindDenied); ge.Param != "src" {
		t.Errorf("refusal %+v; want src", ge)
	}
	if !wExists(b.p("w/t/sub/b")) || wExists(b.p("w/d/t")) {
		t.Fatal("denied tree move changed the tree")
	}
	b.gate.deny = b.gate.deny[:len(b.gate.deny)-1]

	// One mapped destination entry is not writable: nothing moves.
	b.gate.deny = append(b.gate.deny, scopeRule{root: b.p("w/d/t/sub"), ops: scope.Write})
	_, err = call(t, b.eng, l, `{"src":["t"],"dst":"d"}`)
	if ge := wantKind(t, err, gate.KindDenied); ge.Param != "dst" {
		t.Errorf("refusal %+v; want dst", ge)
	}
	if wExists(b.p("w/d/t")) {
		t.Fatal("denied tree move created w/d/t")
	}
	b.gate.deny = b.gate.deny[:len(b.gate.deny)-1]

	res := mustCall(t, b.eng, l, `{"src":["t"],"dst":"d"}`)
	wWantOK(t, res)
	if wRead(t, b.p("w/d/t/sub/b")) != "B" || wExists(b.p("w/t")) {
		t.Fatal("tree not moved into w/d")
	}

	// An existing directory at the mapped place: refused even with
	// overwrite (mv would merge or nest somewhere unchecked).
	b.file(t, "w/t/c", "C")
	if err := os.MkdirAll(b.p("w/d/t"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = call(t, b.eng, l, `{"src":["t"],"dst":"d","overwrite":true}`)
	wantKind(t, err, gate.KindInvalidArgs)
	if !wExists(b.p("w/t/c")) {
		t.Fatal("refused move changed the tree")
	}
}
