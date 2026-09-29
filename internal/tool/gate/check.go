package gate

import (
	"io"
	"slices"
	"strings"

	"charm.land/log/v2"
	"hop.top/kit/go/core/scope"
)

// PathVerdict is the gate's verdict on one path argument, as
// `foo scope check` reports it.
type PathVerdict struct {
	// Path is where the value resolves: the canonical path a tool call
	// would run on, or the value cleaned lexically when it does not
	// resolve.
	Path string
	// Verdict is what the call does with the value.
	Verdict Verdict
	// Reason explains a verdict other than VerdictAllow. It is for the
	// user, not the model: it may be about where the path resolves.
	Reason string
}

// CheckPath returns the gate's verdict on raw, sent as the one value of
// a path argument that reads its content (Target Follow: the final
// symlink is followed, as for cat or ls) for op, in a call started in
// the absolute directory cwd. It runs the Authorizer's own resolution
// and scope checks on the value, so the verdict includes the refusals
// Classify alone cannot see: a ".." that climbs out of a directory the
// scope does not grant, a path that does not resolve there, and one
// that resolves through a symlink there unless an allow rule as
// written names it.
//
// The error is the value's own, when the scope lets the call reach it:
// raw is empty or holds a NUL, or it does not resolve where the scope
// grants. Without a scope.yaml every value is denied, as by Classify.
func (s Scope) CheckPath(cwd, raw string, op scope.Op) (PathVerdict, error) {
	g, err := New(WithScope(s), WithCwd(cwd), WithLogger(log.New(io.Discard)))
	if err != nil {
		return PathVerdict{}, err
	}
	arg := PathArg{Param: "path", Values: []string{raw}, Op: op, Target: Follow}
	args, err := g.resolveArgs(Request{Paths: []PathArg{arg}})
	if err != nil {
		return PathVerdict{}, err
	}
	v := args[0].values[0]
	out := PathVerdict{Path: v.entry.path}
	if v.unresolved {
		out.Path = v.lexical
	}
	if !s.Configured() {
		out.Verdict, out.Reason = s.Classify(out.Path, op)
		return out, nil
	}

	a := &audit{g: g, flagged: map[int]bool{}}
	a.checkArg(args[0])
	var worst []finding
	switch {
	case len(a.denys) > 0:
		out.Verdict, worst = VerdictDeny, a.denys
	case len(a.asks) > 0:
		out.Verdict, worst = VerdictPrompt, a.asks
	case len(a.warns) > 0:
		out.Verdict, worst = VerdictWarn, a.warns
	default:
		if v.err != nil {
			return out, v.err
		}
		return out, nil
	}
	var reasons []string
	for _, f := range worst {
		if !slices.Contains(reasons, f.reason) {
			reasons = append(reasons, f.reason)
		}
	}
	out.Reason = strings.Join(reasons, "; ")
	return out, nil
}
