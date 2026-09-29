package gate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// recurse applies each argument's recursion mode. A tree under a value
// the scope does not simply allow is still walked (its entries join
// the question), but what the walk finds wrong is kept for after
// approval.
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

// keep keeps err for v until the scope lets the model see it, or
// returns it now when v is clear.
func (a *audit) keep(v *value, err *Error) *Error {
	if a.clear(v) {
		return err
	}
	if v.err == nil {
		v.err = err
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
		if v.err != nil {
			continue // the call fails on the value's own error
		}
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
			if e := a.keep(v, &Error{Kind: KindInvalidArgs, Param: param, Path: root, Op: st.arg.Op,
				Message: fmt.Sprintf("more than %d entries under it; narrow the path", a.g.maxWalk)}); e != nil {
				return e
			}
			continue
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
		if v.err != nil {
			continue // the call fails on the value's own error
		}
		dest := v.entry.path
		var offenders []finding
		record := func(path string) {
			for _, bit := range opBits(arg.Op) {
				verdict, reason, public := a.g.scope.classify(path, bit)
				f := finding{param: arg.Param, path: path, op: bit, reason: reason, id: -1, show: path, showOp: bit, public: public}
				if verdict == VerdictDeny {
					offenders = append(offenders, f)
				} else {
					a.add(verdict, f)
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
				reason := "cannot be read: " + err.Error()
				offenders = append(offenders, finding{param: arg.Param, path: path, op: arg.Op, reason: reason, id: -1, show: path, showOp: arg.Op, public: reason})
				return nil
			}
			if mapTo != "" {
				path = mapTo + path[len(walkRoot):]
			}
			record(path)
			return nil
		})
		var refusal *Error
		switch {
		case errors.Is(err, errWalkCap):
			refusal = &Error{Kind: KindDenied, Param: arg.Param, Path: dest, Op: arg.Op,
				Message: fmt.Sprintf("more than %d entries under it; recursive changes are checked entry by entry and this tree is too large", a.g.maxWalk)}
		case err != nil:
			return err
		case len(offenders) > 0:
			refusal = offendersError(arg, dest, offenders)
		}
		if refusal != nil {
			a.denyErr = a.keep(v, refusal)
			if a.denyErr != nil {
				return nil
			}
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
//
// Warn-mode hits are logged at most once per call: entries arrive one
// at a time with no end-of-call signal, so the first entry the call's
// own warning does not already cover is logged, and later ones are not.
func (a *audit) outputFilter(args []PathArg) func(string) bool {
	var (
		warnOnce sync.Once
		prior    = append([]finding(nil), a.warns...)
	)
	return func(abs string) bool {
		if !filepath.IsAbs(abs) {
			return false
		}
		target, err := physical(abs, len(abs), nil)
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
		entry.warns = slices.DeleteFunc(entry.warns, func(f finding) bool { return warnedUnder(prior, f) })
		if len(entry.warns) > 0 {
			warnOnce.Do(func() {
				entry.logWarningsAs("scope: output entry not allowed (warn mode, allowing; later entries of this call are not logged)")
			})
		}
		return true
	}
}

// warnedUnder reports whether f repeats a warning already logged for
// the call: same reason, on the same path or a directory above it.
func warnedUnder(prior []finding, f finding) bool {
	for _, p := range prior {
		if p.reason != f.reason {
			continue
		}
		dir := p.path
		if !strings.HasSuffix(dir, "/") {
			dir += "/"
		}
		if f.path == p.path || strings.HasPrefix(f.path, dir) {
			return true
		}
	}
	return false
}
