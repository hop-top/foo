package shim

import (
	"fmt"
	"strings"
)

func (s *Spec) lintScript(l *linter) {
	sc := s.Script
	if sc == nil {
		return
	}
	if sc.Kind != "sed_substitute" {
		l.addf("script: kind %q unknown", sc.Kind)
		return
	}
	use := func(name, typ string) {
		p, ok := s.byName[name]
		switch {
		case !ok:
			l.addf("script: unknown param %q", name)
		case p.Type != typ:
			l.addf("script: param %q must be %s", name, typ)
		case !p.Argv.IsZero():
			l.addf("script: param %q feeds the script and takes no argv", name)
		default:
			p.script = true
		}
	}
	use(sc.Find, TypeString)
	use(sc.Replace, TypeString)
	for name, flag := range sc.Flags {
		if flag != "g" && flag != "I" {
			l.addf("script: flag %q must be g or I", flag)
		}
		use(name, TypeBool)
	}
	if sc.Backrefs != "" {
		use(sc.Backrefs, TypeBool)
	}
	if sc.Occurrence != "" {
		use(sc.Occurrence, TypeInt)
		if p, ok := s.byName[sc.Occurrence]; ok && p.Min != nil && p.Max != nil &&
			(*p.Min < 1 || *p.Max > 512) {
			l.addf("script: occurrence bounds must be within 1..512")
		}
	}
}

func (s *Spec) lintExclusive(l *linter) {
	for _, group := range s.Exclusive {
		if len(group) < 2 {
			l.addf("exclusive: group %v needs two or more params", group)
		}
		for _, n := range group {
			if _, ok := s.byName[n]; !ok {
				l.addf("exclusive: unknown param %q", n)
			}
		}
	}
}

func (s *Spec) lintVariants(l *linter) {
	for flavor, v := range s.Command.Variants {
		if flavor != FlavorGNU && flavor != FlavorBSD && flavor != FlavorBusyBox {
			l.addf("variants: flavor %q must be gnu, bsd or busybox", flavor)
		}
		if err := literalFragment(v.Prefix); err != nil {
			l.addf("variants.%s.prefix: %v", flavor, err)
		}
		for _, c := range v.OkExitCodes {
			if c < 0 || c > 255 {
				l.addf("variants.%s.ok_exit_codes: %d out of range", flavor, c)
			}
		}
		for _, n := range v.Unsupported {
			p, ok := s.byName[n]
			switch {
			case !ok:
				l.addf("variants.%s.unsupported: unknown param %q", flavor, n)
			case p.Required || (p.Type == TypePath && p.def == nil):
				l.addf("variants.%s.unsupported: param %q is required", flavor, n)
			}
		}
		for n, o := range v.Params {
			p, ok := s.byName[n]
			if !ok {
				l.addf("variants.%s.params: unknown param %q", flavor, n)
				continue
			}
			s.lintOverride(l, flavor, p, &o)
			v.Params[n] = o
		}
	}
}

func (s *Spec) lintOverride(l *linter, flavor string, p *Param, o *ParamOverride) {
	where := fmt.Sprintf("variants.%s.params.%s", flavor, p.Name)
	switch p.Type {
	case TypeBool:
		var m map[string][]string
		if err := o.Argv.Decode(&m); err != nil {
			l.addf("%s: argv: %v", where, err)
			return
		}
		bm, err := boolFragments(m)
		if err != nil {
			l.addf("%s: %v", where, err)
		}
		o.boolArgv = bm
	case TypeEnum:
		for v, frag := range o.Values {
			if _, ok := p.Values[v]; !ok {
				l.addf("%s: value %q not declared", where, v)
			}
			if err := literalFragment(frag); err != nil {
				l.addf("%s: %v", where, err)
			}
		}
	case TypeInt, TypeString:
		var frag []string
		if err := o.Argv.Decode(&frag); err != nil {
			l.addf("%s: argv: %v", where, err)
			return
		}
		if err := valueFragment(frag); err != nil {
			l.addf("%s: %v", where, err)
		}
		o.fragment = frag
	default:
		l.addf("%s: %s params have no argv to override", where, p.Type)
	}
}

