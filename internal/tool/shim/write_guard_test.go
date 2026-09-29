package shim

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"hop.top/foo/internal/tool/gate"
)

// guardBox is a writeBox whose authorizer grants every path (fakeAuth:
// canonicalizes, never walks a tree), HOME is a throwaway directory,
// and rm/mv/cp are argv recorders that log each run: the guard must
// refuse on its own, and even a guard bug runs nothing but a printf.
type guardBox struct {
	*writeBox
	auth       *fakeAuth
	home       string
	runs       string // the recorders' run log
	rm, mv, cp *Loaded
}

func newGuardBox(t *testing.T) *guardBox {
	t.Helper()
	b := newWriteBox(t)
	auth := &fakeAuth{cwd: b.p("w")}
	b.eng.Authorizer = auth
	home := b.dir(t, "home")
	b.dir(t, "home/sub")
	t.Setenv("HOME", home)
	runs := b.p("runs")
	return &guardBox{
		writeBox: b,
		auth:     auth,
		home:     home,
		runs:     runs,
		rm:       wWithBin(wTool(t, "rm"), wRunRecorder(t, b.p("out"), "rm", "rm: illegal option -- -", runs)),
		mv:       wWithBin(wTool(t, "mv"), wRunRecorder(t, b.p("out"), "mv", "mv: illegal option -- -", runs)),
		cp:       wWithBin(wTool(t, "cp"), wRunRecorder(t, b.p("out"), "cp", "cp: illegal option -- -", runs)),
	}
}

// wRunRecorder is wArgvRecorder that also appends a line to log each
// time it runs (not for --version), so a test can prove it never ran.
func wRunRecorder(t *testing.T, dir, name, version, log string) string {
	t.Helper()
	return writeScript(t, dir, name, `if [ "$1" = --version ]; then echo "`+version+`"; exit 0; fi
echo "$0" >> '`+log+`'
for a in "$@"; do printf '[%s]' "$a"; done`)
}

// ran is how many times a recorder ran.
func (g *guardBox) ran() int {
	data, _ := os.ReadFile(g.runs)
	return strings.Count(string(data), "\n")
}

// lexicalSpellings name /, $HOME or an entry directly under / as
// written: "~" expanded, anchored at the working directory and cleaned,
// with no lookup at all.
func (g *guardBox) lexicalSpellings() []string {
	return []string{
		"/", "//", "/.", "/usr/..", "/tmp/..", "w/../../../../../../../../../../..",
		"~", "~/", "~/.", "~/sub/..", g.home, g.home + "/", g.home + "/sub/..",
		"/usr", "/usr/", "/tmp", "/etc", "/no-such-top-entry-foo-guard",
	}
}

// topLinkTarget is what a top-level link points to when the target is
// not itself directly under / (macOS /tmp -> /private/tmp); "" when no
// such link exists.
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
	return ""
}

// resolvedSpellings reach /, $HOME or an entry directly under / (or
// what one links to) only once the filesystem is consulted: through
// links, a top-level link's target, another case of $HOME.
func (g *guardBox) resolvedSpellings(t *testing.T) []string {
	t.Helper()
	g.link(t, "/", "w/rootln")
	g.link(t, g.home, "w/homeln")
	g.link(t, "/usr", "w/usrln")
	list := []string{"rootln", "rootln/.", "homeln", "homeln/", "usrln"}
	// macOS: /tmp, /var and /etc are links to /private/*; their
	// targets are guarded like the links.
	for _, top := range []string{"/tmp", "/var", "/etc", "/bin", "/lib", "/sbin"} {
		fi, err := os.Lstat(top)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			continue
		}
		if target, err := filepath.EvalSymlinks(top); err == nil && filepath.Dir(target) != "/" {
			list = append(list, target, target+"/")
		}
	}
	// A case-insensitive filesystem reaches $HOME under another case.
	upper := filepath.Join(filepath.Dir(g.home), strings.ToUpper(filepath.Base(g.home)))
	if _, err := os.Lstat(upper); err == nil {
		list = append(list, upper)
	}
	return list
}

func wantGuard(t *testing.T, err error, param string) {
	t.Helper()
	ge := wantKind(t, err, gate.KindInvalidArgs)
	if ge.Param != param || !strings.Contains(ge.Message, "whatever the scope") {
		t.Errorf("refusal %+v; want param %s and the guard message", ge, param)
	}
}

