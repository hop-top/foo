package shim

import (
	"os"
	"slices"
	"strings"
	"testing"

	"hop.top/foo/internal/tool/gate"
	"hop.top/kit/go/core/scope"
)

func TestMkdir_CreatesThroughCanonicalPaths(t *testing.T) {
	b := newWriteBox(t)
	l := wTool(t, "mkdir")

	res := mustCall(t, b.eng, l, `{"path":["a","b"]}`)

	wWantOK(t, res)
	wWantArgv(t, l, res, b.p("w/a"), b.p("w/b"))
	for _, d := range []string{"w/a", "w/b"} {
		if fi, err := os.Stat(b.p(d)); err != nil || !fi.IsDir() {
			t.Errorf("%s not created", d)
		}
	}
	req := b.gate.last
	if pa := req.Paths[0]; req.SideEffect != "write" || pa.Op != scope.Write || pa.Target != gate.Dirent || pa.Parents {
		t.Errorf("request side effect %q path %+v", req.SideEffect, pa)
	}
	if strings.Contains(string(NewTool(b.eng, l).Parameters()), "mode") {
		t.Error("mkdir exposes a mode param")
	}

	// Missing parent without parents=true: mkdir fails, nothing made.
	res = mustCall(t, b.eng, l, `{"path":["x/y"]}`)
	if res.OK || wExists(b.p("w/x")) {
		t.Errorf("mkdir without parents: exit %d, w/x exists %v", res.ExitCode, wExists(b.p("w/x")))
	}
}

func TestMkdir_ParentsChecksEveryMissingAncestor(t *testing.T) {
	b := newWriteBox(t)
	l := wTool(t, "mkdir")

	res := mustCall(t, b.eng, l, `{"path":["x/y/z"],"parents":true}`)
	wWantOK(t, res)
	if indexOf(res.Argv, "-p") < 0 || !b.gate.last.Paths[0].Parents {
		t.Errorf("argv %q parents %v", res.Argv, b.gate.last.Paths[0].Parents)
	}
	for _, anc := range []string{"w/x", "w/x/y"} {
		if !slices.Contains(b.gate.checked, "write "+b.p(anc)) {
			t.Errorf("missing ancestor %s not write-checked: %q", anc, b.gate.checked)
		}
	}
	if fi, err := os.Stat(b.p("w/x/y/z")); err != nil || !fi.IsDir() {
		t.Fatal("w/x/y/z not created")
	}

	// A missing ancestor the scope denies: nothing is created.
	b.gate.deny = append(b.gate.deny, scopeRule{root: b.p("w/p/q"), ops: scope.Write})
	_, err := call(t, b.eng, l, `{"path":["p/q/r"],"parents":true}`)
	wantKind(t, err, gate.KindDenied)
	if wExists(b.p("w/p")) {
		t.Fatal("denied mkdir -p created w/p")
	}
	// Outside every write rule.
	_, err = call(t, b.eng, l, `{"path":["`+b.p("r/n")+`"]}`)
	wantKind(t, err, gate.KindDenied)
	if wExists(b.p("r/n")) {
		t.Fatal("mkdir in the read-only tree")
	}
}

// rm names the directory entry: a symlink is removed, never its target.
func TestRM_RemovesLinkNotTarget(t *testing.T) {
	b := newWriteBox(t)
	target := b.file(t, "w/target", "keep me")
	b.dir(t, "w/dir/inner")
	b.file(t, "w/dir/inner/f", "keep me too")
	b.link(t, "target", "w/ln")
	b.link(t, "dir", "w/dirln")
	l := wTool(t, "rm")

	res := mustCall(t, b.eng, l, `{"path":["ln"]}`)
	if !wExists(target) || wRead(t, target) != "keep me" {
		t.Fatal("rm removed the link's target")
	}
	if wExists(b.p("w/ln")) {
		t.Error("link not removed")
	}
	wWantOK(t, res)
	wWantArgv(t, l, res, b.p("w/ln"))

	// recursive on a link to a directory removes the link only.
	res = mustCall(t, b.eng, l, `{"path":["dirln"],"recursive":true}`)
	wWantOK(t, res)
	if wExists(b.p("w/dirln")) || wRead(t, b.p("w/dir/inner/f")) != "keep me too" {
		t.Fatal("recursive rm of a link touched the linked directory")
	}
	req := b.gate.last
	if pa := req.Paths[0]; req.SideEffect != "destructive" || pa.Op != scope.Write || pa.Target != gate.Dirent || !pa.MustExist {
		t.Errorf("request side effect %q path %+v", req.SideEffect, pa)
	}
}

