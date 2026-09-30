package shim

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"hop.top/foo/internal/tool/gate"
	"hop.top/kit/go/core/scope"
)

// pluginSchema is a plugin's parameters: one path, a path list, a
// condition flag, a mode enum and a free-form note.
const pluginSchema = `{"type":"object","properties":{
  "file":{"type":"string"},
  "files":{"type":"array","items":{"type":"string"},"maxItems":3},
  "force":{"type":"boolean"},
  "mode":{"type":"string","enum":["fast","slow"]},
  "note":{"type":"string"},
  "nested":{"type":"object"}
}}`

func mustPlugin(t *testing.T, footool string) *Plugin {
	t.Helper()
	p, err := ParsePlugin("demo", json.RawMessage(pluginSchema), json.RawMessage(footool))
	if err != nil {
		t.Fatalf("ParsePlugin: %v", err)
	}
	if p == nil {
		t.Fatal("ParsePlugin returned no plugin for a declared block")
	}
	return p
}

func TestParsePlugin_Undeclared(t *testing.T) {
	for _, raw := range []string{"", "null", "  null "} {
		p, err := ParsePlugin("demo", json.RawMessage(pluginSchema), json.RawMessage(raw))
		if p != nil || err != nil {
			t.Errorf("ParsePlugin(%q) = %v, %v; want nil, nil", raw, p, err)
		}
	}
}

func TestParsePlugin_Valid(t *testing.T) {
	p := mustPlugin(t, `{"spec":1,"side_effect":"write",
	  "side_effect_if":[{"when":{"force":true},"side_effect":"destructive"}],
	  "network":"none","digest":"sha256:x",
	  "paths":{"file":{"op":["read","write"],"target":"dirent","must_exist":true,"kind":"file",
	                   "op_when":{"when":{"mode":"fast"},"op":["write"]},"protect_roots":true,
	                   "clobber_when":{"force":true}},
	           "files":{"op":["read"],"recursive_when":{"force":true},"recursion":"all_or_nothing"}}}`)
	if got := p.SideEffect(); got != "write" {
		t.Errorf("SideEffect = %q", got)
	}
	if got := p.PathSummary(); got != "file:rw files:r" {
		t.Errorf("PathSummary = %q", got)
	}
}

