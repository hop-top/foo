package tool_test

import (
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hop.top/foo/internal/llm"
	"hop.top/foo/internal/llmxrr"
	"hop.top/foo/internal/tool"
	xrr "hop.top/xrr"
)

// The recorded tool round: foo's real llm client (kit's adapters) and
// the Dispatcher against responses real providers returned, one xrr
// cassette set per provider under testdata/cassettes/<provider>.
//
// Replay is the default and never dials anything. Re-record one
// provider against the live API with -update; see CONTRIBUTING.md for
// the command and the env each provider needs.
var update = flag.Bool("update", false, "re-record provider cassettes against the live APIs")

// echoTool answers with the text it was given, so a round is
// deterministic.
type echoTool struct{}

func (echoTool) Name() string        { return "echo" }
func (echoTool) Description() string { return "Echo the given text back verbatim." }
func (echoTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"text":{"type":"string","description":"Text to echo"}},"required":["text"]}`)
}

func (echoTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var in struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]string{"echo": in.Text})
}

const (
	recordedPrompt = `Call the echo tool once with the text "pelican". When its result comes back, do not call any tool again: reply with the word the tool echoed, and nothing else.`
	echoWord       = "pelican"
)

// provider is one model provider's recorded round and how its wire
// format links a tool result to its call.
type provider struct {
	name string
	// model is the --model value foo resolves; the recorded model.
	model string
	// keyEnv is the env var foo's key precheck reads (none for Ollama).
	keyEnv string
	// record is the command that records this provider's cassettes.
	record string
	// callID pulls the tool call's link out of the first response.
	callID func(t *testing.T, resp map[string]any) string
	// linked asserts the second request carries the assistant's call
	// and the result linked to it.
	linked func(t *testing.T, req map[string]any, id string)
}

// ollamaBase is the Ollama server recorded against: set
// FOO_RECORD_OLLAMA_BASE_URL to reach one elsewhere. Replay ignores the
// host: cassettes store path and query.
func ollamaBase() string {
	if v := os.Getenv("FOO_RECORD_OLLAMA_BASE_URL"); v != "" {
		return v
	}
	return "http://127.0.0.1:11434/v1"
}

var providers = []provider{
	{
		name:   "openai",
		model:  "gpt-4.1-nano-2025-04-14",
		keyEnv: "OPENAI_API_KEY",
		record: "OPENAI_API_KEY=... go test ./internal/tool -run 'TestRecordedToolRound/openai' -update",
		callID: openAICallID,
		linked: openAILinked,
	},
	{
		// kit's ollama adapter has no ToolCaller; Ollama's
		// OpenAI-compatible /v1 does tools, so it is reached as openai.
		name:   "ollama",
		model:  "openai://ornith:9b?api_key=ollama&base_url=" + ollamaBase(),
		record: "FOO_RECORD_OLLAMA_BASE_URL=http://<ollama-host>:11434/v1 go test ./internal/tool -run 'TestRecordedToolRound/ollama' -update",
		callID: openAICallID,
		linked: openAILinked,
	},
	{
		// OpenRouter speaks OpenAI's wire shape; kit reaches it through
		// the openai adapter. foo adds OPENROUTER_API_KEY to the
		// URI-form model.
		name:   "openrouter",
		model:  "openrouter://openai/gpt-4.1-nano",
		keyEnv: "OPENROUTER_API_KEY",
		record: "OPENROUTER_API_KEY=... go test ./internal/tool -run 'TestRecordedToolRound/openrouter' -update",
		callID: openAICallID,
		linked: openAILinked,
	},
	{
		name:   "anthropic",
		model:  "claude-haiku-4-5-20251001",
		keyEnv: "ANTHROPIC_API_KEY",
		record: "ANTHROPIC_API_KEY=... go test ./internal/tool -run 'TestRecordedToolRound/anthropic' -update",
		callID: func(t *testing.T, resp map[string]any) string {
			for _, b := range list(resp["content"]) {
				if blk := obj(b); blk["type"] == "tool_use" {
					requireEqual(t, "echo", blk["name"], "tool_use name")
					return str(blk["id"])
				}
			}
			t.Fatalf("first response has no tool_use block: %v", resp)
			return ""
		},
		linked: func(t *testing.T, req map[string]any, id string) {
			msgs := list(req["messages"])
			use := indexOf(msgs, func(m map[string]any) bool {
				return m["role"] == "assistant" && hasBlock(m, func(b map[string]any) bool {
					return b["type"] == "tool_use" && b["id"] == id && b["name"] == "echo"
				})
			})
			if use < 0 {
				t.Fatalf("second request has no assistant tool_use %q:\n%s", id, dump(req))
			}
			res := indexOf(msgs[use+1:], func(m map[string]any) bool {
				return m["role"] == "user" && hasBlock(m, func(b map[string]any) bool {
					return b["type"] == "tool_result" && b["tool_use_id"] == id && strings.Contains(dump(b["content"]), echoWord)
				})
			})
			if res < 0 {
				t.Fatalf("second request has no tool_result for tool_use_id %q after the call:\n%s", id, dump(req))
			}
		},
	},
	{
		name:   "gemini",
		model:  "gemini-3.5-flash-lite",
		keyEnv: "GOOGLE_API_KEY",
		record: "GOOGLE_API_KEY=... go test ./internal/tool -run 'TestRecordedToolRound/gemini' -update",
		// Gemini links a functionResponse to its functionCall by name
		// and position, not by an ID on the wire.
		callID: func(t *testing.T, resp map[string]any) string {
			for _, c := range list(resp["candidates"]) {
				for _, p := range list(obj(obj(c)["content"])["parts"]) {
					if fc := obj(obj(p)["functionCall"]); fc != nil {
						requireEqual(t, "echo", fc["name"], "functionCall name")
						return str(fc["name"])
					}
				}
			}
			t.Fatalf("first response has no functionCall part: %v", resp)
			return ""
		},
		linked: func(t *testing.T, req map[string]any, name string) {
			contents := list(req["contents"])
			call := indexOf(contents, func(c map[string]any) bool {
				return c["role"] == "model" && hasPart(c, func(p map[string]any) bool {
					return obj(p["functionCall"])["name"] == name
				})
			})
			if call < 0 {
				t.Fatalf("second request has no model functionCall %q:\n%s", name, dump(req))
			}
			res := indexOf(contents[call+1:], func(c map[string]any) bool {
				return c["role"] == "user" && hasPart(c, func(p map[string]any) bool {
					fr := obj(p["functionResponse"])
					return fr["name"] == name && strings.Contains(dump(fr["response"]), echoWord)
				})
			})
			if res < 0 {
				t.Fatalf("second request has no functionResponse for %q after the call:\n%s", name, dump(req))
			}
		},
	},
}

// TestRecordedToolRound runs a two-round tool loop per provider: the
// model asks for echo, the Dispatcher runs it and sends the result
// linked to the call, the model answers. The second request must carry
// the assistant turn's call and the result linked to it in the
// provider's own wire shape.
func TestRecordedToolRound(t *testing.T) {
	keys := llmxrr.RecordingKeys()
	for _, p := range providers {
		t.Run(p.name, func(t *testing.T) {
			dir := filepath.Join("testdata", "cassettes", p.name)
			mode := xrr.ModeReplay
			if *update {
				mode = xrr.ModeRecord
				if p.keyEnv == "GOOGLE_API_KEY" && os.Getenv(p.keyEnv) == "" && os.Getenv("GEMINI_API_KEY") != "" {
					t.Setenv(p.keyEnv, os.Getenv("GEMINI_API_KEY"))
				}
				if p.keyEnv != "" && os.Getenv(p.keyEnv) == "" {
					t.Skipf("-update needs %s; record with: %s", p.keyEnv, p.record)
				}
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				if entries, _ := os.ReadDir(dir); len(entries) == 0 {
					t.Skipf("no %s cassettes yet; record with: %s", p.name, p.record)
				}
				// The key precheck wants a value; replay never sends it.
				if p.keyEnv != "" {
					t.Setenv(p.keyEnv, "replay-no-key")
				}
			}
			isolateLLMConfig(t)

			var exchanges []llmxrr.Exchange
			tr := &llmxrr.Transport{
				Session:    xrr.NewSession(mode, xrr.NewFileCassette(dir)),
				Next:       http.DefaultTransport,
				OnExchange: func(ex llmxrr.Exchange) { exchanges = append(exchanges, ex) },
			}
			prev := http.DefaultTransport
			http.DefaultTransport = tr
			t.Cleanup(func() { http.DefaultTransport = prev })

			client, err := llm.NewClient(context.Background(), llm.ClientOpts{Model: p.model})
			if err != nil {
				t.Fatalf("NewClient(%s): %v", p.model, err)
			}
			reg := tool.NewRegistry()
			if err := reg.Register(echoTool{}); err != nil {
				t.Fatal(err)
			}
			answer, err := tool.NewDispatcher(client, reg, tool.DispatchConfig{}).Run(context.Background(), recordedPrompt)
			if err != nil {
				for i, ex := range exchanges {
					if ex.Error != "" && !ex.Miss {
						// A refused recording or a stored failure.
						t.Errorf("request %d: %s", i+1, ex.Error)
					}
					if ex.Miss {
						t.Errorf("request %d matched no recording: foo's wire request changed.\nsent (normalized):\n%s", i+1, ex.Canonical)
					}
				}
				t.Fatalf("tool round: %v", err)
			}

			// The seam carried the traffic, and replay stayed off the
			// network. Both checks are needed: without the first a
			// bypassed seam passes; without the second a live one does.
			if tr.Seen() == 0 {
				t.Fatal("no model request went through the xrr seam")
			}
			if !*update && tr.Live() != 0 {
				t.Fatalf("%d live request(s) during replay", tr.Live())
			}
			// A model may take more than one round; the second request
			// is the one that answers the first call.
			if len(exchanges) < 2 {
				t.Fatalf("want at least 2 model requests (tool call, answer), got %d", len(exchanges))
			}

			id := p.callID(t, decode(t, exchanges[0].Response))
			if id == "" {
				t.Fatal("first response's tool call has no id")
			}
			p.linked(t, decode(t, exchanges[1].Body), id)

			if !strings.Contains(strings.ToLower(answer), echoWord) {
				t.Errorf("final answer %q does not repeat the tool result", answer)
			}
			if err := llmxrr.CheckNoSecrets(dir, keys...); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// isolateLLMConfig keeps the developer's llm.yaml (fallbacks, base
// URLs) and LLM_* env out of the round.
func isolateLLMConfig(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	for _, k := range []string{"LLM_BASE_URL", "LLM_FALLBACK", "LLM_API_KEY", "OPENAI_BASE_URL", "ANTHROPIC_BASE_URL"} {
		// Unset, not empty: the Anthropic SDK takes a present-but-empty
		// ANTHROPIC_BASE_URL as the base URL and posts to a bare path.
		// t.Setenv first so the original value is restored on cleanup.
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

func openAICallID(t *testing.T, resp map[string]any) string {
	t.Helper()
	for _, c := range list(resp["choices"]) {
		for _, tc := range list(obj(obj(c)["message"])["tool_calls"]) {
			fn := obj(obj(tc)["function"])
			requireEqual(t, "echo", fn["name"], "tool call name")
			return str(obj(tc)["id"])
		}
	}
	t.Fatalf("first response has no tool_calls: %v", resp)
	return ""
}

func openAILinked(t *testing.T, req map[string]any, id string) {
	t.Helper()
	msgs := list(req["messages"])
	call := indexOf(msgs, func(m map[string]any) bool {
		if m["role"] != "assistant" {
			return false
		}
		for _, tc := range list(m["tool_calls"]) {
			if obj(tc)["id"] == id && obj(obj(tc)["function"])["name"] == "echo" {
				return true
			}
		}
		return false
	})
	if call < 0 {
		t.Fatalf("second request has no assistant tool_calls entry %q:\n%s", id, dump(req))
	}
	res := indexOf(msgs[call+1:], func(m map[string]any) bool {
		return m["role"] == "tool" && m["tool_call_id"] == id && strings.Contains(dump(m["content"]), echoWord)
	})
	if res < 0 {
		t.Fatalf("second request has no tool message with tool_call_id %q after the call:\n%s", id, dump(req))
	}
}

func decode(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}

func obj(v any) map[string]any { m, _ := v.(map[string]any); return m }
func list(v any) []any         { l, _ := v.([]any); return l }
func str(v any) string         { s, _ := v.(string); return s }

func dump(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func indexOf(items []any, match func(map[string]any) bool) int {
	for i, it := range items {
		if m := obj(it); m != nil && match(m) {
			return i
		}
	}
	return -1
}

// hasBlock matches an Anthropic message's content blocks.
func hasBlock(m map[string]any, match func(map[string]any) bool) bool {
	return indexOf(list(m["content"]), match) >= 0
}

// hasPart matches a Gemini content's parts.
func hasPart(c map[string]any, match func(map[string]any) bool) bool {
	return indexOf(list(c["parts"]), match) >= 0
}

func requireEqual(t *testing.T, want string, got any, what string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %q", what, got, want)
	}
}