func TestRM_RecursiveAllOrNothing(t *testing.T) {
	b := newWriteBox(t)
	b.file(t, "w/t/a", "A")
	b.file(t, "w/t/sub/b", "B")
	l := wTool(t, "rm")

	// A directory without recursive: rm refuses (a result).
	res := mustCall(t, b.eng, l, `{"path":["t"]}`)
	if res.OK || !wExists(b.p("w/t/a")) {
		t.Fatalf("rm dir without recursive: exit %d", res.ExitCode)
	}

	b.gate.deny = append(b.gate.deny, scopeRule{root: b.p("w/t/sub/b"), ops: scope.Write})
	_, err := call(t, b.eng, l, `{"path":["t"],"recursive":true}`)
	if ge := wantKind(t, err, gate.KindDenied); !strings.Contains(ge.Message, b.p("w/t/sub/b")) {
		t.Errorf("refusal %v; want it to name w/t/sub/b", ge)
	}
	if !wExists(b.p("w/t/a")) || !wExists(b.p("w/t/sub/b")) {
		t.Fatal("denied recursive rm removed entries")
	}
	if b.gate.last.Paths[0].Recursion != gate.AllOrNothing {
		t.Errorf("recursion %v; want all-or-nothing", b.gate.last.Paths[0].Recursion)
	}
	b.gate.deny = b.gate.deny[:len(b.gate.deny)-1]

	res = mustCall(t, b.eng, l, `{"path":["t"],"recursive":true}`)
	wWantOK(t, res)
	wWantArgv(t, l, res, b.p("w/t"))
	if indexOf(res.Argv, "-R") < 0 || wExists(b.p("w/t")) {
		t.Fatalf("argv %q; tree still there %v", res.Argv, wExists(b.p("w/t")))
	}
	for _, a := range res.Argv {
		if a == "-f" || a == "-rf" || a == "-Rf" {
			t.Fatalf("argv %q forces", res.Argv)
		}
	}
}

func TestRM_MissingAndOutOfScope(t *testing.T) {
	b := newWriteBox(t)
	b.file(t, "r/a", "A")
	b.file(t, "w/.env", "SECRET")
	l := wTool(t, "rm")

	_, err := call(t, b.eng, l, `{"path":["nope"]}`)
	wantKind(t, err, gate.KindNotFound)
	for _, p := range []string{b.p("r/a"), ".env"} {
		_, err = call(t, b.eng, l, `{"path":["`+p+`"]}`)
		wantKind(t, err, gate.KindDenied)
	}
	if !wExists(b.p("r/a")) || !wExists(b.p("w/.env")) {
		t.Fatal("denied rm removed a file")
	}
}

// The root of the filesystem and the grant root itself are refused by
// the parent-write rule. A recorder stands in for rm, so even a gate
// bug could not remove anything outside the temp tree.
func TestRM_RootAndGrantRootRefused(t *testing.T) {
	b := newWriteBox(t)
	rec := wArgvRecorder(t, b.p("out"), "rm", "rm: illegal option -- -")
	l := wWithBin(wTool(t, "rm"), rec)

	for _, p := range []string{"/", ".", b.p("w"), b.p("w/."), "..", "/.."} {
		_, err := call(t, b.eng, l, `{"path":["`+p+`"],"recursive":true}`)
		wantKind(t, err, gate.KindDenied)
	}
}

// busybox rm has no -d: the param is not offered on that flavor.
func TestRM_BusyboxHasNoDirFlag(t *testing.T) {
	b := newWriteBox(t)
	l := wTool(t, "rm")
	bb := wWithBin(l, wArgvRecorder(t, b.p("out"), "bbrm", "BusyBox v1.37.0 multi-call binary."))
	bsd := wWithBin(l, wArgvRecorder(t, b.p("out"), "bsdrm", "rm: illegal option -- -"))
	b.file(t, "w/f", "F")

	if strings.Contains(string(NewTool(b.eng, bb).Parameters()), `"dir"`) {
		t.Error("busybox schema offers dir")
	}
	_, err := call(t, b.eng, bb, `{"path":["f"],"dir":true}`)
	wantKind(t, err, gate.KindInvalidArgs)

	res := mustCall(t, b.eng, bsd, `{"path":["f"],"dir":true}`)
	if got := stdout(res); got != "[-d][--]["+b.p("w/f")+"]" {
		t.Errorf("bsd argv = %s", got)
	}
}
