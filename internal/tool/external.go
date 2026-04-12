package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
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
type ExternalTool struct {
	name        string
	description string
	parameters  json.RawMessage
	path        string
}

// NewExternalTool creates a Tool backed by an external binary.
func NewExternalTool(
	name, description, path string, parameters json.RawMessage,
) *ExternalTool {
	if parameters == nil {
		parameters = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return &ExternalTool{
		name:        name,
		description: description,
		parameters:  parameters,
		path:        path,
	}
}

func (t *ExternalTool) Name() string              { return t.name }
func (t *ExternalTool) Description() string        { return t.description }
func (t *ExternalTool) Parameters() json.RawMessage { return t.parameters }

// Execute runs the external binary with JSON stdin/stdout protocol.
func (t *ExternalTool) Execute(
	ctx context.Context, args json.RawMessage,
) (json.RawMessage, error) {
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

// DiscoverExternalTools scans PATH for foo-tool-* binaries and wraps
// them as ExternalTool. Interrogation errors are logged and skipped.
func DiscoverExternalTools() ([]*ExternalTool, error) {
	// Use kit/ext/discover with prefix "foo-tool-".
	// For now we scan PATH manually with a simple approach
	// matching the kit discover pattern.
	scanner := pathScanner{prefix: "foo-tool-"}
	return scanner.scan()
}

// extInfoResponse matches the --ext-info JSON output from external tools.
type extInfoResponse struct {
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type pathScanner struct {
	prefix string
}

func (s *pathScanner) scan() ([]*ExternalTool, error) {
	// Import and delegate to kit/ext/discover.Scanner
	// For the actual scan, we replicate the minimal logic since
	// kit/ext/discover.Found doesn't carry parameters.
	// The real integration interrogates each binary.
	return nil, nil // No external tools discovered yet; placeholder.
}
