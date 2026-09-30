package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"hop.top/foo/internal/tool"
	"hop.top/kit/go/ai/llm"
)

// callOnce asks for one call of a tool, then records the tool message
// it gets back.
type callOnce struct {
	name, args string
	calls      int
	reply      string
}

func (c *callOnce) CallWithTools(_ context.Context, msgs []llm.Message, _ []llm.ToolDef) (llm.ToolResponse, error) {
	c.calls++
	if c.calls == 1 {
		return llm.ToolResponse{ToolCalls: []llm.ToolCall{{ID: "c1", Name: c.name, Arguments: json.RawMessage(c.args)}}}, nil
	}
	c.reply = msgs[len(msgs)-1].Content
	return llm.ToolResponse{Content: "done"}, nil
}

// shimRun is a -T run up to the model call: the registry and dispatch
// config come from the same functions runPromptOrREPL uses.
type shimRun struct {
	t        *testing.T
	cmd      *cobra.Command
	stderr   *bytes.Buffer
	registry *tool.Registry
	prompter *tool.Prompter
}

// newShimRun resolves -T names the way a run does, in the current
// working directory.
func newShimRun(t *testing.T, names ...string) (*shimRun, error) {
	t.Helper()
	prev := toolNames
	toolNames = names
	t.Cleanup(func() { toolNames = prev })

	cmd := &cobra.Command{}
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	p := newToolPrompter(cmd)
	reg, err := selectedTools(cmd.ErrOrStderr(), p)
	return &shimRun{t: t, cmd: cmd, stderr: &stderr, registry: reg, prompter: p}, err
}

func mustShimRun(t *testing.T, names ...string) *shimRun {
	t.Helper()
	r, err := newShimRun(t, names...)
	if err != nil {
		t.Fatalf("-T %v: %v", names, err)
	}
	return r
}

// call has the stub model call name with args and returns the tool
// message the model got back.
func (r *shimRun) call(name, args string) string {
	r.t.Helper()
	c := &callOnce{name: name, args: args}
	d := tool.NewDispatcher(c, r.registry, toolDispatchConfig(r.cmd, r.prompter))
	if _, err := d.Run(context.Background(), "go"); err != nil {
		r.t.Fatalf("dispatch: %v", err)
	}
	return c.reply
}

// questions counts approval questions asked on the terminal.
func (r *shimRun) questions() int { return strings.Count(r.stderr.String(), "[y/N]") }

type toolMessage struct {
	Error *struct {
		Kind    string `json:"kind"`
		Message string `json:"message"`
		Param   string `json:"param"`
		Path    string `json:"path"`
	} `json:"error"`
	Stdout *string `json:"stdout"`
	OK     bool    `json:"ok"`
	Paths  []struct {
		Resolved string `json:"resolved"`
	} `json:"paths"`
}

func decodeToolMessage(t *testing.T, msg string) toolMessage {
	t.Helper()
	var m toolMessage
	if err := json.Unmarshal([]byte(msg), &m); err != nil {
		t.Fatalf("tool message %q is not JSON: %v", msg, err)
	}
	return m
}

func (m toolMessage) wantDenied(t *testing.T, kind, contains string) {
	t.Helper()
	if m.Error == nil || m.Error.Kind != kind || !strings.Contains(m.Error.Message+" "+m.Error.Path, contains) {
		t.Errorf("tool message = %+v; want %s error mentioning %q", m, kind, contains)
	}
	if m.Stdout != nil {
		t.Errorf("command ran: stdout %q", *m.Stdout)
	}
}

func (m toolMessage) wantResult(t *testing.T, resolved string) {
	t.Helper()
	if m.Error != nil {
		t.Fatalf("tool message is an error %+v; want a wc result", *m.Error)
	}
	if !m.OK || m.Stdout == nil || !strings.Contains(*m.Stdout, resolved) {
		t.Errorf("result = %+v; want ok wc output naming %s", m, resolved)
	}
	if len(m.Paths) != 1 || m.Paths[0].Resolved != resolved {
		t.Errorf("paths = %+v; want %s", m.Paths, resolved)
	}
}

// noTerminal fails the test if anything opens the approval terminal.
func noTerminal(t *testing.T) {
	t.Helper()
	prev := openApprovalTerminal
	openApprovalTerminal = func() (io.Reader, error) {
		t.Error("approval terminal opened; no question was expected")
		return nil, errors.New("no terminal in this test")
	}
	t.Cleanup(func() { openApprovalTerminal = prev })
}

// gateTree is scopeEnv plus files and a link: p/a.txt and p/b.txt are
// readable, outside/b.txt is not, p/link points at outside/deep.
func gateTree(t *testing.T) string {
	t.Helper()
	root := scopeEnv(t)
	for _, f := range []string{"p/a.txt", "p/b.txt", "outside/a.txt", "outside/b.txt"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("one two\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "outside/deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside/deep"), filepath.Join(root, "p/link")); err != nil {
		t.Fatal(err)
	}
	return root
}

func allowRead(root string) string {
	return "allow:\n  - path: \"" + root + "/p/**\"\n    ops: [read]\n"
}

func wcArgs(paths ...string) string {
	data, _ := json.Marshal(map[string]any{"path": paths})
	return string(data)
}

// Without scope.yaml nothing is granted: the model gets a structured
// denial naming the file to create, and wc never runs.
func TestShimGate_NoScopeDenied(t *testing.T) {
	root := gateTree(t)
	noTerminal(t)
	r := mustShimRun(t, "wc")

	m := decodeToolMessage(t, r.call("wc", wcArgs(root+"/p/a.txt")))
	want := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "foo", "scope.yaml")
	m.wantDenied(t, "denied", want)
}

