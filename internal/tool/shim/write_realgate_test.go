package shim

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/log/v2"
	"hop.top/foo/internal/tool/gate"
	"hop.top/kit/go/core/scope"
)

// askLog answers every approval question and keeps them.
type askLog struct {
	answer bool
	asked  []string
}

func (a *askLog) Confirm(q string) (bool, error) {
	a.asked = append(a.asked, q)
	return a.answer, nil
}

// realGateBox wires the write specs to the real gate: scope.yaml in a
// throwaway XDG_CONFIG_HOME grants read+write on w/ (and on home/),
// read on r/; kit's secret paths are denied as in production.
func realGateBox(t *testing.T) (*writeBox, *askLog) {
	t.Helper()
	ask := &askLog{answer: true}
	return realGateBoxMode(t, "strict", ask, true), ask
}

// realGateBoxMode is realGateBox in the given scope mode, asking c (a
// nil c is no terminal), with home/ granted or not.
func realGateBoxMode(t *testing.T, mode string, c gate.Confirmer, grantHome bool) *writeBox {
	t.Helper()
	b := newWriteBox(t)
	home := b.dir(t, "home")
	cfg := b.dir(t, "cfg")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", cfg)
	homeRule := ""
	if grantHome {
		homeRule = "\n  - \"{root}/home/**\""
	}
	b.file(t, "cfg/foo/scope.yaml", strings.ReplaceAll(`mode: `+mode+`
allow:
  - "{root}/w/**"`+homeRule+`
  - path: "{root}/r/**"
    ops: [read]
  - path: "{root}/wo/**"
    ops: [write]
`, "{root}", b.root))
	sc, err := gate.LoadScope("foo")
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := gate.LoadPolicy("foo")
	if err != nil {
		t.Fatal(err)
	}
	opts := []gate.Option{gate.WithScope(sc), gate.WithPolicy(tbl), gate.WithCwd(b.p("w")), gate.WithLogger(log.New(&bytes.Buffer{}))}
	if c != nil {
		opts = append(opts, gate.WithConfirmer(c))
	}
	g, err := gate.New(opts...)
	if err != nil {
		t.Fatal(err)
	}
	b.eng.Authorizer = g
	return b
}

// modelBody is the refusal as the model receives it, with the path it
// supplied (cleaned, and its parent) replaced by placeholders.
func modelBody(t *testing.T, err error, supplied string) string {
	t.Helper()
	var ge *gate.Error
	if !errors.As(err, &ge) {
		return fmt.Sprintf("<not refused: %v>", err)
	}
	data, merr := json.Marshal(NewErrorBody(ge))
	if merr != nil {
		t.Fatal(merr)
	}
	out := string(data)
	clean := filepath.Clean(supplied)
	for _, kv := range [][2]string{{supplied, "<P>"}, {clean, "<P>"}, {filepath.Dir(clean), "<P/..>"}} {
		out = strings.ReplaceAll(out, `"`+kv[0]+`"`, `"`+kv[1]+`"`)
		out = strings.ReplaceAll(out, kv[0]+" ", kv[1]+" ")
	}
	return out
}

