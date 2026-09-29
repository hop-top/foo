package gate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"hop.top/kit/go/ai/toolspec/policy"
	"hop.top/kit/go/core/scope"
)

// verdict is a scope decision after the policy mode is applied.
type verdict int

const (
	allow verdict = iota
	warn          // denied or uncovered, but warn mode lets it run
	ask           // denied or uncovered, prompt mode asks
	deny
)

// uncoveredReason explains a path no scope rule covers.
const uncoveredReason = "no scope rule covers this path"

// maxOffenders is how many denied tree entries an error lists.
const maxOffenders = 5

// finding is one (path, op) that did not simply pass.
type finding struct {
	param  string
	path   string
	op     scope.Op
	reason string
}

// audit collects the scope findings of one call.
type audit struct {
	g     *Gate
	ctx   context.Context
	tool  string
	denys []finding
	asks  []finding
	warns []finding
	// denyErr overrides the error built from denys (tree offenders).
	denyErr *Error
}

// verdictOf classifies (path, op): a deny rule wins, then an allow
// rule. A path no rule covers is treated like a denied one: denied in
// strict mode, asked about in prompt mode, logged in warn mode. kit
// allows such paths outside strict mode; foo does not let a gap in the
// rules run silently.
func (a *audit) verdictOf(path string, op scope.Op) (verdict, string) {
	pol := a.g.scope.Policy
	mode := pol.Mode()
	dec, err := pol.Check(scope.Path(path), op)
	if err != nil {
		if mode == scope.Strict {
			return deny, "cannot be checked: " + err.Error()
		}
		return warn, "cannot be checked: " + err.Error()
	}
	switch dec {
	case scope.Allowed:
		return allow, ""
	case scope.Denied:
		reason := fmt.Sprintf("matches a scope deny rule for %s", opName(op))
		switch mode {
		case scope.Warn:
			return warn, reason
		case scope.Prompt:
			return ask, reason
		default:
			return deny, reason
		}
	default:
		switch mode {
		case scope.Warn:
			return warn, uncoveredReason
		case scope.Prompt:
			return ask, uncoveredReason
		default:
			return deny, fmt.Sprintf("no scope allow rule covers %s here", opName(op))
		}
	}
}

// check records the verdict of every op bit of (path, op).
func (a *audit) check(param, path string, op scope.Op) {
	for _, bit := range opBits(op) {
		v, reason := a.verdictOf(path, bit)
		f := finding{param: param, path: path, op: bit, reason: reason}
		switch v {
		case deny:
			a.denys = append(a.denys, f)
		case ask:
			a.asks = append(a.asks, f)
		case warn:
			a.warns = append(a.warns, f)
		}
	}
}

// passes reports whether every bit of op is allowed on path without
// asking, recording warn-mode hits. Used where asking per entry is
// not possible (filtered walks, output filters).
func (a *audit) passes(param, path string, op scope.Op) bool {
	var warns []finding
	for _, bit := range opBits(op) {
		v, reason := a.verdictOf(path, bit)
		switch v {
		case allow:
		case warn:
			warns = append(warns, finding{param: param, path: path, op: bit, reason: reason})
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
		if arg.Target == Dirent {
			a.check(arg.Param, v.parent.path, scope.Write)
			if arg.Parents {
				for _, anc := range missingAncestors(v.parent) {
					a.check(arg.Param, anc, scope.Write)
				}
			}
			a.check(arg.Param, v.entry.path, arg.Op)
			a.checkLinkTarget(arg.Param, v.entry.path, arg.Op)
			continue
		}
		a.check(arg.Param, v.entry.path, arg.Op)
		if arg.Parents {
			for _, anc := range missingAncestors(v.entry) {
				a.check(arg.Param, anc, scope.Write)
			}
		}
	}
}

// checkLinkTarget checks the physical target of path when path is a
// symlink.
func (a *audit) checkLinkTarget(param, path string, op scope.Op) {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		return
	}
	target, err := physical(path)
	if err != nil {
		a.denys = append(a.denys, finding{param: param, path: path, op: op, reason: "link target cannot be resolved: " + err.Error()})
		return
	}
	a.check(param, target.path, op)
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
	msg := f.reason
	if n := countOthers(a.denys); n > 0 {
		msg += fmt.Sprintf(" (and %d more denied paths)", n)
	}
	return &Error{Kind: KindDenied, Param: f.param, Path: f.path, Op: f.op, Message: msg}
}

// countOthers counts distinct paths denied besides the first.
func countOthers(fs []finding) int {
	seen := map[string]bool{fs[0].path: true}
	for _, f := range fs[1:] {
		seen[f.path] = true
	}
	return len(seen) - 1
}

// recurse applies each argument's recursion mode.
func (a *audit) recurse(args []argState, grant *Grant) error {
	var after []PathArg
	for _, st := range args {
		switch st.arg.Recursion {
		case FilterBefore:
			if err := a.filterBefore(st, grant); err != nil {
				return err
			}
		case FilterAfter:
			after = append(after, st.arg)
		case AllOrNothing:
			if err := a.allOrNothing(st); err != nil {
				return err
			}
		}
		if a.denyErr != nil {
			return nil
		}
	}
	if len(after) > 0 {
		grant.Allow = a.outputFilter(after)
	}
	return nil
}

// errWalkCap stops a walk that exceeded max_walk.
var errWalkCap = errors.New("walk cap exceeded")

// walk visits every entry under root (root excluded) without following
// symlinks, stopping after max_walk entries.
func (a *audit) walk(root string, visit func(path string, d fs.DirEntry, err error) error) error {
	n := 0
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if cerr := a.ctx.Err(); cerr != nil {
			return cerr
		}
		if path == root {
			if err != nil && !isMissing(err) {
				return visit(path, d, err)
			}
			return nil
		}
		n++
		if n > a.g.maxWalk {
			return errWalkCap
		}
		return visit(path, d, err)
	})
}

