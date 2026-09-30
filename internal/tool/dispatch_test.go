package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

// selfApprover asks for approval inside its own Execute, like a shim
// tool whose gate merges every reason to ask into one question.
type selfApprover struct {
	failingTool
	runs int
}

func (*selfApprover) ApprovesItself() bool { return true }
func (s *selfApprover) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	s.runs++
	return json.RawMessage(`{"ok":true}`), nil
}

// The dispatcher's approval question is for tools that do not ask on
// their own; a self-approving tool is not asked about twice.
func TestDispatch_ApproveSkipsSelfApprovingTools(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tool  Tool
		asked int
	}{
		{"plain tool asked", failingTool{err: errors.New("ran")}, 1},
		{"self-approving tool not asked", &selfApprover{failingTool: failingTool{}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := NewRegistry()
			if err := reg.Register(tc.tool); err != nil {
				t.Fatal(err)
			}
			asked := 0
			approve := func(string, json.RawMessage) (bool, error) { asked++; return true, nil }
			client := &scriptedClient{call: llm.ToolCall{ID: "c1", Name: "f", Arguments: json.RawMessage(`{}`)}}
			if _, err := NewDispatcher(client, reg, DispatchConfig{Approve: approve}).Run(context.Background(), "go"); err != nil {
				t.Fatal(err)
			}
			if asked != tc.asked {
				t.Errorf("approval asked %d times; want %d", asked, tc.asked)
			}
			if s, ok := tc.tool.(*selfApprover); ok && s.runs != 1 {
				t.Errorf("self-approving tool ran %d times; want 1 (its own check decides)", s.runs)
			}
		})
	}
}

// A declined call reaches the model as the structured declined error
// every tool shares (spec §6), never as {"skipped": true}. When nobody
// could be asked, the message says why.
func TestDispatch_DeclinedIsStructuredError(t *testing.T) {
	for _, tc := range []struct {
		name    string
		approve ApproveFunc
		want    []string
	}{
		{"user said no", func(string, json.RawMessage) (bool, error) { return false, nil },
			[]string{"the user declined this call"}},
		{"cannot ask", func(string, json.RawMessage) (bool, error) {
			return false, fmt.Errorf("%w (open /dev/tty: device not configured)", ErrNoTerminal)
		}, []string{"cannot be asked", "no terminal", "device not configured"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := NewRegistry()
			probe := &countingTool{}
			if err := reg.Register(probe); err != nil {
				t.Fatal(err)
			}
			client := &scriptedClient{call: llm.ToolCall{ID: "c1", Name: "f", Arguments: json.RawMessage(`{}`)}}
			if _, err := NewDispatcher(client, reg, DispatchConfig{Approve: tc.approve}).Run(context.Background(), "go"); err != nil {
				t.Fatal(err)
			}
			if probe.runs != 0 {
				t.Fatalf("declined tool ran %d times", probe.runs)
			}
			msgs := client.msgsSeen[1]
			var got struct {
				Error *struct {
					Kind    string `json:"kind"`
					Message string `json:"message"`
				} `json:"error"`
			}
			content := msgs[len(msgs)-1].Content
			if err := json.Unmarshal([]byte(content), &got); err != nil || got.Error == nil {
				t.Fatalf("tool message %q is not a structured error (%v)", content, err)
			}
			if got.Error.Kind != "declined" {
				t.Errorf("kind = %q; want declined (message %s)", got.Error.Kind, content)
			}
			for _, w := range tc.want {
				if !strings.Contains(got.Error.Message, w) {
					t.Errorf("message %q missing %q", got.Error.Message, w)
				}
			}
		})
	}
}

// countingTool counts its runs and asks for no approval of its own.
type countingTool struct {
	failingTool
	runs int
}

func (c *countingTool) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	c.runs++
	return json.RawMessage(`{"ok":true}`), nil
}