// A value that names a protected place as written is refused before
// authorization: no scope check, no prompt, no lookup.
func TestRootGuard_RMLexicalRefusedBeforeAuthorizing(t *testing.T) {
	g := newGuardBox(t)
	for _, p := range g.lexicalSpellings() {
		for _, recursive := range []bool{false, true} {
			args, _ := json.Marshal(map[string]any{"path": []string{"w/f", p}, "recursive": recursive})
			calls := g.auth.calls
			res, err := call(t, g.eng, g.rm, string(args))
			if res != nil {
				t.Fatalf("rm %s ran: argv %q", p, res.Argv)
			}
			wantGuard(t, err, "path")
			if g.auth.calls != calls {
				t.Errorf("rm %s reached the authorizer", p)
			}
		}
	}
	// The home directory as the working directory: "." is $HOME.
	e := &Engine{Authorizer: g.auth, Cwd: g.home, Flavors: &Flavors{}}
	calls := g.auth.calls
	_, err := call(t, e, g.rm, `{"path":["."],"recursive":true}`)
	wantGuard(t, err, "path")
	if g.auth.calls != calls {
		t.Error("rm . in $HOME reached the authorizer")
	}
	if n := g.ran(); n != 0 {
		t.Errorf("rm ran %d times", n)
	}
}

// A value that reaches a protected place only through the filesystem
// is refused once the scope granted it, before anything runs: before
// authorization, the refusal would tell the model what an ungranted
// link points to.
func TestRootGuard_RMResolvedRefusedBeforeExec(t *testing.T) {
	g := newGuardBox(t)
	for _, p := range g.resolvedSpellings(t) {
		for _, recursive := range []bool{false, true} {
			args, _ := json.Marshal(map[string]any{"path": []string{"w/f", p}, "recursive": recursive})
			calls := g.auth.calls
			res, err := call(t, g.eng, g.rm, string(args))
			if res != nil {
				t.Fatalf("rm %s ran: argv %q", p, res.Argv)
			}
			wantGuard(t, err, "path")
			if g.auth.calls != calls+1 {
				t.Errorf("rm %s: authorizer calls %d; want the guard after authorization", p, g.auth.calls-calls)
			}
		}
	}
	if n := g.ran(); n != 0 {
		t.Errorf("rm ran %d times", n)
	}
}

// Where the scope does not grant a path, a link to $HOME, to / or to a
// top-level link's target gets the authorizer's answer, like a missing
// sibling: the guard never looks at it.
func TestRootGuard_OutOfScopeAliasesGetTheScopeAnswer(t *testing.T) {
	g := newGuardBox(t)
	deny := &denyAll{}
	g.eng.Authorizer = deny
	g.link(t, g.home, "out/homeln")
	g.link(t, "/", "out/rootln")
	g.link(t, "/usr", "out/usrln")
	probes := []string{"homeln", "homeln/", "rootln", "rootln/.", "usrln", "missing"}
	if tl := topLinkTarget(); tl != "" {
		g.link(t, tl, "out/tln")
		probes = append(probes, "tln")
	}
	g.dir(t, "w/d")
	for _, rel := range probes {
		p := g.p("out") + "/" + rel
		for _, tc := range []struct {
			l     *Loaded
			args  map[string]any
			param string
		}{
			{g.rm, map[string]any{"path": []string{p}}, "path"},
			{g.rm, map[string]any{"path": []string{p}, "recursive": true}, "path"},
			{g.mv, map[string]any{"src": []string{p}, "dst": "d"}, "src"},
		} {
			args, _ := json.Marshal(tc.args)
			res, err := call(t, g.eng, tc.l, string(args))
			if res != nil {
				t.Fatalf("%s %s ran: argv %q", tc.l.Spec.Name, rel, res.Argv)
			}
			if ge := wantKind(t, err, gate.KindDenied); ge.Param != tc.param {
				t.Errorf("%s %s: denied %+v; want param %s", tc.l.Spec.Name, rel, ge, tc.param)
			}
		}
	}
	if want := 3 * len(probes); deny.calls != want {
		t.Errorf("authorizer asked %d times; want %d", deny.calls, want)
	}
	if n := g.ran(); n != 0 {
		t.Errorf("recorders ran %d times", n)
	}
}

// denyAll refuses every call, as a scope granting none of its paths.
type denyAll struct{ calls int }

func (d *denyAll) Authorize(_ context.Context, req gate.Request) (gate.Grant, error) {
	d.calls++
	pa := req.Paths[0]
	return gate.Grant{}, &gate.Error{Kind: gate.KindDenied, Param: pa.Param, Path: pa.Values[0], Op: pa.Op, Message: "no scope rule covers this path"}
}

