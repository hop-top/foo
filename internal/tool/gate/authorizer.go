package gate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"charm.land/log/v2"
	"github.com/spf13/viper"
	"hop.top/kit/go/ai/toolspec/policy"
	kitlog "hop.top/kit/go/console/log"
	"hop.top/kit/go/core/scope"
)

// DefaultMaxWalk caps the entries one recursive path argument may walk.
const DefaultMaxWalk = 20000

// Confirmer asks the user a yes/no question. *tool.Prompter satisfies
// it; an error means the question could not be asked (no terminal).
type Confirmer interface {
	Confirm(question string) (bool, error)
}

// Gate is the Authorizer shim calls go through. Build it with New.
type Gate struct {
	scope      Scope
	cwd        string
	table      policy.Table
	confirm    Confirmer
	approveAll bool
	maxWalk    int
	logger     *log.Logger
}

var _ Authorizer = (*Gate)(nil)

// Option configures a Gate.
type Option func(*Gate)

// WithScope sets the path policy, normally from LoadScope. Without it,
// or when s is not Configured, every call is denied.
func WithScope(s Scope) Option { return func(g *Gate) { g.scope = s } }

// WithCwd sets the base for relative paths: the directory foo was run
// in, captured once at start. Default: the working directory at New.
func WithCwd(dir string) Option { return func(g *Gate) { g.cwd = dir } }

// WithPolicy sets the side-effect table, normally from LoadPolicy.
// Default: kit's table with foo's Overlay.
func WithPolicy(t policy.Table) Option { return func(g *Gate) { g.table = t } }

// WithConfirmer sets who is asked when a call needs approval. Without
// one, such calls are refused as if no terminal were available.
func WithConfirmer(c Confirmer) Option { return func(g *Gate) { g.confirm = c } }

// WithApproveAll asks before every call, even one the policy
// auto-allows (--tools-approve). It never overrides a denial.
func WithApproveAll(on bool) Option { return func(g *Gate) { g.approveAll = on } }

// WithMaxWalk caps the entries a recursive argument may walk.
func WithMaxWalk(n int) Option { return func(g *Gate) { g.maxWalk = n } }

// WithLogger sets where warn-mode scope hits are logged. Default:
// kit's logger over the global viper.
func WithLogger(l *log.Logger) Option { return func(g *Gate) { g.logger = l } }

// New builds a Gate.
func New(opts ...Option) (*Gate, error) {
	g := &Gate{table: policy.Merge(policy.Default(), Overlay()), maxWalk: DefaultMaxWalk}
	for _, opt := range opts {
		opt(g)
	}
	if g.cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("gate: working directory: %w", err)
		}
		g.cwd = wd
	}
	if !filepath.IsAbs(g.cwd) {
		return nil, fmt.Errorf("gate: cwd %q is not absolute", g.cwd)
	}
	if g.maxWalk <= 0 {
		return nil, fmt.Errorf("gate: max walk %d must be positive", g.maxWalk)
	}
	if g.scope.Policy == nil {
		g.scope.Policy = scope.New()
	}
	if g.logger == nil {
		g.logger = kitlog.New(viper.GetViper())
	}
	return g, nil
}

// Authorize resolves every path argument, checks it against the scope
// policy and the side-effect table, and asks for approval when either
// requires it, in one question. The call may run only when it returns
// a nil error, and only with the Grant's paths.
//
// For an IntoDir argument naming an existing directory, Canonical holds
// one mapped path dst/<base(src)> per source value, in source order;
// sources are the values of the path arguments declared before it.
// With several sources the destination must be an existing directory.
// An IntoDir argument with AllOrNothing also write-checks every entry
// of an AllOrNothing source tree at its mapped place under the
// destination.
//
// The scope is checked before anything about a path is reported: a
// path the scope does not grant is refused alike whether it exists or
// not, and whatever it is. A path's own errors (not found, cannot be
// resolved, not a directory, a tree too large) are reported once the
// scope lets the call reach it: at once for a path the scope allows,
// after approval (or with the warning) for one the scope asks or warns
// about.
func (g *Gate) Authorize(ctx context.Context, req Request) (Grant, error) {
	if err := ctx.Err(); err != nil {
		return Grant{}, err
	}
	if !g.scope.Configured() {
		return Grant{}, &Error{Kind: KindDenied, Message: NoScopeMessage(g.scope.UserFile)}
	}

	args, err := g.resolveArgs(req)
	if err != nil {
		return Grant{}, err
	}
	grant := Grant{Canonical: make(map[string][]string, len(args))}
	for _, st := range args {
		paths := make([]string, len(st.values))
		for i, v := range st.values {
			paths[i] = v.entry.path
		}
		grant.Canonical[st.arg.Param] = paths
	}

	a := &audit{g: g, ctx: ctx, tool: req.Tool, flagged: map[int]bool{}}
	for _, st := range args {
		a.checkArg(st)
	}
	if err := a.denial(); err != nil {
		return Grant{}, err
	}
	if err := a.later(args, true); err != nil {
		return Grant{}, err
	}
	if err := a.recurse(args, &grant); err != nil {
		return Grant{}, err
	}
	if err := a.denial(); err != nil {
		return Grant{}, err
	}

	dec := g.table.Resolve(policy.SideEffect(req.SideEffect), networkOf(req))
	if dec.Action == policy.ActionDeny {
		return Grant{}, &Error{
			Kind: KindPolicy,
			Message: fmt.Sprintf("side effect %q with network %q is denied by the tool policy: %s",
				req.SideEffect, networkOf(req), dec.Reason),
		}
	}
	if err := a.approve(req, grant.Canonical, dec); err != nil {
		return Grant{}, err
	}
	if err := a.later(args, false); err != nil {
		return Grant{}, err
	}
	a.logWarnings()
	return grant, nil
}

