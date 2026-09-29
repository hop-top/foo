package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"hop.top/foo/internal/tool"
	"hop.top/kit/go/ai/llm"
	"hop.top/kit/go/console/output"
)

// demoToolScript is a minimal foo-tool-* binary. Every --ext-info
// interrogation appends a line to $FOO_TEST_EXTINFO_LOG so a test can
// count how many times discovery executed it.
const demoToolScript = `#!/bin/sh
if [ "$1" = "--ext-info" ]; then
  echo x >> "$FOO_TEST_EXTINFO_LOG"
  echo '{"name":"demo","version":"0.1.0","description":"Demo tool for tests"}'
  exit 0
fi
cat >/dev/null
echo '{"result":{"ok":true}}'
`

// toolTestEnv isolates a foo run: throwaway HOME and XDG dirs, a PATH
// holding only the demo tool plus the system dirs, no provider
// credentials, and no bus peers. It returns the demo binary's path and
// the ext-info log.
type toolTestEnv struct {
	demoPath   string
	extInfoLog string
}

func newToolTestEnv(t *testing.T) toolTestEnv {
	t.Helper()
	tmp := t.TempDir()
	for k, sub := range map[string]string{
		"HOME":            "home",
		"XDG_CONFIG_HOME": "config",
		"XDG_DATA_HOME":   "data",
		"XDG_CACHE_HOME":  "cache",
		"XDG_STATE_HOME":  "state",
	} {
		dir := filepath.Join(tmp, sub)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv(k, dir)
	}
	for _, k := range []string{
		"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY",
		"GOOGLE_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY", "GROQ_API_KEY",
		"OPENROUTER_API_KEY", "MISTRAL_API_KEY", "FOO_BUS_PEERS",
		"FOO_MODEL", "FOO_BUDGET", "LLM_BASE_URL",
	} {
		t.Setenv(k, "")
	}

	bin := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	demo := filepath.Join(bin, "foo-tool-demo")
	if err := os.WriteFile(demo, []byte(demoToolScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")

	log := filepath.Join(tmp, "extinfo.log")
	t.Setenv("FOO_TEST_EXTINFO_LOG", log)
	return toolTestEnv{demoPath: demo, extInfoLog: log}
}

func (e toolTestEnv) extInfoCalls(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(e.extInfoLog)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "\n")
}

// runFooArgs drives the real root command. extInfoBefore is the
// interrogation count after New() and before Execute, so a caller can
// isolate what the command body itself cost.
func runFooArgs(t *testing.T, env toolTestEnv, args ...string) (stdout, stderr string, extInfoBefore int, err error) {
	t.Helper()
	r := New("test")
	extInfoBefore = env.extInfoCalls(t)
	var out, errOut bytes.Buffer
	r.Cmd.SetOut(&out)
	r.Cmd.SetErr(&errOut)
	r.Cmd.SetIn(strings.NewReader(""))
	r.Cmd.SetArgs(args)
	err = r.Cmd.Execute()
	return out.String(), errOut.String(), extInfoBefore, err
}

type toolListRow struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	Description string `json:"description"`
}

func TestToolList_JSON(t *testing.T) {
	env := newToolTestEnv(t)

	stdout, _, _, err := runFooArgs(t, env, "tool", "list", "--offline", "--format=json")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var rows []toolListRow
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("stdout is not a JSON array (%v): %q", err, stdout)
	}
	byName := make(map[string]toolListRow, len(rows))
	for _, r := range rows {
		byName[r.Name] = r
	}
	for _, name := range []string{"foo_time", "foo_version"} {
		r, ok := byName[name]
		if !ok {
			t.Errorf("builtin %q missing from listing: %+v", name, rows)
			continue
		}
		if r.Source != "builtin" {
			t.Errorf("%s source = %q; want %q", name, r.Source, "builtin")
		}
		if r.Description == "" {
			t.Errorf("%s has no description", name)
		}
	}
	demo, ok := byName["demo"]
	if !ok {
		t.Fatalf("PATH tool foo-tool-demo missing from listing: %+v", rows)
	}
	if demo.Source != env.demoPath {
		t.Errorf("demo source = %q; want binary path %q", demo.Source, env.demoPath)
	}
	if demo.Description != "Demo tool for tests" {
		t.Errorf("demo description = %q; want the --ext-info description", demo.Description)
	}
}

