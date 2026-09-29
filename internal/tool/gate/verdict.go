package gate

import (
	"fmt"

	"hop.top/kit/go/core/scope"
)

// Verdict is what a tool call does with one (path, op): the scope
// decision after the policy mode is applied.
type Verdict int

const (
	// VerdictAllow runs the call: an allow rule covers the path.
	VerdictAllow Verdict = iota
	// VerdictWarn runs the call and logs a warning (mode: warn).
	VerdictWarn
	// VerdictPrompt asks before the call runs; with no terminal it is
	// denied (mode: prompt).
	VerdictPrompt
	// VerdictDeny refuses the call (mode: strict, or no scope.yaml).
	VerdictDeny
)

// String returns the verdict's label as `foo scope check` prints it.
func (v Verdict) String() string {
	switch v {
	case VerdictAllow:
		return "allowed"
	case VerdictWarn:
		return "warn"
	case VerdictPrompt:
		return "prompt"
	case VerdictDeny:
		return "denied"
	default:
		return fmt.Sprintf("verdict(%d)", int(v))
	}
}

// uncoveredReason explains a path no scope rule covers.
const uncoveredReason = "no scope rule covers this path"

// Classify returns the gate's verdict on one canonical path for one
// operation, with the reason when it is not VerdictAllow. The gate and
// `foo scope check` both decide here, so they cannot disagree.
//
// A deny rule wins, then an allow rule. A path no rule covers is
// treated like a denied one: denied in strict mode, asked about in
// prompt mode, logged in warn mode. kit allows such paths outside
// strict mode; foo does not let a gap in the rules run silently. A
// path kit cannot check (a component is a file, or unreadable) is
// handled the same way. Without a scope.yaml every path is denied.
func (s Scope) Classify(path string, op scope.Op) (Verdict, string) {
	v, reason, _ := s.classify(path, op)
	return v, reason
}

// classify is Classify plus the reason a model may be told. It differs
// only for a path kit cannot check: kit's error says what is on disk
// there (a file where a directory would be), so the model gets the
// reason of a path no rule covers instead.
func (s Scope) classify(path string, op scope.Op) (v Verdict, reason, public string) {
	if !s.Configured() || s.Policy == nil {
		reason = "no scope policy: no scope.yaml exists"
		return VerdictDeny, reason, reason
	}
	pol := s.Policy
	mode := pol.Mode()
	dec, err := pol.Check(scope.Path(path), op)
	if err != nil {
		return byMode(mode), "cannot be checked: " + err.Error(), uncovered(mode, op)
	}
	switch dec {
	case scope.Allowed:
		return VerdictAllow, "", ""
	case scope.Denied:
		reason = fmt.Sprintf("matches a scope deny rule for %s", opName(op))
	default:
		reason = uncovered(mode, op)
	}
	return byMode(mode), reason, reason
}

// uncovered explains a path no scope rule covers.
func uncovered(mode scope.Mode, op scope.Op) string {
	if mode == scope.Strict {
		return fmt.Sprintf("no scope allow rule covers %s here", opName(op))
	}
	return uncoveredReason
}

// grants reports whether an allow rule lets the model read or write
// dir: whatever it may learn by resolving a path there, it could learn
// with a tool call anyway.
func (s Scope) grants(dir string) bool {
	for _, op := range []scope.Op{scope.Read, scope.Write} {
		if v, _, _ := s.classify(dir, op); v == VerdictAllow {
			return true
		}
	}
	return false
}

// grantsAsWritten reports whether an allow rule, as the user wrote it,
// covers every bit of op on the absolute lexical path, deny rules
// winning: neither side resolved, so the answer never depends on the
// filesystem.
func (s Scope) grantsAsWritten(path string, op scope.Op) bool {
	if !s.Configured() || s.Policy == nil {
		return false
	}
	dec, err := s.Policy.CheckLexical(scope.Path(path), op)
	return err == nil && dec == scope.Allowed
}

// byMode maps a path that did not pass to what mode does with it.
func byMode(mode scope.Mode) Verdict {
	switch mode {
	case scope.Warn:
		return VerdictWarn
	case scope.Prompt:
		return VerdictPrompt
	default:
		return VerdictDeny
	}
}
