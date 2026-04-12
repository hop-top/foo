package builtin

import (
	"context"
	"encoding/json"
	"runtime"
)

// Version is the foo binary version. Set via ldflags in production;
// defaults to "dev" for local builds.
var Version = "0.1.0"

// VersionTool returns the foo binary version and Go runtime version.
type VersionTool struct{}

func (VersionTool) Name() string        { return "foo_version" }
func (VersionTool) Description() string { return "Returns foo binary version" }

func (VersionTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

func (VersionTool) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	return json.Marshal(map[string]string{
		"version": Version,
		"go":      runtime.Version(),
	})
}