// Outside the grant ($HOME not granted either), a link to $HOME, to /,
// to an entry directly under / or to a top-level link's target gets
// the answer a missing sibling gets, byte for byte: the root guard never looks at a path the scope
// does not let the call reach. strict denies; prompt with nobody to ask
// denies too.
func TestRealGate_RootGuardOutsideGrantAlike(t *testing.T) {
	for _, mode := range []string{"strict", "prompt"} {
		t.Run(mode, func(t *testing.T) {
			b := realGateBoxMode(t, mode, nil, false)
			runs := b.p("runs")
			rm := wWithBin(wTool(t, "rm"), wRunRecorder(t, b.p("out"), "rm", "rm: illegal option -- -", runs))
			mv := wWithBin(wTool(t, "mv"), wRunRecorder(t, b.p("out"), "mv", "mv: illegal option -- -", runs))
			cp := wWithBin(wTool(t, "cp"), wRunRecorder(t, b.p("out"), "cp", "cp: illegal option -- -", runs))
			b.link(t, b.p("home"), "out/homeln")
			b.link(t, "/", "out/rootln")
			b.link(t, "/usr", "out/usrln")
			probes := []string{"missing", "homeln", "rootln", "usrln"}
			if tl := topLinkTarget(); tl != "" {
				b.link(t, tl, "out/tln")
				probes = append(probes, "tln")
			}
			b.file(t, "w/a", "A")
			b.dir(t, "w/d")
			calls := []struct {
				name string
				l    *Loaded
				args func(p string) map[string]any
			}{
				{"rm", rm, func(p string) map[string]any { return map[string]any{"path": []string{p}} }},
				{"rm -r", rm, func(p string) map[string]any { return map[string]any{"path": []string{p}, "recursive": true} }},
				{"mv src", mv, func(p string) map[string]any { return map[string]any{"src": []string{p}, "dst": "d"} }},
				{"mv dst", mv, func(p string) map[string]any {
					return map[string]any{"src": []string{"a"}, "dst": p, "overwrite": true}
				}},
				{"cp dst", cp, func(p string) map[string]any {
					return map[string]any{"src": []string{"a"}, "dst": p, "overwrite": true}
				}},
			}
			for _, c := range calls {
				var first, firstBody string
				for _, rel := range probes {
					p := b.p("out") + "/" + rel
					args, _ := json.Marshal(c.args(p))
					res, err := call(t, b.eng, c.l, string(args))
					if res != nil {
						t.Fatalf("%s %s ran: argv %q", c.name, rel, res.Argv)
					}
					body := modelBody(t, err, p)
					if ge, ok := err.(*gate.Error); !ok || ge.Kind != gate.KindDenied {
						t.Errorf("%s %s: %s; want denied", c.name, rel, body)
					}
					if first == "" {
						first, firstBody = rel, body
						continue
					}
					if body != firstBody {
						t.Errorf("%s answers differ:\n  %-8s %s\n  %-8s %s", c.name, first, firstBody, rel, body)
					}
				}
			}
			if wExists(runs) {
				t.Errorf("a recorder ran: %q", wRead(t, runs))
			}
		})
	}
}

// Inside the grant, a link to $HOME (home/** is granted) passes the
// scope, may be approved, and is then refused by the root guard before
// rm runs. Out of the grant in prompt mode, the same holds once the
// user approves. Either way $HOME and the link stay.
func TestRealGate_RootGuardAfterApproval(t *testing.T) {
	b, ask := realGateBox(t)
	runs := b.p("runs")
	rm := wWithBin(wTool(t, "rm"), wRunRecorder(t, b.p("out"), "rm", "rm: illegal option -- -", runs))
	b.link(t, b.p("home"), "w/homeln")
	_, err := call(t, b.eng, rm, `{"path":["homeln"]}`)
	if ge := wantKind(t, err, gate.KindInvalidArgs); !strings.Contains(ge.Message, "is the home directory") {
		t.Errorf("rm homeln: %v; want the root guard", ge)
	}
	if len(ask.asked) != 1 {
		t.Errorf("prompts %q; want one (destructive), then the guard", ask.asked)
	}

	pa := &askLog{answer: true}
	pb := realGateBoxMode(t, "prompt", pa, true)
	prm := wWithBin(wTool(t, "rm"), wRunRecorder(t, pb.p("out"), "rm", "rm: illegal option -- -", pb.p("runs")))
	pb.link(t, pb.p("home"), "out/homeln")
	_, err = call(t, pb.eng, prm, `{"path":["`+pb.p("out/homeln")+`"]}`)
	wantGuard(t, err, "path")
	if len(pa.asked) != 1 {
		t.Errorf("prompt mode: prompts %q; want one", pa.asked)
	}
	if wExists(b.p("runs")) || wExists(pb.p("runs")) {
		t.Error("rm ran")
	}
	if !wIsLink(b.p("w/homeln")) || !wIsLink(pb.p("out/homeln")) {
		t.Error("a link to $HOME is gone")
	}
}

