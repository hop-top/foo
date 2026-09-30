package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"hop.top/foo/internal/tool/shim"
	"hop.top/kit/go/ai/ext/discover"
)

const externalTimeout = 30 * time.Second

// externalRequest is sent to an external tool binary on stdin.
type externalRequest struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// externalResponse is read from an external tool binary on stdout.
type externalResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *string         `json:"error"`
}

// ExternalTool wraps a discovered foo-tool-* binary as a Tool.
//
// A binary whose --ext-info declares a foo_tool block is gated: each
// call goes through the shim engine's authorizer before the binary
// runs, and the binary receives the canonical paths. One that declares
// nothing runs with the arguments as the model sent them.
type ExternalTool struct {
	name        string
	description string
	parameters  json.RawMessage
	path        string
	// plugin holds the foo_tool annotations; nil when undeclared.
	plugin *shim.Plugin
	// engine authorizes gated calls; nil denies them all.
	engine *shim.Engine
}

// NewExternalTool creates a Tool backed by an external binary.
func NewExternalTool(
	name, description, path string, parameters json.RawMessage,
) *ExternalTool {
	if parameters == nil {
		parameters = json.RawMessage(emptyParameters)
	}
	return &ExternalTool{
		name:        name,
		description: description,
		parameters:  parameters,
		path:        path,
	}
}

func (t *ExternalTool) Name() string                { return t.name }
func (t *ExternalTool) Description() string         { return t.description }
func (t *ExternalTool) Parameters() json.RawMessage { return t.parameters }

// Path returns the absolute path of the backing binary.
func (t *ExternalTool) Path() string { return t.path }

// Gated reports whether the binary declared foo_tool annotations, so
// foo checks its declared paths and side effect before each call.
func (t *ExternalTool) Gated() bool { return t.plugin != nil }

// ApprovesItself reports that a gated plugin's authorizer asks any
// approval question the call needs (scope, side-effect policy,
// --tools-approve) as one, so the dispatcher must not ask first.
func (t *ExternalTool) ApprovesItself() bool { return t.Gated() }

// SideEffect is the declared side effect, "" when ungated.
func (t *ExternalTool) SideEffect() string {
	if t.plugin == nil {
		return ""
	}
	return t.plugin.SideEffect()
}

// PathSummary lists the declared path params with their ops, e.g.
// "src:r dst:w"; "" when ungated.
func (t *ExternalTool) PathSummary() string {
	if t.plugin == nil {
		return ""
	}
	return t.plugin.PathSummary()
}

// SetEngine sets the engine whose authorizer gates this plugin's
// calls. Until it is set, and while the engine has no authorizer,
// every call of a gated plugin is denied.
func (t *ExternalTool) SetEngine(e *shim.Engine) { t.engine = e }

// Execute runs the external binary with JSON stdin/stdout protocol. A
// gated plugin's call is authorized first; a refusal returns a
// *shim.CallError and the binary never runs.
func (t *ExternalTool) Execute(
	ctx context.Context, args json.RawMessage,
) (json.RawMessage, error) {
	if t.plugin != nil {
		engine := t.engine
		if engine == nil {
			engine = &shim.Engine{}
		}
		sent, err := engine.AuthorizePlugin(ctx, t.plugin, t.path, args)
		if err != nil {
			return nil, &shim.CallError{Err: err}
		}
		args = sent
	}
	ctx, cancel := context.WithTimeout(ctx, externalTimeout)
	defer cancel()

	reqJSON, err := json.Marshal(externalRequest{
		Name:      t.name,
		Arguments: args,
	})
	if err != nil {
		return nil, fmt.Errorf("external tool %s: marshal request: %w", t.name, err)
	}

	cmd := exec.CommandContext(ctx, t.path)
	cmd.Stdin = bytes.NewReader(reqJSON)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf(
			"external tool %s: exec: %w (stderr: %s)",
			t.name, err, stderr.String(),
		)
	}

	var resp externalResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf(
			"external tool %s: parse response: %w",
			t.name, err,
		)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("external tool %s: %s", t.name, *resp.Error)
	}

	return resp.Result, nil
}

// emptyParameters is the schema of a tool that takes no arguments.
const emptyParameters = `{"type":"object","properties":{}}`

