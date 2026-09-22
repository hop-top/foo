package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
	"hop.top/kit/go/console/output"
)

// errProviderRefused stands in for a provider refusing an oversized
// request, the failure the no-truncation decision deliberately lets
// through to the user.
var errProviderRefused = errors.New("maximum context length is 128000 tokens")

// asExitError is errors.As specialised to the sidecar's envelope type.
func asExitError(err error, target **exitError) bool { return errors.As(err, target) }

// clearPromptEnv removes every prompt/model env name the resolvers read,
// so a developer's own shell settings cannot steer a test.
func clearPromptEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, env := range []string{"FOO_YOUTUBE_PROMPT", "FOO_PROMPT", "FOO_YOUTUBE_MODEL", "FOO_MODEL"} {
		t.Setenv(env, "")
	}
}

// swapAnswer replaces the model-call seam for the test's duration.
func swapAnswer(t *testing.T, fn func(context.Context, string, string) (string, error)) {
	t.Helper()
	prev := answerFunc
	answerFunc = fn
	t.Cleanup(func() { answerFunc = prev })
}

// promptCmd builds the minimal cobra command run() needs: a context and
// somewhere to write stdout.
func promptCmd(ctx context.Context, out io.Writer) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	return cmd
}

// runCLI drives run() with the given opts and positionals, returning
// stdout. Failures are fatal: every caller expects a successful run.
func runCLI(t *testing.T, opts runOpts, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if err := run(promptCmd(context.Background(), &out), args, opts); err != nil {
		t.Fatalf("run(%q): %v", args, err)
	}
	return out.String()
}

// withFullYTDLP stands the whole extraction path up: the file-writing
// fake yt-dlp from the transcript suite serves the subtitle fetch
// through the REAL exec path (so argv construction and the
// file-not-stdout contract stay under test), while --dump-json is
// answered with canned metadata the fake does not model.
//
// Going through execYTDLP for the transcript matters: a fully stubbed
// runner would let a broken argv pass, and the prompt path's whole
// value depends on a real transcript reaching the model.
func withFullYTDLP(t *testing.T) {
	t.Helper()
	argvFile := filepath.Join(t.TempDir(), "argv.txt")
	withSubtitleYTDLP(t, argvFile, json3Fixture, []string{"en"})

	real := ytRunner
	ytRunner = func(ctx context.Context, args []string) ([]byte, error) {
		for _, a := range args {
			if a == "--dump-json" {
				return []byte(`{"title":"On Writing","channel":"Someone","upload_date":"20240115","duration":4140}`), nil
			}
		}
		return real(ctx, args)
	}
	t.Cleanup(func() { ytRunner = real })
}

// --- prompt resolution -------------------------------------------------------

// TestResolvePrompt_Precedence pins the FOO_YOUTUBE_PROMPT > FOO_PROMPT
// namespace the cache env vars established, and pins the positional
// argument above both.
func TestResolvePrompt_Precedence(t *testing.T) {
	tests := []struct {
		name string
		arg  string
		ext  string
		host string
		want string
	}{
		{"arg wins over both", "typed", "ext", "host", "typed"},
		{"ext wins over host", "", "ext", "host", "ext"},
		{"host used alone", "", "", "host", "host"},
		{"nothing configured", "", "", "", ""},
		{"blank arg falls through", "   ", "", "host", "host"},
		{"blank env ignored", "", "   ", "host", "host"},
		{"arg trimmed", "  typed  ", "", "", "typed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FOO_YOUTUBE_PROMPT", tt.ext)
			t.Setenv("FOO_PROMPT", tt.host)
			if got := resolvePrompt(tt.arg); got != tt.want {
				t.Errorf("resolvePrompt(%q) = %q, want %q", tt.arg, got, tt.want)
			}
		})
	}
}

// TestResolvePrompt_NoBuiltinDefault is the explicit guard on the
// decision that foo never invents a question. A built-in default here
// would make every bare invocation answer something the user did not
// ask, and would break the documented markdown pipe by default.
func TestResolvePrompt_NoBuiltinDefault(t *testing.T) {
	t.Setenv("FOO_YOUTUBE_PROMPT", "")
	t.Setenv("FOO_PROMPT", "")
	if got := resolvePrompt(""); got != "" {
		t.Errorf("bare invocation resolved prompt %q; must be empty so markdown is emitted", got)
	}
}

