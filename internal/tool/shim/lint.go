package shim

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"hop.top/kit/go/core/scope"
)

// launchers are binaries that run other programs from their arguments;
// a spec pinning one would turn argv values into code.
var launchers = []string{
	"sh", "bash", "zsh", "dash", "ksh", "mksh", "csh", "tcsh", "fish",
	"env", "xargs", "sudo", "doas", "su", "nohup", "nice", "timeout",
	"stdbuf", "chroot", "setsid", "unshare", "nsenter", "busybox",
	"perl", "python", "python3", "ruby", "node", "osascript", "script",
	"watch", "parallel", "exec", "eval", "time", "command", "builtin",
}

type linter struct{ probs []string }

func (l *linter) addf(format string, args ...any) {
	l.probs = append(l.probs, fmt.Sprintf(format, args...))
}

func (s *Spec) lint() []string {
	l := &linter{}
	if s.Version != SpecVersion {
		l.addf("spec: version %d not supported (want %d)", s.Version, SpecVersion)
	}
	if !nameRe.MatchString(s.Name) {
		l.addf("name %q must match %s", s.Name, nameRe)
	}
	if strings.TrimSpace(s.Description) == "" {
		l.addf("description is required")
	}
	s.lintCommand(l)
	s.lintEffects(l)
	s.lintParams(l)
	s.lintScript(l)
	s.lintTemplate(l)
	s.lintVariants(l)
	s.lintExclusive(l)
	return l.probs
}

func (s *Spec) lintCommand(l *linter) {
	if len(s.Command.Bin) == 0 {
		l.addf("command.bin is required")
	}
	for _, b := range s.Command.Bin {
		if !strings.HasPrefix(b, "/") || path.Clean(b) != b {
			l.addf("command.bin %q must be a clean absolute path", b)
			continue
		}
		if slices.Contains(launchers, path.Base(b)) {
			l.addf("command.bin %q is a program launcher", b)
		}
	}
}

func (s *Spec) lintEffects(l *linter) {
	if !validSideEffect(s.SideEffect) {
		l.addf("side_effect %q must be read, write or destructive", s.SideEffect)
	}
	switch s.Network {
	case "":
		s.Network = "none"
	case "none", "local-only", "egress":
	default:
		l.addf("network %q must be none, local-only or egress", s.Network)
	}
	s.timeout = defaultTimeout
	if s.Timeout != "" {
		d, err := time.ParseDuration(s.Timeout)
		switch {
		case err != nil:
			l.addf("timeout: %v", err)
		case d <= 0 || d > maxTimeout:
			l.addf("timeout %s must be in (0, %s]", d, maxTimeout)
		default:
			s.timeout = d
		}
	}
	if len(s.OkExitCodes) == 0 {
		s.OkExitCodes = []int{0}
	}
	for _, c := range s.OkExitCodes {
		if c < 0 || c > 255 {
			l.addf("ok_exit_codes: %d out of range", c)
		}
	}
	switch s.Output {
	case "":
		s.Output = OutputText
	case OutputText, OutputPaths0:
	default:
		l.addf("output %q must be text or paths0", s.Output)
	}
}

func validSideEffect(v string) bool {
	return v == "read" || v == "write" || v == "destructive"
}

func (s *Spec) lintParams(l *linter) {
	s.byName = make(map[string]*Param, len(s.Params))
	var params []*Param
	for i, p := range s.Params {
		if p == nil {
			l.addf("params[%d] is empty", i)
			continue
		}
		if !nameRe.MatchString(p.Name) {
			l.addf("param name %q must match %s", p.Name, nameRe)
			continue
		}
		if p.Name == scriptPlaceholder {
			l.addf("param name %q is reserved", p.Name)
		}
		if _, dup := s.byName[p.Name]; dup {
			l.addf("param %q declared twice", p.Name)
			continue
		}
		s.byName[p.Name] = p
		params = append(params, p)
	}
	filterBefore, filterAfter := 0, 0
	for _, p := range params {
		if strings.TrimSpace(p.Description) == "" {
			l.addf("param %q: description is required", p.Name)
		}
		if p.Required && !p.Default.IsZero() {
			l.addf("param %q: required and default are exclusive", p.Name)
		}
		switch p.Type {
		case TypePath:
			p.lintPath(l, s)
			if p.Recursion == RecursionFilterBefore {
				filterBefore++
			}
			if p.Recursion == RecursionFilterAfter {
				filterAfter++
			}
		case TypeBool:
			p.lintBool(l)
		case TypeEnum:
			p.lintEnum(l)
		case TypeInt:
			p.lintInt(l)
		case TypeString:
			p.lintString(l)
		default:
			l.addf("param %q: type %q must be path, bool, enum, int or string", p.Name, p.Type)
		}
		if p.Type != TypePath {
			p.lintNotPath(l)
		}
	}
	if filterBefore > 1 {
		l.addf("at most one path param may use recursion filter_before")
	}
	if s.Output == OutputPaths0 && filterAfter == 0 {
		l.addf("output paths0 needs a path param with recursion filter_after")
	}
	for _, e := range s.SideEffectIf {
		if !validSideEffect(e.SideEffect) {
			l.addf("side_effect_if: side_effect %q invalid", e.SideEffect)
		}
		s.lintWhen(l, "side_effect_if", e.When)
	}
}