func TestToolList_OtherFormats(t *testing.T) {
	for _, tc := range []struct{ format, want string }{
		{"yaml", "name: foo_time"},
		{"csv", "NAME,SOURCE,DESCRIPTION"},
		{"table", "SOURCE"},
	} {
		t.Run(tc.format, func(t *testing.T) {
			env := newToolTestEnv(t)
			stdout, _, _, err := runFooArgs(t, env, "tool", "list", "--offline", "--format="+tc.format)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if !strings.Contains(stdout, tc.want) {
				t.Errorf("%s output missing %q: %q", tc.format, tc.want, stdout)
			}
			if !strings.Contains(stdout, "demo") {
				t.Errorf("%s output missing PATH tool demo: %q", tc.format, stdout)
			}
		})
	}
}

// TestToolList_ScansOnce pins the discovery cost: every foo-tool-*
// binary is exec'd with --ext-info, so the listing must interrogate
// each one exactly once.
func TestToolList_ScansOnce(t *testing.T) {
	env := newToolTestEnv(t)

	_, _, before, err := runFooArgs(t, env, "tool", "list", "--offline", "--format=json")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := env.extInfoCalls(t) - before; got != 1 {
		t.Errorf("tool list interrogated foo-tool-demo %d times; want 1", got)
	}
}

func TestToolCmd_Wiring(t *testing.T) {
	r := New("test")
	cmd, _, err := r.Cmd.Find([]string{"tool", "list"})
	if err != nil || cmd.CommandPath() != "foo tool list" {
		t.Fatalf("`foo tool list` not registered (resolved %q, err %v)", cmd.CommandPath(), err)
	}
	if got := cmd.Parent().GroupID; got != "organize" {
		t.Errorf("tool GroupID = %q; want %q (beside model and provider)", got, "organize")
	}
}

func TestToolFlag_HelpPointsAtToolList(t *testing.T) {
	r := New("test")
	f := r.Cmd.Flags().Lookup("tool")
	if f == nil {
		t.Fatal("-T/--tool not registered")
	}
	if !strings.Contains(f.Usage, "foo tool list") {
		t.Errorf("--tool usage %q must point at `foo tool list`", f.Usage)
	}
	if strings.Contains(f.Usage, "`") {
		t.Errorf("--tool usage %q must not use backquotes: pflag renders a "+
			"backquoted word as the flag's value placeholder", f.Usage)
	}
}

// countingModelServer is an OpenAI-compatible endpoint that answers
// every chat completion with a plain reply and counts the calls.
func countingModelServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","created":0,"model":"gpt-4o",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"hello from stub"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// TestUnknownTool_Rejected is the headline defect: an unknown -T name
// used to be dropped silently, so the model ran with fewer tools (or
// none) and nothing said why. It must now fail before any model call,
// name the bad tool, list the good ones, and exit not-found — in either
// flag position.
func TestUnknownTool_Rejected(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"flag before prompt", []string{"--offline", "-m", "gpt-4o", "-T", "nope", "hi"}},
		{"flag after prompt", []string{"--offline", "-m", "gpt-4o", "hi", "-T", "nope"}},
		{"mixed with a valid tool", []string{"--offline", "-m", "gpt-4o", "-T", "foo_time,nope", "hi"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newToolTestEnv(t)
			srv, hits := countingModelServer(t)
			t.Setenv("OPENAI_API_KEY", "sk-test")
			t.Setenv("LLM_BASE_URL", srv.URL+"/v1")

			stdout, _, _, err := runFooArgs(t, env, tc.args...)
			if err == nil {
				t.Fatalf("unknown -T must fail; got success with stdout %q", stdout)
			}
			if n := hits.Load(); n != 0 {
				t.Errorf("model endpoint called %d times; the -T check must fire before any model call", n)
			}
			msg := err.Error()
			for _, want := range []string{`"nope"`, "foo_time", "foo_version", "demo", "foo tool list"} {
				if !strings.Contains(msg, want) {
					t.Errorf("error %q missing %q", msg, want)
				}
			}
			var ce interface{ AsCLIError() *output.Error }
			if !errors.As(err, &ce) || ce.AsCLIError().ExitCode != 3 {
				t.Errorf("error %T must carry the not-found exit code 3, like unknown --pattern / --schema", err)
			}
		})
	}
}

// TestUnknownTool_DidYouMean mirrors the pattern/schema not-found
// enrichment: a near miss names the likely intended tool.
func TestUnknownTool_DidYouMean(t *testing.T) {
	env := newToolTestEnv(t)
	_, _, _, err := runFooArgs(t, env, "--offline", "--dry-run", "-T", "foo_tme", "hi")
	if err == nil {
		t.Fatal("unknown -T must fail, including under --dry-run")
	}
	if !strings.Contains(err.Error(), `did you mean "foo_time"`) {
		t.Errorf("error %q should suggest foo_time", err.Error())
	}
}