// --- model resolution --------------------------------------------------------

func TestResolveModel_Precedence(t *testing.T) {
	// Point the host-config lookup at an empty dir so a real
	// ~/.config/foo/config.yaml on the machine cannot sway the test.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	t.Setenv("FOO_YOUTUBE_MODEL", "ext-model")
	t.Setenv("FOO_MODEL", "host-model")
	if got := resolveModel(); got != "ext-model" {
		t.Errorf("FOO_YOUTUBE_MODEL must win, got %q", got)
	}

	t.Setenv("FOO_YOUTUBE_MODEL", "")
	if got := resolveModel(); got != "host-model" {
		t.Errorf("FOO_MODEL must apply when the ext var is unset, got %q", got)
	}

	t.Setenv("FOO_MODEL", "")
	if got := resolveModel(); got != youtubeModelDefault {
		t.Errorf("unset env + no config must fall to the built-in default, got %q", got)
	}
}

func TestResolveModel_HostConfigFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("FOO_YOUTUBE_MODEL", "")
	t.Setenv("FOO_MODEL", "")

	writeFooConfig(t, dir, "accent: \"#E040FB\"\nmodel: gpt-4o-mini\nbudget: cheap\n")

	if got := resolveModel(); got != "gpt-4o-mini" {
		t.Errorf("model from the host config file = %q, want gpt-4o-mini", got)
	}

	// An env var still outranks the file.
	t.Setenv("FOO_MODEL", "env-model")
	if got := resolveModel(); got != "env-model" {
		t.Errorf("FOO_MODEL must outrank the config file, got %q", got)
	}
}

// TestModelFromHostConfig_IgnoresNestedKeys guards the line scan against
// the shape foo's config actually grows into: llm pool and provider
// blocks carry their own indented `model:` keys, and picking one of
// those up would silently answer with a pool candidate instead of the
// user's default.
func TestModelFromHostConfig_IgnoresNestedKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	// The nested keys are spelled WITHOUT a list dash: a "- model:"
	// line is already rejected by the key comparison, so a fixture
	// built only from list items would pass with the indent guard
	// deleted and prove nothing.
	writeFooConfig(t, dir, "providers:\n  openai:\n    model: nested-one\n  ollama:\n    model: nested-two\nmodel: top-level\n")

	if got := modelFromHostConfig(); got != "top-level" {
		t.Errorf("modelFromHostConfig = %q, want top-level (nested keys must be ignored)", got)
	}
}

// TestModelFromHostConfig_NestedOnlyYieldsNothing is the other half of
// the guard: a file whose only `model:` keys are nested must resolve to
// nothing, so the caller falls through to the built-in default rather
// than answering with some provider block's model.
func TestModelFromHostConfig_NestedOnlyYieldsNothing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	writeFooConfig(t, dir, "providers:\n  openai:\n    model: nested-only\n")

	if got := modelFromHostConfig(); got != "" {
		t.Errorf("modelFromHostConfig = %q; a nested-only file must yield nothing", got)
	}
}

func TestModelFromHostConfig_QuotesAndComments(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	writeFooConfig(t, dir, "model: \"gpt-4o\" # the default\n")

	if got := modelFromHostConfig(); got != "gpt-4o" {
		t.Errorf("modelFromHostConfig = %q, want gpt-4o", got)
	}
}

func TestModelFromHostConfig_MissingFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := modelFromHostConfig(); got != "" {
		t.Errorf("absent config must yield %q, got %q", "", got)
	}
}

