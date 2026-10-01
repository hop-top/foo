package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const terseSystem = "Answer in one word. Never explain."

// chatStub is an OpenAI-compatible endpoint that records every request
// body. A request offering tools gets one call to the demo tool until a
// tool result comes back; a streaming request gets SSE; anything else a
// plain reply.
type chatStub struct {
	mu   sync.Mutex
	reqs []map[string]any
}

func newChatStub(t *testing.T) (*chatStub, *httptest.Server) {
	t.Helper()
	s := &chatStub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode request %q: %v", raw, err)
		}
		s.mu.Lock()
		s.reqs = append(s.reqs, body)
		s.mu.Unlock()

		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w,
				`data: {"id":"c","object":"chat.completion.chunk","created":0,"model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"hello from stub"},"finish_reason":null}]}`+"\n\n"+
					`data: {"id":"c","object":"chat.completion.chunk","created":0,"model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n"+
					"data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, tools := body["tools"]; tools && !hasRole(body, "tool") {
			_, _ = io.WriteString(w, `{"id":"c1","object":"chat.completion","created":0,"model":"gpt-4o",`+
				`"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[`+
				`{"id":"call_1","type":"function","function":{"name":"demo","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"c2","object":"chat.completion","created":0,"model":"gpt-4o",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"hello from stub"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(srv.Close)
	return s, srv
}

func (s *chatStub) requests() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.reqs...)
}

func hasRole(body map[string]any, role string) bool {
	for _, m := range messagesOf(body) {
		if m["role"] == role {
			return true
		}
	}
	return false
}

func messagesOf(body map[string]any) []map[string]any {
	list, _ := body["messages"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, m := range list {
		if mm, ok := m.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out
}

func rolesOf(body map[string]any) []string {
	var roles []string
	for _, m := range messagesOf(body) {
		roles = append(roles, m["role"].(string))
	}
	return roles
}

// contentText flattens a message's content, string or parts.
func contentText(m map[string]any) string {
	switch c := m["content"].(type) {
	case string:
		return c
	case []any:
		var b strings.Builder
		for _, p := range c {
			if pm, ok := p.(map[string]any); ok {
				if s, ok := pm["text"].(string); ok {
					b.WriteString(s)
				}
			}
		}
		return b.String()
	}
	return ""
}

// A pattern's system prompt reaches the model as one system message,
// first, on every request — tool rounds included — and the user
// message is the prompt alone. Every run path builds the same shape:
// -T dispatch, --no-stream and streaming.
func TestRun_SystemPromptSentWithSystemRole(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		// rounds is how many model requests the run makes.
		rounds int
		// last is the role sequence of the final request.
		last []string
	}{
		{"tool dispatch", []string{"-T", "demo"}, 2, []string{"system", "user", "assistant", "tool"}},
		{"no stream", []string{"--no-stream"}, 1, []string{"system", "user"}},
		{"stream", nil, 1, []string{"system", "user"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newToolTestEnv(t)
			patterns := filepath.Join(t.TempDir(), "patterns")
			if err := os.MkdirAll(filepath.Join(patterns, "terse"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(patterns, "terse", "system.md"), []byte(terseSystem+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("FOO_PATTERNS_PATH", patterns)
			stub, srv := newChatStub(t)
			t.Setenv("OPENAI_API_KEY", "sk-test")
			t.Setenv("LLM_BASE_URL", srv.URL+"/v1")

			args := append([]string{"--offline", "-m", "gpt-4o", "-p", "terse"}, tc.args...)
			stdout, stderr, _, err := runFooArgs(t, env, append(args, "hi")...)
			if err != nil {
				t.Fatalf("execute: %v\nstderr: %s", err, stderr)
			}
			if !strings.Contains(stdout, "hello from stub") {
				t.Errorf("stdout %q missing the model reply", stdout)
			}

			reqs := stub.requests()
			if len(reqs) != tc.rounds {
				t.Fatalf("model called %d times, want %d", len(reqs), tc.rounds)
			}
			for i, req := range reqs {
				msgs := messagesOf(req)
				if len(msgs) < 2 {
					t.Errorf("request %d: want system and user messages at least; got %v", i+1, msgs)
					continue
				}
				systems := 0
				for j, m := range msgs {
					if m["role"] == "system" {
						systems++
						continue
					}
					if strings.Contains(contentText(m), terseSystem) {
						t.Errorf("request %d message %d (%s) carries the system prompt: %q", i+1, j, m["role"], contentText(m))
					}
				}
				if systems != 1 || msgs[0]["role"] != "system" || contentText(msgs[0]) != terseSystem {
					t.Errorf("request %d: want exactly one system message, first, holding the pattern; roles %v, first %v", i+1, rolesOf(req), msgs[0])
				}
				if len(msgs) < 2 || msgs[1]["role"] != "user" || contentText(msgs[1]) != "hi" {
					t.Errorf("request %d: want the user message to be the prompt alone; got %v", i+1, msgs)
				}
			}
			if got := rolesOf(reqs[len(reqs)-1]); strings.Join(got, ",") != strings.Join(tc.last, ",") {
				t.Errorf("final request roles %v, want %v", got, tc.last)
			}
		})
	}
}

// Without a system prompt nothing changes: the conversation opens with
// the user message, the shape the recorded cassettes hold.
func TestRun_NoSystemPromptSendsNoSystemMessage(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"tool dispatch", []string{"-T", "demo"}},
		{"no stream", []string{"--no-stream"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newToolTestEnv(t)
			stub, srv := newChatStub(t)
			t.Setenv("OPENAI_API_KEY", "sk-test")
			t.Setenv("LLM_BASE_URL", srv.URL+"/v1")

			args := append([]string{"--offline", "-m", "gpt-4o"}, tc.args...)
			if _, stderr, _, err := runFooArgs(t, env, append(args, "hi")...); err != nil {
				t.Fatalf("execute: %v\nstderr: %s", err, stderr)
			}
			for i, req := range stub.requests() {
				msgs := messagesOf(req)
				if hasRole(req, "system") {
					t.Errorf("request %d has a system message with no system prompt: %v", i+1, rolesOf(req))
				}
				if len(msgs) == 0 || msgs[0]["role"] != "user" || contentText(msgs[0]) != "hi" {
					t.Errorf("request %d: want the prompt as the first message; got %v", i+1, msgs)
				}
			}
		})
	}
}
