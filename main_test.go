package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildFoo compiles this module's foo binary once per test.
func buildFoo(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "foo")
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	cmd := exec.Command(gobin, "build", "-buildvcs=false", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// isolatedEnv is a throwaway HOME/XDG tree, a minimal PATH and no
// provider credentials.
func isolatedEnv(t *testing.T, pathDir string) []string {
	t.Helper()
	root := t.TempDir()
	env := []string{"PATH=" + pathDir + ":/usr/bin:/bin", "NO_COLOR=1"}
	for k, sub := range map[string]string{
		"HOME": "home", "XDG_CONFIG_HOME": "config", "XDG_DATA_HOME": "data",
		"XDG_CACHE_HOME": "cache", "XDG_STATE_HOME": "state",
	} {
		dir := filepath.Join(root, sub)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		env = append(env, k+"="+dir)
	}
	return env
}

func run(t *testing.T, env []string, stdin string, bin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run %s: %v", bin, err)
	}
	return out.String(), errb.String(), code
}

// TestMultiCall_LinkDispatchesOnArgv0 drives foo through a
// foo-tool-wc symlink, the way another host would: --ext-info returns
// the spec payload, and a request gets the protocol envelope — here the
// denial for a missing scope.yaml, which grants no path.
func TestMultiCall_LinkDispatchesOnArgv0(t *testing.T) {
	foo := buildFoo(t)
	linkDir := t.TempDir()
	link := filepath.Join(linkDir, "foo-tool-wc")
	if err := os.Symlink(foo, link); err != nil {
		t.Fatal(err)
	}
	env := isolatedEnv(t, linkDir)

	out, stderr, code := run(t, env, "", link, "--ext-info")
	if code != 0 {
		t.Fatalf("--ext-info exit %d: %s", code, stderr)
	}
	var info struct {
		Name       string          `json:"name"`
		Version    string          `json:"version"`
		Parameters json.RawMessage `json:"parameters"`
		FooTool    struct {
			SideEffect string                     `json:"side_effect"`
			Paths      map[string]json.RawMessage `json:"paths"`
		} `json:"foo_tool"`
	}
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		t.Fatalf("--ext-info is not JSON (%v): %q", err, out)
	}
	if info.Name != "wc" || info.Version != "dev" || info.FooTool.SideEffect != "read" ||
		info.FooTool.Paths["path"] == nil || !strings.Contains(string(info.Parameters), `"path"`) {
		t.Errorf("--ext-info = %s", out)
	}

	target := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(target, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, stderr, code = run(t, env, `{"name":"wc","arguments":{"path":["`+target+`"]}}`, link)
	if code != 0 {
		t.Fatalf("request exit %d: %s", code, stderr)
	}
	var resp struct {
		Error  string `json:"error"`
		Detail struct {
			Kind string `json:"kind"`
		} `json:"error_detail"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("response is not JSON (%v): %q", err, out)
	}
	if resp.Result != nil || resp.Detail.Kind != "denied" || !strings.Contains(resp.Error, "scope.yaml does not exist") {
		t.Errorf("response = %s; want the no-scope denial", out)
	}

	// A link naming no spec is not foo's CLI either.
	nope := filepath.Join(linkDir, "foo-tool-nope")
	if err := os.Symlink(foo, nope); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := run(t, env, "", nope, "--ext-info"); code != 3 || !strings.Contains(stderr, "nope") {
		t.Errorf("unknown shim: exit %d stderr %q; want 3 naming it", code, stderr)
	}

	// Plain foo is untouched by the argv[0] check.
	if out, _, code := run(t, env, "", foo, "--version"); code != 0 || !strings.Contains(out, "foo v") {
		t.Errorf("foo --version: exit %d %q", code, out)
	}
}

// --offline refuses a remote model provider with exit 10 and the
// OFFLINE code, whichever side of the prompt the flag sits on. The
// .invalid endpoint never resolves, so nothing leaves the machine even
// if the refusal regresses.
func TestOffline_RemoteProviderExitCode(t *testing.T) {
	foo := buildFoo(t)
	env := append(isolatedEnv(t, t.TempDir()),
		"OPENAI_API_KEY=sk-test", "LLM_BASE_URL=http://model.invalid/v1")
	for _, args := range [][]string{
		{"--offline", "-m", "gpt-4o", "--no-stream", "hi"},
		{"-m", "gpt-4o", "-T", "foo_time", "hi", "--offline"},
	} {
		_, stderr, code := run(t, env, "", foo, args...)
		if code != 10 || !strings.Contains(stderr, "OFFLINE") || !strings.Contains(stderr, "model.invalid") {
			t.Errorf("foo %v: exit %d, stderr %q; want 10 with the OFFLINE refusal naming the endpoint", args, code, stderr)
		}
	}
}
