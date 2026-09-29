package shim

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hop.top/foo/internal/tool/gate"
	"hop.top/kit/go/core/scope"
)

func TestWC_CountsThroughCanonicalPath(t *testing.T) {
	dir := tempDir(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "one two\nthree\n")
	// A relative path through the non-canonical temp root (/var on
	// macOS) must still run on the canonical file.
	e := &Engine{Authorizer: &fakeAuth{cwd: dir}, Cwd: dir}

	res := mustCall(t, e, builtinWC(t), `{"path":["a.txt"],"lines":true}`)

	want := filepath.Join(dir, "a.txt")
	if got := res.Argv; len(got) != 4 || got[1] != "-l" || got[2] != "--" || got[3] != want {
		t.Fatalf("argv = %q; want [<wc> -l -- %s]", got, want)
	}
	if !filepath.IsAbs(res.Argv[0]) {
		t.Errorf("argv[0] %q is not the pinned absolute binary", res.Argv[0])
	}
	if !res.OK || res.ExitCode != 0 {
		t.Fatalf("exit %d ok %v stderr %q", res.ExitCode, res.OK, res.Stderr)
	}
	fields := strings.Fields(stdout(res))
	if len(fields) != 2 || fields[0] != "2" || fields[1] != want {
		t.Errorf("stdout = %q; want `2 %s`", stdout(res), want)
	}
	if len(res.Paths) != 1 || res.Paths[0].Given != "a.txt" || res.Paths[0].Resolved != want || res.Paths[0].Op != "read" {
		t.Errorf("paths = %+v", res.Paths)
	}
}

// The model's raw value must never reach argv: only Grant.Canonical.
func TestArgv_UsesGrantNotRawValues(t *testing.T) {
	dir := tempDir(t)
	writeFile(t, filepath.Join(dir, "asked.txt"), "1\n")
	granted := writeFile(t, filepath.Join(dir, "granted.txt"), "1\n2\n3\n")
	auth := &fakeAuth{cwd: dir, rewrite: map[string][]string{"path": {granted}}}
	e := &Engine{Authorizer: auth, Cwd: dir}

	res := mustCall(t, e, builtinWC(t), `{"path":["asked.txt"],"lines":true}`)

	if indexOf(res.Argv, granted) < 0 {
		t.Fatalf("argv %q lacks the granted path %s", res.Argv, granted)
	}
	for _, a := range res.Argv {
		if strings.Contains(a, "asked.txt") {
			t.Fatalf("argv %q carries the raw model value", res.Argv)
		}
	}
	if !strings.Contains(stdout(res), "3 "+granted) {
		t.Errorf("stdout %q: wc did not run on the granted file", stdout(res))
	}
}

func TestArgv_DoubleDashPrecedesEveryPath(t *testing.T) {
	dir := tempDir(t)
	// A file literally named like a flag.
	flagFile := writeFile(t, filepath.Join(dir, "-l"), "a\nb\n")
	e := &Engine{Authorizer: &fakeAuth{cwd: dir}, Cwd: dir}

	res := mustCall(t, e, builtinWC(t), `{"path":["-l"],"words":true}`)

	dd := indexOf(res.Argv, "--")
	pi := indexOf(res.Argv, flagFile)
	if dd < 0 || pi < 0 || dd > pi {
		t.Fatalf("argv %q: want -- before path %s", res.Argv, flagFile)
	}
	if !strings.Contains(stdout(res), "2 "+flagFile) {
		t.Errorf("stdout %q: want word count of the file", stdout(res))
	}
}

func TestValidate_RejectsBeforeAuthorizing(t *testing.T) {
	for _, tc := range []struct{ name, args, param string }{
		{"unknown key", `{"path":["a"],"recursive":true}`, "recursive"},
		{"missing required path", `{"lines":true}`, "path"},
		{"wrong type", `{"path":"a"}`, "path"},
		{"bool as string", `{"path":["a"],"lines":"yes"}`, "lines"},
		{"empty list", `{"path":[]}`, "path"},
		{"NUL in path", `{"path":["a\u0000b"]}`, "path"},
		{"too many paths", `{"path":[` + strings.Repeat(`"a",`, 64) + `"a"]}`, "path"},
		{"not an object", `["a"]`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := &fakeAuth{cwd: "/"}
			e := &Engine{Authorizer: auth}
			_, err := call(t, e, builtinWC(t), tc.args)
			ge := wantKind(t, err, gate.KindInvalidArgs)
			if ge.Param != tc.param {
				t.Errorf("param = %q; want %q (%v)", ge.Param, tc.param, ge)
			}
			if auth.calls != 0 {
				t.Errorf("authorizer called %d times for invalid args", auth.calls)
			}
		})
	}
}

