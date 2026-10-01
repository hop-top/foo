package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const testSystem = "Answer in one word."

func TestMessages(t *testing.T) {
	got := Messages(testSystem, "hi")
	if len(got) != 2 || got[0].Role != "system" || got[0].Content != testSystem ||
		got[1].Role != "user" || got[1].Content != "hi" {
		t.Errorf("Messages(system, prompt) = %+v; want system then user", got)
	}
	got = Messages("", "hi")
	if len(got) != 1 || got[0].Role != "user" || got[0].Content != "hi" {
		t.Errorf("Messages(\"\", prompt) = %+v; want the user message only", got)
	}
}

// recordBodies stands in for a provider: it records every request body
// and answers with reply.
func recordBodies(t *testing.T, reply string) (*httptest.Server, func() []map[string]any) {
	t.Helper()
	var (
		mu   sync.Mutex
		reqs []map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode %s: %v", raw, err)
		}
		mu.Lock()
		reqs = append(reqs, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), reqs...)
	}
}

// afterSystem is an OpenAI- or Ollama-shaped conversation without its
// leading system message, when it has one.
func afterSystem(v any) []any {
	msgs, _ := v.([]any)
	if len(msgs) > 0 {
		if m, _ := msgs[0].(map[string]any); m["role"] == "system" {
			return msgs[1:]
		}
	}
	return msgs
}

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// A system prompt sent through Complete lands in each provider's own
// system slot, once, and the user turn is the prompt alone.
func TestComplete_SystemPromptWire(t *testing.T) {
	t.Setenv("FOO_MODEL", "")
	t.Setenv("LLM_FALLBACK", "")
	for _, tc := range []struct {
		name  string
		model string
		reply string
		// system pulls the system slot's text out of a request.
		system func(map[string]any) string
		// turns are the conversation entries (system slot excluded
		// unless the provider keeps it there).
		turns func(map[string]any) []any
	}{
		{
			name:  "openai",
			model: "openai://m?api_key=k&base_url={{base}}/v1",
			reply: `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
			system: func(b map[string]any) string {
				msgs, _ := b["messages"].([]any)
				if len(msgs) == 0 {
					return ""
				}
				if m, _ := msgs[0].(map[string]any); m["role"] == "system" {
					c, _ := m["content"].(string)
					return c
				}
				return ""
			},
			turns: func(b map[string]any) []any { return afterSystem(b["messages"]) },
		},
		{
			name:  "anthropic",
			model: "anthropic://m?api_key=k&base_url={{base}}",
			reply: `{"id":"msg","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
			system: func(b map[string]any) string {
				blocks, _ := b["system"].([]any)
				if len(blocks) != 1 {
					return ""
				}
				text, _ := blocks[0].(map[string]any)["text"].(string)
				return text
			},
			turns: func(b map[string]any) []any { msgs, _ := b["messages"].([]any); return msgs },
		},
		{
			name:  "gemini",
			model: "gemini://m?api_key=k&base_url={{base}}",
			reply: `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`,
			system: func(b map[string]any) string {
				si, _ := b["systemInstruction"].(map[string]any)
				parts, _ := si["parts"].([]any)
				if len(parts) != 1 {
					return ""
				}
				text, _ := parts[0].(map[string]any)["text"].(string)
				return text
			},
			turns: func(b map[string]any) []any { c, _ := b["contents"].([]any); return c },
		},
		{
			name:  "ollama",
			model: "ollama://m?base_url={{base}}",
			reply: `{"model":"m","message":{"role":"assistant","content":"ok"},"done":true}`,
			system: func(b map[string]any) string {
				msgs, _ := b["messages"].([]any)
				if len(msgs) == 0 {
					return ""
				}
				if m, _ := msgs[0].(map[string]any); m["role"] == "system" {
					c, _ := m["content"].(string)
					return c
				}
				return ""
			},
			turns: func(b map[string]any) []any { return afterSystem(b["messages"]) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, bodies := recordBodies(t, tc.reply)
			client, err := NewClient(context.Background(), ClientOpts{
				Model: strings.ReplaceAll(tc.model, "{{base}}", srv.URL),
			})
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			if _, err := client.Complete(context.Background(), Messages(testSystem, "hi")); err != nil {
				t.Fatalf("Complete: %v", err)
			}
			reqs := bodies()
			if len(reqs) != 1 {
				t.Fatalf("%d requests, want 1", len(reqs))
			}
			req := reqs[0]
			if got := tc.system(req); got != testSystem {
				t.Errorf("system slot %q, want %q:\n%s", got, testSystem, jsonOf(req))
			}
			turns := tc.turns(req)
			if len(turns) != 1 || !strings.Contains(jsonOf(turns[0]), `"hi"`) {
				t.Errorf("want one user turn holding the prompt:\n%s", jsonOf(req))
			}
			for _, turn := range turns {
				if strings.Contains(jsonOf(turn), testSystem) {
					t.Errorf("a conversation turn repeats the system prompt:\n%s", jsonOf(req))
				}
			}
		})
	}
}

// Stream sends the same messages, and so does its non-streaming
// fallback: the stub refuses streaming, and the retry must not lose
// the system message.
func TestStream_FallbackKeepsSystemPrompt(t *testing.T) {
	t.Setenv("FOO_MODEL", "")
	t.Setenv("LLM_FALLBACK", "")
	var (
		mu   sync.Mutex
		reqs []map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode %s: %v", raw, err)
		}
		mu.Lock()
		reqs = append(reqs, body)
		mu.Unlock()
		if body["stream"] == true {
			http.Error(w, `{"error":{"message":"streaming not supported"}}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(srv.Close)
	client, err := NewClient(context.Background(), ClientOpts{
		Model: "openai://m?api_key=k&base_url=" + srv.URL + "/v1",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	var out strings.Builder
	if err := client.Stream(context.Background(), &out, Messages(testSystem, "hi")); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if out.String() != "ok" {
		t.Errorf("output %q, want the fallback reply", out.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reqs) != 2 || reqs[0]["stream"] != true || reqs[1]["stream"] == true {
		t.Fatalf("want a refused stream request then a plain one; got %d requests: %s", len(reqs), jsonOf(reqs))
	}
	for i, req := range reqs {
		msgs, _ := req["messages"].([]any)
		if len(msgs) != 2 || jsonOf(msgs[0]) != `{"content":"`+testSystem+`","role":"system"}` ||
			jsonOf(msgs[1]) != `{"content":"hi","role":"user"}` {
			t.Errorf("request %d messages %s; want system then user", i+1, jsonOf(msgs))
		}
	}
}