// argState is one path argument after resolution.
type argState struct {
	arg    PathArg
	values []*value
}

// value is one resolved path value.
type value struct {
	entry  resolved
	parent resolved // Dirent only
	// srcRoot is the source tree an IntoDir value receives, when that
	// source argument is AllOrNothing; "" otherwise. srcID is that
	// source's id.
	srcRoot string
	srcID   int
	// id numbers the value the model sent; an IntoDir destination
	// mapped once per source gives several values the same id.
	id int
	// abs is the value as the model sent it, anchored; lexical is abs
	// cleaned lexically: what a refusal names when the canonical path
	// would tell the model more than it wrote.
	abs, lexical string
	// opaque: resolving the value read the filesystem where the scope
	// grants the model nothing (a component it named is a symlink, or
	// an into_dir destination is a directory, outside the grant).
	// Refusals then name lexical paths and give the reason of a path
	// no rule covers.
	opaque bool
	// linked: resolving the value followed a symlink the model named
	// in a directory the scope does not grant. Unless the rules as
	// written grant the value's lexical path, it is refused like a
	// path outside the grant: that it resolves into the grant is
	// something only that link says.
	linked bool
	// climb: a ".." the model wrote leaves a directory the scope does
	// not grant; whether that resolves depends on what exists there.
	climb bool
	// unresolved: the value could not be resolved; entry is how far it
	// got and err says why.
	unresolved bool
	// err is the value's own error, reported only once the scope lets
	// the call reach the value (see Authorize).
	err *Error
}

func (g *Gate) resolveArgs(req Request) ([]argState, error) {
	out := make([]argState, 0, len(req.Paths))
	id := 0
	for _, arg := range req.Paths {
		st := argState{arg: arg}
		for _, raw := range arg.Values {
			abs, err := anchor(g.cwd, raw)
			if err != nil {
				return nil, &Error{Kind: KindInvalidArgs, Param: arg.Param, Path: raw, Op: arg.Op, Message: err.Error()}
			}
			from := modelFrom(abs, raw)
			if arg.IntoDir {
				st.values = append(st.values, g.resolveIntoDir(arg, abs, from, id, out)...)
			} else {
				st.values = append(st.values, g.resolveValue(arg, abs, from, id, "", -1))
			}
			id++
		}
		out = append(out, st)
	}
	return out, nil
}

// source is a value an IntoDir destination receives.
type source struct {
	path      string
	id        int
	recursive bool
}

