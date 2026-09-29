package tool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"hop.top/kit/go/ai/llm"
)

// refusal is an error that renders its own tool message, like a shim
// denial.
type refusal struct{}

func (refusal) Error() string { return "denied: not in scope" }
func (refusal) ToolMessage() json.RawMessage {
	return json.RawMessage(`{"error":{"kind":"denied","message":"not in scope"}}`)
}

type failingTool struct{ err error }

func (failingTool) Name() string                { return "f" }
func (failingTool) Description() string         { return "f" }
func (failingTool) Parameters() json.RawMessage { return json.RawMessage(emptyParameters) }
func (t failingTool) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	return nil, t.err
}

func TestDispatch_ErrorToolMessage(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"structured refusal sent as is", refusal{}, `{"error":{"kind":"denied","message":"not in scope"}}`},
		{"wrapped refusal", errors.Join(refusal{}), `{"error":{"kind":"denied","message":"not in scope"}}`},
		{"plain error flattened", errors.New("boom"), `{"error":"boom"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := NewRegistry()
			if err := reg.Register(failingTool{err: tc.err}); err != nil {
				t.Fatal(err)
			}
			client := &scriptedClient{call: llm.ToolCall{ID: "c1", Name: "f", Arguments: json.RawMessage(`{}`)}}
			if _, err := NewDispatcher(client, reg, DispatchConfig{}).Run(context.Background(), "go"); err != nil {
				t.Fatal(err)
			}
			msgs := client.msgsSeen[1]
			assertJSONEqual(t, []byte(msgs[len(msgs)-1].Content), tc.want)
		})
	}
}
