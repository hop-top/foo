package shim

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func linkActions(res []LinkResult) map[string]string {
	out := map[string]string{}
	for _, r := range res {
		out[r.Name] = r.Action
	}
	return out
}

func TestInstall_IdempotentAndOwnLinksOnly(t *testing.T) {
	root := tempDir(t)
	foo := writeScript(t, root, "foo", "exit 0\n")
	dir := filepath.Join(root, "bin")
	in := &Installer{Dir: dir, Target: foo, Manifest: filepath.Join(root, "state", "tool-shims.json"), Version: "1.2.3"}
	wc := builtinWC(t)
	other := mustSpec(t, strings.Replace(userSpec("catx", "/bin/cat", ""), "catx", "catx", 1))

	// Third-party files already in the dir.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	thirdParty := writeScript(t, dir, "foo-tool-thirdparty", "exit 0\n")
	elsewhere := writeScript(t, root, "elsewhere", "exit 0\n")
	if err := os.Symlink(elsewhere, filepath.Join(dir, "foo-tool-catx")); err != nil {
		t.Fatal(err)
	}

	res, err := in.Install([]*Loaded{wc, other})
	if err != nil {
		t.Fatal(err)
	}
	if got := linkActions(res); got["wc"] != ActionCreated || got["catx"] != ActionSkipped {
		t.Fatalf("first install = %+v", res)
	}
	link := filepath.Join(dir, "foo-tool-wc")
	if dest, _ := os.Readlink(link); dest != foo {
		t.Fatalf("link -> %q; want %q", dest, foo)
	}

	res, err = in.Install([]*Loaded{wc, other})
	if err != nil || linkActions(res)["wc"] != ActionUnchanged {
		t.Fatalf("second install = %+v, %v; want unchanged", res, err)
	}

	// foo moved (upgrade): the recorded link is refreshed, not skipped.
	moved := writeScript(t, root, "foo2", "exit 0\n")
	in2 := *in
	in2.Target = moved
	res, err = in2.Install([]*Loaded{wc})
	if err != nil || linkActions(res)["wc"] != ActionReplaced {
		t.Fatalf("install after move = %+v, %v; want replaced", res, err)
	}

	// Uninstall: only links to foo go.
	res, err = in2.Uninstall()
	if err != nil {
		t.Fatal(err)
	}
	if got := linkActions(res); got["wc"] != ActionRemoved || len(got) != 1 {
		t.Fatalf("uninstall = %+v; want only wc removed", res)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Error("foo link survived uninstall")
	}
	for _, p := range []string{thirdParty, filepath.Join(dir, "foo-tool-catx")} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("uninstall touched %s: %v", p, err)
		}
	}
	res, err = in2.Uninstall()
	if err != nil || len(res) != 0 {
		t.Errorf("second uninstall = %+v, %v; want no-op", res, err)
	}
}

func TestInstall_PrunesLinksForRemovedSpecs(t *testing.T) {
	root := tempDir(t)
	foo := writeScript(t, root, "foo", "exit 0\n")
	in := &Installer{Dir: filepath.Join(root, "bin"), Target: foo, Manifest: filepath.Join(root, "m.json")}
	gone := mustSpec(t, userSpec("gonex", "/bin/cat", ""))
	if _, err := in.Install([]*Loaded{builtinWC(t), gone}); err != nil {
		t.Fatal(err)
	}
	res, err := in.Install([]*Loaded{builtinWC(t)})
	if err != nil || linkActions(res)["gonex"] != ActionRemoved {
		t.Fatalf("regenerate = %+v, %v; want gonex removed", res, err)
	}
}

func TestMultiCall_ExtInfoAndRequest(t *testing.T) {
	dir := tempDir(t)
	writeFile(t, filepath.Join(dir, "a"), "x y\n")
	cat := Load(LoadOptions{})
	run := func(auth any, stdin string, args ...string) (string, string, int) {
		var out, errb bytes.Buffer
		e := &Engine{Cwd: dir}
		if a, ok := auth.(*fakeAuth); ok {
			e.Authorizer = a
		} else {
			e.Authorizer = DenyAll{Message: "path scope not configured yet"}
		}
		m := &MultiCall{Name: "wc", Version: "9.9.9", Catalog: cat, Engine: e,
			Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb}
		code := m.Run(context.Background(), args)
		return out.String(), errb.String(), code
	}

	out, _, code := run(nil, "", "--ext-info")
	var info ExtInfo
	if code != 0 || json.Unmarshal([]byte(out), &info) != nil {
		t.Fatalf("--ext-info exit %d: %q", code, out)
	}
	if info.Name != "wc" || info.Version != "9.9.9" || info.FooTool.Spec != 1 || info.FooTool.SideEffect != "read" ||
		info.FooTool.Paths["path"].Op[0] != "read" || !strings.HasPrefix(info.FooTool.Digest, "sha256:") ||
		!strings.Contains(string(info.Parameters), `"additionalProperties":false`) {
		t.Errorf("ext-info = %s", out)
	}
	if strings.Contains(string(info.Parameters), "foo_tool") {
		t.Error("foo_tool annotations leaked into the model schema")
	}

	out, _, code = run(nil, `{"name":"wc","arguments":{"path":["a"]}}`)
	if code != 0 || !strings.Contains(out, `"error":"denied: path scope not configured yet"`) || !strings.Contains(out, `"kind":"denied"`) {
		t.Errorf("deny-all request: exit %d %s", code, out)
	}

	out, _, code = run(&fakeAuth{cwd: dir}, `{"name":"wc","arguments":{"path":["a"],"words":true}}`)
	if code != 0 || !strings.Contains(out, `"result":{"exit_code":0`) || !strings.Contains(out, `2 `+filepath.Join(dir, "a")) {
		t.Errorf("granted request: exit %d %s", code, out)
	}

	for _, req := range []string{`not json`, `{"name":"ls","arguments":{}}`, `{"nme":"wc"}`} {
		out, _, code = run(&fakeAuth{cwd: dir}, req)
		if code != 0 || !strings.Contains(out, `"kind":"protocol"`) {
			t.Errorf("request %q: exit %d %s; want protocol error", req, code, out)
		}
	}
	if _, _, code = run(nil, "", "--bogus"); code != 2 {
		t.Errorf("unknown flag exit %d; want 2", code)
	}
	m := &MultiCall{Name: "nope", Catalog: cat, Engine: &Engine{}, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	if code := m.Run(context.Background(), nil); code != 3 {
		t.Errorf("unknown shim exit %d; want 3", code)
	}
}

func TestMultiCallName(t *testing.T) {
	for argv0, want := range map[string]string{
		"/usr/local/bin/foo-tool-wc": "wc",
		"foo-tool-wc":                "wc",
		"/bin/foo":                   "",
		"foo-tool-":                  "",
		"/x/foo-toolbox":             "",
	} {
		got, ok := MultiCallName(argv0)
		if got != want || ok != (want != "") {
			t.Errorf("MultiCallName(%q) = %q, %v", argv0, got, ok)
		}
	}
}
