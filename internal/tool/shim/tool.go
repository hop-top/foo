package shim

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Tool adapts a loaded spec to foo's tool.Tool interface so the
// dispatcher runs it in-process.
type Tool struct {
	engine *Engine
	loaded *Loaded
}

// NewTool binds a loaded spec to an engine.
func NewTool(e *Engine, l *Loaded) *Tool { return &Tool{engine: e, loaded: l} }

func (t *Tool) Name() string { return t.loaded.Spec.Name }

func (t *Tool) Description() string { return modelDescription(t.loaded.Spec) }

// Parameters is the JSON schema for the binary's flavor, with the run's
// working directory named as the base for relative paths.
func (t *Tool) Parameters() json.RawMessage {
	base := t.engine.Cwd
	if base == "" {
		base = "the working directory"
	}
	return t.loaded.Spec.schema(t.engine.variant(t.loaded), base)
}

// Execute runs the call. The result is the envelope body; a refusal is
// a *CallError carrying the structured error.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	res, err := t.engine.Call(ctx, t.loaded, args)
	if err != nil {
		return nil, &CallError{Err: err}
	}
	return json.Marshal(res)
}

// ApprovesItself reports that the engine's authorizer asks for any
// approval a call needs (scope prompt, side-effect policy,
// --tools-approve) in one question, so the dispatcher must not ask
// first.
func (t *Tool) ApprovesItself() bool { return true }

// Source reports where the spec came from, for `foo tool list`.
func (t *Tool) Source() string { return t.loaded.SourceLabel() }

// SideEffect is the spec's base side effect.
func (t *Tool) SideEffect() string { return t.loaded.Spec.SideEffect }

// PathSummary lists path params with their ops, e.g. "src:r dst:w".
func (t *Tool) PathSummary() string { return t.loaded.Spec.PathSummary() }

// Loaded exposes the spec behind the tool.
func (t *Tool) Loaded() *Loaded { return t.loaded }

// PathSummary lists path params with op letters, e.g. "src:r dst:w".
func (s *Spec) PathSummary() string {
	var parts []string
	for _, p := range s.Params {
		if p.Type != TypePath {
			continue
		}
		letters := ""
		for _, o := range p.Op {
			letters += o[:1]
		}
		parts = append(parts, p.Name+":"+letters)
	}
	return strings.Join(parts, " ")
}

// modelDescription appends the path conventions the model must know.
func modelDescription(s *Spec) string {
	for _, p := range s.Params {
		if p.Type == TypePath {
			return s.Description + " Paths are taken literally: no globbing, no $VAR; ~ is the home directory. Output shows canonical paths."
		}
	}
	return s.Description
}

// schema emits the portable JSON-schema subset (§2.3): defaults are
// stated in descriptions, never as a keyword. Params unsupported on
// the binary's flavor are left out.
func (s *Spec) schema(v *Variant, base string) json.RawMessage {
	props := map[string]any{}
	required := []string{}
	for _, p := range s.Params {
		if v != nil && slices.Contains(v.Unsupported, p.Name) {
			continue
		}
		props[p.Name] = p.schema(base)
		if p.Required || (p.Type == TypePath && p.def == nil) {
			required = append(required, p.Name)
		}
	}
	data, _ := json.Marshal(map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             required,
		"additionalProperties": false,
	})
	return data
}

func (p *Param) schema(base string) map[string]any {
	desc := strings.TrimSpace(p.Description)
	out := map[string]any{}
	switch p.Type {
	case TypePath:
		desc += " Absolute, or relative to " + base + "."
		if p.Repeated {
			out["type"] = "array"
			out["items"] = map[string]any{"type": "string"}
			out["minItems"] = 1
			out["maxItems"] = p.MaxItems
		} else {
			out["type"] = "string"
		}
	case TypeBool:
		out["type"] = "boolean"
	case TypeEnum:
		out["type"] = "string"
		out["enum"] = p.enumValues()
	case TypeInt:
		out["type"] = "integer"
		out["minimum"] = deref(p.Min)
		out["maximum"] = deref(p.Max)
	case TypeString:
		out["type"] = "string"
		out["maxLength"] = p.MaxLen
	}
	if d := defaultText(p); d != "" {
		desc += " Default " + d + "."
	}
	out["description"] = desc
	return out
}

func defaultText(p *Param) string {
	switch d := p.def.(type) {
	case nil:
		if p.Type == TypeBool {
			return "false"
		}
		return ""
	case []string:
		if !p.Repeated && len(d) == 1 {
			return fmt.Sprintf("%q", d[0])
		}
		data, _ := json.Marshal(d)
		return string(data)
	case string:
		return fmt.Sprintf("%q", d)
	default:
		return fmt.Sprint(d)
	}
}