func (g *Gate) resolveIntoDir(arg PathArg, abs string, from, id int, before []argState) []*value {
	var srcs []source
	for _, st := range before {
		for _, v := range st.values {
			s := source{path: v.entry.path, id: v.id, recursive: st.arg.Recursion == AllOrNothing}
			if v.err != nil {
				// The call fails on the source's own error: map it by
				// the name the model gave, and walk nothing.
				s.path, s.recursive = v.lexical, false
			}
			srcs = append(srcs, s)
		}
	}
	root := func(s source) (string, int) {
		if s.recursive {
			return s.path, s.id
		}
		return "", -1
	}

	dir, err := physical(abs, from, g.scope.grants)
	var climb *climbError
	if errors.As(err, &climb) {
		dir, err = physical(abs, from, nil)
	}
	isDir := false
	if fi, serr := os.Stat(dir.path); err == nil && dir.missing == 0 && serr == nil && fi.IsDir() {
		isDir = true
	}
	if !isDir || len(srcs) == 0 {
		srcRoot, srcID := "", -1
		if len(srcs) == 1 {
			srcRoot, srcID = root(srcs[0])
		}
		v := g.resolveValue(arg, abs, from, id, srcRoot, srcID)
		if len(srcs) > 1 && v.err == nil {
			v.err = &Error{Kind: KindInvalidArgs, Param: arg.Param, Path: v.entry.path, Op: arg.Op,
				Message: fmt.Sprintf("must be an existing directory to receive %d sources", len(srcs))}
		}
		return []*value{v}
	}

	// dst is an existing directory: that it is one was read in its
	// parent.
	opaque := !g.scope.grants(filepath.Dir(dir.path)) || g.hidden(dir)
	out := make([]*value, 0, len(srcs))
	for _, s := range srcs {
		base := filepath.Base(s.path)
		if base == "/" {
			v := g.resolveValue(arg, abs, from, id, "", -1)
			if v.err == nil {
				v.err = &Error{Kind: KindInvalidArgs, Param: arg.Param, Path: dir.path, Op: arg.Op,
					Message: "cannot place / inside a directory"}
			}
			return []*value{v}
		}
		mapped := dir.path + "/" + base
		srcRoot, srcID := root(s)
		v := g.resolveValue(arg, mapped, len(mapped), id, srcRoot, srcID)
		v.abs, v.lexical = abs, filepath.Clean(abs)
		v.opaque = v.opaque || opaque
		v.linked = v.linked || g.hidden(dir)
		v.climb = climb != nil
		out = append(out, v)
	}
	return out
}

func (g *Gate) resolveValue(arg PathArg, abs string, from, id int, srcRoot string, srcID int) *value {
	v := &value{srcRoot: srcRoot, srcID: srcID, id: id, abs: abs, lexical: filepath.Clean(abs)}
	resolve := func(climb func(string) bool) (err error) {
		if arg.Target == Dirent {
			v.entry, v.parent, err = dirent(abs, from, climb)
		} else {
			v.entry, err = physical(abs, from, climb)
		}
		return err
	}
	err := resolve(g.scope.grants)
	var climb *climbError
	if errors.As(err, &climb) {
		// Refused unless approved; resolve it fully for then.
		v.climb = true
		err = resolve(nil)
	}
	v.linked = g.hidden(v.entry) || g.hidden(v.parent)
	v.opaque = v.linked
	if err != nil {
		v.unresolved = true
		v.err = &Error{Kind: KindInvalidArgs, Param: arg.Param, Path: abs, Op: arg.Op, Message: "cannot resolve: " + err.Error()}
		return v
	}
	if arg.MustExist && v.entry.missing > 0 {
		v.err = &Error{Kind: KindNotFound, Param: arg.Param, Path: v.entry.path, Op: arg.Op, Message: "no such file or directory"}
	}
	return v
}

// hidden reports whether resolving r followed a symlink the model named
// in a directory the scope does not grant.
func (g *Gate) hidden(r resolved) bool {
	for _, dir := range r.seen {
		if !g.scope.grants(dir) {
			return true
		}
	}
	return false
}

// opBits splits op into single operations: kit matches a rule when it
// shares any bit, so read|write must be checked bit by bit.
func opBits(op scope.Op) []scope.Op {
	var out []scope.Op
	for _, b := range []scope.Op{scope.Read, scope.Write, scope.Exec} {
		if op&b != 0 {
			out = append(out, b)
		}
	}
	return out
}

func opName(op scope.Op) string {
	names := make([]string, 0, 3)
	for _, b := range opBits(op) {
		switch b {
		case scope.Read:
			names = append(names, "read")
		case scope.Write:
			names = append(names, "write")
		case scope.Exec:
			names = append(names, "exec")
		}
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, "|")
}

// renderArgv joins argv for a prompt, quoting tokens a reader could
// misparse.
func renderArgv(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if a == "" || strings.ContainsFunc(a, func(r rune) bool { return r <= ' ' || r == '"' || r == '\'' || r == 0x7f }) {
			a = strconv.Quote(a)
		}
		parts[i] = a
	}
	return strings.Join(parts, " ")
}

// errNoConfirmer stands in for a missing terminal when the gate has no
// Confirmer.
var errNoConfirmer = errors.New("no terminal to ask for approval")

// networkOf is the policy network of req: none when undeclared.
func networkOf(req Request) policy.Network {
	if req.Network == "" {
		return policy.NetworkNone
	}
	return policy.Network(req.Network)
}
