// Package tool defines the Tool interface, registry, and agentic dispatch
// loop for LLM function-calling in foo.
package tool

import (
	"context"
	"encoding/json"
)

// Tool is the universal contract for LLM-callable tools.
// Tools are stateless; context carries cancellation + deadline.
type Tool interface {
	// Name returns the tool's unique identifier.
	Name() string
	// Description returns a human-readable summary for the LLM.
	Description() string
	// Parameters returns a JSON Schema describing accepted input.
	Parameters() json.RawMessage
	// Execute receives validated args and returns a JSON result.
	Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error)
}

// SelfApproving is implemented by tools that ask for approval inside
// their own Execute, as part of deciding whether a call may run (shim
// tools: the path gate merges scope, side-effect policy and
// --tools-approve into one question). The dispatcher does not ask
// again for a tool whose ApprovesItself reports true, so the user gets
// one question per call.
type SelfApproving interface {
	ApprovesItself() bool
}
