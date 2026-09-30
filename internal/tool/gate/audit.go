package gate

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"hop.top/kit/go/ai/toolspec/policy"
	"hop.top/kit/go/core/scope"
)

// maxOffenders is how many denied tree entries an error lists.
const maxOffenders = 5

// finding is one (path, op) that did not simply pass.
type finding struct {
	param  string
	path   string
	op     scope.Op
	reason string
	// id is the value the finding is about; -1 for a tree entry.
	id int
	// show, showOp and public are what the model is told: the path,
	// op and reason, unless the value is opaque.
	show   string
	showOp scope.Op
	public string
}

// audit collects the scope findings of one call.
type audit struct {
	g     *Gate
	ctx   context.Context
	tool  string
	denys []finding
	asks  []finding
	warns []finding
	// flagged holds the ids of values with any finding: the scope does
	// not simply allow them.
	flagged map[int]bool
	// denyErr overrides the error built from denys (tree offenders).
	denyErr *Error
}

// verdictOf classifies (path, op) the way Scope.Classify does.
func (a *audit) verdictOf(path string, op scope.Op) (Verdict, string) {
	return a.g.scope.Classify(path, op)
}

// add files f under its verdict.
func (a *audit) add(v Verdict, f finding) {
	switch v {
	case VerdictAllow:
		return
	case VerdictDeny:
		a.denys = append(a.denys, f)
	case VerdictPrompt:
		a.asks = append(a.asks, f)
	case VerdictWarn:
		a.warns = append(a.warns, f)
	}
	if f.id >= 0 {
		a.flagged[f.id] = true
	}
}

// check records the verdict of every op bit of (path, op) for value v.
// lex is what a refusal names instead of path when v is opaque.
func (a *audit) check(v *value, param, path, lex string, op scope.Op) {
	for _, bit := range opBits(op) {
		verdict, reason, public := a.g.scope.classify(path, bit)
		if reason != public && !v.opaque && a.g.scope.grants(existing(path)) {
			public = reason // kit's error is about a place the model may see
		}
		f := finding{param: param, path: path, op: bit, reason: reason, id: v.id, show: path, showOp: bit, public: public}
		if v.opaque {
			f.show, f.showOp, f.public = lex, opBits(op)[0], uncovered(a.g.scope.Policy.Mode(), opBits(op)[0])
		}
		a.add(verdict, f)
	}
}

// Reasons for a refused value whose path, read lexically, the scope
// would allow: resolving it went through places the scope does not
// grant, so the model is not told what it found there.
const (
	climbReason   = `a ".." in it climbs out of a directory the scope does not grant; name the path without it`
	outsideReason = "it resolves through a place the scope does not grant"
)

// refuse records a value the scope cannot clear by its path alone (a
// ".." out of an ungranted directory, a path that does not resolve
// there, or one that resolves through a symlink there), as if no rule
// covered it. The model is told what it would be told for a missing
// path in the same place: the first check a resolved value gets, on
// the lexical path; or, when the rules as written allow the lexical
// path, why that is not enough. The rules are matched as written: a
// resolving check would follow the very links in question.
func (a *audit) refuse(v *value, arg PathArg, path, why string) {
	mode := a.g.scope.Policy.Mode()
	show, op := v.lexical, opBits(arg.Op)[0]
	public := ""
	switch {
	case a.g.scope.grantsAsWritten(v.lexical, arg.Op):
		show, public = v.abs, outsideReason
		if slices.Contains(strings.Split(v.abs, "/"), "..") {
			public = climbReason
		}
	case v.climb && !v.opaque && !v.unresolved:
		show = v.entry.path
	}
	if public == "" {
		if arg.Target == Dirent {
			show, op = filepath.Dir(show), scope.Write
		}
		public = uncovered(mode, op)
	}
	a.add(byMode(mode), finding{param: arg.Param, path: path, op: op, reason: why,
		id: v.id, show: show, showOp: op, public: public})
}

// passes reports whether every bit of op is allowed on path without
// asking, recording warn-mode hits. Used where asking per entry is
// not possible (filtered walks, output filters).
func (a *audit) passes(param, path string, op scope.Op) bool {
	var warns []finding
	for _, bit := range opBits(op) {
		v, reason := a.verdictOf(path, bit)
		switch v {
		case VerdictAllow:
		case VerdictWarn:
			warns = append(warns, finding{param: param, path: path, op: bit, reason: reason, id: -1})
		default:
			return false
		}
	}
	a.warns = append(a.warns, warns...)
	return true
}

