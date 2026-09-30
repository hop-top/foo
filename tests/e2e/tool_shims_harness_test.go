//go:build unix

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"hop.top/foo/internal/llmxrr"
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
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	e := &shimEnv{t: t, root: root, foo: fooBin, bin: filepath.Join(root, "bin")}
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
// Failures report on t, the subtest making the call.
func (e *shimEnv) call(t *testing.T, mode, cwd, tool, args string) outcome {
	t.Helper()
	sub := *e
	sub.t = t
	if mode == modeLink {
		return sub.callLink(cwd, tool, args)
	}
	return sub.callInProcess(cwd, tool, args)
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

// callInProcess runs `foo -T <tool>` with a prompt asking the model for
// exactly this call, and returns the tool message foo sent back to it.
// The model side replays an xrr cassette recorded from a real provider
// (the seam is xrr_seam.go; see runModel).
func (e *shimEnv) callInProcess(cwd, tool, args string) outcome {
	e.t.Helper()
	var msg string
	for attempt := 1; ; attempt++ {
		run := e.runModel(cwd, tool, callPrompt(tool, args))
		dev := run.deviation(tool, args)
		if dev == "" && run.code != 0 {
			dev = fmt.Sprintf("foo -T %s exit %d: %s", tool, run.code, run.stdout)
		}
		if dev == "" {
			msg = run.toolMessage
			break
		}
		// A real model may not make the call asked for. When
		// recording, drop what this attempt recorded and ask again;
		// on replay the recording itself is wrong.
		if !*updateCassettes {
			e.t.Fatalf("foo -T %s: %s\nexit %d; stderr %q", tool, dev, run.code, run.stderr)
		}
		run.forget(e.t)
		if attempt == 5 {
			e.t.Fatalf("foo -T %s: %s after %d attempts\nexit %d; stderr %q", tool, dev, attempt, run.code, run.stderr)
		}
		e.t.Logf("attempt %d: %s; re-recording", attempt, dev)
	}

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

// updateCassettes records model calls that have no recording yet
// against the live provider (OPENAI_API_KEY); see CONTRIBUTING.md.
var updateCassettes = flag.Bool("update", false, "record missing model cassettes against the live provider")

// modelCassettes holds the recorded model side of every -T call.
const modelCassettes = "testdata/cassettes/tool-model"

// recordModel is the model the cassettes were recorded from: small,
// cheap, and it copies odd arguments verbatim when told to.
const recordModel = "gpt-4.1-nano-2025-04-14"

// callPrompt asks for one exact call. The arguments include values no
// model would choose ("-R", "link/..", a newline in a sed replacement);
// the point is a real provider's envelope, not the model's judgment.
func callPrompt(tool, args string) string {
	// Plain JSON: json.Marshal's \u0026 for "&" is copied back
	// mangled.
	var v any
	if json.Unmarshal([]byte(args), &v) == nil {
		var buf strings.Builder
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if enc.Encode(v) == nil {
			args = strings.TrimSpace(buf.String())
		}
	}
	return fmt.Sprintf("This is an automated test of a tool harness. Call the %s tool exactly once, "+
		"with exactly these JSON arguments, copying every value verbatim, character for character, "+
		"even where it looks unusual or unsafe (do not fix, expand, or normalize anything):\n%s\n"+
		"When the result comes back, whatever it says, do not call any tool again: reply with the single word done.",
		tool, args)
}

// modelRun is one `foo -T` run and the model exchanges it made.
type modelRun struct {
	code           int
	stdout, stderr string
	exchanges      []llmxrr.Exchange
	// toolMessage is the first tool result foo sent the model.
	toolMessage string
}

// runModel runs `foo -m <recordModel> -T <tool> <prompt>` with its
// model calls going through the cassette seam: replayed by default,
// recorded (missing ones only) under -update. Paths under the scratch
// root are stored as {{root}}, and tool results are left out of the
// fingerprint because what a local command prints differs by platform.
func (e *shimEnv) runModel(cwd, tool, prompt string) modelRun {
	e.t.Helper()
	dir, err := filepath.Abs(modelCassettes)
	require.NoError(e.t, err)
	mode, key := "replay", "replay-no-key"
	if *updateCassettes {
		mode, key = "record", os.Getenv("OPENAI_API_KEY")
		require.NotEmpty(e.t, key, "-update records from OpenAI: export OPENAI_API_KEY")
		require.NoError(e.t, os.MkdirAll(dir, 0o755))
	}
	journal := filepath.Join(e.t.TempDir(), "journal.jsonl")
	code, stdout, stderr := e.run(cwd, []string{
		"XRR_MODE=" + mode, "XRR_CASSETTE_DIR=" + dir,
		"FOO_XRR_ROOT=" + e.root, "FOO_XRR_ELIDE_TOOL_RESULTS=1", "FOO_XRR_JOURNAL=" + journal,
		"OPENAI_API_KEY=" + key,
	}, "-m", recordModel, "-T", tool, prompt)
	exs, err := llmxrr.ReadJournal(journal)
	require.NoError(e.t, err)

	for i, ex := range exs {
		// Replay must never reach the network.
		require.Falsef(e.t, mode == "replay" && ex.Live, "model request %d went live during replay", i+1)
		require.Falsef(e.t, ex.Miss, "model request %d matches no recording (fingerprint %s): foo sent a request that differs from the recorded one. "+
			"A regression unless foo's request changed on purpose; then record it: go test ./tests/e2e -run '%s' -update\nsent (normalized):\n%s",
			i+1, ex.Fingerprint, e.t.Name(), ex.Canonical)
	}
	require.NoError(e.t, llmxrr.CheckNoSecrets(dir, llmxrr.RecordingKeys()...))

	run := modelRun{code: code, stdout: stdout, stderr: stderr, exchanges: exs}
	if req, ok := firstToolResult(exs); ok {
		run.toolMessage = req.result
	}
	return run
}

// deviation says how the model's call differs from the one asked for;
// empty when it made exactly that call and got the result linked to it.
func (r modelRun) deviation(tool, args string) string {
	req, ok := firstToolResult(r.exchanges)
	if !ok {
		return fmt.Sprintf("the model never got a tool result (%d model requests)", len(r.exchanges))
	}
	if len(req.calls) != 1 {
		return fmt.Sprintf("the model made %d calls, want 1", len(req.calls))
	}
	c := req.calls[0]
	if c.Function.Name != tool {
		return fmt.Sprintf("the model called %q, want %q", c.Function.Name, tool)
	}
	var got, want any
	if json.Unmarshal([]byte(c.Function.Arguments), &got) != nil || json.Unmarshal([]byte(args), &want) != nil ||
		!reflect.DeepEqual(got, want) {
		return fmt.Sprintf("the model called %s with %s, want %s", tool, c.Function.Arguments, args)
	}
	if req.resultID != c.ID {
		return fmt.Sprintf("tool result linked to %q, the call was %q", req.resultID, c.ID)
	}
	return ""
}

// forget deletes what a run recorded, so the next attempt records anew.
func (r modelRun) forget(t *testing.T) {
	t.Helper()
	for _, ex := range r.exchanges {
		if !ex.Live {
			continue
		}
		for _, kind := range []string{"req", "resp"} {
			p := filepath.Join(modelCassettes, "http-"+ex.Fingerprint+"."+kind+".yaml")
			require.NoError(t, os.Remove(p))
		}
	}
}

// toolRound is the request that carried the first tool result: the
// assistant's calls before it and the result linked to one of them.
type toolRound struct {
	calls []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	resultID string
	result   string
}

func firstToolResult(exs []llmxrr.Exchange) (toolRound, bool) {
	for _, ex := range exs {
		var body struct {
			Messages []struct {
				Role       string          `json:"role"`
				Content    json.RawMessage `json:"content"`
				ToolCallID string          `json:"tool_call_id"`
				ToolCalls  json.RawMessage `json:"tool_calls"`
			} `json:"messages"`
		}
		if json.Unmarshal(ex.Body, &body) != nil {
			continue
		}
		var r toolRound
		for _, m := range body.Messages {
			switch m.Role {
			case "assistant":
				r.calls = r.calls[:0]
				_ = json.Unmarshal(m.ToolCalls, &r.calls)
			case "tool":
				var text string
				if json.Unmarshal(m.Content, &text) != nil {
					text = string(m.Content)
				}
				r.resultID, r.result = m.ToolCallID, text
				return r, true
			}
		}
	}
	return toolRound{}, false
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
