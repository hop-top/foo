package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"hop.top/kit/go/ai/llm"
)

// ApproveFunc is called before executing a tool when approval is required.
// It receives the tool name and pretty-printed arguments. Return true
// to proceed, false to skip.
type ApproveFunc func(name string, args json.RawMessage) bool

// DispatchConfig controls the agentic dispatch loop.
type DispatchConfig struct {
	// ChainLimit caps tool-call iterations (default 5).
	ChainLimit int
	// Debug enables logging of tool calls and results.
	Debug bool
	// Approve is called before each tool execution when non-nil.
	Approve ApproveFunc
	// DebugWriter receives debug output. Defaults to io.Discard.
	DebugWriter io.Writer
}

func (c *DispatchConfig) chainLimit() int {
	if c.ChainLimit <= 0 {
		return 5
	}
	return c.ChainLimit
}

func (c *DispatchConfig) debugWriter() io.Writer {
	if c.DebugWriter == nil {
		return io.Discard
	}
	return c.DebugWriter
}

// Dispatcher runs the agentic tool-calling loop.
type Dispatcher struct {
	client   ToolClient
	registry *Registry
	cfg      DispatchConfig
}

// ToolClient is the subset of llm.Client needed by the dispatch loop.
type ToolClient interface {
	CallWithTools(
		ctx context.Context,
		messages []llm.Message,
		tools []llm.ToolDef,
	) (llm.ToolResponse, error)
}

// NewDispatcher creates a new dispatch loop runner.
func NewDispatcher(client ToolClient, reg *Registry, cfg DispatchConfig) *Dispatcher {
	return &Dispatcher{
		client:   client,
		registry: reg,
		cfg:      cfg,
	}
}

// Run executes the agentic loop: sends prompt + tools to LLM, executes
// any tool calls, feeds results back, and repeats until the LLM returns
// a text-only response or the chain limit is reached.
func (d *Dispatcher) Run(ctx context.Context, prompt string) (string, error) {
	defs := d.registry.ToolDefs()
	messages := []llm.Message{
		{Role: "user", Content: prompt},
	}
	dbg := d.cfg.debugWriter()

	for i := 0; i < d.cfg.chainLimit(); i++ {
		resp, err := d.client.CallWithTools(ctx, messages, defs)
		if err != nil {
			return "", fmt.Errorf("tool dispatch: llm call: %w", err)
		}

		// No tool calls — final answer.
		if len(resp.ToolCalls) == 0 {
			return resp.Content, nil
		}

		// Append the assistant's response (with tool_calls) to messages.
		messages = append(messages, llm.Message{
			Role:    "assistant",
			Content: resp.Content,
		})

		// Execute each tool call and feed results back.
		for _, tc := range resp.ToolCalls {
			if d.cfg.Debug {
				fmt.Fprintf(dbg,
					"[tool] call: %s (id=%s) args=%s\n",
					tc.Name, tc.ID, string(tc.Arguments),
				)
			}

			result, execErr := d.executeTool(ctx, tc)

			if d.cfg.Debug {
				if execErr != nil {
					fmt.Fprintf(dbg, "[tool] error: %s: %v\n", tc.Name, execErr)
				} else {
					fmt.Fprintf(dbg, "[tool] result: %s: %s\n", tc.Name, string(result))
				}
			}

			// Build the tool result message.
			var content string
			if execErr != nil {
				content = fmt.Sprintf(`{"error": %q}`, execErr.Error())
			} else {
				content = string(result)
			}

			messages = append(messages, llm.Message{
				Role:    "tool",
				Content: content,
			})
		}
	}

	return "", fmt.Errorf(
		"tool dispatch: chain limit (%d) reached", d.cfg.chainLimit(),
	)
}

// executeTool looks up and runs a single tool call.
func (d *Dispatcher) executeTool(
	ctx context.Context, tc llm.ToolCall,
) (json.RawMessage, error) {
	t, ok := d.registry.Get(tc.Name)
	if !ok {
		return nil, fmt.Errorf("unknown tool %q", tc.Name)
	}

	// Approval gate.
	if d.cfg.Approve != nil {
		if !d.cfg.Approve(tc.Name, tc.Arguments) {
			return json.RawMessage(`{"skipped": true}`), nil
		}
	}

	return t.Execute(ctx, tc.Arguments)
}