// filterBefore grants the allowed regular files under each root.
// Denied files and directories are withheld and counted; symlinks and
// special files are skipped, as grep -r does.
func (a *audit) filterBefore(st argState, grant *Grant) error {
	param := st.arg.Param
	if grant.Files == nil {
		grant.Files = map[string][]string{}
	}
	for _, v := range st.values {
		root := v.entry.path
		fi, err := os.Lstat(root)
		if err != nil {
			continue // the command reports the missing path
		}
		if !fi.IsDir() {
			if fi.Mode().IsRegular() {
				grant.Files[param] = append(grant.Files[param], root)
			}
			continue
		}
		err = a.walk(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			switch {
			case d.IsDir():
				if !a.passes(param, path, st.arg.Op) {
					grant.Filtered++
					return fs.SkipDir
				}
			case d.Type().IsRegular():
				if a.passes(param, path, st.arg.Op) {
					grant.Files[param] = append(grant.Files[param], path)
				} else {
					grant.Filtered++
				}
			}
			return nil
		})
		if errors.Is(err, errWalkCap) {
			return &Error{Kind: KindInvalidArgs, Param: param, Path: root, Op: st.arg.Op,
				Message: fmt.Sprintf("more than %d entries under it; narrow the path", a.g.maxWalk)}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// allOrNothing checks every entry of each tree; one denied entry
// denies the call. An IntoDir argument checks the source tree mapped
// under the destination instead of its own tree.
func (a *audit) allOrNothing(st argState) error {
	arg := st.arg
	for _, v := range st.values {
		dest := v.entry.path
		var offenders []finding
		record := func(path string) {
			for _, bit := range opBits(arg.Op) {
				switch verdict, reason := a.verdictOf(path, bit); verdict {
				case deny:
					offenders = append(offenders, finding{param: arg.Param, path: path, op: bit, reason: reason})
				case ask:
					a.asks = append(a.asks, finding{param: arg.Param, path: path, op: bit, reason: reason})
				case warn:
					a.warns = append(a.warns, finding{param: arg.Param, path: path, op: bit, reason: reason})
				}
			}
		}
		walkRoot, mapTo := dest, ""
		if arg.IntoDir {
			if v.srcRoot == "" {
				continue
			}
			walkRoot, mapTo = v.srcRoot, dest
		}
		err := a.walk(walkRoot, func(path string, _ fs.DirEntry, err error) error {
			if err != nil {
				offenders = append(offenders, finding{param: arg.Param, path: path, op: arg.Op, reason: "cannot be read: " + err.Error()})
				return nil
			}
			if mapTo != "" {
				path = mapTo + path[len(walkRoot):]
			}
			record(path)
			return nil
		})
		if errors.Is(err, errWalkCap) {
			a.denyErr = &Error{Kind: KindDenied, Param: arg.Param, Path: dest, Op: arg.Op,
				Message: fmt.Sprintf("more than %d entries under it; recursive changes are checked entry by entry and this tree is too large", a.g.maxWalk)}
			return nil
		}
		if err != nil {
			return err
		}
		if len(offenders) > 0 {
			a.denyErr = offendersError(arg, dest, offenders)
			return nil
		}
	}
	return nil
}

func offendersError(arg PathArg, root string, offenders []finding) *Error {
	var paths []string
	seen := map[string]bool{}
	for _, f := range offenders {
		if !seen[f.path] {
			seen[f.path] = true
			paths = append(paths, f.path)
		}
	}
	shown := paths
	if len(shown) > maxOffenders {
		shown = shown[:maxOffenders]
	}
	msg := fmt.Sprintf("%d entries under it are not allowed for %s, so nothing was changed: %s",
		len(paths), opName(arg.Op), strings.Join(shown, ", "))
	if rest := len(paths) - len(shown); rest > 0 {
		msg += fmt.Sprintf(" and %d more", rest)
	}
	return &Error{Kind: KindDenied, Param: arg.Param, Path: root, Op: offenders[0].op, Message: msg}
}

// outputFilter checks one output entry of a FilterAfter call: the
// entry itself (kit follows an existing final link) and its physical
// target (a dangling link).
func (a *audit) outputFilter(args []PathArg) func(string) bool {
	return func(abs string) bool {
		if !filepath.IsAbs(abs) {
			return false
		}
		target, err := physical(abs)
		if err != nil {
			return false
		}
		// A fresh audit per entry: Allow runs after Authorize returned,
		// possibly concurrently.
		entry := &audit{g: a.g, ctx: context.Background(), tool: a.tool}
		for _, arg := range args {
			if !entry.passes(arg.Param, abs, arg.Op) || !entry.passes(arg.Param, target.path, arg.Op) {
				return false
			}
		}
		entry.logWarnings()
		return true
	}
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
			return &Error{Kind: KindDenied, Param: f.param, Path: f.path, Op: f.op,
				Message: fmt.Sprintf("%s and the scope policy asks before allowing it, but approval cannot be asked: %v", f.reason, err)}
		}
		return &Error{Kind: KindDeclined, Message: "approval required but cannot be asked: " + err.Error()}
	}
	if !ok {
		return &Error{Kind: KindDeclined, Message: "the user declined this call; do not retry it unchanged"}
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

func (a *audit) logWarnings() {
	for _, f := range a.warns {
		a.g.logger.Warn("scope: path not allowed (warn mode, allowing)",
			"tool", a.tool, "param", f.param, "path", f.path, "op", opName(f.op), "reason", f.reason)
	}
}
