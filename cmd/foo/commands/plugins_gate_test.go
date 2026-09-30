package commands

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gatedPluginScript is a foo-tool-<name> plugin that declares extInfo
// and, on a call, saves its stdin to calls/<name>.json and answers ok.
func gatedPluginScript(dir, name, extInfo string) string {
	return "#!/bin/sh\n" +
		"if [ \"$1\" = \"--ext-info\" ]; then\n" +
		"  cat <<'JSON'\n" + extInfo + "\nJSON\n" +
		"  exit 0\n" +
		"fi\n" +
		"cat > '" + filepath.Join(dir, name+".json") + "'\n" +
		"echo '{\"result\":{\"ok\":true}}'\n"
}

// pluginEnv is gateTree plus foo-tool-* plugins in the test PATH's bin
// dir. Each call a plugin receives is saved under calls.
type pluginEnv struct {
	root, bin, calls string
}

func newPluginEnv(t *testing.T) pluginEnv {
	t.Helper()
	root := gateTree(t)
	e := pluginEnv{root: root, bin: filepath.SplitList(os.Getenv("PATH"))[0], calls: filepath.Join(root, "calls")}
	if err := os.MkdirAll(e.calls, 0o755); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e pluginEnv) plugin(t *testing.T, name, footool string) {
	t.Helper()
	info := `{"name":"` + name + `","version":"0.1.0","description":"` + name + ` plugin",` +
		`"parameters":{"type":"object","properties":{"file":{"type":"string"},"note":{"type":"string"}},"required":["file"]}`
	if footool != "" {
		info += `,"foo_tool":` + footool
	}
	info += "}"
	p := filepath.Join(e.bin, "foo-tool-"+name)
	if err := os.WriteFile(p, []byte(gatedPluginScript(e.calls, name, info)), 0o755); err != nil {
		t.Fatal(err)
	}
}

