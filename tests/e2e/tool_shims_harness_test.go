//go:build unix

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Shim tools run two ways: in-process, when the model calls a tool
// enabled with -T, and in link mode, when another host runs a
// foo-tool-<name> link to foo. The acceptance cases run through both
// on the built binary.
const (
	modeInProcess = "in-process"
	modeLink      = "link"
)

var shimModes = []string{modeInProcess, modeLink}

// shimEnv is an isolated foo installation: throwaway HOME and XDG
// dirs, foo-tool-* links in bin (made by `foo tool install`), a PATH of
// bin plus the system dirs, and no provider credentials. Every process
// runs in its own session with no controlling terminal, so a call that
// needs approval is refused instead of waiting on the developer's tty.
type shimEnv struct {
	t      *testing.T
	root   string // canonical scratch dir for trees, links, cwds
	foo    string
	bin    string
	home   string
	config string // XDG_CONFIG_HOME
	env    []string
}

func newShimEnv(t *testing.T) *shimEnv {
	t.Helper()
	foo, err := filepath.Abs(filepath.Join("..", "..", "bin", "foo"))
	require.NoError(t, err)
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	e := &shimEnv{t: t, root: root, foo: foo, bin: filepath.Join(root, "bin")}
	dirs := map[string]string{
		"HOME": "home", "XDG_CONFIG_HOME": "config", "XDG_DATA_HOME": "data",
		"XDG_CACHE_HOME": "cache", "XDG_STATE_HOME": "state",
	}
	for k, sub := range dirs {
		d := filepath.Join(root, sub)
		require.NoError(t, os.MkdirAll(d, 0o755))
		e.env = append(e.env, k+"="+d)
	}
	e.home, e.config = filepath.Join(root, "home"), filepath.Join(root, "config")
	require.NoError(t, os.MkdirAll(e.bin, 0o755))
	e.setPath(e.bin)

	code, stdout, stderr := e.run(root, nil, "tool", "install", "--dir", e.bin)
	require.Equalf(t, 0, code, "foo tool install: %s%s", stdout, stderr)
	return e
}

// setPath puts dirs, then the system dirs, on PATH.
func (e *shimEnv) setPath(dirs ...string) {
	out := e.env[:0:0]
	for _, kv := range e.env {
		if !strings.HasPrefix(kv, "PATH=") && !strings.HasPrefix(kv, "NO_COLOR=") {
			out = append(out, kv)
		}
	}
	e.env = append(out, "PATH="+strings.Join(append(dirs, "/usr/bin", "/bin"), ":"), "NO_COLOR=1")
}

// path joins elems under the scratch root.
func (e *shimEnv) path(elems ...string) string {
	return filepath.Join(append([]string{e.root}, elems...)...)
}

// writeConfig writes a file under foo's config dir; nil body removes it.
func (e *shimEnv) writeConfig(name string, body *string) {
	e.t.Helper()
	p := filepath.Join(e.config, "foo", name)
	if body == nil {
		require.NoError(e.t, os.RemoveAll(p))
		return
	}
	require.NoError(e.t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(e.t, os.WriteFile(p, []byte(*body), 0o644))
}

func (e *shimEnv) scope(body string)  { e.writeConfig("scope.yaml", &body) }
func (e *shimEnv) policy(body string) { e.writeConfig("tool-policy.yaml", &body) }

// allowLocalChanges is a tool-policy.yaml that auto-allows local write
// and destructive calls, so a refusal in a test is the scope's alone
// and never "no terminal to ask".
const allowLocalChanges = `schema_version: "1.0"
rules:
  - {side_effect: write, network: none, action: auto-allow, reason: test}
  - {side_effect: destructive, network: none, action: auto-allow, reason: test}
`

// exec runs name with args in cwd, detached from any terminal.
func (e *shimEnv) exec(cwd string, extraEnv []string, stdin, name string, args ...string) (int, string, string) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = cwd
	cmd.Env = append(append([]string(nil), e.env...), extraEnv...)
	cmd.Stdin = strings.NewReader(stdin)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	require.NoErrorf(e.t, ctx.Err(), "%s hung; stderr=%q", name, stderr.String())
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		return ee.ExitCode(), stdout.String(), stderr.String()
	case err != nil:
		e.t.Fatalf("run %s: %v", name, err)
	}
	return 0, stdout.String(), stderr.String()
}

// run runs foo itself.
func (e *shimEnv) run(cwd string, extraEnv []string, args ...string) (int, string, string) {
	e.t.Helper()
	return e.exec(cwd, extraEnv, "", e.foo, args...)
}

// errBody is the structured refusal a call returns.
type errBody struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Param   string `json:"param"`
	Path    string `json:"path"`
	Op      string `json:"op"`
}

// resultBody is the envelope of a call that ran.
type resultBody struct {
	ExitCode        int      `json:"exit_code"`
	OK              bool     `json:"ok"`
	Stdout          *string  `json:"stdout"`
	StdoutBytes     int64    `json:"stdout_bytes"`
	StdoutTruncated bool     `json:"stdout_truncated"`
	StdoutBinary    bool     `json:"stdout_binary"`
	Argv            []string `json:"argv"`
	Paths           []struct {
		Param    string `json:"param"`
		Resolved string `json:"resolved"`
	} `json:"paths"`
	Filtered int `json:"filtered"`
}

