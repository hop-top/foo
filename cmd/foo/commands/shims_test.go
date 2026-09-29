package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hop.top/foo/internal/tool"
	"hop.top/kit/go/ai/llm"
	"hop.top/kit/go/console/output"
)

func TestToolList_ShimSpecListed(t *testing.T) {
	env := newToolTestEnv(t)
	stdout, _, _, err := runFooArgs(t, env, "tool", "list", "--offline", "--format=json")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	wc, ok := toolRowsByName(t, stdout)["wc"]
	if !ok {
		t.Fatalf("wc missing from %s", stdout)
	}
	for k, want := range map[string]any{
		"source": "builtin", "side_effect": "read", "paths": "path:r", "status": "active", "params": true,
	} {
		if wc[k] != want {
			t.Errorf("wc %s = %v; want %v", k, wc[k], want)
		}
	}
	demo := toolRowsByName(t, stdout)["demo"]
	if demo["side_effect"] != "unknown" || demo["paths"] != "ungated" || demo["status"] != "active" {
		t.Errorf("plugin row = %v", demo)
	}
	if ft := toolRowsByName(t, stdout)["foo_time"]; ft["side_effect"] != "read" || ft["status"] != "active" {
		t.Errorf("builtin row = %v", ft)
	}
}

// A PATH plugin claiming a spec tool's name never registers and is
// never exec'd; the listing shows it shadowed.
func TestToolList_PathRivalShadowed(t *testing.T) {
	env := newToolTestEnv(t)
	rival := env.addToolScript(t, "wc", `{"name":"wc","version":"0","description":"rival wc"}`)
	stdout, _, before, err := runFooArgs(t, env, "tool", "list", "--offline", "--format=json")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var rows []toolRow
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatal(err)
	}
	var active, shadowed int
	for _, r := range rows {
		if r.Name != "wc" {
			continue
		}
		switch {
		case r.Status == statusActive && r.Source == "builtin":
			active++
		case r.Status == statusShadowed && r.Source == rival && strings.Contains(r.Description, "shadowed by builtin"):
			shadowed++
		default:
			t.Errorf("unexpected wc row %+v", r)
		}
	}
	if active != 1 || shadowed != 1 {
		t.Errorf("wc rows: %d active, %d shadowed; want 1 and 1: %s", active, shadowed, stdout)
	}
	if got := env.extInfoCalls(t) - before; got != 1 {
		t.Errorf("--ext-info ran %d times; want 1 (demo only, never the shadowed rival)", got)
	}

	reg, err := buildRegistry([]string{"wc"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := reg.Get("wc"); tool.SourceOf(got) != "builtin" {
		t.Errorf("-T wc resolves to %q; want the builtin spec", tool.SourceOf(got))
	}
}

func userToolsDir(t *testing.T) string {
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "foo", "tools")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

const userWC = `spec: 1
name: wc
description: Count lines only.
command: {bin: [/usr/bin/wc, /bin/wc]}
side_effect: read
params:
  - {name: path, type: path, description: Files., op: [read], repeated: true}
argv: ["-l", "--", "{path}"]
`

func TestToolList_UserOverrideFlagged(t *testing.T) {
	env := newToolTestEnv(t)
	p := filepath.Join(userToolsDir(t), "wc.yaml")
	if err := os.WriteFile(p, []byte(userWC), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, _, err := runFooArgs(t, env, "tool", "list", "--offline", "--format=json")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	wc := toolRowsByName(t, stdout)["wc"]
	if wc["source"] != "user:"+p+" (overrides builtin)" || wc["description"] == nil ||
		!strings.HasPrefix(wc["description"].(string), "Count lines only.") {
		t.Errorf("wc row = %v", wc)
	}
}

// A broken user override owns the name: -T wc fails naming the lint
// error instead of falling back to the builtin.
func TestSelectedTool_InvalidUserSpecNotFound(t *testing.T) {
	env := newToolTestEnv(t)
	p := filepath.Join(userToolsDir(t), "wc.yaml")
	broken := strings.Replace(userWC, `"--", "{path}"`, `"{path}"`, 1)
	if err := os.WriteFile(p, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, _, err := runFooArgs(t, env, "--offline", "--dry-run", "-T", "wc", "hi")
	if err == nil {
		t.Fatal("-T wc with a broken override must fail")
	}
	var ce interface{ AsCLIError() *output.Error }
	if !errors.As(err, &ce) || ce.AsCLIError().ExitCode != 3 {
		t.Errorf("error %v: want not-found exit 3", err)
	}
	if !strings.Contains(err.Error(), "must follow a literal --") || !strings.Contains(err.Error(), p) {
		t.Errorf("error %q must name the lint problem and the spec path", err)
	}
	if !strings.Contains(stderr, "warning") {
		t.Errorf("stderr %q: want a skip warning for the selected broken spec", stderr)
	}

	stdout, stderr, _, err := runFooArgs(t, env, "tool", "list", "--offline", "--format=json")
	if err != nil {
		t.Fatal(err)
	}
	if _, listed := toolRowsByName(t, stdout)["wc"]; listed || !strings.Contains(stderr, "wc") {
		t.Errorf("list: wc listed=%v stderr=%q; want skipped with warning", listed, stderr)
	}
}

// shimClient calls wc once and records the tool message it gets back.
type shimClient struct {
	calls int
	reply string
}

func (c *shimClient) CallWithTools(_ context.Context, msgs []llm.Message, _ []llm.ToolDef) (llm.ToolResponse, error) {
	c.calls++
	if c.calls == 1 {
		return llm.ToolResponse{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "wc", Arguments: json.RawMessage(`{"path":["/etc/hosts"]}`)}}}, nil
	}
	c.reply = msgs[len(msgs)-1].Content
	return llm.ToolResponse{Content: "done"}, nil
}

// Until path scope is wired, the in-process engine fails closed and
// the model gets a structured denial, not a flattened string.
func TestShimTool_FailsClosedInProcess(t *testing.T) {
	newToolTestEnv(t)
	reg, err := buildRegistry([]string{"wc"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	client := &shimClient{}
	if _, err := tool.NewDispatcher(client, reg, tool.DispatchConfig{}).Run(context.Background(), "count"); err != nil {
		t.Fatal(err)
	}
	var msg struct {
		Error struct {
			Kind    string `json:"kind"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(client.reply), &msg); err != nil {
		t.Fatalf("tool message %q is not the structured error: %v", client.reply, err)
	}
	if msg.Error.Kind != "denied" || msg.Error.Message != "path scope not configured yet" {
		t.Errorf("tool message = %s", client.reply)
	}
}

func TestToolInstall_LinksRoundTrip(t *testing.T) {
	env := newToolTestEnv(t)
	dir := filepath.Join(t.TempDir(), "links")
	for _, args := range [][]string{
		{"tool", "install", "--dir", dir, "--format=json"},
		{"tool", "install", "--format=json", "--dir=" + dir},
	} {
		stdout, _, _, err := runFooArgs(t, env, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		var rows []map[string]string
		if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
			t.Fatalf("%v: %q", args, stdout)
		}
		if len(rows) != 1 || rows[0]["name"] != "wc" || (rows[0]["action"] != "created" && rows[0]["action"] != "unchanged") {
			t.Errorf("%v rows = %v", args, rows)
		}
	}
	self, _ := os.Executable()
	if dest, err := os.Readlink(filepath.Join(dir, "foo-tool-wc")); err != nil || dest != self {
		t.Fatalf("link -> %q (%v); want %s", dest, err, self)
	}

	// The link to foo itself is never registered as a PATH plugin.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	stdout, _, _, err := runFooArgs(t, env, "tool", "list", "--offline", "--format=json")
	if err != nil {
		t.Fatal(err)
	}
	var listed []toolRow
	if err := json.Unmarshal([]byte(stdout), &listed); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range listed {
		if r.Name == "wc" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("wc listed %d times with the link on PATH; want 1: %s", n, stdout)
	}

	stdout, _, _, err = runFooArgs(t, env, "tool", "uninstall", "--dir", dir, "--format=json")
	var removed []map[string]string
	if err != nil || json.Unmarshal([]byte(stdout), &removed) != nil || len(removed) != 1 || removed[0]["action"] != "removed" {
		t.Fatalf("uninstall: %v %s", err, stdout)
	}
	if _, err := os.Lstat(filepath.Join(dir, "foo-tool-wc")); !os.IsNotExist(err) {
		t.Error("link survived uninstall")
	}
	stdout, _, _, err = runFooArgs(t, env, "tool", "uninstall", "--dir", dir, "--format=json")
	if err != nil || strings.TrimSpace(stdout) != "[]" {
		t.Errorf("second uninstall = %q, %v; want []", stdout, err)
	}
}