// TestKnownTool_ScansOnceAndCallsModel covers the happy path and the
// discovery cost together: validating -T and running the dispatcher
// must share one registry, so foo-tool-demo is interrogated once.
func TestKnownTool_ScansOnceAndCallsModel(t *testing.T) {
	env := newToolTestEnv(t)
	srv, hits := countingModelServer(t)
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("LLM_BASE_URL", srv.URL+"/v1")

	stdout, _, before, err := runFooArgs(t, env, "--offline", "-m", "gpt-4o", "-T", "demo", "hi")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(stdout, "hello from stub") {
		t.Errorf("stdout %q missing the model reply", stdout)
	}
	if hits.Load() == 0 {
		t.Error("model endpoint never called")
	}
	if got := env.extInfoCalls(t) - before; got != 1 {
		t.Errorf("prompt run interrogated foo-tool-demo %d times; want 1", got)
	}
}

// addToolScript drops another foo-tool-<file> script on the test PATH.
// It prints extInfo for --ext-info; a call copies stdin to
// <bin>/<file>.stdin and prints {"result":{"ok":true}}.
func (e toolTestEnv) addToolScript(t *testing.T, file, extInfo string) string {
	t.Helper()
	bin := filepath.Dir(e.demoPath)
	path := filepath.Join(bin, "foo-tool-"+file)
	script := "#!/bin/sh\nif [ \"$1\" = \"--ext-info\" ]; then\n  cat <<'JSON'\n" + extInfo +
		"\nJSON\n  exit 0\nfi\ncat > '" + filepath.Join(bin, file+".stdin") + "'\necho '{\"result\":{\"ok\":true}}'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

const weatherToolInfo = `{"name":"weather","version":"0.1.0","description":"Forecast for a city",` +
	`"parameters":{"type":"object","properties":{"city":{"type":"string"},"days":{"type":"integer"}},"required":["city"]}}`

func toolRowsByName(t *testing.T, stdout string) map[string]map[string]any {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("stdout is not a JSON array (%v): %q", err, stdout)
	}
	out := make(map[string]map[string]any, len(rows))
	for _, r := range rows {
		out[r["name"].(string)] = r
	}
	return out
}

// TestToolList_ShowsDeclaredParameters: the listing says which tools
// take arguments, so a plugin author can see foo picked up the schema.
func TestToolList_ShowsDeclaredParameters(t *testing.T) {
	env := newToolTestEnv(t)
	env.addToolScript(t, "weather", weatherToolInfo)

	stdout, _, _, err := runFooArgs(t, env, "tool", "list", "--offline", "--format=json")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	rows := toolRowsByName(t, stdout)
	for name, want := range map[string]bool{"weather": true, "demo": false, "foo_time": false} {
		row, ok := rows[name]
		if !ok {
			t.Errorf("%s missing from listing: %v", name, rows)
			continue
		}
		if got, _ := row["params"].(bool); got != want || row["params"] == nil {
			t.Errorf("%s params = %v; want %v", name, row["params"], want)
		}
	}
}

// TestToolList_InvalidParametersSkippedLoudly: a plugin whose
// --ext-info parameters is not a JSON Schema object is left out of the
// registry and named on stderr — never offered to the model with a
// schema it cannot honour, never dropped silently.
func TestToolList_InvalidParametersSkippedLoudly(t *testing.T) {
	env := newToolTestEnv(t)
	bad := env.addToolScript(t, "bad",
		`{"name":"bad","version":"0.1.0","description":"d","parameters":"city"}`)

	stdout, stderr, _, err := runFooArgs(t, env, "tool", "list", "--offline", "--format=json")
	if err != nil {
		t.Fatalf("a broken plugin must not fail the listing: %v", err)
	}
	rows := toolRowsByName(t, stdout)
	if _, ok := rows["bad"]; ok {
		t.Errorf("plugin with invalid parameters was listed: %v", rows["bad"])
	}
	if _, ok := rows["demo"]; !ok {
		t.Errorf("healthy plugin demo missing: %v", rows)
	}
	for _, want := range []string{"warning", bad, "parameters"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr %q missing %q", stderr, want)
		}
	}
}

