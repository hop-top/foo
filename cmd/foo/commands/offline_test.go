package commands

import (
	"errors"
	"strings"
	"testing"

	"hop.top/kit/go/console/output"
	"hop.top/kit/go/core/netpolicy"

	"hop.top/foo/internal/exitcode"
)

// remoteBaseURL names an endpoint that is not loopback. The .invalid
// TLD never resolves (RFC 2606), so a run that is not refused fails on
// DNS instead of reaching anything, and the test tells the two apart.
const remoteBaseURL = "http://model.invalid/v1"

// --offline refuses a remote model provider on every path that calls
// one: a plain prompt (streamed or not) and a -T run, with the flag
// before or after the prompt. The refusal is kit's network guard,
// surfaced as foo's OFFLINE envelope with the unauthorized exit code.
func TestOffline_RefusesRemoteProvider(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"prompt, flag first", []string{"--offline", "-m", "gpt-4o", "--no-stream", "hi"}},
		{"prompt, flag last", []string{"-m", "gpt-4o", "--no-stream", "hi", "--offline"}},
		{"streamed prompt", []string{"--offline", "-m", "gpt-4o", "hi"}},
		{"tool run, flag first", []string{"--offline", "-m", "gpt-4o", "-T", "foo_time", "hi"}},
		{"tool run, flag last", []string{"-m", "gpt-4o", "-T", "foo_time", "hi", "--offline"}},
		{"anthropic", []string{"--offline", "-m", "claude-sonnet-4-5", "--no-stream", "hi"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newToolTestEnv(t)
			t.Setenv("OPENAI_API_KEY", "sk-test")
			t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
			t.Setenv("LLM_BASE_URL", remoteBaseURL)

			_, _, _, err := runFooArgs(t, env, tc.args...)
			if err == nil {
				t.Fatal("run succeeded; want the --offline refusal")
			}
			if !errors.Is(err, netpolicy.ErrOffline) {
				t.Fatalf("err = %v; want kit's offline refusal (the request left the guard)", err)
			}
			var ce interface{ AsCLIError() *output.Error }
			if !errors.As(err, &ce) {
				t.Fatalf("err %v carries no CLI envelope", err)
			}
			e := ce.AsCLIError()
			if e.Code != "OFFLINE" || e.ExitCode != exitcode.Offline {
				t.Errorf("envelope = %s/%d; want OFFLINE/%d", e.Code, e.ExitCode, exitcode.Offline)
			}
			if !strings.Contains(e.Message, "model.invalid") || !strings.Contains(e.Message, "--offline") {
				t.Errorf("message %q should name the endpoint and --offline", e.Message)
			}
		})
	}
}

// A model on loopback is local: --offline lets the call through, by
// address or by the name localhost, in either flag position.
func TestOffline_AllowsLoopbackProvider(t *testing.T) {
	for _, tc := range []struct {
		name      string
		localhost bool
		args      []string
	}{
		{"prompt, flag first", false, []string{"--offline", "-m", "gpt-4o", "--no-stream", "hi"}},
		{"prompt, flag last", false, []string{"-m", "gpt-4o", "--no-stream", "hi", "--offline"}},
		{"tool run", false, []string{"-m", "gpt-4o", "-T", "foo_time", "hi", "--offline"}},
		{"localhost name", true, []string{"--offline", "-m", "gpt-4o", "--no-stream", "hi"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newToolTestEnv(t)
			srv, hits := countingModelServer(t)
			base := srv.URL
			if tc.localhost {
				base = strings.Replace(base, "127.0.0.1", "localhost", 1)
			}
			t.Setenv("OPENAI_API_KEY", "sk-test")
			t.Setenv("LLM_BASE_URL", base+"/v1")

			stdout, _, _, err := runFooArgs(t, env, tc.args...)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if !strings.Contains(stdout, "hello from stub") || hits.Load() == 0 {
				t.Errorf("stdout %q, %d hits; want the loopback model's reply", stdout, hits.Load())
			}
		})
	}
}
