package shim

import (
	"bytes"
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
// and rm/mv/cp are argv recorders: the guard must refuse on its own, and
// even a guard bug runs nothing but a printf.
type guardBox struct {
	*writeBox
	auth       *fakeAuth
	home       string
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
	return &guardBox{
		writeBox: b,
		auth:     auth,
		home:     home,
		rm:       wWithBin(wTool(t, "rm"), wArgvRecorder(t, b.p("out"), "rm", "rm: illegal option -- -")),
		mv:       wWithBin(wTool(t, "mv"), wArgvRecorder(t, b.p("out"), "mv", "mv: illegal option -- -")),
		cp:       wWithBin(wTool(t, "cp"), wArgvRecorder(t, b.p("out"), "cp", "cp: illegal option -- -")),
	}
}

// protectedSpellings are ways to name /, $HOME or an entry directly
// under / (or what one links to), lexically, through .. and through
// links.
func (g *guardBox) protectedSpellings(t *testing.T) []string {
	t.Helper()
	g.link(t, "/", "w/rootln")
	g.link(t, g.home, "w/homeln")
	g.link(t, "/usr", "w/usrln")
	list := []string{
		"/", "//", "/.", "/usr/..", "/tmp/..", "w/../../../../../../../../../../..",
		"~", "~/", "~/.", "~/sub/..", g.home, g.home + "/", g.home + "/sub/..",
		"rootln", "rootln/.", "homeln", "usrln",
		"/usr", "/usr/", "/tmp", "/etc", "/no-such-top-entry-foo-guard",
	}
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

func TestRootGuard_RMRefusesBeforeAuthorizing(t *testing.T) {
	g := newGuardBox(t)
	for _, p := range g.protectedSpellings(t) {
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
	_, err := call(t, e, g.rm, `{"path":["."],"recursive":true}`)
	wantGuard(t, err, "path")
}

func TestRootGuard_MVSourceRefusedBeforeAuthorizing(t *testing.T) {
	g := newGuardBox(t)
	g.dir(t, "w/d")
	for _, p := range g.protectedSpellings(t) {
		args, _ := json.Marshal(map[string]any{"src": []string{p}, "dst": "d"})
		calls := g.auth.calls
		res, err := call(t, g.eng, g.mv, string(args))
		if res != nil {
			t.Fatalf("mv %s ran: argv %q", p, res.Argv)
		}
		wantGuard(t, err, "src")
		if g.auth.calls != calls {
			t.Errorf("mv %s reached the authorizer", p)
		}
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
