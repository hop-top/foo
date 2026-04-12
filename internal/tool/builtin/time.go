// Package builtin provides built-in tools for foo.
package builtin

import (
	"context"
	"encoding/json"
	"time"
)

// TimeTool returns the current date/time in RFC3339 format.
type TimeTool struct{}

func (TimeTool) Name() string        { return "foo_time" }
func (TimeTool) Description() string { return "Returns current date/time in RFC3339" }

func (TimeTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

func (TimeTool) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	return json.Marshal(map[string]string{
		"time": time.Now().UTC().Format(time.RFC3339),
	})
}