func TestRealGate_RMRefusesRootHomeAndGrantRoot(t *testing.T) {
	b, ask := realGateBox(t)
	// A recorder stands in for rm: nothing outside the temp tree can
	// be touched even if the gate let a call through.
	l := wWithBin(wTool(t, "rm"), wArgvRecorder(t, b.p("out"), "rm", "rm: illegal option -- -"))

	// /, ~ and entries directly under /: the root guard, whatever the
	// scope says (home/** is granted read+write).
	for _, p := range []string{"/", "~", "~/", b.p("home"), "/..", "/tmp/..", "/usr"} {
		_, err := call(t, b.eng, l, `{"path":["`+p+`"],"recursive":true}`)
		if ge := wantKind(t, err, gate.KindInvalidArgs); !strings.Contains(ge.Message, "whatever the scope") {
			t.Errorf("rm %s: refusal %v; want the root guard", p, ge)
		}
	}
	// The grant root: the parent-write rule.
	for _, p := range []string{".", b.p("w"), ".."} {
		_, err := call(t, b.eng, l, `{"path":["`+p+`"],"recursive":true}`)
		if ge := wantKind(t, err, gate.KindDenied); ge.Op != scope.Write {
			t.Errorf("rm %s: denied op %v; want write on the parent", p, ge.Op)
		}
	}
	if len(ask.asked) != 0 {
		t.Errorf("refused calls prompted: %q", ask.asked)
	}

	// mv of ~ is refused unprompted; mv into ~ lands below it and runs
	// (one write prompt).
	mv := wWithBin(wTool(t, "mv"), wArgvRecorder(t, b.p("out"), "mv", "mv: illegal option -- -"))
	_, err := call(t, b.eng, mv, `{"src":["~"],"dst":"x"}`)
	wantKind(t, err, gate.KindInvalidArgs)
	b.file(t, "w/into", "I")
	res := mustCall(t, b.eng, mv, `{"src":["into"],"dst":"~"}`)
	if got := stdout(res); got != "[-n][--]["+b.p("w/into")+"]["+b.p("home")+"]" {
		t.Errorf("mv into ~: argv %s", got)
	}
	if len(ask.asked) != 1 {
		t.Errorf("prompts %q; want one, for mv into ~", ask.asked)
	}

	// cp into ~ lands below it and runs too (one more write prompt).
	cp := wWithBin(wTool(t, "cp"), wArgvRecorder(t, b.p("out"), "cp", "cp: illegal option -- -"))
	res = mustCall(t, b.eng, cp, `{"src":["into"],"dst":"~"}`)
	if got := stdout(res); got != "[-n][--]["+b.p("w/into")+"]["+b.p("home")+"]" {
		t.Errorf("cp into ~: argv %s", got)
	}
	if len(ask.asked) != 2 {
		t.Errorf("prompts %q; want two, for mv and cp into ~", ask.asked)
	}
}

