package shim

import (
	"encoding/json"
	"errors"

	"hop.top/foo/internal/tool/gate"
)

// ErrorBody is the structured error sent to the model (§6).
type ErrorBody struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Param   string `json:"param,omitempty"`
	Path    string `json:"path,omitempty"`
	Op      string `json:"op,omitempty"`
}

// NewErrorBody converts a refusal into its envelope form. Errors that
// are not *gate.Error become exec_failed.
func NewErrorBody(err error) ErrorBody {
	var ge *gate.Error
	if !errors.As(err, &ge) {
		return ErrorBody{Kind: string(KindExecFailed), Message: err.Error()}
	}
	return ErrorBody{Kind: string(ge.Kind), Message: ge.Message, Param: ge.Param, Path: ge.Path, Op: opString(ge.Op)}
}

// CallError is a refused or failed call as the in-process tool returns
// it. ToolMessage renders the structured {"error": {...}} message the
// dispatcher sends to the model instead of a flattened string.
type CallError struct{ Err error }

func (e *CallError) Error() string { return e.Err.Error() }
func (e *CallError) Unwrap() error { return e.Err }

// ToolMessage is the tool-message content for the model.
func (e *CallError) ToolMessage() json.RawMessage {
	data, _ := json.Marshal(map[string]ErrorBody{"error": NewErrorBody(e.Err)})
	return data
}
