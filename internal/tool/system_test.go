package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"hop.top/kit/go/ai/llm"
)

// roundsClient asks for one echo call per round for calls rounds, then
// answers. It keeps a copy of every request's messages.
type roundsClient struct {
	calls    int
	msgsSeen [][]llm.Message
}

func (c *roundsClient) CallWithTools(
	_ context.Context, msgs []llm.Message, _ []llm.ToolDef,
) (llm.ToolResponse, error) {
	c.msgsSeen = append(c.msgsSeen, append([]llm.Message(nil), msgs...))
	if n := len(c.msgsSeen); n <= c.calls {
		return llm.ToolResponse{ToolCalls: []llm.ToolCall{
			{ID: fmt.Sprintf("c%d", n), Name: "echo", Arguments: json.RawMessage(fmt.Sprintf(`{"n":%d}`, n))},
		}}, nil
	}
	return llm.ToolResponse{Content: "done"}, nil
}

func roles(msgs []llm.Message) string {
	r := make([]string, len(msgs))
	for i, m := range msgs {
		r[i] = m.Role
	}
	return strings.Join(r, ",")
}

// The system prompt opens the conversation with the system role and
// stays its one system message, first, on every round; the user turn
// is the prompt alone. Each round adds exactly its assistant turn and
// the results, nothing is repeated.
func TestDispatch_SystemPromptOnceFirstEveryRound(t *testing.T) {
	const sys = "You are terse."
	reg := NewRegistry()
	if err := reg.Register(echoTool{}); err != nil {
		t.Fatal(err)
	}
	client := &roundsClient{calls: 3}
	out, err := NewDispatcher(client, reg, DispatchConfig{System: sys}).Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "done" {
		t.Errorf("answer %q, want done", out)
	}
	if len(client.msgsSeen) != 4 {
		t.Fatalf("model called %d times, want 4", len(client.msgsSeen))
	}
	for i, msgs := range client.msgsSeen {
		want := "system,user" + strings.Repeat(",assistant,tool", i)
		if got := roles(msgs); got != want {
			t.Errorf("request %d roles %s, want %s", i+1, got, want)
			continue
		}
		if msgs[0].Content != sys {
			t.Errorf("request %d system content %q, want %q", i+1, msgs[0].Content, sys)
		}
		if msgs[1].Content != "go" {
			t.Errorf("request %d user content %q, want the prompt alone", i+1, msgs[1].Content)
		}
		for j, m := range msgs[1:] {
			if strings.Contains(m.Content, sys) {
				t.Errorf("request %d message %d (%s) repeats the system prompt: %q", i+1, j+1, m.Role, m.Content)
			}
		}
		// Each round answers the call made in the round before it.
		for r := 1; r <= i; r++ {
			asst, res := msgs[2*r], msgs[2*r+1]
			id := fmt.Sprintf("c%d", r)
			if len(asst.ToolCalls) != 1 || asst.ToolCalls[0].ID != id || res.ToolCallID != id {
				t.Errorf("request %d round %d: assistant %+v result %+v, want call and result %s", i+1, r, asst, res, id)
			}
		}
	}
}

// No system prompt, no system message: the conversation opens with the
// user prompt, the shape every recorded cassette holds.
func TestDispatch_NoSystemPromptNoSystemMessage(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(echoTool{}); err != nil {
		t.Fatal(err)
	}
	client := &roundsClient{calls: 1}
	if _, err := NewDispatcher(client, reg, DispatchConfig{}).Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for i, msgs := range client.msgsSeen {
		want := "user" + strings.Repeat(",assistant,tool", i)
		if got := roles(msgs); got != want {
			t.Errorf("request %d roles %s, want %s", i+1, got, want)
		}
	}
}