// ExternalToolFromFound builds the registry entry for a discovered
// foo-tool-* binary. It runs the binary with --ext-info once (Enrich)
// and reads, beyond kit's metadata, the foo-defined top-level
// "parameters" field: the JSON Schema object the model sees as the
// tool's arguments; and "foo_tool", the annotations that make foo gate
// the plugin's declared paths and side effect (shim.ParsePlugin).
//
// A binary whose --ext-info fails still registers under its
// filename-derived name with no parameters. A blank --ext-info name
// also falls back to the filename. Absent or null "parameters" means
// the tool takes no arguments. Any other "parameters" that is not a
// JSON object with "type": "object" returns *InvalidParametersError:
// offered with no schema instead, the model would call the plugin
// without the arguments it declared. Invalid foo_tool annotations also
// return *InvalidParametersError (Field "foo_tool"): offered ungated
// instead, the plugin would run without the checks it asked for.
func ExternalToolFromFound(f *discover.Found) (*ExternalTool, error) {
	if err := f.Enrich(); err != nil {
		return NewExternalTool(f.Name, f.Name, f.Path, nil), nil
	}
	info := f.Info()
	name := strings.TrimSpace(info.Metadata.Name)
	if name == "" {
		name = f.Name
	}

	var fields extInfoFields
	if err := info.Decode(&fields); err != nil {
		return nil, &InvalidParametersError{Name: name, Path: f.Path, Reason: err.Error()}
	}
	params, reason := validParameters(fields.Parameters)
	if reason != "" {
		return nil, &InvalidParametersError{Name: name, Path: f.Path, Reason: reason}
	}
	plugin, err := shim.ParsePlugin(name, params, fields.FooTool)
	if err != nil {
		reason := err.Error()
		var lint *shim.LintError
		if errors.As(err, &lint) {
			reason = strings.Join(lint.Problems, "; ")
		}
		return nil, &InvalidParametersError{Name: name, Path: f.Path, Field: "foo_tool", Reason: "is invalid: " + reason}
	}
	t := NewExternalTool(name, info.Metadata.Description, f.Path, params)
	t.plugin = plugin
	return t, nil
}

// extInfoFields holds the --ext-info fields foo defines on top of the
// kit discovery protocol.
type extInfoFields struct {
	Parameters json.RawMessage `json:"parameters"`
	FooTool    json.RawMessage `json:"foo_tool"`
}

// validParameters returns the schema to offer the model, nil for "no
// arguments", or a reason the declared value is unusable. It checks
// the shape every provider requires of function parameters; it is not
// a full JSON Schema validator.
func validParameters(raw json.RawMessage) (json.RawMessage, string) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, ""
	}
	var schema map[string]json.RawMessage
	if trimmed[0] != '{' || json.Unmarshal(trimmed, &schema) != nil {
		return nil, "must be a JSON Schema object"
	}
	var typ string
	if json.Unmarshal(schema["type"], &typ) != nil || typ != "object" {
		return nil, `must declare "type": "object"`
	}
	if props, ok := schema["properties"]; ok {
		var m map[string]json.RawMessage
		if bytes.TrimSpace(props)[0] != '{' || json.Unmarshal(props, &m) != nil {
			return nil, `"properties" must be a JSON object`
		}
	}
	return json.RawMessage(trimmed), ""
}

// InvalidParametersError reports a foo-tool-* binary whose --ext-info
// "parameters" field cannot be offered to the model, or whose
// "foo_tool" annotations cannot be enforced.
type InvalidParametersError struct {
	// Name is the tool name the binary would have registered under.
	Name string
	// Path is the binary's absolute path.
	Path string
	// Field is the --ext-info field at fault; "" means "parameters".
	Field string
	// Reason says what is wrong with the field.
	Reason string
}

func (e *InvalidParametersError) Error() string {
	field := e.Field
	if field == "" {
		field = "parameters"
	}
	return fmt.Sprintf("%s: --ext-info %q %s", e.Path, field, e.Reason)
}

// DeclaresParameters reports whether t's schema names at least one
// argument.
func DeclaresParameters(t Tool) bool {
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(t.Parameters(), &schema) != nil {
		return false
	}
	return len(schema.Properties) > 0
}