// checkArg checks every value of one argument: dirent values need
// write on the parent and the op on the entry, and a final symlink's
// target is checked too (kit cannot follow a dangling one).
func (a *audit) checkArg(st argState) {
	arg := st.arg
	for _, v := range st.values {
		if v.climb {
			a.refuse(v, arg, v.lexical, "a \"..\" leaves a directory no scope rule grants")
		}
		if v.unresolved {
			// Why it does not resolve is the filesystem's business
			// unless the scope grants where resolution stopped.
			if !v.climb && (v.opaque || !a.g.scope.grants(v.entry.path)) {
				a.refuse(v, arg, v.entry.path, v.err.Message)
			}
			continue
		}
		if v.linked && !v.climb && !a.g.scope.grantsAsWritten(v.lexical, arg.Op) {
			// Checked on as well: approving it must not skip a deny
			// rule on where it resolves.
			a.refuse(v, arg, v.entry.path, "it resolves through a symlink in a directory no scope rule grants")
		}
		if arg.Target == Dirent {
			a.check(v, arg.Param, v.parent.path, filepath.Dir(v.lexical), scope.Write)
			if arg.Parents {
				for i, anc := range missingAncestors(v.parent) {
					a.check(v, arg.Param, anc, dirN(v.lexical, i+2), scope.Write)
				}
			}
			a.check(v, arg.Param, v.entry.path, v.lexical, arg.Op)
			a.checkLinkTarget(v, arg.Param, arg.Op)
			continue
		}
		a.check(v, arg.Param, v.entry.path, v.lexical, arg.Op)
		if arg.Parents {
			for i, anc := range missingAncestors(v.entry) {
				a.check(v, arg.Param, anc, dirN(v.lexical, i+1), scope.Write)
			}
		}
	}
}

// existing is the deepest ancestor of the absolute path p (p itself
// included) that exists.
func existing(p string) string {
	for {
		if _, err := os.Lstat(p); err == nil || p == "/" {
			return p
		}
		p = filepath.Dir(p)
	}
}

// dirN is the n-th lexical parent of p.
func dirN(p string, n int) string {
	for range n {
		p = filepath.Dir(p)
	}
	return p
}

// checkLinkTarget checks the physical target of a dirent value's entry
// when it is a symlink. What the link points to was read in the
// entry's directory, so refusals keep to the lexical path unless the
// scope grants that directory.
func (a *audit) checkLinkTarget(v *value, param string, op scope.Op) {
	path := v.entry.path
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		return
	}
	tv := *v
	tv.opaque = v.opaque || !a.g.scope.grants(v.parent.path)
	target, err := physical(path, len(path), nil)
	if err != nil {
		mode := a.g.scope.Policy.Mode()
		f := finding{param: param, path: path, op: op, reason: "link target cannot be resolved: " + err.Error(),
			id: v.id, show: path, showOp: op, public: "link target cannot be resolved: " + err.Error()}
		if tv.opaque {
			f.show, f.showOp, f.public = v.lexical, opBits(op)[0], uncovered(mode, opBits(op)[0])
		}
		a.add(byMode(mode), f)
		return
	}
	a.check(&tv, param, target.path, v.lexical, op)
}

// denial returns the call's scope denial, if any.
func (a *audit) denial() error {
	if a.denyErr != nil {
		return a.denyErr
	}
	if len(a.denys) == 0 {
		return nil
	}
	f := a.denys[0]
	msg := f.public
	if n := countOthers(a.denys); n > 0 {
		msg += fmt.Sprintf(" (and %d more denied paths)", n)
	}
	return &Error{Kind: KindDenied, Param: f.param, Path: f.show, Op: f.showOp, Message: msg}
}

// countOthers counts the other values (or tree entries) denied besides
// the first: values, not the paths checked for them, since how many
// paths a value needs checked depends on what exists.
func countOthers(fs []finding) int {
	key := func(f finding) string {
		if f.id >= 0 {
			return fmt.Sprintf("#%d", f.id)
		}
		return f.path
	}
	seen := map[string]bool{key(fs[0]): true}
	for _, f := range fs[1:] {
		seen[key(f)] = true
	}
	return len(seen) - 1
}