const argsSpec = `
spec: 1
name: argsdemo
description: Fixture for typed argument validation.
command: {bin: [%BIN%]}
side_effect: read
params:
  - {name: path, type: path, description: p, op: [read], default: "."}
  - {name: mode, type: enum, description: m, values: {fast: ["-f"], slow: ["-s"]}}
  - {name: count, type: int, description: c, min: 1, max: 10, argv: ["-n", "{}"]}
  - {name: bytes, type: int, description: b, min: 1, max: 10, argv: ["--bytes={}"]}
  - {name: pattern, type: string, description: s, max_len: 5, required: true, argv: ["-e", "{}"]}
exclusive: [[count, bytes]]
argv: ["{mode}", "{count}", "{bytes}", "{pattern}", "--", "{path}"]
`

func argsFixture(t *testing.T) (*Engine, *Loaded, string) {
	t.Helper()
	dir := tempDir(t)
	bin := writeScript(t, dir, "argsbin", `for a in "$@"; do printf '[%s]' "$a"; done`)
	l := mustSpec(t, strings.ReplaceAll(argsSpec, "%BIN%", bin))
	return &Engine{Authorizer: &fakeAuth{cwd: dir}, Cwd: dir}, l, dir
}

func TestValidate_TypedArguments(t *testing.T) {
	e, l, dir := argsFixture(t)
	for _, tc := range []struct{ name, args, param string }{
		{"enum outside values", `{"pattern":"x","mode":"medium"}`, "mode"},
		{"int below min", `{"pattern":"x","count":0}`, "count"},
		{"int above max", `{"pattern":"x","count":11}`, "count"},
		{"int not integral", `{"pattern":"x","count":1.5}`, "count"},
		{"string too long", `{"pattern":"abcdef"}`, "pattern"},
		{"string newline", `{"pattern":"a\nb"}`, "pattern"},
		{"string NUL", `{"pattern":"a\u0000"}`, "pattern"},
		{"required string missing", `{}`, "pattern"},
		{"exclusive pair", `{"pattern":"x","count":1,"bytes":2}`, "bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := call(t, e, l, tc.args)
			if ge := wantKind(t, err, gate.KindInvalidArgs); ge.Param != tc.param {
				t.Errorf("param = %q; want %q (%v)", ge.Param, tc.param, ge)
			}
		})
	}

	res := mustCall(t, e, l, `{"pattern":"-rf","mode":"slow","count":3}`)
	want := "[-s][-n][3][-e][-rf][--][" + dir + "]"
	if got := stdout(res); got != want {
		t.Errorf("argv echo = %q; want %q (default path \".\" resolved to cwd)", got, want)
	}
	res = mustCall(t, e, l, `{"pattern":"x","bytes":4,"mode":null}`)
	if got := stdout(res); got != "[--bytes=4][-e][x][--]["+dir+"]" {
		t.Errorf("joined fragment argv = %q", got)
	}
}

func TestDeny_IsErrorAndNothingRuns(t *testing.T) {
	dir := tempDir(t)
	marker := filepath.Join(dir, "ran")
	bin := writeScript(t, dir, "touchbin", "echo x > "+marker+"\n")
	l := mustSpec(t, `
spec: 1
name: touchy
description: Fixture that records whether it ran.
command: {bin: [`+bin+`]}
side_effect: read
params: [{name: path, type: path, description: p, op: [read]}]
argv: ["--", "{path}"]
`)
	e := &Engine{Authorizer: &fakeAuth{cwd: dir, deny: map[string]bool{"secret": true}}, Cwd: dir}

	_, err := call(t, e, l, `{"path":"secret"}`)
	ge := wantKind(t, err, gate.KindDenied)
	if ge.Param != "path" || ge.Op != scope.Read {
		t.Errorf("denial = %+v; want param path, op read", ge)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("command ran despite the denial")
	}

	_, err = call(t, &Engine{Authorizer: DenyAll{Message: "path scope not configured yet"}}, l, `{"path":"x"}`)
	if ge := wantKind(t, err, gate.KindDenied); ge.Message != "path scope not configured yet" {
		t.Errorf("DenyAll message = %q", ge.Message)
	}
	_, err = call(t, &Engine{}, l, `{"path":"x"}`)
	wantKind(t, err, gate.KindDenied)
}

func TestNonzeroExit_IsResult(t *testing.T) {
	dir := tempDir(t)
	e := &Engine{Authorizer: &fakeAuth{cwd: dir}, Cwd: dir}
	res, err := call(t, e, builtinWC(t), `{"path":["missing.txt"]}`)
	if err != nil {
		t.Fatalf("nonzero exit returned error %v; want a result", err)
	}
	if res.OK || res.ExitCode == 0 || !strings.Contains(res.Stderr, "missing.txt") {
		t.Errorf("result = %+v; want ok=false, nonzero exit, stderr naming the file", res)
	}
}

const catSpec = `
spec: 1
name: catx
description: Fixture that prints files.
command: {bin: [/bin/cat]}
side_effect: read
timeout: %TIMEOUT%
params: [{name: path, type: path, description: p, op: [read]}]
argv: ["--", "{path}"]
`

func catFixture(t *testing.T, timeout string) (*Engine, *Loaded, string) {
	dir := tempDir(t)
	return &Engine{Authorizer: &fakeAuth{cwd: dir}, Cwd: dir},
		mustSpec(t, strings.ReplaceAll(catSpec, "%TIMEOUT%", timeout)), dir
}

