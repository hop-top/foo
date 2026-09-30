package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hop.top/foo/internal/tool/gate"
	"hop.top/foo/internal/tool/shim"
	"hop.top/kit/go/ai/llm"
)

const fileSchema = `{"type":"object","properties":{"file":{"type":"string"},"note":{"type":"string"}},"required":["file"]}`

const fileFooTool = `{"spec":1,"side_effect":"write","paths":{"file":{"op":["read","write"]}}}`

func gatedExtInfo(footool string) string {
	return `{"name":"demo","version":"0.1.0","description":"d","parameters":` + fileSchema + `,"foo_tool":` + footool + `}`
}

// linkAuth grants every path in canonical form, except paths under a
// "secret" directory; it counts calls.
type linkAuth struct {
	calls int
	last  gate.Request
}

func (a *linkAuth) Authorize(_ context.Context, req gate.Request) (gate.Grant, error) {
	a.calls++
	a.last = req
	g := gate.Grant{Canonical: map[string][]string{}}
	for _, pa := range req.Paths {
		for _, v := range pa.Values {
			c, err := filepath.EvalSymlinks(v)
			if err != nil {
				return gate.Grant{}, &gate.Error{Kind: gate.KindInvalidArgs, Param: pa.Param, Path: v, Message: err.Error()}
			}
			if strings.Contains(c, "/secret/") {
				return gate.Grant{}, &gate.Error{Kind: gate.KindDenied, Param: pa.Param, Path: c, Op: pa.Op, Message: "not in scope"}
			}
			g.Canonical[pa.Param] = append(g.Canonical[pa.Param], c)
		}
	}
	return g, nil
}

func TestExternalToolFromFound_FooTool(t *testing.T) {
	path, _ := writePlugin(t, "demo", gatedExtInfo(fileFooTool), `{"result":{}}`)
	got, err := ExternalToolFromFound(foundAt(path, "demo"))
	if err != nil {
		t.Fatalf("ExternalToolFromFound: %v", err)
	}
	if !got.Gated() || !got.ApprovesItself() {
		t.Errorf("Gated/ApprovesItself = %v/%v; want true/true", got.Gated(), got.ApprovesItself())
	}
	if got.SideEffect() != "write" || got.PathSummary() != "file:rw" {
		t.Errorf("side effect %q, paths %q; want write, file:rw", got.SideEffect(), got.PathSummary())
	}
	assertJSONEqual(t, got.Parameters(), fileSchema)

	plain, _ := writePlugin(t, "weather",
		`{"name":"weather","version":"0.1.0","description":"d","parameters":`+weatherSchema+`}`, `{"result":{}}`)
	ungated, err := ExternalToolFromFound(foundAt(plain, "weather"))
	if err != nil {
		t.Fatal(err)
	}
	if ungated.Gated() || ungated.ApprovesItself() || ungated.PathSummary() != "" {
		t.Errorf("undeclared plugin gated: %v %v %q", ungated.Gated(), ungated.ApprovesItself(), ungated.PathSummary())
	}
}

func TestExternalToolFromFound_InvalidFooTool(t *testing.T) {
	path, _ := writePlugin(t, "demo",
		gatedExtInfo(`{"spec":1,"side_effect":"write","paths":{"file":{"op":["write"],"into_dir":true}}}`),
		`{"result":{}}`)
	got, err := ExternalToolFromFound(foundAt(path, "demo"))
	var invalid *InvalidParametersError
	if !errors.As(err, &invalid) {
		t.Fatalf("got %v, %v; want *InvalidParametersError", got, err)
	}
	if invalid.Name != "demo" || invalid.Field != "foo_tool" {
		t.Errorf("error = %+v", invalid)
	}
	if msg := err.Error(); !strings.Contains(msg, path) || !strings.Contains(msg, `"foo_tool"`) || !strings.Contains(msg, "into_dir") {
		t.Errorf("error %q must name the binary, the foo_tool field and the problem", msg)
	}
}

// A gated plugin runs only once its declared paths are authorized, and
// then with the canonical paths; the dispatcher asks no question of
// its own for it.
func TestExternalTool_GatedCalls(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"real", "secret"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, d, "f"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	run := func(t *testing.T, engine *shim.Engine, args string) (reply, stdin string, asked int) {
		t.Helper()
		path, dir := writePlugin(t, "demo", gatedExtInfo(fileFooTool), `{"result":{"ok":true}}`)
		ext, err := ExternalToolFromFound(foundAt(path, "demo"))
		if err != nil {
			t.Fatal(err)
		}
		if engine != nil {
			ext.SetEngine(engine)
		}
		reg := NewRegistry()
		if err := reg.Register(ext); err != nil {
			t.Fatal(err)
		}
		client := &scriptedClient{call: llm.ToolCall{ID: "c1", Name: "demo", Arguments: json.RawMessage(args)}}
		approve := func(string, json.RawMessage) (bool, error) { asked++; return true, nil }
		if _, err := NewDispatcher(client, reg, DispatchConfig{Approve: approve}).Run(context.Background(), "go"); err != nil {
			t.Fatalf("Run: %v", err)
		}
		msgs := client.msgsSeen[len(client.msgsSeen)-1]
		reply = msgs[len(msgs)-1].Content
		data, err := os.ReadFile(filepath.Join(dir, "stdin.json"))
		if err == nil {
			stdin = string(data)
		}
		return reply, stdin, asked
	}

	t.Run("allowed gets the canonical path", func(t *testing.T) {
		auth := &linkAuth{}
		reply, stdin, asked := run(t, &shim.Engine{Authorizer: auth, Cwd: root}, `{"file":"`+root+`/link/f","note":"n"}`)
		assertJSONEqual(t, []byte(stdin), `{"name":"demo","arguments":{"file":"`+root+`/real/f","note":"n"}}`)
		if !strings.Contains(reply, `"ok":true`) {
			t.Errorf("reply %q", reply)
		}
		if auth.calls != 1 || auth.last.SideEffect != "write" {
			t.Errorf("authorizer calls %d, request %+v", auth.calls, auth.last)
		}
		if asked != 0 {
			t.Errorf("dispatcher asked %d times; the gate asks for gated plugins", asked)
		}
	})
	t.Run("denied never runs", func(t *testing.T) {
		reply, stdin, _ := run(t, &shim.Engine{Authorizer: &linkAuth{}, Cwd: root}, `{"file":"`+root+`/secret/f"}`)
		if stdin != "" {
			t.Errorf("plugin ran with %s", stdin)
		}
		assertJSONEqual(t, []byte(reply), `{"error":{"kind":"denied","message":"not in scope","param":"file","path":"`+root+`/secret/f","op":"read,write"}}`)
	})
	t.Run("no engine denies", func(t *testing.T) {
		reply, stdin, _ := run(t, nil, `{"file":"`+root+`/real/f"}`)
		if stdin != "" {
			t.Errorf("plugin ran with %s", stdin)
		}
		if !strings.Contains(reply, `"kind":"denied"`) {
			t.Errorf("reply %q; want a denial", reply)
		}
	})
}