// TestToolList_BlankExtInfoNameUsesFilename: an empty --ext-info name
// must not register a blank-named tool.
func TestToolList_BlankExtInfoNameUsesFilename(t *testing.T) {
	env := newToolTestEnv(t)
	env.addToolScript(t, "anon", `{"name":"","version":"0.1.0","description":"Nameless"}`)

	stdout, _, _, err := runFooArgs(t, env, "tool", "list", "--offline", "--format=json")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	rows := toolRowsByName(t, stdout)
	if _, ok := rows[""]; ok {
		t.Error("blank-named tool registered")
	}
	if row, ok := rows["anon"]; !ok || row["description"] != "Nameless" {
		t.Errorf("anon = %v; want the filename-derived name with its --ext-info description", row)
	}
}

// TestInvalidParametersTool_Rejected: selecting a skipped plugin by
// name fails as an unknown tool, with the reason on stderr.
func TestInvalidParametersTool_Rejected(t *testing.T) {
	env := newToolTestEnv(t)
	env.addToolScript(t, "bad",
		`{"name":"bad","version":"0.1.0","description":"d","parameters":{"type":"string"}}`)

	_, stderr, _, err := runFooArgs(t, env, "--offline", "--dry-run", "-T", "bad", "hi")
	if err == nil || !strings.Contains(err.Error(), `unknown tool "bad"`) {
		t.Fatalf("err = %v; want unknown tool \"bad\"", err)
	}
	if !strings.Contains(stderr, "warning") || !strings.Contains(stderr, "parameters") {
		t.Errorf("stderr %q must explain why bad was skipped", stderr)
	}
}

// argsClient is foo's injectable ToolClient: it calls weather once
// with fixed arguments, then returns a text answer.
type argsClient struct {
	args  string
	defs  []llm.ToolDef
	calls int
}

func (c *argsClient) CallWithTools(_ context.Context, _ []llm.Message, defs []llm.ToolDef) (llm.ToolResponse, error) {
	c.calls++
	if c.calls == 1 {
		c.defs = defs
		return llm.ToolResponse{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "weather", Arguments: json.RawMessage(c.args)}}}, nil
	}
	return llm.ToolResponse{Content: "done"}, nil
}

// TestSelectedTool_ArgumentsReachPlugin drives foo's own -T registry
// through the dispatcher: the model is offered the plugin's declared
// schema, and the arguments it sends arrive on the plugin's stdin.
func TestSelectedTool_ArgumentsReachPlugin(t *testing.T) {
	env := newToolTestEnv(t)
	env.addToolScript(t, "weather", weatherToolInfo)

	var warn bytes.Buffer
	reg, err := buildRegistry([]string{"weather"}, &warn)
	if err != nil {
		t.Fatalf("buildRegistry: %v", err)
	}
	client := &argsClient{args: `{"city":"Toronto","days":3}`}
	if _, err := tool.NewDispatcher(client, reg, tool.DispatchConfig{}).Run(context.Background(), "pack for Toronto"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	if len(client.defs) != 1 {
		t.Fatalf("model offered %d tools; want 1", len(client.defs))
	}
	var schema struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(client.defs[0].Parameters, &schema); err != nil {
		t.Fatalf("offered schema is not JSON: %v", err)
	}
	if schema.Type != "object" || schema.Properties["city"] == nil || schema.Properties["days"] == nil {
		t.Errorf("offered schema %s; want the declared city/days schema", client.defs[0].Parameters)
	}

	raw, err := os.ReadFile(filepath.Join(filepath.Dir(env.demoPath), "weather.stdin"))
	if err != nil {
		t.Fatalf("plugin never ran: %v", err)
	}
	var got struct {
		Name      string `json:"name"`
		Arguments struct {
			City string `json:"city"`
			Days int    `json:"days"`
		} `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("plugin stdin is not JSON (%v): %s", err, raw)
	}
	if got.Name != "weather" || got.Arguments.City != "Toronto" || got.Arguments.Days != 3 {
		t.Errorf("plugin stdin = %s; want name weather, city Toronto, days 3", raw)
	}
	if warn.Len() != 0 {
		t.Errorf("unexpected warnings: %q", warn.String())
	}
}

// TestInvalidParametersTool_QuietWhenNotSelected: a broken plugin the
// user did not ask for must not add a warning to every prompt run.
func TestInvalidParametersTool_QuietWhenNotSelected(t *testing.T) {
	env := newToolTestEnv(t)
	env.addToolScript(t, "bad",
		`{"name":"bad","version":"0.1.0","description":"d","parameters":{"type":"string"}}`)

	stdout, stderr, _, err := runFooArgs(t, env, "--offline", "--dry-run", "-T", "demo", "hi")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(stdout, "hi") {
		t.Errorf("dry-run stdout %q missing the prompt", stdout)
	}
	if strings.Contains(stderr, "bad") {
		t.Errorf("stderr %q warns about a plugin -T did not select", stderr)
	}
}