func TestEnvelope_StdoutCapKeepsCounting(t *testing.T) {
	e, l, dir := catFixture(t, "30s")
	big := strings.Repeat("0123456789abcdef", 200<<10/16) // 200 KiB
	writeFile(t, filepath.Join(dir, "big"), big)

	res := mustCall(t, e, l, `{"path":"big"}`)

	if !res.StdoutTruncated || res.StdoutBytes != int64(len(big)) {
		t.Errorf("truncated %v bytes %d; want true, %d", res.StdoutTruncated, res.StdoutBytes, len(big))
	}
	if got := len(stdout(res)); got != DefaultMaxStdout {
		t.Errorf("kept %d bytes; want the %d-byte cap", got, DefaultMaxStdout)
	}
	if !strings.HasPrefix(big, stdout(res)) {
		t.Error("kept output is not the head of the stream")
	}

	e.MaxStdout = 10
	if res := mustCall(t, e, l, `{"path":"big"}`); stdout(res) != big[:10] || res.StdoutBytes != int64(len(big)) {
		t.Errorf("custom cap: kept %q, counted %d", stdout(res), res.StdoutBytes)
	}
}

func TestEnvelope_BinaryStdout(t *testing.T) {
	e, l, dir := catFixture(t, "30s")
	writeFile(t, filepath.Join(dir, "img"), "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	writeFile(t, filepath.Join(dir, "latin1"), "caf\xe9\n")

	for _, name := range []string{"img", "latin1"} {
		res := mustCall(t, e, l, `{"path":"`+name+`"}`)
		if !res.StdoutBinary || res.Stdout != nil || res.StdoutBytes == 0 {
			t.Errorf("%s: binary %v stdout %v bytes %d; want binary, null stdout, size kept", name, res.StdoutBinary, res.Stdout, res.StdoutBytes)
		}
		data, _ := json.Marshal(res)
		if !strings.Contains(string(data), `"stdout":null`) {
			t.Errorf("%s: envelope %s lacks stdout:null", name, data)
		}
	}
}

func TestTimeout_KillsProcessGroup(t *testing.T) {
	dir := tempDir(t)
	bin := writeScript(t, dir, "slow", "sleep 30 &\nsleep 30\n")
	l := mustSpec(t, `
spec: 1
name: slow
description: Fixture that never finishes in time.
command: {bin: [`+bin+`]}
side_effect: read
timeout: 300ms
params: [{name: path, type: path, description: p, op: [read], default: "."}]
argv: ["--", "{path}"]
`)
	e := &Engine{Authorizer: &fakeAuth{cwd: dir}, Cwd: dir}
	start := time.Now()
	_, err := call(t, e, l, `{}`)
	wantKind(t, err, KindTimeout)
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("timeout took %s; the process group was not killed", d)
	}
}

func TestKind_FileRejectsDirectoryAfterGrant(t *testing.T) {
	dir := tempDir(t)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Authorizer: &fakeAuth{cwd: dir}, Cwd: dir}
	_, err := call(t, e, builtinWC(t), `{"path":["sub"]}`)
	if ge := wantKind(t, err, gate.KindInvalidArgs); ge.Path != filepath.Join(dir, "sub") {
		t.Errorf("kind error names %q", ge.Path)
	}
}

func TestToolAdapter(t *testing.T) {
	dir := tempDir(t)
	writeFile(t, filepath.Join(dir, "a"), "x\n")
	tl := NewTool(&Engine{Authorizer: &fakeAuth{cwd: dir}, Cwd: dir}, builtinWC(t))

	var schema struct {
		Type       string                    `json:"type"`
		Properties map[string]map[string]any `json:"properties"`
		Required   []string                  `json:"required"`
		Additional bool                      `json:"additionalProperties"`
	}
	if err := json.Unmarshal(tl.Parameters(), &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Type != "object" || schema.Additional || len(schema.Required) != 1 || schema.Required[0] != "path" {
		t.Errorf("schema = %s", tl.Parameters())
	}
	if p := schema.Properties["path"]; p["type"] != "array" || !strings.Contains(p["description"].(string), dir) {
		t.Errorf("path schema = %v; want array naming cwd %s", p, dir)
	}
	if _, has := schema.Properties["lines"]["default"]; has {
		t.Error("schema carries a default keyword; defaults belong in descriptions")
	}

	out, err := tl.Execute(t.Context(), json.RawMessage(`{"path":["a"]}`))
	if err != nil || !strings.Contains(string(out), `"exit_code":0`) {
		t.Fatalf("Execute = %s, %v", out, err)
	}
	_, err = tl.Execute(t.Context(), json.RawMessage(`{"nope":1}`))
	ce, ok := err.(*CallError)
	if !ok {
		t.Fatalf("Execute error %T; want *CallError", err)
	}
	var msg struct {
		Error ErrorBody `json:"error"`
	}
	if json.Unmarshal(ce.ToolMessage(), &msg) != nil || msg.Error.Kind != "invalid_args" || msg.Error.Param != "nope" {
		t.Errorf("tool message = %s", ce.ToolMessage())
	}
}
