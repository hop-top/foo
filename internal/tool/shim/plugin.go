package shim

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"hop.top/foo/internal/tool/gate"
)

// Plugin is the foo_tool block of a third-party foo-tool-<name>
// plugin's --ext-info, checked against its parameters schema and held
// in the spec form the engine gates spec tools with. A plugin that
// declares one is authorized like a spec tool before it runs: each
// declared path is resolved and checked for its op, the side effect
// goes through the policy table, and the plugin receives the canonical
// paths in place of the ones the model sent.
//
// foo can only check what it passes to the plugin. Annotations that
// promise more than that are rejected: recursion filter_before and
// filter_after (foo would have to filter the plugin's input or output)
// and into_dir (foo cannot know where the plugin places each source).
type Plugin struct {
	spec *Spec
}

// ParsePlugin reads a plugin's foo_tool block. parameters is the
// plugin's model-facing schema (nil when it takes no arguments). It
// returns nil and no error when raw is absent or null: the plugin
// declares nothing and foo gates nothing. Any problem returns a
// *LintError.
//
// Every foo_tool.paths key must name a top-level parameter of type
// "string" (one path) or "array" of "string" items (several; maxItems
// bounds them, default 64, at most 256). side_effect_if, op_when,
// recursive_when and clobber_when conditions may name boolean,
// integer and string parameters.
func ParsePlugin(name string, parameters, raw json.RawMessage) (*Plugin, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var ft FooTool
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ft); err != nil {
		return nil, &LintError{Problems: []string{"decode: " + err.Error()}}
	}

	l := &linter{}
	s := &Spec{
		Version:      ft.Spec,
		Name:         name,
		SideEffect:   ft.SideEffect,
		SideEffectIf: ft.SideEffectIf,
		Network:      ft.Network,
	}
	if s.Version != SpecVersion {
		l.addf("spec: version %d not supported (want %d)", s.Version, SpecVersion)
	}
	props, err := schemaProps(parameters)
	if err != nil {
		l.addf("parameters: %v", err)
	}
	s.byName = map[string]*Param{}
	for _, pn := range sortedKeys(props) {
		prop := props[pn]
		annots, isPath := ft.Paths[pn]
		var p *Param
		if isPath {
			p = pluginPathParam(l, pn, prop, annots)
		} else {
			p = prop.condParam(pn)
		}
		if p != nil {
			s.Params = append(s.Params, p)
			s.byName[pn] = p
		}
	}
	for _, pn := range sortedKeys(ft.Paths) {
		if _, ok := props[pn]; !ok {
			l.addf("paths: %q is not a parameter", pn)
		}
	}
	if len(l.probs) > 0 {
		return nil, &LintError{Problems: l.probs}
	}

	s.lintEffects(l)
	s.lintSideEffectIf(l)
	for _, p := range s.Params {
		if p.Type == TypePath {
			p.lintPath(l, s)
		}
	}
	if len(l.probs) > 0 {
		return nil, &LintError{Problems: l.probs}
	}
	return &Plugin{spec: s}, nil
}

// pluginPathParam builds the path param for a declared path, or nil
// with the problems recorded.
func pluginPathParam(l *linter, name string, prop schemaProp, a PathAnnots) *Param {
	p := &Param{
		Name: name, Type: TypePath,
		Op: a.Op, OpWhen: a.OpWhen, Target: a.Target, MustExist: a.MustExist, Kind: a.Kind,
		Recursive: a.Recursive, RecursiveWhen: a.RecursiveWhen, Recursion: a.Recursion,
		ClobberWhen: a.ClobberWhen, ProtectRoots: a.ProtectRoots,
	}
	switch {
	case prop.typ == "string":
	case prop.typ == "array" && prop.itemsType == "string":
		p.Repeated = true
		p.MaxItems = prop.maxItems
	default:
		l.addf("paths: %q must be a string or an array of strings in parameters", name)
		return nil
	}
	ok := true
	if a.IntoDir {
		l.addf("paths: %q: into_dir is not supported for plugins: foo cannot know where the plugin places each source; declare the path the plugin writes", name)
		ok = false
	}
	switch a.Recursion {
	case RecursionFilterBefore, RecursionFilterAfter:
		l.addf("paths: %q: recursion %s is not supported for plugins: foo cannot filter what a plugin reads or prints; use all_or_nothing", name, a.Recursion)
		ok = false
	}
	if !ok {
		return nil
	}
	return p
}

// schemaProp is what foo reads of one parameters property.
type schemaProp struct {
	typ, itemsType string
	enum           []string
	maxItems       int
}

// condParam types a non-path parameter so conditions can name it; nil
// for a type conditions cannot compare.
func (sp schemaProp) condParam(name string) *Param {
	p := &Param{Name: name}
	switch sp.typ {
	case "boolean":
		p.Type = TypeBool
	case "integer":
		p.Type = TypeInt
	case "string":
		if len(sp.enum) > 0 {
			p.Type = TypeEnum
			p.Values = map[string][]string{}
			for _, v := range sp.enum {
				p.Values[v] = nil
			}
		} else {
			p.Type, p.Newline = TypeString, true
		}
	default:
		return nil
	}
	return p
}