// lintTemplate enforces the argv template rules: placeholders name
// declared params, every path lands after a literal `--`, and option
// values never land after `--` (unless the command takes an expression
// after its paths, like find).
func (s *Spec) lintTemplate(l *linter) {
	if len(s.Argv) == 0 {
		l.addf("argv template is required")
		return
	}
	dashdash, firstPath, lastPath := -1, -1, -1
	seen := map[string]bool{}
	for i, tok := range s.Argv {
		name, isPh := placeholder(tok)
		if !isPh {
			if strings.ContainsAny(tok, "{}") {
				l.addf("argv token %q: braces only in whole-token placeholders", tok)
			}
			if tok == "--" && dashdash < 0 {
				dashdash = i
			}
			continue
		}
		if seen[name] {
			l.addf("argv: placeholder {%s} used twice", name)
		}
		seen[name] = true
		if name == scriptPlaceholder {
			if s.Script == nil {
				l.addf("argv: {script} without a script section")
			} else if i == 0 || !isOption(s.Argv[i-1]) || (dashdash >= 0 && i > dashdash) {
				l.addf("argv: {script} must follow a literal option token before --")
			}
			continue
		}
		p, ok := s.byName[name]
		if !ok {
			l.addf("argv: placeholder {%s} names no param", name)
			continue
		}
		switch p.Type {
		case TypePath:
			if dashdash < 0 {
				l.addf("argv: path {%s} must follow a literal --", name)
			}
			if firstPath < 0 {
				firstPath = i
			}
			lastPath = i
		case TypeInt, TypeString:
			if p.script {
				l.addf("argv: param %q feeds the script and cannot appear in argv", name)
			}
			if dashdash >= 0 && i > dashdash && !(s.ExpressionAfterPaths && lastPath >= 0) {
				l.addf("argv: option value {%s} after --", name)
			}
		case TypeBool, TypeEnum:
			if p.script {
				l.addf("argv: param %q feeds the script and cannot appear in argv", name)
			}
		}
	}
	for i := firstPath; firstPath >= 0 && i < len(s.Argv); i++ {
		name, isPh := placeholder(s.Argv[i])
		if isPh {
			if p, ok := s.byName[name]; ok && p.Type == TypePath {
				continue
			}
		}
		if !s.ExpressionAfterPaths {
			l.addf("argv: token %q after paths needs expression_after_paths", s.Argv[i])
			break
		}
		if i < lastPath {
			l.addf("argv: token %q between paths", s.Argv[i])
		}
	}
	if s.Script != nil && !seen[scriptPlaceholder] {
		l.addf("argv: script declared but {script} unused")
	}
	for _, p := range s.Params {
		if p == nil || p.script {
			continue
		}
		used := seen[p.Name]
		switch {
		case p.Type == TypePath && !used:
			l.addf("argv: path param %q never placed", p.Name)
		case (p.Type == TypeEnum || p.fragment != nil) && !used:
			l.addf("argv: param %q never placed", p.Name)
		case p.Type == TypeBool && p.boolArgv != nil && !used:
			l.addf("argv: param %q never placed", p.Name)
		case (p.Type == TypeInt || p.Type == TypeString) && p.fragment == nil:
			l.addf("param %q: %s params need an argv fragment", p.Name, p.Type)
		}
	}
}

func placeholder(tok string) (string, bool) {
	if len(tok) > 2 && tok[0] == '{' && tok[len(tok)-1] == '}' && !strings.ContainsAny(tok[1:len(tok)-1], "{}") {
		return tok[1 : len(tok)-1], true
	}
	return "", false
}

func isOption(tok string) bool {
	return strings.HasPrefix(tok, "-") && tok != "-" && tok != "--" && !strings.ContainsAny(tok, "{}")
}