func TestParsePlugin_Invalid(t *testing.T) {
	for _, tc := range []struct{ name, footool, want string }{
		{"not an object", `[]`, "decode"},
		{"unknown key", `{"spec":1,"side_effect":"read","paths":{"file":{"op":["read"],"must_exists":true}}}`, "must_exists"},
		{"spec version", `{"spec":2,"side_effect":"read","paths":{}}`, "version 2"},
		{"no side effect", `{"spec":1,"paths":{"file":{"op":["read"]}}}`, "side_effect"},
		{"bad side effect", `{"spec":1,"side_effect":"maybe","paths":{}}`, "side_effect"},
		{"path not a parameter", `{"spec":1,"side_effect":"read","paths":{"missing":{"op":["read"]}}}`, `"missing" is not a parameter`},
		{"path of wrong type", `{"spec":1,"side_effect":"read","paths":{"nested":{"op":["read"]}}}`, "string or an array of strings"},
		{"path is a bool", `{"spec":1,"side_effect":"read","paths":{"force":{"op":["read"]}}}`, "string or an array of strings"},
		{"no op", `{"spec":1,"side_effect":"read","paths":{"file":{}}}`, "op is required"},
		{"bad op", `{"spec":1,"side_effect":"read","paths":{"file":{"op":["delete"]}}}`, "delete"},
		{"bad target", `{"spec":1,"side_effect":"read","paths":{"file":{"op":["read"],"target":"parent"}}}`, "target"},
		{"into_dir", `{"spec":1,"side_effect":"write","paths":{"file":{"op":["write"],"into_dir":true}}}`, "into_dir is not supported for plugins"},
		{"filter_before", `{"spec":1,"side_effect":"read","paths":{"file":{"op":["read"],"recursive":true,"recursion":"filter_before"}}}`, "filter_before is not supported for plugins"},
		{"filter_after", `{"spec":1,"side_effect":"read","paths":{"file":{"op":["read"],"recursive":true,"recursion":"filter_after"}}}`, "filter_after is not supported for plugins"},
		{"recursive without mode", `{"spec":1,"side_effect":"read","paths":{"file":{"op":["read"],"recursive":true}}}`, "recursive needs recursion"},
		{"when on unknown param", `{"spec":1,"side_effect":"read","side_effect_if":[{"when":{"nope":true},"side_effect":"write"}],"paths":{}}`, `unknown param "nope"`},
		{"when on untyped param", `{"spec":1,"side_effect":"read","side_effect_if":[{"when":{"nested":1},"side_effect":"write"}],"paths":{}}`, `unknown param "nested"`},
		{"when of wrong type", `{"spec":1,"side_effect":"read","side_effect_if":[{"when":{"force":"yes"},"side_effect":"write"}],"paths":{}}`, "boolean"},
		{"op_when widens", `{"spec":1,"side_effect":"read","paths":{"file":{"op":["read"],"op_when":{"when":{"force":true},"op":["write"]}}}}`, "narrow"},
		{"op_when drops write on a write call", `{"spec":1,"side_effect":"write","paths":{"file":{"op":["read","write"],"op_when":{"when":{"force":true},"op":["read"]}}}}`, "drops write"},
		{"protect_roots on read", `{"spec":1,"side_effect":"read","paths":{"file":{"op":["read"],"protect_roots":true}}}`, "protect_roots needs op write"},
		{"too many items", `{"spec":1,"side_effect":"read","paths":{"files":{"op":["read"]}}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := pluginSchema
			if tc.name == "too many items" {
				schema = `{"type":"object","properties":{"files":{"type":"array","items":{"type":"string"},"maxItems":1000}}}`
				tc.want = "max_items 1000"
			}
			p, err := ParsePlugin("demo", json.RawMessage(schema), json.RawMessage(tc.footool))
			var lint *LintError
			if p != nil || !errors.As(err, &lint) {
				t.Fatalf("ParsePlugin = %v, %v; want a *LintError", p, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
	t.Run("no parameters", func(t *testing.T) {
		_, err := ParsePlugin("demo", nil, json.RawMessage(`{"spec":1,"side_effect":"read","paths":{"file":{"op":["read"]}}}`))
		if err == nil || !strings.Contains(err.Error(), `"file" is not a parameter`) {
			t.Errorf("error = %v", err)
		}
	})
}

func authorizePlugin(t *testing.T, e *Engine, p *Plugin, args string) (map[string]any, error) {
	t.Helper()
	out, err := e.AuthorizePlugin(context.Background(), p, "/opt/bin/foo-tool-demo", json.RawMessage(args))
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("sent arguments %s: %v", out, err)
	}
	return m, nil
}

// The request the authorizer sees is built from the declared
// annotations, and the plugin gets the canonical paths in place of
// the model's, in the shape it declared; other arguments pass through.
func TestAuthorizePlugin_CanonicalPaths(t *testing.T) {
	dir := tempDir(t)
	writeFile(t, filepath.Join(dir, "real", "a.txt"), "a")
	writeFile(t, filepath.Join(dir, "real", "b.txt"), "b")
	if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	auth := &fakeAuth{cwd: dir}
	e := &Engine{Authorizer: auth, Cwd: dir}
	p := mustPlugin(t, `{"spec":1,"side_effect":"read",
	  "side_effect_if":[{"when":{"force":true},"side_effect":"destructive"}],
	  "paths":{"file":{"op":["read"]},"files":{"op":["read","write"],"target":"dirent"}}}`)

	got, err := authorizePlugin(t, e, p, `{"file":"link/a.txt","files":["real/../real/b.txt"],"force":true,"note":"keep","extra":[1,2]}`)
	if err != nil {
		t.Fatalf("AuthorizePlugin: %v", err)
	}
	want := map[string]any{
		"file":  filepath.Join(dir, "real", "a.txt"),
		"files": []any{filepath.Join(dir, "real", "b.txt")},
		"force": true, "note": "keep", "extra": []any{1.0, 2.0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sent %v; want %v", got, want)
	}

	req := auth.last
	if req.Tool != "demo" || req.SideEffect != "destructive" {
		t.Errorf("request tool/side effect = %q/%q; want demo/destructive", req.Tool, req.SideEffect)
	}
	if len(req.Paths) != 2 {
		t.Fatalf("paths = %+v", req.Paths)
	}
	file, files := req.Paths[0], req.Paths[1]
	if file.Param != "file" || file.Op != scope.Read || file.Target != gate.Follow || !reflect.DeepEqual(file.Values, []string{"link/a.txt"}) {
		t.Errorf("file arg = %+v", file)
	}
	if files.Param != "files" || files.Op != scope.Read|scope.Write || files.Target != gate.Dirent {
		t.Errorf("files arg = %+v", files)
	}
	argv := req.Argv(map[string][]string{"file": {"/c/a"}, "files": {"/c/b"}})
	wantArgv := []string{"/opt/bin/foo-tool-demo", "extra=[1,2]", "file=/c/a", "files=[\"/c/b\"]", "force=true", "note=keep"}
	if !reflect.DeepEqual(argv, wantArgv) {
		t.Errorf("prompt argv = %q; want %q", argv, wantArgv)
	}
}

// Conditions pick the op, recursion and side effect of each call.
func TestAuthorizePlugin_Conditions(t *testing.T) {
	dir := tempDir(t)
	auth := &fakeAuth{cwd: dir}
	e := &Engine{Authorizer: auth, Cwd: dir}
	p := mustPlugin(t, `{"spec":1,"side_effect":"write",
	  "side_effect_if":[{"when":{"mode":"slow"},"side_effect":"read"}],
	  "paths":{"file":{"op":["read","write"],"op_when":{"when":{"mode":"slow"},"op":["read"]},
	                   "recursive_when":{"force":true},"recursion":"all_or_nothing"}}}`)

	if _, err := authorizePlugin(t, e, p, `{"file":"x","mode":"slow"}`); err != nil {
		t.Fatal(err)
	}
	if pa := auth.last.Paths[0]; pa.Op != scope.Read || pa.Recursion != gate.NoRecursion || auth.last.SideEffect != "read" {
		t.Errorf("slow call: %+v side effect %q", pa, auth.last.SideEffect)
	}
	if _, err := authorizePlugin(t, e, p, `{"file":"x","force":true}`); err != nil {
		t.Fatal(err)
	}
	if pa := auth.last.Paths[0]; pa.Op != scope.Read|scope.Write || pa.Recursion != gate.AllOrNothing || auth.last.SideEffect != "write" {
		t.Errorf("forced call: %+v side effect %q", pa, auth.last.SideEffect)
	}
}

func TestAuthorizePlugin_Refusals(t *testing.T) {
	dir := tempDir(t)
	writeFile(t, filepath.Join(dir, "secret", "k"), "k")
	writeFile(t, filepath.Join(dir, "d", "f"), "f")
	p := mustPlugin(t, `{"spec":1,"side_effect":"write",
	  "paths":{"file":{"op":["write"],"target":"dirent","kind":"file","protect_roots":true,"clobber_when":{"force":true}},
	           "files":{"op":["read"],"must_exist":true}}}`)

	for _, tc := range []struct {
		name, args string
		kind       gate.Kind
		authorized bool
	}{
		{"denied path", `{"file":"secret/k","force":true}`, gate.KindDenied, true},
		{"one denied path in a list", `{"files":["d/f","secret/k"]}`, gate.KindDenied, true},
		{"not an object", `["x"]`, gate.KindInvalidArgs, false},
		{"path of wrong type", `{"file":["a"]}`, gate.KindInvalidArgs, false},
		{"too many paths", `{"files":["a","b","c","d"]}`, gate.KindInvalidArgs, false},
		{"condition of wrong type", `{"file":"new","force":"yes"}`, gate.KindInvalidArgs, false},
		{"case variant of a path", `{"file":"new","FILE":"secret/k"}`, gate.KindInvalidArgs, false},
		{"root as written", `{"file":"/"}`, gate.KindInvalidArgs, false},
		{"clobber without force", `{"file":"d/f"}`, gate.KindInvalidArgs, true},
		{"kind file on a dir", `{"file":"d","force":true}`, gate.KindInvalidArgs, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := &fakeAuth{cwd: dir, deny: map[string]bool{"secret": true}}
			e := &Engine{Authorizer: auth, Cwd: dir}
			_, err := authorizePlugin(t, e, p, tc.args)
			wantKind(t, err, tc.kind)
			if got := auth.calls > 0; got != tc.authorized {
				t.Errorf("authorizer called = %v; want %v", got, tc.authorized)
			}
		})
	}

	t.Run("no authorizer", func(t *testing.T) {
		_, err := (&Engine{Cwd: dir}).AuthorizePlugin(context.Background(), p, "/x", json.RawMessage(`{"files":["d/f"]}`))
		wantKind(t, err, gate.KindDenied)
	})
	t.Run("authorizer granted a different count", func(t *testing.T) {
		auth := &fakeAuth{cwd: dir, rewrite: map[string][]string{"files": {dir + "/d/f", dir + "/d/f"}}}
		_, err := (&Engine{Authorizer: auth, Cwd: dir}).AuthorizePlugin(context.Background(), p, "/x", json.RawMessage(`{"files":["d/f"]}`))
		wantKind(t, err, gate.KindDenied)
	})
}

// A declared path the model leaves out has nothing to check; the call
// still goes through the side-effect policy.
func TestAuthorizePlugin_AbsentPath(t *testing.T) {
	dir := tempDir(t)
	auth := &fakeAuth{cwd: dir}
	p := mustPlugin(t, `{"spec":1,"side_effect":"write","paths":{"file":{"op":["write"]}}}`)
	got, err := authorizePlugin(t, &Engine{Authorizer: auth, Cwd: dir}, p, `{"note":"n"}`)
	if err != nil {
		t.Fatal(err)
	}
	if auth.calls != 1 || len(auth.last.Paths) != 0 || auth.last.SideEffect != "write" {
		t.Errorf("request = %+v after %d calls", auth.last, auth.calls)
	}
	if !reflect.DeepEqual(got, map[string]any{"note": "n"}) {
		t.Errorf("sent %v", got)
	}
}