func TestRootGuard_MVSourceRefused(t *testing.T) {
	g := newGuardBox(t)
	g.dir(t, "w/d")
	check := func(p string, wantCalls int) {
		t.Helper()
		args, _ := json.Marshal(map[string]any{"src": []string{p}, "dst": "d"})
		calls := g.auth.calls
		res, err := call(t, g.eng, g.mv, string(args))
		if res != nil {
			t.Fatalf("mv %s ran: argv %q", p, res.Argv)
		}
		wantGuard(t, err, "src")
		if g.auth.calls != calls+wantCalls {
			t.Errorf("mv %s: authorizer calls %d; want %d", p, g.auth.calls-calls, wantCalls)
		}
	}
	for _, p := range g.lexicalSpellings() {
		check(p, 0)
	}
	for _, p := range g.resolvedSpellings(t) {
		check(p, 1)
	}
	if n := g.ran(); n != 0 {
		t.Errorf("mv ran %d times", n)
	}
}

// The destination is guarded where it lands: into "/", as a new entry
// directly under /, or onto $HOME is refused after mapping and before
// exec; into $HOME is fine, since the entry lands below it.
func TestRootGuard_MVDestinationWhereItLands(t *testing.T) {
	g := newGuardBox(t)
	g.link(t, "/", "w/rootln")
	g.file(t, "w/a", "A")
	for _, dst := range []string{"/", "//", "/no-such-top-entry-foo-guard", "rootln", "rootln/no-such-top-entry-foo-guard"} {
		args, _ := json.Marshal(map[string]any{"src": []string{"a"}, "dst": dst, "overwrite": true})
		res, err := call(t, g.eng, g.mv, string(args))
		if res != nil {
			t.Fatalf("mv to %s ran: argv %q", dst, res.Argv)
		}
		wantGuard(t, err, "dst")
	}

	// An entry named like $HOME moved into $HOME's parent lands on $HOME.
	g.file(t, "w/"+filepath.Base(g.home), "H")
	args, _ := json.Marshal(map[string]any{"src": []string{filepath.Base(g.home)}, "dst": filepath.Dir(g.home), "overwrite": true})
	_, err := call(t, g.eng, g.mv, string(args))
	wantGuard(t, err, "dst")

	// (fakeAuth does not expand ~; the real gate test covers "~".)
	for _, dst := range []string{g.home, g.home + "/"} {
		args, _ := json.Marshal(map[string]any{"src": []string{"a"}, "dst": dst})
		res := mustCall(t, g.eng, g.mv, string(args))
		if got, want := stdout(res), "[-n][--]["+g.p("w/a")+"]["+g.home+"]"; got != want {
			t.Errorf("mv into %s: argv %s; want %s", dst, got, want)
		}
	}
}

// cp dst is guarded like mv dst, where the copy lands: replacing an
// entry directly under /, landing on $HOME, or copying into "/" is
// refused after mapping and before exec, overwrite or not; copying into
// $HOME lands below it and runs.
func TestRootGuard_CPDestinationWhereItLands(t *testing.T) {
	g := newGuardBox(t)
	g.link(t, "/", "w/rootln")
	g.file(t, "w/a", "A")
	g.dir(t, "w/d")
	for _, dst := range []string{"/", "//", "/no-such-top-entry-foo-guard", "rootln", "rootln/no-such-top-entry-foo-guard"} {
		for _, overwrite := range []bool{false, true} {
			args, _ := json.Marshal(map[string]any{"src": []string{"a"}, "dst": dst, "overwrite": overwrite})
			res, err := call(t, g.eng, g.cp, string(args))
			if res != nil {
				t.Fatalf("cp to %s ran: argv %q", dst, res.Argv)
			}
			wantGuard(t, err, "dst")
		}
		args, _ := json.Marshal(map[string]any{"src": []string{"d"}, "dst": dst, "recursive": true, "overwrite": true})
		res, err := call(t, g.eng, g.cp, string(args))
		if res != nil {
			t.Fatalf("cp -R to %s ran: argv %q", dst, res.Argv)
		}
		wantGuard(t, err, "dst")
	}

	// An entry named like $HOME copied into $HOME's parent lands on
	// $HOME itself: the replaced entry is the home directory.
	g.file(t, "w/"+filepath.Base(g.home), "H")
	g.dir(t, "w/x/"+filepath.Base(g.home))
	for _, args := range []map[string]any{
		{"src": []string{filepath.Base(g.home)}, "dst": filepath.Dir(g.home), "overwrite": true},
		{"src": []string{"x/" + filepath.Base(g.home)}, "dst": filepath.Dir(g.home), "recursive": true, "overwrite": true},
	} {
		raw, _ := json.Marshal(args)
		res, err := call(t, g.eng, g.cp, string(raw))
		if res != nil {
			t.Fatalf("cp %s ran: argv %q", raw, res.Argv)
		}
		wantGuard(t, err, "dst")
	}

	// Into $HOME lands at a child: fine. (fakeAuth does not expand ~;
	// the real gate test covers "~".)
	for _, dst := range []string{g.home, g.home + "/"} {
		args, _ := json.Marshal(map[string]any{"src": []string{"a"}, "dst": dst})
		res := mustCall(t, g.eng, g.cp, string(args))
		if got, want := stdout(res), "[-n][--]["+g.p("w/a")+"]["+g.home+"]"; got != want {
			t.Errorf("cp into %s: argv %s; want %s", dst, got, want)
		}
	}
}

