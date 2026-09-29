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

	a := &audit{g: g, ctx: ctx, tool: req.Tool}
	for _, st := range args {
		a.checkArg(st)
	}
	if err := a.denial(); err != nil {
		return Grant{}, err
	}
	if err := a.recurse(args, &grant); err != nil {
		return Grant{}, err
	}
	if err := a.denial(); err != nil {
		return Grant{}, err
	}

	dec := g.table.Resolve(policy.SideEffect(req.SideEffect), policy.NetworkNone)
	if dec.Action == policy.ActionDeny {
		return Grant{}, &Error{
			Kind:    KindPolicy,
			Message: fmt.Sprintf("side effect %q is denied by the tool policy: %s", req.SideEffect, dec.Reason),
		}
	}
	if err := a.approve(req, grant.Canonical, dec); err != nil {
		return Grant{}, err
	}
	a.logWarnings()
	return grant, nil
}

// argState is one path argument after resolution.
type argState struct {
	arg    PathArg
	values []value
}

// value is one resolved path value.
type value struct {
	entry  resolved
	parent resolved // Dirent only
	// srcRoot is the source tree an IntoDir value receives, when that
	// source argument is AllOrNothing; "" otherwise.
	srcRoot string
}

func (g *Gate) resolveArgs(req Request) ([]argState, error) {
	out := make([]argState, 0, len(req.Paths))
	for _, arg := range req.Paths {
		st := argState{arg: arg}
		for _, raw := range arg.Values {
			abs, err := anchor(g.cwd, raw)
			if err != nil {
				return nil, &Error{Kind: KindInvalidArgs, Param: arg.Param, Path: raw, Op: arg.Op, Message: err.Error()}
			}
			if !arg.IntoDir {
				v, err := g.resolveValue(arg, abs, "")
				if err != nil {
					return nil, err
				}
				st.values = append(st.values, v)
				continue
			}
			vs, err := g.resolveIntoDir(arg, abs, out)
			if err != nil {
				return nil, err
			}
			st.values = append(st.values, vs...)
		}
		out = append(out, st)
	}
	return out, nil
}

// source is a value an IntoDir destination receives.
type source struct {
	path      string
	recursive bool
}

func (g *Gate) resolveIntoDir(arg PathArg, abs string, before []argState) ([]value, error) {
	var srcs []source
	for _, st := range before {
		for _, v := range st.values {
			srcs = append(srcs, source{path: v.entry.path, recursive: st.arg.Recursion == AllOrNothing})
		}
	}
	root := func(s source) string {
		if s.recursive {
			return s.path
		}
		return ""
	}

	dir, err := physical(abs)
	if err != nil {
		return nil, &Error{Kind: KindInvalidArgs, Param: arg.Param, Path: abs, Op: arg.Op, Message: "cannot resolve: " + err.Error()}
	}
	isDir := false
	if fi, serr := os.Stat(dir.path); dir.missing == 0 && serr == nil && fi.IsDir() {
		isDir = true
	}
	if !isDir || len(srcs) == 0 {
		if len(srcs) > 1 {
			return nil, &Error{Kind: KindInvalidArgs, Param: arg.Param, Path: dir.path, Op: arg.Op,
				Message: fmt.Sprintf("must be an existing directory to receive %d sources", len(srcs))}
		}
		srcRoot := ""
		if len(srcs) == 1 {
			srcRoot = root(srcs[0])
		}
		v, err := g.resolveValue(arg, abs, srcRoot)
		if err != nil {
			return nil, err
		}
		return []value{v}, nil
	}

	out := make([]value, 0, len(srcs))
	for _, s := range srcs {
		base := filepath.Base(s.path)
		if base == "/" {
			return nil, &Error{Kind: KindInvalidArgs, Param: arg.Param, Path: dir.path, Op: arg.Op,
				Message: "cannot place / inside a directory"}
		}
		v, err := g.resolveValue(arg, dir.path+"/"+base, root(s))
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (g *Gate) resolveValue(arg PathArg, abs, srcRoot string) (value, error) {
	var (
		v   = value{srcRoot: srcRoot}
		err error
	)
	if arg.Target == Dirent {
		v.entry, v.parent, err = dirent(abs)
	} else {
		v.entry, err = physical(abs)
	}
	if err != nil {
		return value{}, &Error{Kind: KindInvalidArgs, Param: arg.Param, Path: abs, Op: arg.Op, Message: "cannot resolve: " + err.Error()}
	}
	if arg.MustExist && v.entry.missing > 0 {
		return value{}, &Error{Kind: KindNotFound, Param: arg.Param, Path: v.entry.path, Op: arg.Op, Message: "no such file or directory"}
	}
	return v, nil
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