// received is the request plugin name got, "" when it never ran.
func (e pluginEnv) received(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.calls, name+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (e pluginEnv) wantReceived(t *testing.T, name, file string) {
	t.Helper()
	var req struct {
		Arguments struct {
			File string `json:"file"`
		} `json:"arguments"`
	}
	got := e.received(t, name)
	if got == "" {
		t.Fatalf("%s never ran", name)
	}
	if err := json.Unmarshal([]byte(got), &req); err != nil {
		t.Fatalf("request %q: %v", got, err)
	}
	if req.Arguments.File != file {
		t.Errorf("%s received file %q; want %q (request %s)", name, req.Arguments.File, file, got)
	}
}

const (
	readFooTool  = `{"spec":1,"side_effect":"read","paths":{"file":{"op":["read"],"must_exist":true}}}`
	writeFooTool = `{"spec":1,"side_effect":"write","paths":{"file":{"op":["write"],"target":"dirent"}}}`
)

func fileArgs(path string) string {
	data, _ := json.Marshal(map[string]string{"file": path, "note": "n"})
	return string(data)
}

// answers makes the approval terminal answer from s, without turning
// on --tools-approve.
func answers(t *testing.T, s string) {
	t.Helper()
	prev := openApprovalTerminal
	openApprovalTerminal = func() (io.Reader, error) { return strings.NewReader(s), nil }
	t.Cleanup(func() { openApprovalTerminal = prev })
}

// A plugin that declares foo_tool paths is gated by the user's scope,
// and runs with the canonical path, never the one the model wrote.
func TestPluginGate_ReadScope(t *testing.T) {
	e := newPluginEnv(t)
	e.plugin(t, "reader", readFooTool)
	writeScopeYAML(t, allowRead(e.root))
	noTerminal(t)
	t.Chdir(filepath.Join(e.root, "p"))
	r := mustShimRun(t, "reader")

	t.Run("allowed gets the canonical path", func(t *testing.T) {
		msg := r.call("reader", fileArgs("sub/../a.txt"))
		if !strings.Contains(msg, `"ok":true`) {
			t.Fatalf("tool message %q; want the plugin's result", msg)
		}
		e.wantReceived(t, "reader", e.root+"/p/a.txt")
	})
	t.Run("denied never runs", func(t *testing.T) {
		_ = os.Remove(filepath.Join(e.calls, "reader.json"))
		m := decodeToolMessage(t, r.call("reader", fileArgs(e.root+"/outside/a.txt")))
		m.wantDenied(t, "denied", e.root+"/outside/a.txt")
		if m.Error != nil && m.Error.Param != "file" {
			t.Errorf("denial param = %q; want file", m.Error.Param)
		}
		if got := e.received(t, "reader"); got != "" {
			t.Errorf("denied plugin ran with %s", got)
		}
	})
	t.Run("link escape denied", func(t *testing.T) {
		m := decodeToolMessage(t, r.call("reader", fileArgs("link/../b.txt")))
		m.wantDenied(t, "denied", e.root+"/outside/b.txt")
		if got := e.received(t, "reader"); got != "" {
			t.Errorf("denied plugin ran with %s", got)
		}
	})
	if n := r.questions(); n != 0 {
		t.Errorf("%d questions for read calls; want none", n)
	}
}

// Without scope.yaml a declaring plugin is denied like a spec tool.
func TestPluginGate_NoScopeDenied(t *testing.T) {
	e := newPluginEnv(t)
	e.plugin(t, "reader", readFooTool)
	noTerminal(t)
	r := mustShimRun(t, "reader")
	decodeToolMessage(t, r.call("reader", fileArgs(e.root+"/p/a.txt"))).wantDenied(t, "denied", "scope.yaml")
	if got := e.received(t, "reader"); got != "" {
		t.Errorf("plugin ran with %s", got)
	}
}

// A write side effect goes through the policy table: one question per
// call, showing the canonical path; a no means the plugin never runs.
func TestPluginGate_WriteAsksOnce(t *testing.T) {
	for _, tc := range []struct {
		answer string
		runs   bool
	}{{"y\n", true}, {"n\n", false}} {
		t.Run(strings.TrimSpace(tc.answer), func(t *testing.T) {
			e := newPluginEnv(t)
			e.plugin(t, "writer", writeFooTool)
			writeScopeYAML(t, "allow:\n  - path: \""+e.root+"/p/**\"\n    ops: [read, write]\n")
			answers(t, tc.answer)
			r := mustShimRun(t, "writer")

			msg := r.call("writer", fileArgs(e.root+"/p/sub/../new.txt"))
			if n := r.questions(); n != 1 {
				t.Fatalf("asked %d times; want 1: %q", n, r.stderr)
			}
			if q := r.stderr.String(); !strings.Contains(q, "policy: write side effect") || !strings.Contains(q, "file="+e.root+"/p/new.txt") {
				t.Errorf("question %q must name the side effect and the canonical path", q)
			}
			if tc.runs {
				e.wantReceived(t, "writer", e.root+"/p/new.txt")
				return
			}
			decodeToolMessage(t, msg).wantDenied(t, "declined", "declined")
			if got := e.received(t, "writer"); got != "" {
				t.Errorf("declined plugin ran with %s", got)
			}
		})
	}
}

// --tools-approve on a declaring plugin: the gate's one question, never
// the dispatcher's as well.
func TestPluginGate_ToolsApproveAsksOnce(t *testing.T) {
	e := newPluginEnv(t)
	e.plugin(t, "reader", readFooTool)
	writeScopeYAML(t, allowRead(e.root))
	withApproval(t, func() (io.Reader, error) { return strings.NewReader("y\n"), nil })
	r := mustShimRun(t, "reader")
	r.call("reader", fileArgs(e.root+"/p/a.txt"))
	if n := r.questions(); n != 1 || strings.Contains(r.stderr.String(), "[tool] execute reader") {
		t.Errorf("asked %d times: %q; want the gate's question once", n, r.stderr)
	}
	e.wantReceived(t, "reader", e.root+"/p/a.txt")
}

// A plugin that declares nothing is unchanged: no scope, no question,
// the arguments exactly as the model sent them.
func TestPluginGate_UndeclaredUnchanged(t *testing.T) {
	e := newPluginEnv(t)
	e.plugin(t, "plain", "")
	noTerminal(t)
	r := mustShimRun(t, "plain")
	args := fileArgs("../outside/a.txt")
	r.call("plain", args)
	var got struct {
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(e.received(t, "plain")), &got); err != nil {
		t.Fatal(err)
	}
	if string(got.Arguments) != args {
		t.Errorf("undeclared plugin received %s; want %s unchanged", got.Arguments, args)
	}
}

// Annotations foo cannot enforce skip the plugin with a warning, like
// an invalid parameters schema.
func TestPluginGate_InvalidAnnotationsSkipped(t *testing.T) {
	e := newPluginEnv(t)
	e.plugin(t, "bad", `{"spec":1,"side_effect":"read","paths":{"file":{"op":["read"],"recursive":true,"recursion":"filter_after"}}}`)
	r, err := newShimRun(t, "bad")
	if err == nil || !strings.Contains(err.Error(), `unknown tool "bad"`) {
		t.Fatalf("-T bad error = %v; want unknown tool", err)
	}
	if w := r.stderr.String(); !strings.Contains(w, "skipping tool plugin") || !strings.Contains(w, `"foo_tool"`) || !strings.Contains(w, "filter_after") {
		t.Errorf("warning %q must name the foo_tool problem", w)
	}
}

// foo tool list shows what a declaring plugin is gated on, and
// "ungated" for one that declares nothing.
func TestPluginGate_ToolList(t *testing.T) {
	env := newToolTestEnv(t)
	e := pluginEnv{bin: filepath.Dir(env.demoPath), calls: t.TempDir()}
	e.plugin(t, "writer", writeFooTool)
	e.plugin(t, "bad", `{"spec":1,"side_effect":"write","paths":{"file":{"op":["write"],"into_dir":true}}}`)

	stdout, stderr, _, err := runFooArgs(t, env, "tool", "list", "--offline", "--format=json")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var rows []toolRow
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("stdout %q: %v", stdout, err)
	}
	byName := map[string]toolRow{}
	for _, row := range rows {
		byName[row.Name] = row
	}
	if w := byName["writer"]; w.SideEffect != "write" || w.Paths != "file:w" || w.Status != statusActive {
		t.Errorf("writer row = %+v; want write, file:w, active", w)
	}
	if d := byName["demo"]; d.SideEffect != sideEffectUnknown || d.Paths != pathsUngated {
		t.Errorf("demo row = %+v; want unknown, ungated", d)
	}
	if _, ok := byName["bad"]; ok {
		t.Error("plugin with invalid foo_tool listed")
	}
	if !strings.Contains(stderr, "into_dir is not supported for plugins") {
		t.Errorf("stderr %q; want the skip warning for bad", stderr)
	}
}