// outcome is what the model (or the host) got back from one call.
type outcome struct {
	Err *errBody
	Res *resultBody
	Raw string
}

func (o outcome) stdout() string {
	if o.Res == nil || o.Res.Stdout == nil {
		return ""
	}
	return *o.Res.Stdout
}

// call runs one tool call from cwd in mode and returns what came back.
func (e *shimEnv) call(mode, cwd, tool, args string) outcome {
	e.t.Helper()
	if mode == modeLink {
		return e.callLink(cwd, tool, args)
	}
	return e.callInProcess(cwd, tool, args)
}

// callLink pipes the request into bin/foo-tool-<tool>, as another host
// would.
func (e *shimEnv) callLink(cwd, tool, args string) outcome {
	e.t.Helper()
	req := fmt.Sprintf(`{"name":%q,"arguments":%s}`, tool, args)
	code, stdout, stderr := e.exec(cwd, nil, req, filepath.Join(e.bin, "foo-tool-"+tool))
	require.Equalf(e.t, 0, code, "foo-tool-%s exit %d: %s", tool, code, stderr)
	var resp struct {
		Result *resultBody `json:"result"`
		Detail *errBody    `json:"error_detail"`
	}
	require.NoErrorf(e.t, json.Unmarshal([]byte(stdout), &resp), "response %q", stdout)
	return outcome{Err: resp.Detail, Res: resp.Result, Raw: stdout}
}

// callInProcess runs `foo -T <tool>` against a stub model endpoint
// that asks for exactly this call, then returns the tool message foo
// sent back to it. No real provider is involved.
func (e *shimEnv) callInProcess(cwd, tool, args string) outcome {
	e.t.Helper()
	stub := newStubModel(e.t, tool, args)
	code, stdout, stderr := e.run(cwd,
		[]string{"OPENAI_API_KEY=sk-test", "LLM_BASE_URL=" + stub.URL + "/v1"},
		"--offline", "-m", "gpt-4o", "-T", tool, "go")
	require.Equalf(e.t, 0, code, "foo -T %s exit %d: %s%s", tool, code, stdout, stderr)
	msg := stub.toolMessage()
	require.NotEmptyf(e.t, msg, "the model never got a tool result; stderr %q", stderr)

	var probe struct {
		Error json.RawMessage `json:"error"`
	}
	require.NoErrorf(e.t, json.Unmarshal([]byte(msg), &probe), "tool message %q", msg)
	if len(probe.Error) > 0 {
		var body errBody
		if json.Unmarshal(probe.Error, &body) != nil {
			body.Message = string(probe.Error) // a plain string error
		}
		return outcome{Err: &body, Raw: msg}
	}
	var res resultBody
	require.NoError(e.t, json.Unmarshal([]byte(msg), &res))
	return outcome{Res: &res, Raw: msg}
}

// stubModel is an OpenAI-compatible chat endpoint: the first request
// gets one tool call, every later one a plain reply. It keeps the last
// message of the second request, which carries the tool result.
type stubModel struct {
	*httptest.Server
	mu    sync.Mutex
	calls int
	reply string
}

func newStubModel(t *testing.T, tool, args string) *stubModel {
	t.Helper()
	s := &stubModel{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)

		s.mu.Lock()
		s.calls++
		n := s.calls
		if n == 2 && len(req.Messages) > 0 {
			s.reply = req.Messages[len(req.Messages)-1].Content
		}
		s.mu.Unlock()

		msg := map[string]any{"role": "assistant", "content": "done"}
		finish := "stop"
		if n == 1 {
			msg = map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{
				"id": "call_1", "type": "function",
				"function": map[string]any{"name": tool, "arguments": args},
			}}}
			finish = "tool_calls"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "c1", "object": "chat.completion", "created": 0, "model": "gpt-4o",
			"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}},
			"usage":   map[string]int{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *stubModel) toolMessage() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reply
}

// wantDenied asserts a refusal of kind on param (when set) whose path
// or message contains what; the command never ran.
func wantRefused(t *testing.T, o outcome, kind, param, what string) {
	t.Helper()
	require.NotNilf(t, o.Err, "want %s, got %s", kind, o.Raw)
	require.Equalf(t, kind, o.Err.Kind, "refusal: %s", o.Raw)
	if param != "" {
		require.Equalf(t, param, o.Err.Param, "refusal: %s", o.Raw)
	}
	require.Containsf(t, o.Err.Path+" "+o.Err.Message, what, "refusal: %s", o.Raw)
	require.Nil(t, o.Res)
}

// wantRan asserts the command ran and exited ok.
func wantRan(t *testing.T, o outcome) *resultBody {
	t.Helper()
	require.Nilf(t, o.Err, "want a result, got %s", o.Raw)
	require.NotNil(t, o.Res)
	require.Truef(t, o.Res.OK, "command failed: %s", o.Raw)
	return o.Res
}

// jsonArgs marshals tool arguments.
func jsonArgs(t *testing.T, v map[string]any) string {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return string(data)
}

func mkfile(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}