// Ordinary entries, including ones inside $HOME and links that point
// at ordinary files, are not caught by the guard.
func TestRootGuard_LeavesOrdinaryPathsAlone(t *testing.T) {
	g := newGuardBox(t)
	g.file(t, "home/sub/f", "F")
	g.file(t, "w/t", "T")
	g.link(t, "t", "w/ln")
	for _, p := range []string{g.p("home/sub/f"), "~/sub", "~/sub/f", "ln", "t", g.p("w")} {
		args, _ := json.Marshal(map[string]any{"path": []string{p}})
		res := mustCall(t, g.eng, g.rm, string(args))
		if !strings.HasPrefix(stdout(res), "[--][") {
			t.Errorf("rm %s: recorder argv %s", p, stdout(res))
		}
	}
}

// Link mode goes through the same engine: the guard answers with a
// protocol error and the authorizer is never built into a decision.
func TestRootGuard_LinkMode(t *testing.T) {
	g := newGuardBox(t)
	for _, name := range []string{"rm", "mv"} {
		var out bytes.Buffer
		l := g.rm
		req := `{"name":"rm","arguments":{"path":["~"],"recursive":true}}`
		if name == "mv" {
			l = g.mv
			req = `{"name":"mv","arguments":{"src":["/tmp/.."],"dst":"x"}}`
		}
		m := &MultiCall{Name: name, Catalog: catalogWith(t, l), Engine: g.eng, Stdin: strings.NewReader(req), Stdout: &out, Stderr: &bytes.Buffer{}}
		calls := g.auth.calls
		if code := m.Run(t.Context(), nil); code != 0 {
			t.Fatalf("%s exit %d", name, code)
		}
		var resp struct {
			Result *Result    `json:"result"`
			Detail *ErrorBody `json:"error_detail"`
		}
		if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Result != nil || resp.Detail == nil || resp.Detail.Kind != string(gate.KindInvalidArgs) || !strings.Contains(resp.Detail.Message, "whatever the scope") {
			t.Errorf("%s link-mode response %s", name, out.String())
		}
		if g.auth.calls != calls {
			t.Errorf("%s link mode reached the authorizer", name)
		}
	}
}

// catalogWith is the built-in catalog with l in place of its spec, so
// link mode runs the recorder binary.
func catalogWith(t *testing.T, l *Loaded) *Catalog {
	t.Helper()
	cat := Load(LoadOptions{})
	for i, have := range cat.Specs {
		if have.Spec.Name == l.Spec.Name {
			cat.Specs[i] = l
		}
	}
	return cat
}

func TestRootGuard_Declarations(t *testing.T) {
	protected := map[string][]string{"rm": {"path"}, "mv": {"src", "dst"}, "cp": {"dst"}}
	for tool, params := range protected {
		l := wTool(t, tool)
		for _, name := range params {
			p, _ := l.Spec.Param(name)
			if !p.ProtectRoots {
				t.Errorf("%s %s: protect_roots off", tool, name)
			}
			if !NewExtInfo(l, nil, "0").FooTool.Paths[name].ProtectRoots {
				t.Errorf("%s %s: ext-info lacks protect_roots", tool, name)
			}
		}
	}
	for _, tool := range []string{"rm", "mv", "cp", "mkdir", "sed", "cat", "ls"} {
		for _, p := range wTool(t, tool).Spec.Params {
			if p.ProtectRoots && !slices.Contains(protected[tool], p.Name) {
				t.Errorf("%s %s: unexpected protect_roots", tool, p.Name)
			}
		}
	}
}
