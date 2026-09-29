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
// strict mode; foo does not let a gap in the rules run silently.
// Without a scope.yaml every path is denied.
func (s Scope) Classify(path string, op scope.Op) (Verdict, string) {
	if !s.Configured() || s.Policy == nil {
		return VerdictDeny, "no scope policy: no scope.yaml exists"
	}
	pol := s.Policy
	mode := pol.Mode()
	dec, err := pol.Check(scope.Path(path), op)
	if err != nil {
		if mode == scope.Strict {
			return VerdictDeny, "cannot be checked: " + err.Error()
		}
		return VerdictWarn, "cannot be checked: " + err.Error()
	}
	switch dec {
	case scope.Allowed:
		return VerdictAllow, ""
	case scope.Denied:
		return byMode(mode), fmt.Sprintf("matches a scope deny rule for %s", opName(op))
	default:
		if mode == scope.Strict {
			return VerdictDeny, fmt.Sprintf("no scope allow rule covers %s here", opName(op))
		}
		return byMode(mode), uncoveredReason
	}
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
