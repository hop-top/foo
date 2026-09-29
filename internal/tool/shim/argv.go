package shim

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// sedDelim separates the parts of a generated sed s command. Inputs
// containing it are rejected, so a value can never end its own part.
const sedDelim = "\x01"

// check rejects script inputs the generator cannot render safely. A
// literal replace may end in a backslash: it is escaped like any other.
func (sc *Script) check(vals values) error {
	for _, name := range []string{sc.Find, sc.Replace} {
		str, _ := vals[name].(string)
		if strings.Contains(str, sedDelim) {
			return invalid(name, "contains the \\x01 control character")
		}
		if strings.ContainsAny(str, "\n\r\x00") {
			return invalid(name, "contains a newline or NUL")
		}
		if name == sc.Replace && !sc.backrefs(vals) {
			continue
		}
		if trailingBackslashes(str)%2 == 1 {
			return invalid(name, "ends with an unescaped backslash")
		}
	}
	return nil
}

// backrefs reports whether this call opted into sed's replacement
// syntax.
func (sc *Script) backrefs(vals values) bool {
	on, _ := vals[sc.Backrefs].(bool)
	return sc.Backrefs != "" && on
}

// sedLiteral escapes the only characters a sed replacement interprets
// besides newline (rejected) and the delimiter (rejected): backslash
// and &. BSD, GNU and busybox sed all read \\ as \ and \& as &, so
// the text lands verbatim, including GNU's \L \U \n \x41 sequences.
func sedLiteral(s string) string {
	if !strings.ContainsAny(s, `\&`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' || s[i] == '&' {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func trailingBackslashes(s string) int {
	n := 0
	for i := len(s) - 1; i >= 0 && s[i] == '\\'; i-- {
		n++
	}
	return n
}

// render builds the s command: s<D>find<D>replace<D>flags, with replace
// escaped to a literal unless backrefs is on. Flags come only from
// declared bools (g, I) and the occurrence int, so the w and e flags
// cannot be expressed.
func (sc *Script) render(vals values) string {
	find, _ := vals[sc.Find].(string)
	repl, _ := vals[sc.Replace].(string)
	if !sc.backrefs(vals) {
		repl = sedLiteral(repl)
	}
	var flags strings.Builder
	names := make([]string, 0, len(sc.Flags))
	for n := range sc.Flags {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		if on, _ := vals[n].(bool); on {
			flags.WriteString(sc.Flags[n])
		}
	}
	if sc.Occurrence != "" {
		if n, ok := vals[sc.Occurrence].(int64); ok {
			flags.WriteString(strconv.FormatInt(n, 10))
		}
	}
	return "s" + sedDelim + find + sedDelim + repl + sedDelim + flags.String()
}

// render expands the argv template. Path placeholders expand to the
// tokens in paths — the canonical values the authorizer granted, never
// the model's raw strings — and every one must be a clean absolute path.
func (s *Spec) render(bin string, v *Variant, vals values, paths map[string][]string) ([]string, error) {
	argv := []string{bin}
	if v != nil {
		argv = append(argv, v.Prefix...)
	}
	for _, tok := range s.Argv {
		name, isPh := placeholder(tok)
		if !isPh {
			argv = append(argv, tok)
			continue
		}
		if name == scriptPlaceholder {
			argv = append(argv, s.Script.render(vals))
			continue
		}
		p := s.byName[name]
		frag, err := p.expand(v, vals, paths)
		if err != nil {
			return nil, err
		}
		argv = append(argv, frag...)
	}
	return argv, nil
}

func (p *Param) expand(v *Variant, vals values, paths map[string][]string) ([]string, error) {
	var o *ParamOverride
	if v != nil {
		if po, ok := v.Params[p.Name]; ok {
			o = &po
		}
	}
	val, present := vals[p.Name]
	switch p.Type {
	case TypePath:
		toks, ok := paths[p.Name]
		if !ok {
			return nil, fmt.Errorf("no granted paths for %q", p.Name)
		}
		for _, t := range toks {
			if !filepath.IsAbs(t) || filepath.Clean(t) != t || !strings.HasPrefix(t, "/") {
				return nil, fmt.Errorf("granted path %q for %q is not clean and absolute", t, p.Name)
			}
		}
		return toks, nil
	case TypeBool:
		m := p.boolArgv
		if o != nil && o.boolArgv != nil {
			m = o.boolArgv
		}
		b, _ := val.(bool)
		return m[b], nil
	case TypeEnum:
		if !present {
			return nil, nil
		}
		str := val.(string)
		if o != nil {
			if frag, ok := o.Values[str]; ok {
				return frag, nil
			}
		}
		return p.Values[str], nil
	case TypeInt, TypeString:
		if !present {
			return nil, nil
		}
		frag := p.fragment
		if o != nil && o.fragment != nil {
			frag = o.fragment
		}
		var sval string
		if i, ok := val.(int64); ok {
			sval = strconv.FormatInt(i, 10)
		} else {
			sval = val.(string)
		}
		out := make([]string, len(frag))
		for i, t := range frag {
			out[i] = strings.Replace(t, "{}", sval, 1)
		}
		return out, nil
	}
	return nil, fmt.Errorf("param %q: unsupported type %q", p.Name, p.Type)
}

// argvSize approximates the kernel's accounting of one argv: bytes plus
// a NUL and a pointer per argument.
func argvSize(argv []string) int {
	n := 0
	for _, a := range argv {
		n += len(a) + 1 + 8
	}
	return n
}

// chunk splits list into runs whose argv stays within budget when
// rendered alongside the rest of the call (base bytes). A single entry
// larger than the budget still gets a run of its own.
func chunk(list []string, base, budget int) [][]string {
	var out [][]string
	var cur []string
	size := base
	for _, f := range list {
		n := len(f) + 1 + 8
		if len(cur) > 0 && size+n > budget {
			out = append(out, cur)
			cur, size = nil, base
		}
		cur = append(cur, f)
		size += n
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}