// clear reports whether the scope simply allows value v (and the
// source tree it receives).
func (a *audit) clear(v *value) bool {
	return !a.flagged[v.id] && (v.srcID < 0 || !a.flagged[v.srcID])
}

// later returns the first value error the scope lets the model see:
// before approval (before) for values the scope simply allows, after
// it for the rest.
func (a *audit) later(args []argState, before bool) error {
	for _, st := range args {
		for _, v := range st.values {
			if v.err != nil && a.clear(v) == before {
				return v.err
			}
		}
	}
	return nil
}

// approve asks once when the scope (prompt mode), the policy table or
// --tools-approve wants a confirmation.
func (a *audit) approve(req Request, canonical map[string][]string, dec policy.Decision) error {
	policyAsks := dec.Action == policy.ActionPrompt
	if len(a.asks) == 0 && !policyAsks && !a.g.approveAll {
		return nil
	}

	var q strings.Builder
	fmt.Fprintf(&q, "[tool] %s wants to run: %s\n", req.Tool, renderArgv(argvOf(req, canonical)))
	for i, f := range a.asks {
		if i == maxOffenders {
			fmt.Fprintf(&q, "  scope: and %d more\n", len(a.asks)-i)
			break
		}
		fmt.Fprintf(&q, "  scope: %s %s (%s)\n", opName(f.op), f.path, f.reason)
	}
	if policyAsks {
		fmt.Fprintf(&q, "  policy: %s side effect (%s)\n", sideEffectName(req.SideEffect), dec.Reason)
	}
	if a.g.approveAll {
		q.WriteString("  --tools-approve: confirm every tool call\n")
	}
	q.WriteString("Allow this call?")

	var (
		ok  bool
		err = errNoConfirmer
	)
	if a.g.confirm != nil {
		ok, err = a.g.confirm.Confirm(q.String())
	}
	if err != nil {
		if len(a.asks) > 0 {
			f := a.asks[0]
			return &Error{Kind: KindDenied, Param: f.param, Path: f.show, Op: f.showOp,
				Message: fmt.Sprintf("%s and the scope policy asks before allowing it, but approval cannot be asked: %v", f.public, err)}
		}
		return Declined(err)
	}
	if !ok {
		return Declined(nil)
	}
	return nil
}

func sideEffectName(se string) string {
	if se == "" {
		return "undeclared"
	}
	return se
}

// argvOf renders what will run, or a plain summary without an Argv
// callback.
func argvOf(req Request, canonical map[string][]string) []string {
	if req.Argv != nil {
		return req.Argv(canonical)
	}
	out := []string{req.Tool}
	for _, arg := range req.Paths {
		for _, p := range canonical[arg.Param] {
			out = append(out, arg.Param+"="+p)
		}
	}
	return out
}

// logWarnings logs the call's warn-mode hits as one warning: how many
// paths, the first few, and the ops and reasons involved. A recursive
// walk would otherwise log a line per file.
func (a *audit) logWarnings() {
	a.logWarningsAs("scope: paths not allowed (warn mode, allowing)")
}

func (a *audit) logWarningsAs(msg string) {
	if len(a.warns) == 0 {
		return
	}
	var paths, ops, reasons []string
	seen := map[string]bool{}
	add := func(list *[]string, kind, v string) {
		if !seen[kind+v] {
			seen[kind+v] = true
			*list = append(*list, v)
		}
	}
	for _, f := range a.warns {
		add(&paths, "p:", f.path)
		add(&ops, "o:", opName(f.op))
		add(&reasons, "r:", f.reason)
	}
	shown := paths
	if len(shown) > maxOffenders {
		shown = shown[:maxOffenders]
	}
	list := strings.Join(shown, ", ")
	if rest := len(paths) - len(shown); rest > 0 {
		list += fmt.Sprintf(" and %d more", rest)
	}
	a.g.logger.Warn(msg, "tool", a.tool, "count", len(paths), "paths", list,
		"op", strings.Join(ops, "|"), "reason", strings.Join(reasons, "; "))
}