// schemaProps reads the top-level properties of a parameters schema.
// A property whose type is not a single string is kept with no type.
func schemaProps(parameters json.RawMessage) (map[string]schemaProp, error) {
	out := map[string]schemaProp{}
	if t := bytes.TrimSpace(parameters); len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return out, nil
	}
	var schema struct {
		Properties map[string]map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(parameters, &schema); err != nil {
		return nil, err
	}
	for name, raw := range schema.Properties {
		var sp schemaProp
		_ = json.Unmarshal(raw["type"], &sp.typ)
		_ = json.Unmarshal(raw["maxItems"], &sp.maxItems)
		var enum []any
		if json.Unmarshal(raw["enum"], &enum) == nil {
			for _, v := range enum {
				if str, ok := v.(string); ok {
					sp.enum = append(sp.enum, str)
				}
			}
		}
		var items struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw["items"], &items) == nil {
			sp.itemsType = items.Type
		}
		out[name] = sp
	}
	return out, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// SideEffect is the declared base side effect.
func (p *Plugin) SideEffect() string { return p.spec.SideEffect }

// PathSummary lists declared path params with op letters, e.g.
// "src:r dst:w".
func (p *Plugin) PathSummary() string { return p.spec.PathSummary() }

// AuthorizePlugin gates one call of plugin p, the binary at bin, the
// way Call gates a spec tool: the lexical root guard, the authorizer
// (scope per declared op, side-effect policy, one approval question),
// then the root, clobber and kind checks on what it granted. It returns
// the arguments to send the plugin: raw with every declared path
// replaced by its canonical path, so the path checked is the path
// used. A refused call returns *gate.Error; the plugin must not run.
//
// Arguments p does not declare pass through unchanged. A declared path
// the model leaves out is not checked, since there is nothing to check.
func (e *Engine) AuthorizePlugin(ctx context.Context, p *Plugin, bin string, raw json.RawMessage) (json.RawMessage, error) {
	s := p.spec
	obj, vals, err := s.pluginValues(raw)
	if err != nil {
		return nil, err
	}
	req := gate.Request{
		Tool:       s.Name,
		SideEffect: s.effectiveSideEffect(vals),
		Paths:      s.pathArgs(vals),
	}
	req.Argv = func(canonical map[string][]string) []string {
		out, err := s.withPaths(obj, canonical)
		if err != nil {
			return nil
		}
		return pluginArgv(bin, out)
	}
	_, paths, err := e.gate(ctx, s, vals, req)
	if err != nil {
		return nil, err
	}
	for _, pa := range req.Paths {
		if len(paths[pa.Param]) != len(pa.Values) {
			return nil, denied(pa.Param, "authorizer granted a different number of paths than were sent")
		}
	}
	out, err := s.withPaths(obj, paths)
	if err != nil {
		return nil, &gate.Error{Kind: KindExecFailed, Message: err.Error()}
	}
	return json.Marshal(out)
}

// pluginValues decodes the model's arguments: the object itself, and
// the typed values of the params foo reads (declared paths and the
// params conditions name). A value foo reads must have its declared
// type; anything else in the object is the plugin's to validate.
func (s *Spec) pluginValues(raw json.RawMessage) (map[string]json.RawMessage, values, error) {
	obj := map[string]json.RawMessage{}
	if t := bytes.TrimSpace(raw); len(t) > 0 && !bytes.Equal(t, []byte("null")) {
		if t[0] != '{' {
			return nil, nil, invalid("", "arguments must be a JSON object")
		}
		if err := json.Unmarshal(t, &obj); err != nil {
			return nil, nil, invalid("", "arguments: %v", err)
		}
	}
	vals := values{}
	for _, k := range sortedKeys(obj) {
		p, ok := s.byName[k]
		if !ok {
			// A decoder that matches keys without regard to case (Go's
			// encoding/json) would read this as the declared param,
			// unchecked.
			if q := s.foldMatch(k); q != "" {
				return nil, nil, invalid(k, "argument %q differs from %q only in case", k, q)
			}
			continue
		}
		rv := bytes.TrimSpace(obj[k])
		if bytes.Equal(rv, []byte("null")) {
			continue
		}
		v, err := p.decodeJSON(rv)
		if err != nil {
			return nil, nil, invalid(k, "%v", err)
		}
		vals[k] = v
	}
	return obj, vals, nil
}

// foldMatch names the declared param k equals under case folding, ""
// when none.
func (s *Spec) foldMatch(k string) string {
	for _, p := range s.Params {
		if strings.EqualFold(p.Name, k) {
			return p.Name
		}
	}
	return ""
}

// withPaths returns a copy of obj with each declared path param
// replaced by paths[param]: a string for a single path, an array for a
// repeated one.
func (s *Spec) withPaths(obj map[string]json.RawMessage, paths map[string][]string) (map[string]json.RawMessage, error) {
	out := make(map[string]json.RawMessage, len(obj))
	for k, v := range obj {
		out[k] = v
	}
	for _, p := range s.Params {
		list, ok := paths[p.Name]
		if p.Type != TypePath || !ok {
			continue
		}
		var v any = list
		if !p.Repeated {
			if len(list) != 1 {
				return nil, fmt.Errorf("param %q: %d paths for a single path", p.Name, len(list))
			}
			v = list[0]
		}
		data, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		out[p.Name] = data
	}
	return out, nil
}

// pluginArgv renders a plugin call for an approval prompt: the binary,
// then name=value per argument in name order; strings as they are,
// other values as JSON.
func pluginArgv(bin string, args map[string]json.RawMessage) []string {
	argv := []string{bin}
	for _, k := range sortedKeys(args) {
		v := string(args[k])
		var str string
		if json.Unmarshal(args[k], &str) == nil {
			v = str
		} else {
			var buf bytes.Buffer
			if json.Compact(&buf, args[k]) == nil {
				v = buf.String()
			}
		}
		argv = append(argv, k+"="+v)
	}
	return argv
}