func TestShimGate_ScopeDecides(t *testing.T) {
	root := gateTree(t)
	writeScopeYAML(t, allowRead(root))
	noTerminal(t)
	r := mustShimRun(t, "wc")

	t.Run("allowed", func(t *testing.T) {
		decodeToolMessage(t, r.call("wc", wcArgs(root+"/p/a.txt"))).wantResult(t, root+"/p/a.txt")
	})
	t.Run("outside", func(t *testing.T) {
		decodeToolMessage(t, r.call("wc", wcArgs(root+"/outside/b.txt"))).wantDenied(t, "denied", root+"/outside/b.txt")
	})
	t.Run("one path outside denies the call", func(t *testing.T) {
		decodeToolMessage(t, r.call("wc", wcArgs(root+"/p/a.txt", root+"/outside/b.txt"))).
			wantDenied(t, "denied", root+"/outside/b.txt")
	})
	// Lexically p/link/../b.txt is p/b.txt (allowed); physically it is
	// outside/b.txt.
	t.Run("link dotdot escape", func(t *testing.T) {
		decodeToolMessage(t, r.call("wc", wcArgs(root+"/p/link/../b.txt"))).wantDenied(t, "denied", root+"/outside/b.txt")
	})
	if n := r.questions(); n != 0 {
		t.Errorf("%d approval questions for read calls without --tools-approve", n)
	}
}

// Relative paths resolve against the directory foo started in, even if
// the process changes directory before the call.
func TestShimGate_CwdCapturedAtStart(t *testing.T) {
	root := gateTree(t)
	writeScopeYAML(t, allowRead(root))
	noTerminal(t)
	t.Chdir(filepath.Join(root, "p"))
	r := mustShimRun(t, "wc")
	t.Chdir(filepath.Join(root, "outside"))

	decodeToolMessage(t, r.call("wc", wcArgs("a.txt"))).wantResult(t, root+"/p/a.txt")
}

// --tools-approve on a shim tool: one question per call, the gate's,
// showing the command that will run; never the dispatcher's too.
func TestShimGate_ToolsApproveAsksOnce(t *testing.T) {
	root := gateTree(t)
	writeScopeYAML(t, allowRead(root))

	t.Run("no", func(t *testing.T) {
		withApproval(t, func() (io.Reader, error) { return strings.NewReader("n\n"), nil })
		r := mustShimRun(t, "wc")
		decodeToolMessage(t, r.call("wc", wcArgs(root+"/p/a.txt"))).wantDenied(t, "declined", "declined")
		if n := r.questions(); n != 1 {
			t.Errorf("asked %d times; want 1: %q", n, r.stderr)
		}
	})
	t.Run("yes", func(t *testing.T) {
		withApproval(t, func() (io.Reader, error) { return strings.NewReader("y\n"), nil })
		r := mustShimRun(t, "wc")
		decodeToolMessage(t, r.call("wc", wcArgs(root+"/p/a.txt"))).wantResult(t, root+"/p/a.txt")
		if n := r.questions(); n != 1 {
			t.Errorf("asked %d times; want 1: %q", n, r.stderr)
		}
		if q := r.stderr.String(); !strings.Contains(q, "wants to run: ") || !strings.Contains(q, root+"/p/a.txt") {
			t.Errorf("question %q must show the command with its canonical path", q)
		}
	})
	// Tools that do not ask on their own keep the dispatcher's question.
	t.Run("builtin keeps dispatcher question", func(t *testing.T) {
		withApproval(t, func() (io.Reader, error) { return strings.NewReader("n\n"), nil })
		r := mustShimRun(t, "wc", "foo_time")
		// Declined the same way as a shim: the structured error, not
		// {"skipped": true}.
		decodeToolMessage(t, r.call("foo_time", `{}`)).wantDenied(t, "declined", "the user declined this call")
		if n := r.questions(); n != 1 || !strings.Contains(r.stderr.String(), "[tool] execute foo_time") {
			t.Errorf("asked %d times: %q; want the dispatcher's question once", n, r.stderr)
		}
	})
	// With no terminal, the model is told why, as the gate does for shims.
	t.Run("builtin no terminal carries reason", func(t *testing.T) {
		withApproval(t, func() (io.Reader, error) { return nil, errors.New("open /dev/tty: device not configured") })
		r := mustShimRun(t, "wc", "foo_time")
		decodeToolMessage(t, r.call("foo_time", `{}`)).wantDenied(t, "declined", "cannot be asked: no terminal")
		if !strings.Contains(r.stderr.String(), "foo_time denied") {
			t.Errorf("stderr %q; want the denial reported to the user too", r.stderr)
		}
	})
}

// A broken scope.yaml or tool-policy.yaml fails a run that selects a
// shim tool before any prompt is read, naming the file. A run without
// shim tools does not depend on them.
func TestShimGate_BrokenConfigFailsRun(t *testing.T) {
	for _, tc := range []struct{ file, body string }{
		{"scope.yaml", "mode: [not a mode\n"},
		{"scope.yaml", "mode: loose\n"},
		{"tool-policy.yaml", "rules: {not: a list\n"},
	} {
		t.Run(tc.file+" "+tc.body, func(t *testing.T) {
			gateTree(t)
			path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "foo", tc.file)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := newShimRun(t, "wc")
			if err == nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("-T wc error = %v; want one naming %s", err, path)
			}
			if _, err := newShimRun(t, "foo_time"); err != nil {
				t.Errorf("-T foo_time failed on an unrelated shim config: %v", err)
			}
		})
	}
}