func TestRealGate_WriteSpecsEndToEnd(t *testing.T) {
	b, ask := realGateBox(t)
	b.file(t, "w/a", "A")
	b.file(t, "r/ro", "R")
	b.file(t, "w/t/x", "X")
	b.file(t, "w/t/.env", "TOKEN=1")

	// write prompts (foo's overlay) and shows the canonical argv.
	res := mustCall(t, b.eng, wTool(t, "cp"), `{"src":["a"],"dst":"b"}`)
	wWantOK(t, res)
	if len(ask.asked) != 1 || !strings.Contains(ask.asked[0], b.p("w/b")) || !strings.Contains(ask.asked[0], "write side effect") {
		t.Errorf("cp prompt %q", ask.asked)
	}

	// mv from a read-only grant: denied on src write.
	_, err := call(t, b.eng, wTool(t, "mv"), `{"src":["`+b.p("r/ro")+`"],"dst":"ro"}`)
	if ge := wantKind(t, err, gate.KindDenied); ge.Param != "src" || ge.Op != scope.Write {
		t.Errorf("mv refusal %+v", ge)
	}

	// mv from a write-only grant: denied on src read.
	b.file(t, "wo/a", "W")
	_, err = call(t, b.eng, wTool(t, "mv"), `{"src":["`+b.p("wo/a")+`"],"dst":"wa"}`)
	if ge := wantKind(t, err, gate.KindDenied); ge.Param != "src" || ge.Op != scope.Read {
		t.Errorf("mv write-only refusal %+v", ge)
	}
	if !wExists(b.p("wo/a")) || wExists(b.p("w/wa")) {
		t.Fatal("denied mv changed the tree")
	}

	// cp -R of a tree holding a secret: all-or-nothing denies it.
	_, err = call(t, b.eng, wTool(t, "cp"), `{"src":["t"],"dst":"u","recursive":true}`)
	if ge := wantKind(t, err, gate.KindDenied); !strings.Contains(ge.Message, ".env") {
		t.Errorf("cp -R refusal %v; want it to name .env", ge)
	}
	if wExists(b.p("w/u")) {
		t.Fatal("denied tree copy created w/u")
	}

	// rm -R of the same tree: denied, nothing removed.
	_, err = call(t, b.eng, wTool(t, "rm"), `{"path":["t"],"recursive":true}`)
	wantKind(t, err, gate.KindDenied)
	if !wExists(b.p("w/t/x")) {
		t.Fatal("denied rm -R removed entries")
	}

	// mkdir -p under the grant; the prompt names the canonical target.
	ask.asked = nil
	res = mustCall(t, b.eng, wTool(t, "mkdir"), `{"path":["m/n"],"parents":true}`)
	wWantOK(t, res)
	if fi, err := os.Stat(b.p("w/m/n")); err != nil || !fi.IsDir() || len(ask.asked) != 1 {
		t.Errorf("mkdir -p: %v, prompts %d", err, len(ask.asked))
	}

	// sed in place is destructive: prompted; declining changes nothing.
	f := b.file(t, "w/s", "x\n")
	ask.answer, ask.asked = false, nil
	_, err = call(t, b.eng, wTool(t, "sed"), `{"path":["s"],"find":"x","replace":"y"}`)
	wantKind(t, err, gate.KindDeclined)
	if wRead(t, f) != "x\n" || len(ask.asked) != 1 || !strings.Contains(ask.asked[0], "destructive") {
		t.Errorf("declined sed: file %q prompts %q", wRead(t, f), ask.asked)
	}
	ask.answer = true
	res = mustCall(t, b.eng, wTool(t, "sed"), `{"path":["s"],"find":"x","replace":"y"}`)
	wWantOK(t, res)
	if wRead(t, f) != "y\n" {
		t.Errorf("sed result %q", wRead(t, f))
	}

	// sed dry_run only reads: it previews a read-only file, no prompt.
	ask.asked = nil
	ro := b.file(t, "r/s.txt", "R\n")
	res = mustCall(t, b.eng, wTool(t, "sed"), `{"path":["`+ro+`"],"find":"R","replace":"&1","dry_run":true}`)
	wWantOK(t, res)
	if got := stdout(res); got != "&1\n" || len(ask.asked) != 0 {
		t.Errorf("sed preview on read-only grant: stdout %q prompts %q", got, ask.asked)
	}
	_, err = call(t, b.eng, wTool(t, "sed"), `{"path":["`+ro+`"],"find":"R","replace":"x"}`)
	if ge := wantKind(t, err, gate.KindDenied); ge.Op != scope.Write {
		t.Errorf("sed in place on read-only grant: %+v", ge)
	}

	// rm of a link: the real dirent resolution removes the link only.
	b.link(t, filepath.Join(b.p("w"), "a"), "w/ln")
	res = mustCall(t, b.eng, wTool(t, "rm"), `{"path":["ln"]}`)
	wWantOK(t, res)
	if wExists(b.p("w/ln")) || wRead(t, b.p("w/a")) != "A" {
		t.Fatal("rm followed the link")
	}
}
