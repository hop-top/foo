package tool_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"hop.top/foo/internal/llm"
	"hop.top/foo/internal/tool"
)

const wireSystem = "Reply with the echoed word only."

// wireProvider is one provider's wire shape for a two-round tool run:
// the replies a stub sends, and where the system prompt must land.
type wireProvider struct {
	name string
	// model is the URI foo resolves; {{base}} is the stub's URL.
	model string
	// replies are the stub's answers, in order: a call, then text.
	replies [2]string
	// check asserts one request body carries the system prompt in the
	// provider's system slot, once, and the user turn as the prompt.
	check func(t *testing.T, round int, req map[string]any)
}

var wireProviders = []wireProvider{
	{
		// Also the wire for OpenRouter and Ollama's /v1: both reach
		// kit's openai adapter.
		name:  "openai",
		model: "openai://gpt-4.1-nano?api_key=k&base_url={{base}}/v1",
		replies: [2]string{
			`{"id":"r1","object":"chat.completion","created":0,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":null,` +
				`"tool_calls":[{"id":"call_1","type":"function","function":{"name":"echo","arguments":"{\"text\":\"pelican\"}"}}]},"finish_reason":"tool_calls"}]}`,
			`{"id":"r2","object":"chat.completion","created":0,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"pelican"},"finish_reason":"stop"}]}`,
		},
		check: func(t *testing.T, round int, req map[string]any) {
			msgs := list(req["messages"])
			if len(msgs) < 2 {
				t.Errorf("round %d: want system and user messages at least:\n%s", round, dump(req))
				return
			}
			systems := 0
			for _, m := range msgs {
				if obj(m)["role"] == "system" {
					systems++
				}
			}
			if systems != 1 || obj(msgs[0])["role"] != "system" || obj(msgs[0])["content"] != wireSystem {
				t.Errorf("round %d: want one system message, first, holding the system prompt:\n%s", round, dump(req))
			}
			if u := obj(msgs[1]); u["role"] != "user" || u["content"] != recordedPrompt {
				t.Errorf("round %d: want the user message to be the prompt alone:\n%s", round, dump(req))
			}
			notElsewhere(t, round, msgs[1:])
		},
	},
	{
		name:  "anthropic",
		model: "anthropic://claude-haiku-4-5-20251001?api_key=k&base_url={{base}}",
		replies: [2]string{
			`{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"tool_use","id":"toolu_1","name":"echo","input":{"text":"pelican"}}],` +
				`"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`,
			`{"id":"msg_2","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"pelican"}],` +
				`"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
		},
		check: func(t *testing.T, round int, req map[string]any) {
			sys := list(req["system"])
			if len(sys) != 1 || obj(sys[0])["text"] != wireSystem {
				t.Errorf("round %d: want the system prompt as the top-level system block:\n%s", round, dump(req))
			}
			msgs := list(req["messages"])
			if len(msgs) == 0 {
				t.Errorf("round %d: no messages:\n%s", round, dump(req))
				return
			}
			for _, m := range msgs {
				if obj(m)["role"] == "system" {
					t.Errorf("round %d: anthropic messages carry a system role:\n%s", round, dump(req))
				}
			}
			first := obj(msgs[0])
			blocks := list(first["content"])
			if first["role"] != "user" || len(blocks) != 1 || obj(blocks[0])["text"] != recordedPrompt {
				t.Errorf("round %d: want the user message to be the prompt alone:\n%s", round, dump(req))
			}
			notElsewhere(t, round, msgs)
		},
	},
	{
		name:  "gemini",
		model: "gemini://gemini-3.5-flash-lite?api_key=k&base_url={{base}}",
		replies: [2]string{
			`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"echo","args":{"text":"pelican"}}}]},"finishReason":"STOP"}]}`,
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"pelican"}]},"finishReason":"STOP"}]}`,
		},
		check: func(t *testing.T, round int, req map[string]any) {
			parts := list(obj(req["systemInstruction"])["parts"])
			if len(parts) != 1 || obj(parts[0])["text"] != wireSystem {
				t.Errorf("round %d: want the system prompt as systemInstruction:\n%s", round, dump(req))
			}
			contents := list(req["contents"])
			if len(contents) == 0 {
				t.Errorf("round %d: no contents:\n%s", round, dump(req))
				return
			}
			first := obj(contents[0])
			fp := list(first["parts"])
			if first["role"] != "user" || len(fp) != 1 || obj(fp[0])["text"] != recordedPrompt {
				t.Errorf("round %d: want the user content to be the prompt alone:\n%s", round, dump(req))
			}
			notElsewhere(t, round, contents)
		},
	},
}

// notElsewhere fails when any conversation entry repeats the system
// prompt: it belongs in the system slot only.
func notElsewhere(t *testing.T, round int, entries []any) {
	t.Helper()
	for i, e := range entries {
		if strings.Contains(dump(e), wireSystem) {
			t.Errorf("round %d: conversation entry %d repeats the system prompt: %s", round, i, dump(e))
		}
	}
}

// TestDispatch_SystemPromptWire runs a two-round tool loop through
// foo's real client and kit's adapters against a stub of each
// provider, and checks where the system prompt lands on the wire in
// both requests. Replay-independent: no cassette is read.
func TestDispatch_SystemPromptWire(t *testing.T) {
	for _, p := range wireProviders {
		t.Run(p.name, func(t *testing.T) {
			isolateLLMConfig(t)
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
				n := len(reqs)
				reqs = append(reqs, body)
				mu.Unlock()
				if n > 1 {
					http.Error(w, "unexpected extra round", http.StatusTeapot)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, p.replies[n])
			}))
			t.Cleanup(srv.Close)

			client, err := llm.NewClient(context.Background(), llm.ClientOpts{
				Model: strings.ReplaceAll(p.model, "{{base}}", srv.URL),
			})
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			reg := tool.NewRegistry()
			if err := reg.Register(echoTool{}); err != nil {
				t.Fatal(err)
			}
			answer, err := tool.NewDispatcher(client, reg, tool.DispatchConfig{System: wireSystem}).
				Run(context.Background(), recordedPrompt)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if !strings.Contains(answer, echoWord) {
				t.Errorf("answer %q", answer)
			}
			if len(reqs) != 2 {
				t.Fatalf("model called %d times, want 2", len(reqs))
			}
			for i, req := range reqs {
				p.check(t, i+1, req)
			}
		})
	}
}
