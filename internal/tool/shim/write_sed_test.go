package shim

import (
	"encoding/json"
	"strings"
	"testing"

	"hop.top/foo/internal/tool/gate"
	"hop.top/kit/go/core/scope"
)

func sedArgs(t *testing.T, kv map[string]any) string {
	t.Helper()
	data, err := json.Marshal(kv)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSed_InPlaceThroughCanonicalPath(t *testing.T) {
	b := newWriteBox(t)
	f := b.file(t, "w/f.txt", "one two one\n")
	l := wTool(t, "sed")

	res := mustCall(t, b.eng, l, `{"path":["f.txt"],"find":"one","replace":"1","global":true}`)

	wWantOK(t, res)
	wWantArgv(t, l, res, f)
	if got := wRead(t, f); got != "1 two 1\n" {
		t.Errorf("file = %q", got)
	}
	if i := indexOf(res.Argv, "s\x01one\x011\x01g"); i < 1 || res.Argv[i-1] != "-e" {
		t.Errorf("argv %q lacks the generated -e script", res.Argv)
	}
	// The host's own sed, whatever its flavor, got that flavor's argv.
	flavor := b.eng.flavors().Detect(l.Bin)
	t.Logf("host sed %s flavor %s argv %q", l.Bin, flavor, res.Argv)
	head := strings.Join(res.Argv[1:wDashDash(res.Argv)], " ")
	want := map[string]string{
		FlavorGNU:     "--sandbox -i -e s\x01one\x011\x01g",
		FlavorBSD:     "-i  -e s\x01one\x011\x01g",
		FlavorBusyBox: "-i -e s\x01one\x011\x01g",
	}[flavor]
	if head != want {
		t.Errorf("%s argv before -- = %q; want %q", flavor, head, want)
	}
	req := b.gate.last
	if pa := req.Paths[0]; req.SideEffect != "destructive" || pa.Op != scope.Read|scope.Write || pa.Target != gate.Follow {
		t.Errorf("request side effect %q path %+v; want destructive read|write follow", req.SideEffect, pa)
	}
}

func TestSed_DryRunChangesNothing(t *testing.T) {
	b := newWriteBox(t)
	f := b.file(t, "w/f.txt", "a-A-a\n")
	l := wTool(t, "sed")

	res := mustCall(t, b.eng, l, `{"path":["f.txt"],"find":"a","replace":"b","global":true,"ignore_case":true,"dry_run":true}`)

	wWantOK(t, res)
	if got := stdout(res); got != "b-b-b\n" {
		t.Errorf("stdout = %q", got)
	}
	if got := wRead(t, f); got != "a-A-a\n" {
		t.Fatalf("dry run edited the file: %q", got)
	}
	if b.gate.last.SideEffect != "read" {
		t.Errorf("dry run side effect %q; want read", b.gate.last.SideEffect)
	}
	for _, a := range res.Argv {
		if strings.HasPrefix(a, "-i") {
			t.Errorf("dry run argv %q has %q", res.Argv, a)
		}
	}
}

// Find and replace are data: slashes, &, backrefs and delimiter-like
// characters behave as sed defines them inside one s command, and can
// never end it or add flags.
func TestSed_TrickyFindReplace(t *testing.T) {
	for _, tc := range []struct {
		name, in, out string
		args          map[string]any
	}{
		{"slashes", "/usr/local/bin\n", "/opt/bin\n", map[string]any{"find": "/usr/local/", "replace": "/opt/"}},
		{"ampersand is the match", "cat\n", "[cat]\n", map[string]any{"find": "cat", "replace": "[&]"}},
		{"escaped ampersand is literal", "cat\n", "c&t\n", map[string]any{"find": "a", "replace": `\&`}},
		{"BRE backref", "ab\n", "ba\n", map[string]any{"find": `\(a\)\(b\)`, "replace": `\2\1`}},
		{"ERE backref", "key=val\n", "val=key\n", map[string]any{"find": `([a-z]+)=([a-z]+)`, "replace": `\2=\1`, "extended": true}},
		{"delimiter-like chars", "a|b#c,d;e\n", "a b c d e\n", map[string]any{"find": `[|#,;]`, "replace": " ", "global": true}},
		{"w and e are text", "x\n", "w /tmp/leak e\n", map[string]any{"find": "x", "replace": "w /tmp/leak e"}},
		{"escaped trailing backslash", "x\n", `\` + "\n", map[string]any{"find": "x", "replace": `\\`}},
		{"occurrence", "a a a\n", "a b a\n", map[string]any{"find": "a", "replace": "b", "occurrence": 2}},
		{"ignore case", "Hello\n", "Bye\n", map[string]any{"find": "hello", "replace": "Bye", "ignore_case": true}},
		{"empty replace deletes", "keep-drop\n", "keep\n", map[string]any{"find": "-drop", "replace": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newWriteBox(t)
			f := b.file(t, "w/f", tc.in)
			tc.args["path"] = []string{"f"}
			res := mustCall(t, b.eng, wTool(t, "sed"), sedArgs(t, tc.args))
			wWantOK(t, res)
			if got := wRead(t, f); got != tc.out {
				t.Errorf("file = %q; want %q", got, tc.out)
			}
		})
	}
}

func TestSed_RejectsUnsafeOrAmbiguousInput(t *testing.T) {
	b := newWriteBox(t)
	f := b.file(t, "w/f", "x\n")
	l := wTool(t, "sed")
	for _, tc := range []struct {
		name  string
		args  map[string]any
		param string
	}{
		{"newline in replace", map[string]any{"find": "x", "replace": "y\nw /tmp/leak"}, "replace"},
		{"newline in find", map[string]any{"find": "x\n1d", "replace": "y"}, "find"},
		{"CR in find", map[string]any{"find": "x\r", "replace": "y"}, "find"},
		{"delimiter in find", map[string]any{"find": "x\x01", "replace": "y"}, "find"},
		{"delimiter in replace", map[string]any{"find": "x", "replace": "y\x01w /tmp/leak"}, "replace"},
		{"NUL", map[string]any{"find": "x\x00", "replace": "y"}, "find"},
		{"trailing backslash", map[string]any{"find": "x", "replace": `y\`}, "replace"},
		{"empty find", map[string]any{"find": "", "replace": "y"}, "find"},
		{"global with occurrence", map[string]any{"find": "x", "replace": "y", "global": true, "occurrence": 2}, "occurrence"},
		{"occurrence out of range", map[string]any{"find": "x", "replace": "y", "occurrence": 513}, "occurrence"},
		{"raw script", map[string]any{"find": "x", "replace": "y", "script": "w /tmp/leak"}, "script"},
		{"raw expression", map[string]any{"find": "x", "replace": "y", "expression": "1d"}, "expression"},
		{"missing find", map[string]any{"replace": "y"}, "find"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.args["path"] = []string{"f"}
			calls := b.gate.calls
			_, err := call(t, b.eng, l, sedArgs(t, tc.args))
			if ge := wantKind(t, err, gate.KindInvalidArgs); ge.Param != tc.param {
				t.Errorf("param %q; want %q (%v)", ge.Param, tc.param, ge)
			}
			if b.gate.calls != calls {
				t.Error("invalid call reached the authorizer")
			}
		})
	}
	if got := wRead(t, f); got != "x\n" {
		t.Fatalf("a rejected call edited the file: %q", got)
	}
}

// The model can only fill find/replace and flags: no param reaches argv
// as a script, file or expression.
func TestSed_NoModelSuppliedScript(t *testing.T) {
	s := wTool(t, "sed").Spec
	if s.Script == nil || s.Script.Kind != "sed_substitute" {
		t.Fatalf("sed spec has no generated script: %+v", s.Script)
	}
	for _, p := range s.Params {
		if p.Type == TypeString && !p.script {
			t.Errorf("string param %q reaches argv directly", p.Name)
		}
	}
	for _, tok := range s.Argv {
		if name, ok := placeholder(tok); ok && name != scriptPlaceholder {
			if p := s.byName[name]; p.Type == TypeString || p.Type == TypeInt {
				t.Errorf("argv places %s param %q", p.Type, name)
			}
		}
		if tok == "-f" || tok == "--file" || tok == "-n" {
			t.Errorf("argv template has %q", tok)
		}
	}
}

// Per-flavor argv: GNU runs sandboxed with a bare -i, BSD takes the
// suffix as its own (empty) token, busybox takes a bare -i.
func TestSed_FlavorArgv(t *testing.T) {
	b := newWriteBox(t)
	b.file(t, "w/f", "x\n")
	l := wTool(t, "sed")
	f := b.p("w/f")
	for _, tc := range []struct{ name, version, want string }{
		{"gnu", "sed (GNU sed) 4.9", "[--sandbox][-i][-e][s\x01x\x01y\x01][--][" + f + "]"},
		{"bsd", "sed: illegal option -- -", "[-i][][-e][s\x01x\x01y\x01][--][" + f + "]"},
		{"busybox", "BusyBox v1.37.0 (2026-01-10) multi-call binary.", "[-i][-e][s\x01x\x01y\x01][--][" + f + "]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := wArgvRecorder(t, b.p("out"), "sed-"+tc.name, tc.version)
			res := mustCall(t, b.eng, wWithBin(l, rec), `{"path":["f"],"find":"x","replace":"y"}`)
			if got := stdout(res); got != tc.want {
				t.Errorf("argv = %q; want %q", got, tc.want)
			}
			res = mustCall(t, b.eng, wWithBin(l, rec), `{"path":["f"],"find":"x","replace":"y","dry_run":true,"extended":true}`)
			wantDry := strings.Replace(strings.Replace(strings.Replace(tc.want, "[-i][]", "", 1), "[-i]", "", 1), "[-e]", "[-E][-e]", 1)
			if got := stdout(res); got != wantDry {
				t.Errorf("dry-run argv = %q; want %q", got, wantDry)
			}
		})
	}
}

// Following the path: a link is edited through, never replaced by a
// regular file.
func TestSed_EditsLinkTargetKeepsLink(t *testing.T) {
	b := newWriteBox(t)
	target := b.file(t, "w/real", "old\n")
	b.link(t, "real", "w/ln")
	l := wTool(t, "sed")

	res := mustCall(t, b.eng, l, `{"path":["ln"],"find":"old","replace":"new"}`)
	wWantOK(t, res)
	wWantArgv(t, l, res, target)
	if !wIsLink(b.p("w/ln")) || wRead(t, target) != "new\n" {
		t.Fatalf("link replaced or target not edited: link %v, target %q", wIsLink(b.p("w/ln")), wRead(t, target))
	}
}

func TestSed_OutOfScopeDenied(t *testing.T) {
	b := newWriteBox(t)
	f := b.file(t, "r/f", "x\n")
	l := wTool(t, "sed")

	_, err := call(t, b.eng, l, `{"path":["`+f+`"],"find":"x","replace":"y"}`)
	if ge := wantKind(t, err, gate.KindDenied); ge.Op != scope.Write {
		t.Errorf("denied op %v; want write", ge.Op)
	}
	if wRead(t, f) != "x\n" {
		t.Fatal("denied sed edited the file")
	}
	d := b.dir(t, "w/dir")
	_, err = call(t, b.eng, l, `{"path":["`+d+`"],"find":"x","replace":"y"}`)
	wantKind(t, err, gate.KindInvalidArgs)
}

func wDashDash(argv []string) int { return indexOf(argv, "--") }
