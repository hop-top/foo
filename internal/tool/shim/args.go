package shim

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"

	"hop.top/foo/internal/tool/gate"
)

// values holds a call's validated arguments by param name, defaults
// applied: path → []string, bool → bool, enum/string → string,
// int → int64. A param with no value and no default is absent.
type values map[string]any

func invalid(param, format string, args ...any) *gate.Error {
	return &gate.Error{Kind: gate.KindInvalidArgs, Param: param, Message: fmt.Sprintf(format, args...)}
}

// validate checks raw model arguments against the spec (§4.1 step 1):
// unknown keys, types, bounds, enum membership, required params and
// exclusive groups. It never touches the filesystem.
func (s *Spec) validate(v *Variant, raw json.RawMessage) (values, error) {
	obj := map[string]json.RawMessage{}
	if t := bytes.TrimSpace(raw); len(t) > 0 && !bytes.Equal(t, []byte("null")) {
		if t[0] != '{' {
			return nil, invalid("", "arguments must be a JSON object")
		}
		if err := json.Unmarshal(t, &obj); err != nil {
			return nil, invalid("", "arguments: %v", err)
		}
	}
	unsupported := func(name string) bool { return v != nil && slices.Contains(v.Unsupported, name) }

	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	given := map[string]bool{}
	vals := values{}
	for _, k := range keys {
		p, ok := s.byName[k]
		if !ok || unsupported(k) {
			return nil, invalid(k, "unknown argument %q", k)
		}
		rv := bytes.TrimSpace(obj[k])
		if bytes.Equal(rv, []byte("null")) {
			continue
		}
		val, err := p.decodeJSON(rv)
		if err != nil {
			return nil, invalid(k, "%v", err)
		}
		vals[k] = val
		given[k] = true
	}
	for _, p := range s.Params {
		if _, ok := vals[p.Name]; ok || unsupported(p.Name) {
			continue
		}
		switch {
		case p.def != nil:
			vals[p.Name] = p.def
		case p.Required || p.Type == TypePath:
			return nil, invalid(p.Name, "missing required argument %q", p.Name)
		case p.Type == TypeBool:
			vals[p.Name] = false
		}
	}
	for _, group := range s.Exclusive {
		var set []string
		for _, n := range group {
			if given[n] {
				set = append(set, n)
			}
		}
		if len(set) > 1 {
			return nil, invalid(set[1], "arguments %s are mutually exclusive", strings.Join(set, ", "))
		}
	}
	if s.Script != nil {
		if err := s.Script.check(vals); err != nil {
			return nil, err
		}
	}
	return vals, nil
}

// decodeJSON converts one JSON argument to the param's typed value.
func (p *Param) decodeJSON(raw json.RawMessage) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if n, ok := v.(json.Number); ok {
		i, err := n.Int64()
		if err != nil {
			return nil, fmt.Errorf("must be an integer, got %s", n)
		}
		v = i
	}
	return p.coerce(v)
}

// coerce checks a decoded value (from JSON or YAML) against the param's
// type and constraints and returns it in canonical Go form.
func (p *Param) coerce(v any) (any, error) {
	switch p.Type {
	case TypePath:
		return p.coercePath(v)
	case TypeBool:
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("must be a boolean")
		}
		return b, nil
	case TypeEnum:
		str, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("must be a string")
		}
		if _, ok := p.Values[str]; !ok {
			return nil, fmt.Errorf("must be one of %s", strings.Join(p.enumValues(), ", "))
		}
		return str, nil
	case TypeInt:
		i, ok := toInt64(v)
		if !ok {
			return nil, fmt.Errorf("must be an integer")
		}
		if (p.Min != nil && i < *p.Min) || (p.Max != nil && i > *p.Max) {
			return nil, fmt.Errorf("must be in %d..%d", deref(p.Min), deref(p.Max))
		}
		return i, nil
	case TypeString:
		str, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("must be a string")
		}
		if n := utf8.RuneCountInString(str); p.MaxLen > 0 && n > p.MaxLen {
			return nil, fmt.Errorf("longer than %d characters", p.MaxLen)
		}
		if strings.ContainsRune(str, 0) {
			return nil, fmt.Errorf("contains NUL")
		}
		if !p.Newline && strings.ContainsAny(str, "\n\r") {
			return nil, fmt.Errorf("contains a newline")
		}
		if p.re != nil && !p.re.MatchString(str) {
			return nil, fmt.Errorf("does not match %s", p.Pattern)
		}
		return str, nil
	}
	return nil, fmt.Errorf("unsupported type %q", p.Type)
}

func (p *Param) coercePath(v any) (any, error) {
	one := func(x any) (string, error) {
		str, ok := x.(string)
		switch {
		case !ok:
			return "", fmt.Errorf("must be a path string")
		case str == "":
			return "", fmt.Errorf("empty path")
		case strings.ContainsRune(str, 0):
			return "", fmt.Errorf("path contains NUL")
		}
		return str, nil
	}
	if !p.Repeated {
		str, err := one(v)
		if err != nil {
			return nil, err
		}
		return []string{str}, nil
	}
	list, ok := v.([]any)
	if !ok {
		if strs, isStrs := v.([]string); isStrs {
			for _, s := range strs {
				list = append(list, s)
			}
		} else {
			return nil, fmt.Errorf("must be an array of path strings")
		}
	}
	if len(list) == 0 || len(list) > p.MaxItems {
		return nil, fmt.Errorf("must hold 1..%d paths, got %d", p.MaxItems, len(list))
	}
	out := make([]string, 0, len(list))
	for _, x := range list {
		str, err := one(x)
		if err != nil {
			return nil, err
		}
		out = append(out, str)
	}
	return out, nil
}

func (p *Param) enumValues() []string {
	out := make([]string, 0, len(p.Values))
	for v := range p.Values {
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}

func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case uint64:
		if n > 1<<63-1 {
			return 0, false
		}
		return int64(n), true
	case float64:
		if n != float64(int64(n)) {
			return 0, false
		}
		return int64(n), true
	}
	return 0, false
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// matches reports whether every condition in when equals the call's
// effective value. Absent values never match.
func (s *Spec) matches(when map[string]any, vals values) bool {
	for name, want := range when {
		p, ok := s.byName[name]
		if !ok {
			return false
		}
		cw, err := p.coerce(want)
		if err != nil {
			return false
		}
		got, ok := vals[name]
		if !ok || !reflect.DeepEqual(got, cw) {
			return false
		}
	}
	return true
}

// effectiveSideEffect applies side_effect_if: first match wins.
func (s *Spec) effectiveSideEffect(vals values) string {
	for _, e := range s.SideEffectIf {
		if s.matches(e.When, vals) {
			return e.SideEffect
		}
	}
	return s.SideEffect
}

// recursive reports whether p recurses on this call.
func (s *Spec) recursive(p *Param, vals values) bool {
	if p.Recursive != nil {
		return *p.Recursive
	}
	return p.RecursiveWhen != nil && s.matches(p.RecursiveWhen, vals)
}

// parents reports whether p creates missing ancestors on this call.
func (s *Spec) parents(p *Param, vals values) bool {
	if p.Parents != nil {
		return *p.Parents
	}
	return p.ParentsWhen != nil && s.matches(p.ParentsWhen, vals)
}