// lintWhen checks a {param: value} condition against declared params.
func (s *Spec) lintWhen(l *linter, where string, when map[string]any) {
	if len(when) == 0 {
		l.addf("%s: empty when", where)
	}
	for name, v := range when {
		p, ok := s.byName[name]
		if !ok {
			l.addf("%s: unknown param %q", where, name)
			continue
		}
		if _, err := p.coerce(v); err != nil {
			l.addf("%s: param %q: %v", where, name, err)
		}
	}
}

func (p *Param) lintNotPath(l *linter) {
	if len(p.Op) > 0 || p.Repeated || p.MaxItems != 0 || p.MustExist || p.Kind != "" ||
		p.Target != "" || p.IntoDir || p.Recursive != nil || p.RecursiveWhen != nil ||
		p.Recursion != "" || p.Parents != nil || p.ParentsWhen != nil || p.ClobberWhen != nil {
		l.addf("param %q: path keys on a %s param", p.Name, p.Type)
	}
	if p.Type != TypeEnum && p.Values != nil {
		l.addf("param %q: values only on enum", p.Name)
	}
	if p.Type != TypeInt && (p.Min != nil || p.Max != nil) {
		l.addf("param %q: min/max only on int", p.Name)
	}
	if p.Type != TypeString && (p.MaxLen != 0 || p.Pattern != "" || p.Newline) {
		l.addf("param %q: max_len/pattern/newline only on string", p.Name)
	}
}

func (p *Param) lintPath(l *linter, s *Spec) {
	if len(p.Op) == 0 {
		l.addf("param %q: op is required", p.Name)
	}
	for _, o := range p.Op {
		switch o {
		case "read":
			p.op |= scope.Read
		case "write":
			p.op |= scope.Write
		case "exec":
			p.op |= scope.Exec
		default:
			l.addf("param %q: op %q must be read, write or exec", p.Name, o)
		}
	}
	if p.Repeated {
		if p.MaxItems == 0 {
			p.MaxItems = defaultMaxItems
		}
		if p.MaxItems < 1 || p.MaxItems > maxMaxItems {
			l.addf("param %q: max_items %d must be in 1..%d", p.Name, p.MaxItems, maxMaxItems)
		}
	} else if p.MaxItems != 0 {
		l.addf("param %q: max_items needs repeated", p.Name)
	}
	switch p.Kind {
	case "", "file", "dir", "any":
	default:
		l.addf("param %q: kind %q must be file, dir or any", p.Name, p.Kind)
	}
	switch p.Target {
	case "":
		p.Target = "follow"
	case "follow", "dirent":
	default:
		l.addf("param %q: target %q must be follow or dirent", p.Name, p.Target)
	}
	if !p.Argv.IsZero() {
		l.addf("param %q: path params take no argv; use the {%s} placeholder after --", p.Name, p.Name)
	}
	if p.Recursive != nil && p.RecursiveWhen != nil {
		l.addf("param %q: recursive and recursive_when are exclusive", p.Name)
	}
	if p.RecursiveWhen != nil {
		s.lintWhen(l, "param "+p.Name+" recursive_when", p.RecursiveWhen)
	}
	mayRecurse := (p.Recursive != nil && *p.Recursive) || p.RecursiveWhen != nil
	switch {
	case mayRecurse && p.Recursion == "":
		l.addf("param %q: recursive needs recursion: filter_before, filter_after or all_or_nothing", p.Name)
	case !mayRecurse && p.Recursion != "":
		l.addf("param %q: recursion without recursive/recursive_when", p.Name)
	}
	switch p.Recursion {
	case "", RecursionAllOrNothing:
	case RecursionFilterBefore:
		if p.op != scope.Read || p.Target != "follow" {
			l.addf("param %q: filter_before needs op [read] and target follow", p.Name)
		}
	case RecursionFilterAfter:
		if s.Output != OutputPaths0 {
			l.addf("param %q: filter_after needs output paths0", p.Name)
		}
	default:
		l.addf("param %q: recursion %q unknown", p.Name, p.Recursion)
	}
	if p.Parents != nil && p.ParentsWhen != nil {
		l.addf("param %q: parents and parents_when are exclusive", p.Name)
	}
	if p.ParentsWhen != nil {
		s.lintWhen(l, "param "+p.Name+" parents_when", p.ParentsWhen)
	}
	if p.ClobberWhen != nil {
		if p.op&scope.Write == 0 {
			l.addf("param %q: clobber_when needs op write", p.Name)
		}
		s.lintWhen(l, "param "+p.Name+" clobber_when", p.ClobberWhen)
	}
	p.lintDefault(l)
}