func writeFooConfig(t *testing.T, xdgHome, body string) {
	t.Helper()
	dir := filepath.Join(xdgHome, "foo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// --- provider URI ------------------------------------------------------------

func TestModelURI_SchemeDerivation(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("ANTHROPIC_API_KEY", "an-test")
	t.Setenv("GOOGLE_API_KEY", "gg-test")
	t.Setenv("LLM_BASE_URL", "")

	tests := []struct {
		model string
		want  string
	}{
		{"gpt-4o", "openai://gpt-4o?api_key=sk-test"},
		{"o3-mini", "openai://o3-mini?api_key=sk-test"},
		{"claude-3-5-sonnet-latest", "anthropic://claude-3-5-sonnet-latest?api_key=an-test"},
		{"gemini-2.0-flash", "google://gemini-2.0-flash?api_key=gg-test"},
		{"llama3", "ollama://llama3"},
		{"router-mf:0.5", "routellm://mf:0.5"},
		// Unknown prefix assumes an OpenAI-compatible endpoint.
		{"qwen3-coder", "openai://qwen3-coder?api_key=sk-test"},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got, err := modelURI(tt.model)
			if err != nil {
				t.Fatalf("modelURI(%q): %v", tt.model, err)
			}
			if got != tt.want {
				t.Errorf("modelURI(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

// TestModelURI_PassesThroughSpelledScheme guards the double-wrap defect:
// re-wrapping an already-qualified URI yields "openai://openai://..."
// which sends the whole URI as the model name and drops the key, and the
// provider then 404s with a misleading "model not available".
func TestModelURI_PassesThroughSpelledScheme(t *testing.T) {
	const uri = "openai://gpt-4o?api_key=abc&base_url=http://127.0.0.1:1/v1"
	got, err := modelURI(uri)
	if err != nil {
		t.Fatalf("modelURI: %v", err)
	}
	if got != uri {
		t.Errorf("modelURI(%q) = %q; a spelled-out scheme must pass through untouched", uri, got)
	}
}

// TestModelURI_BareIDWithBaseURLParamIsNotAURI covers the "://" inside a
// param value: a bare model id may carry ?base_url=http://host/v1, and
// the scheme test must look only at the head.
func TestModelURI_BareIDWithBaseURLParamIsNotAURI(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	got, err := modelURI("qwen3-coder?base_url=http://127.0.0.1:9/v1")
	if err != nil {
		t.Fatalf("modelURI: %v", err)
	}
	const want = "openai://qwen3-coder?base_url=http://127.0.0.1:9/v1&api_key=sk-test"
	if got != want {
		t.Errorf("modelURI = %q, want %q", got, want)
	}
}

// TestModelURI_MissingKeyIsUnauthorized proves the precheck fires with a
// structured envelope rather than letting an unauthenticated request
// reach the provider and come back as an opaque 401.
func TestModelURI_MissingKeyIsUnauthorized(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	_, err := modelURI("gpt-4o")
	if err == nil {
		t.Fatal("expected an error with no API key configured")
	}
	var ee *exitError
	if !asExitError(err, &ee) {
		t.Fatalf("error %T is not an *exitError; exit code would be wrong", err)
	}
	if ee.cli.ExitCode != exitUnauthorized {
		t.Errorf("exit code = %d, want %d", ee.cli.ExitCode, exitUnauthorized)
	}
	if ee.cli.Code != output.CodeUnauthorized {
		t.Errorf("error code = %q, want %q", ee.cli.Code, output.CodeUnauthorized)
	}
	if !strings.Contains(ee.cli.Message, "OPENAI_API_KEY") {
		t.Errorf("message must name the env var to export, got %q", ee.cli.Message)
	}
}

// TestModelURI_LocalProviderNeedsNoKey proves an ollama model resolves
// with no credential at all — a precheck applied to every scheme would
// make local endpoints unusable.
func TestModelURI_LocalProviderNeedsNoKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	if _, err := modelURI("llama3"); err != nil {
		t.Errorf("a local provider must need no API key, got %v", err)
	}
}

// --- prompt assembly ---------------------------------------------------------

// TestBuildPrompt_CarriesWholeDocument is the no-silent-truncation
// guard: every byte of the rendered markdown, metadata header included,
// must reach the model. A trimmed transcript changes the answer without
// saying so.
func TestBuildPrompt_CarriesWholeDocument(t *testing.T) {
	doc := "# Title\n\n## Metadata\n- **Channel:** Someone\n\n## Transcript\n\n" +
		strings.Repeat("sentence of transcript text. ", 4000)

	got := buildPrompt("what are the main claims?", doc)

	if !strings.Contains(got, "what are the main claims?") {
		t.Error("the question is missing from the assembled prompt")
	}
	if !strings.Contains(got, "**Channel:** Someone") {
		t.Error("metadata must accompany the transcript")
	}
	// Count the transcript sentences that survived, not just presence.
	wantN := strings.Count(doc, "sentence of transcript text.")
	if gotN := strings.Count(got, "sentence of transcript text."); gotN != wantN {
		t.Errorf("transcript sentences in prompt = %d, want %d (no truncation)", gotN, wantN)
	}
	if !strings.Contains(got, "<video>") || !strings.Contains(got, "</video>") {
		t.Error("the document must be delimited so the question is not read as part of it")
	}
}

// --- the model call over a real HTTP surface ---------------------------------

// openAIStub is an OpenAI-compatible chat-completions endpoint. Pointing
// the resolved provider at it via base_url exercises the real kit
// adapter, so the request that leaves foo-youtube is observable on the
// wire — which a swapped answerFunc stub can never prove.
type openAIStub struct {
	srv   *httptest.Server
	calls atomic.Int64
	body  atomic.Pointer[string]
	auth  atomic.Pointer[string]
	reply string
}

func newOpenAIStub(t *testing.T, reply string) *openAIStub {
	t.Helper()
	s := &openAIStub{reply: reply}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls.Add(1)
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		s.body.Store(&body)
		auth := r.Header.Get("Authorization")
		s.auth.Store(&auth)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     "chatcmpl-test",
			"object": "chat.completion",
			"model":  "gpt-4o-mini",
			"choices": []any{map[string]any{
				"index":         0,
				"finish_reason": "stop",
				"message":       map[string]any{"role": "assistant", "content": s.reply},
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 3, "total_tokens": 13},
		})
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *openAIStub) lastBody() string {
	if p := s.body.Load(); p != nil {
		return *p
	}
	return ""
}

// TestAnswer_SendsPromptOverTheWire drives the production answer() path
// end to end against the stub endpoint: URI assembly, key precheck,
// kit Resolve and the completion request all run as written.
func TestAnswer_SendsPromptOverTheWire(t *testing.T) {
	stub := newOpenAIStub(t, "Three claims: one, two, three.")
	t.Setenv("OPENAI_API_KEY", "sk-live-test")

	model := "gpt-4o-mini?base_url=" + stub.srv.URL + "/v1"
	prompt := buildPrompt("what are the main claims?", "# Vid\n\n## Transcript\n\nHe said writing is thinking.\n")

	got, err := answer(context.Background(), model, prompt)
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if got != "Three claims: one, two, three." {
		t.Errorf("answer = %q, want the stub's reply", got)
	}
	if n := stub.calls.Load(); n != 1 {
		t.Fatalf("stub received %d request(s), want exactly 1", n)
	}

	body := stub.lastBody()
	// The question AND the transcript must both be on the wire. A
	// request that carried only the question would still return the
	// stub's canned reply, so asserting the reply alone proves nothing.
	if !strings.Contains(body, "what are the main claims?") {
		t.Errorf("outbound request omitted the question:\n%s", body)
	}
	if !strings.Contains(body, "writing is thinking") {
		t.Errorf("outbound request omitted the transcript:\n%s", body)
	}
	if auth := stub.auth.Load(); auth == nil || !strings.Contains(*auth, "sk-live-test") {
		t.Errorf("API key did not reach the provider; Authorization = %v", auth)
	}
}

// TestAnswer_SurfacesProviderError proves an overflow (or any other
// provider refusal) reaches the user instead of being swallowed. This is
// the other half of the no-truncation decision: the model is allowed to
// say the transcript is too long, and foo must say so too.
func TestAnswer_SurfacesProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"maximum context length is 128000 tokens","type":"invalid_request_error"}}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPENAI_API_KEY", "sk-live-test")

	_, err := answer(context.Background(), "gpt-4o-mini?base_url="+srv.URL+"/v1", "hello")
	if err == nil {
		t.Fatal("expected the provider's error to surface")
	}
	if !strings.Contains(err.Error(), "context length") {
		t.Errorf("provider message lost; got %v", err)
	}
}

// --- end to end through run() ------------------------------------------------

// TestRun_PromptPathPrintsAnswerOnly proves stdout carries the answer
// and not the markdown when a prompt is supplied — the whole point of
// the feature is that the user does not have to pipe.
func TestRun_PromptPathPrintsAnswerOnly(t *testing.T) {
	withFullYTDLP(t)
	clearPromptEnv(t)

	var seen struct {
		model  string
		prompt string
	}
	var calls atomic.Int64
	swapAnswer(t, func(_ context.Context, model, prompt string) (string, error) {
		calls.Add(1)
		seen.model, seen.prompt = model, prompt
		return "The main claim is that writing is thinking.", nil
	})
	t.Setenv("FOO_YOUTUBE_MODEL", "gpt-4o-mini")

	out := runCLI(t, runOpts{metadata: true, transcript: true},
		"https://youtu.be/VID123", "what are the main claims?")

	if calls.Load() != 1 {
		t.Fatalf("model called %d time(s), want 1", calls.Load())
	}
	if seen.model != "gpt-4o-mini" {
		t.Errorf("model = %q, want gpt-4o-mini", seen.model)
	}
	if !strings.Contains(seen.prompt, "what are the main claims?") {
		t.Error("the user's question never reached the model")
	}
	if !strings.Contains(seen.prompt, "## Transcript") {
		t.Error("the transcript never reached the model")
	}
	if strings.TrimSpace(out) != "The main claim is that writing is thinking." {
		t.Errorf("stdout = %q; the prompt path must print the answer alone", out)
	}
	if strings.Contains(out, "## Transcript") {
		t.Error("markdown leaked to stdout on the prompt path")
	}
}

// TestRun_BareInvocationStillEmitsMarkdown is the regression guard on
// the documented pipe. If this goes red, every existing
// `foo-youtube <url> | foo '...'` script is broken.
func TestRun_BareInvocationStillEmitsMarkdown(t *testing.T) {
	withFullYTDLP(t)
	clearPromptEnv(t)

	var calls atomic.Int64
	swapAnswer(t, func(context.Context, string, string) (string, error) {
		calls.Add(1)
		return "should never be called", nil
	})

	out := runCLI(t, runOpts{metadata: true, transcript: true}, "https://youtu.be/VID123")

	if calls.Load() != 0 {
		t.Error("a bare invocation must not call a model")
	}
	if !strings.Contains(out, "## Transcript") {
		t.Errorf("bare invocation must emit markdown, got:\n%s", out)
	}
	if !strings.Contains(out, "## Metadata") {
		t.Errorf("bare invocation lost the metadata section, got:\n%s", out)
	}
}

// TestRun_ConfiguredDefaultPromptAnswers proves FOO_YOUTUBE_PROMPT turns
// a bare invocation into an answer — the configured-default half of the
// feature.
func TestRun_ConfiguredDefaultPromptAnswers(t *testing.T) {
	withFullYTDLP(t)
	clearPromptEnv(t)
	t.Setenv("FOO_YOUTUBE_PROMPT", "summarize in one line")

	var seen string
	swapAnswer(t, func(_ context.Context, _, prompt string) (string, error) {
		seen = prompt
		return "A one-line summary.", nil
	})

	out := runCLI(t, runOpts{metadata: true, transcript: true}, "https://youtu.be/VID123")

	if !strings.Contains(seen, "summarize in one line") {
		t.Error("the configured default prompt never reached the model")
	}
	if strings.TrimSpace(out) != "A one-line summary." {
		t.Errorf("stdout = %q, want the answer", out)
	}
}

// TestRun_RawSuppressesConfiguredPrompt is what keeps a machine with
// FOO_YOUTUBE_PROMPT set from breaking every markdown pipeline on it.
func TestRun_RawSuppressesConfiguredPrompt(t *testing.T) {
	withFullYTDLP(t)
	clearPromptEnv(t)
	t.Setenv("FOO_YOUTUBE_PROMPT", "summarize in one line")

	var calls atomic.Int64
	swapAnswer(t, func(context.Context, string, string) (string, error) {
		calls.Add(1)
		return "nope", nil
	})

	out := runCLI(t, runOpts{metadata: true, transcript: true, raw: true}, "https://youtu.be/VID123")

	if calls.Load() != 0 {
		t.Error("--raw must not call a model even with a prompt configured")
	}
	if !strings.Contains(out, "## Transcript") {
		t.Errorf("--raw must emit markdown, got:\n%s", out)
	}
}

// TestRun_PromptWithNoTranscriptIsUsageError: answering a question about
// what a video says, from the metadata header alone, is a wrong answer
// delivered confidently. Refuse instead.
func TestRun_PromptWithNoTranscriptIsUsageError(t *testing.T) {
	clearPromptEnv(t)
	var calls atomic.Int64
	swapAnswer(t, func(context.Context, string, string) (string, error) {
		calls.Add(1)
		return "", nil
	})

	var out bytes.Buffer
	err := run(promptCmd(context.Background(), &out),
		[]string{"https://youtu.be/VID123", "what did they say?"},
		runOpts{metadata: true, transcript: false})

	if err == nil {
		t.Fatal("expected a usage error")
	}
	var ee *exitError
	if !asExitError(err, &ee) || ee.cli.ExitCode != exitUsage {
		t.Errorf("error = %v; want a usage error (exit %d)", err, exitUsage)
	}
	if calls.Load() != 0 {
		t.Error("no model call may happen when the request is refused")
	}
}

// TestRun_ProviderFailureIsNotSilent proves a failed completion aborts
// with an error instead of falling back to printing the markdown, which
// would look like success.
func TestRun_ProviderFailureIsNotSilent(t *testing.T) {
	withFullYTDLP(t)
	clearPromptEnv(t)
	swapAnswer(t, func(context.Context, string, string) (string, error) {
		return "", errProviderRefused
	})

	var out bytes.Buffer
	err := run(promptCmd(context.Background(), &out),
		[]string{"https://youtu.be/VID123", "what did they say?"},
		runOpts{metadata: true, transcript: true})

	if err == nil {
		t.Fatal("a provider failure must not be swallowed")
	}
	if !strings.Contains(err.Error(), "context length") {
		t.Errorf("the provider's message must survive, got %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("nothing may be printed on a failed prompt, got %q", out.String())
	}
}

// --- argument acceptance -----------------------------------------------------

// TestArgs_AcceptsTwoPositionals is the direct regression on the
// reported defect: `foo youtube <id> 'prompt'` used to die with
// "accepts at most 1 arg(s), received 2".
func TestArgs_AcceptsTwoPositionals(t *testing.T) {
	var got []string
	prev := runFunc
	runFunc = func(_ *cobra.Command, args []string, _ runOpts) error {
		got = args
		return nil
	}
	t.Cleanup(func() { runFunc = prev })

	root := newRoot()
	root.Cmd.SetArgs([]string{"dQw4w9WgXcQ", "what are the main claims?"})
	root.Cmd.SetOut(&bytes.Buffer{})
	root.Cmd.SetErr(&bytes.Buffer{})
	if err := root.Cmd.Execute(); err != nil {
		t.Fatalf("two positionals must be accepted: %v", err)
	}
	if len(got) != 2 || got[1] != "what are the main claims?" {
		t.Errorf("args = %q, want the prompt as the second positional", got)
	}
}

// TestArgs_RejectsThreePositionals keeps the widening bounded: an
// unquoted multi-word prompt is a mistake worth naming, not three
// arguments to silently drop.
func TestArgs_RejectsThreePositionals(t *testing.T) {
	prev := runFunc
	runFunc = func(*cobra.Command, []string, runOpts) error { return nil }
	t.Cleanup(func() { runFunc = prev })

	root := newRoot()
	root.Cmd.SetArgs([]string{"dQw4w9WgXcQ", "what", "are"})
	root.Cmd.SetOut(&bytes.Buffer{})
	root.Cmd.SetErr(&bytes.Buffer{})
	if err := root.Cmd.Execute(); err == nil {
		t.Error("three positionals must be rejected so an unquoted prompt is named, not truncated")
	}
}

// TestFlag_RawReachesRunOpts pins the flag wiring through the real
// registration + RunE reconciliation.
func TestFlag_RawReachesRunOpts(t *testing.T) {
	if got := captureRunOpts(t, []string{"--raw", "https://youtu.be/VID123"}); !got.raw {
		t.Error("--raw did not reach runOpts")
	}
	if got := captureRunOpts(t, []string{"https://youtu.be/VID123"}); got.raw {
		t.Error("raw must default to false")
	}
}
