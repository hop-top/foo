package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"hop.top/foo/internal/tool"
	"hop.top/kit/go/ai/llm"
)

// oneCallClient asks for a single probe tool call, then answers with
// text once the tool result comes back.
type oneCallClient struct{ turns int }

func (c *oneCallClient) CallWithTools(
	_ context.Context, _ []llm.Message, _ []llm.ToolDef,
) (llm.ToolResponse, error) {
	c.turns++
	if c.turns == 1 {
		return llm.ToolResponse{ToolCalls: []llm.ToolCall{{
			ID: "call-1", Name: "probe", Arguments: json.RawMessage(`{}`),
		}}}, nil
	}
	return llm.ToolResponse{Content: "done"}, nil
}

type probeTool struct{ runs int }

func (*probeTool) Name() string                { return "probe" }
func (*probeTool) Description() string         { return "records that it ran" }
func (*probeTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (p *probeTool) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	p.runs++
	return json.RawMessage(`{"ok":true}`), nil
}

// pipedCommand returns a command whose stdin is a real pipe carrying
// data, the way `echo ... | foo` delivers it, plus a buffer for stderr.
func pipedCommand(t *testing.T, data string) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if _, err := w.WriteString(data); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	cmd := &cobra.Command{}
	cmd.SetIn(r)
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	return cmd, &stderr
}

// withApproval turns --tools-approve on and points the approval prompt
// at the given terminal stand-in for the duration of the test.
func withApproval(t *testing.T, open func() (io.Reader, error)) {
	t.Helper()
	prevApprove, prevOpen := toolsApprove, openApprovalTerminal
	toolsApprove, openApprovalTerminal = true, open
	t.Cleanup(func() { toolsApprove, openApprovalTerminal = prevApprove, prevOpen })
}

// runProbe drives the dispatcher the way runPromptOrREPL does: read the
// prompt from stdin first, then run the tool loop with approvalFunc.
func runProbe(t *testing.T, cmd *cobra.Command) (prompt string, runs int) {
	t.Helper()
	prompt, err := readPrompt(cmd, nil)
	if err != nil {
		t.Fatalf("readPrompt: %v", err)
	}
	reg := tool.NewRegistry()
	probe := &probeTool{}
	if err := reg.Register(probe); err != nil {
		t.Fatal(err)
	}
	d := tool.NewDispatcher(&oneCallClient{}, reg, tool.DispatchConfig{Approve: approvalFunc(newToolPrompter(cmd))})
	if _, err := d.Run(context.Background(), prompt); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	return prompt, probe.runs
}

// Piped stdin is prompt data. Approval answers come from the terminal,
// so a "y" typed there runs the tool, and a "y" line inside the piped
// data stays part of the prompt instead of being taken as consent.
func TestToolsApprove_PipedStdinAnswersFromTerminal(t *testing.T) {
	cmd, stderr := pipedCommand(t, "summarise this\ny\n")
	withApproval(t, func() (io.Reader, error) { return strings.NewReader("y\n"), nil })

	prompt, runs := runProbe(t, cmd)

	if prompt != "summarise this\ny" {
		t.Fatalf("prompt = %q; piped data must reach the model intact", prompt)
	}
	if runs != 1 {
		t.Fatalf("tool ran %d times, want 1 (approved on the terminal); stderr=%q", runs, stderr.String())
	}
	if !strings.Contains(stderr.String(), "[tool] execute probe with {}? [y/N] ") {
		t.Fatalf("approval question not on stderr: %q", stderr.String())
	}
}

// With no terminal there is nobody to ask. The call is denied and the
// reason is printed, rather than the tool silently never running.
func TestToolsApprove_NoTerminalDeniesLoudly(t *testing.T) {
	cmd, stderr := pipedCommand(t, "summarise this\n")
	withApproval(t, func() (io.Reader, error) {
		return nil, errors.New("open /dev/tty: device not configured")
	})

	_, runs := runProbe(t, cmd)

	if runs != 0 {
		t.Fatalf("tool ran %d times with no terminal to approve it", runs)
	}
	msg := stderr.String()
	for _, want := range []string{"probe", "denied", "no terminal"} {
		if !strings.Contains(msg, want) {
			t.Errorf("stderr %q missing %q", msg, want)
		}
	}
}