func (p *Param) lintBool(l *linter) {
	if !p.Argv.IsZero() {
		var m map[string][]string
		if err := p.Argv.Decode(&m); err != nil {
			l.addf("param %q: argv must map true/false to token lists: %v", p.Name, err)
		} else if bm, err := boolFragments(m); err != nil {
			l.addf("param %q: %v", p.Name, err)
		} else {
			p.boolArgv = bm
		}
	}
	p.lintDefault(l)
}

func boolFragments(m map[string][]string) (map[bool][]string, error) {
	out := make(map[bool][]string, 2)
	for k, frag := range m {
		var b bool
		switch k {
		case "true":
			b = true
		case "false":
		default:
			return nil, fmt.Errorf("argv key %q must be true or false", k)
		}
		if err := literalFragment(frag); err != nil {
			return nil, err
		}
		out[b] = frag
	}
	return out, nil
}

func (p *Param) lintEnum(l *linter) {
	if len(p.Values) == 0 {
		l.addf("param %q: enum needs values", p.Name)
	}
	for v, frag := range p.Values {
		if !enumValueRe.MatchString(v) {
			l.addf("param %q: enum value %q must match %s", p.Name, v, enumValueRe)
		}
		if err := literalFragment(frag); err != nil {
			l.addf("param %q: value %q: %v", p.Name, v, err)
		}
	}
	if !p.Argv.IsZero() {
		l.addf("param %q: enum maps values to argv under values, not argv", p.Name)
	}
	p.lintDefault(l)
}

func (p *Param) lintInt(l *linter) {
	if p.Min == nil || p.Max == nil {
		l.addf("param %q: int needs min and max", p.Name)
	} else if *p.Min > *p.Max {
		l.addf("param %q: min > max", p.Name)
	}
	p.lintValueFragment(l)
	p.lintDefault(l)
}

func (p *Param) lintString(l *linter) {
	if p.MaxLen < 1 || p.MaxLen > maxStringLen {
		l.addf("param %q: max_len must be in 1..%d", p.Name, maxStringLen)
	}
	if p.Pattern != "" {
		re, err := regexp.Compile(`^(?:` + p.Pattern + `)$`)
		if err != nil {
			l.addf("param %q: pattern: %v", p.Name, err)
		}
		p.re = re
	}
	p.lintValueFragment(l)
	p.lintDefault(l)
}

// lintValueFragment checks an int/string argv fragment: the value only
// ever lands as the argument of a literal option (`-e {}`) or joined
// to one (`--opt={}`), never as a bare token.
func (p *Param) lintValueFragment(l *linter) {
	if p.Argv.IsZero() {
		return
	}
	var frag []string
	if err := p.Argv.Decode(&frag); err != nil {
		l.addf("param %q: argv must be a token list: %v", p.Name, err)
		return
	}
	if err := valueFragment(frag); err != nil {
		l.addf("param %q: %v", p.Name, err)
		return
	}
	p.fragment = frag
}

func valueFragment(frag []string) error {
	if len(frag) == 0 {
		return fmt.Errorf("argv fragment is empty")
	}
	holes := 0
	for i, tok := range frag {
		if strings.Count(tok, "{}") > 1 {
			return fmt.Errorf("argv token %q has more than one {}", tok)
		}
		if !strings.Contains(tok, "{}") {
			if strings.ContainsAny(tok, "{}") {
				return fmt.Errorf("argv token %q: braces only as {}", tok)
			}
			continue
		}
		holes++
		switch {
		case tok == "{}":
			if i == 0 {
				return fmt.Errorf("value must follow a literal option token, not start the fragment")
			}
		case strings.HasPrefix(tok, "-") && strings.HasSuffix(tok, "={}") && i == 0 &&
			!strings.ContainsAny(strings.TrimSuffix(tok, "{}"), "{}"):
		default:
			return fmt.Errorf("argv token %q: value only as {} after an option or joined --opt={}", tok)
		}
	}
	if holes != 1 {
		return fmt.Errorf("argv fragment needs exactly one {}")
	}
	if first := frag[0]; !strings.HasPrefix(first, "-") || first == "-" || first == "--" {
		return fmt.Errorf("argv fragment must start with a literal option token, got %q", first)
	}
	return nil
}

func literalFragment(frag []string) error {
	for _, tok := range frag {
		if strings.ContainsAny(tok, "{}") {
			return fmt.Errorf("argv token %q: fixed fragments take no placeholders", tok)
		}
		if strings.ContainsRune(tok, 0) {
			return fmt.Errorf("argv token contains NUL")
		}
	}
	return nil
}

func (p *Param) lintDefault(l *linter) {
	if p.Default.IsZero() {
		return
	}
	var raw any
	if err := p.Default.Decode(&raw); err != nil {
		l.addf("param %q: default: %v", p.Name, err)
		return
	}
	v, err := p.coerce(raw)
	if err != nil {
		l.addf("param %q: default: %v", p.Name, err)
		return
	}
	p.def = v
}
