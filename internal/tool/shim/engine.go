package shim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"hop.top/foo/internal/tool/gate"
	"hop.top/kit/go/core/scope"
)

// Error kinds the engine adds to gate's.
const (
	KindTimeout    gate.Kind = "timeout"
	KindExecFailed gate.Kind = "exec_failed"
	KindProtocol   gate.Kind = "protocol"
)

// defaultArgBudget bounds one argv (bytes + NUL + pointer per arg). It
// sits well under macOS ARG_MAX (1 MiB, environment included) and
// Linux's 2 MiB.
const defaultArgBudget = 256 << 10

// maxArgvReport is the most path tokens a result's argv lists verbatim.
const maxArgvReport = 64

// Engine runs spec-backed tools: validate, authorize, build argv from
// the grant, exec, envelope.
type Engine struct {
	// Authorizer resolves and authorizes every path. Nil denies all.
	Authorizer gate.Authorizer
	// Cwd is the base for relative paths, captured once at run start.
	// Commands also run in it.
	Cwd string
	// Flavors detects binary flavors for specs with variants. Nil keeps
	// an in-memory cache.
	Flavors *Flavors
	// MaxStdout and MaxStderr cap captured output; zero = defaults.
	MaxStdout, MaxStderr int
	// ArgBudget bounds one argv when a file list is chunked; zero =
	// default.
	ArgBudget int
	// Environ supplies the environment filtered for children; nil =
	// os.Environ.
	Environ func() []string
}

// Result is the envelope body of a call that ran (§6).
type Result struct {
	ExitCode        int          `json:"exit_code"`
	OK              bool         `json:"ok"`
	Stdout          *string      `json:"stdout"`
	StdoutBytes     int64        `json:"stdout_bytes"`
	StdoutTruncated bool         `json:"stdout_truncated"`
	StdoutBinary    bool         `json:"stdout_binary"`
	Stderr          string       `json:"stderr"`
	StderrBytes     int64        `json:"stderr_bytes"`
	StderrTruncated bool         `json:"stderr_truncated"`
	Argv            []string     `json:"argv"`
	Files           int          `json:"files,omitempty"`
	Chunks          int          `json:"chunks,omitempty"`
	Paths           []PathReport `json:"paths"`
	Filtered        int          `json:"filtered"`
	DurationMS      int64        `json:"duration_ms"`
}

// PathReport pairs a path argument with the canonical path that ran.
type PathReport struct {
	Param    string `json:"param"`
	Given    string `json:"given"`
	Resolved string `json:"resolved"`
	Op       string `json:"op"`
}

func (e *Engine) flavors() *Flavors {
	if e.Flavors == nil {
		e.Flavors = &Flavors{}
	}
	return e.Flavors
}

// variant returns the spec's overrides for the pinned binary's flavor.
func (e *Engine) variant(l *Loaded) *Variant {
	if len(l.Spec.Command.Variants) == 0 {
		return nil
	}
	v, ok := l.Spec.Command.Variants[e.flavors().Detect(l.Bin)]
	if !ok {
		return nil
	}
	return &v
}

// Call runs one tool call. A refused call returns *gate.Error and never
// executes; a command that ran returns a Result whatever its exit code.
func (e *Engine) Call(ctx context.Context, l *Loaded, raw json.RawMessage) (*Result, error) {
	s := l.Spec
	v := e.variant(l)
	vals, err := s.validate(v, raw)
	if err != nil {
		return nil, err
	}
	if err := e.guardLexical(s, vals); err != nil {
		return nil, err
	}
	req, pathArgs := e.request(l, v, vals)
	grant, err := e.authorize(ctx, req)
	if err != nil {
		return nil, err
	}

	for _, pa := range pathArgs {
		if len(grant.Canonical[pa.Param]) == 0 {
			return nil, denied(pa.Param, "authorizer granted no path")
		}
	}
	if err := e.guardGranted(s, vals, grant.Canonical); err != nil {
		return nil, err
	}
	paths, err := s.destinations(grant.Canonical)
	if err != nil {
		return nil, err
	}
	if err := s.checkClobber(vals, grant.Canonical); err != nil {
		return nil, err
	}
	filterBefore := ""
	for _, pa := range pathArgs {
		canon := grant.Canonical[pa.Param]
		switch pa.Recursion {
		case gate.FilterBefore:
			filterBefore = pa.Param
			paths[pa.Param] = grant.Files[pa.Param]
		case gate.FilterAfter:
			if grant.Allow == nil {
				return nil, denied(pa.Param, "authorizer gave no output filter")
			}
		}
		if err := checkKind(s.byName[pa.Param], pa, canon); err != nil {
			return nil, err
		}
	}

	argvs, err := e.argvs(l, v, vals, paths, filterBefore)
	if err != nil {
		return nil, &gate.Error{Kind: KindExecFailed, Message: err.Error()}
	}
	res, err := e.exec(ctx, l, v, argvs, grant)
	if err != nil {
		return nil, err
	}
	res.Paths = reports(pathArgs, grant)
	res.Filtered += grant.Filtered
	res.Argv = argvs0(argvs, func() []string { return req.Argv(grant.Canonical) })
	if filterBefore != "" {
		res.Files = len(paths[filterBefore])
		if len(argvs) > 1 {
			res.Chunks = len(argvs)
		}
	}
	return res, nil
}

// request builds the gate request: one PathArg per supplied path param,
// the effective side effect, and an argv renderer for prompts.
func (e *Engine) request(l *Loaded, v *Variant, vals values) (gate.Request, []gate.PathArg) {
	s := l.Spec
	var args []gate.PathArg
	for _, p := range s.Params {
		list, ok := vals[p.Name].([]string)
		if p.Type != TypePath || !ok {
			continue
		}
		pa := gate.PathArg{
			Param:     p.Name,
			Values:    list,
			Op:        s.effectiveOp(p, vals),
			IntoDir:   p.IntoDir,
			MustExist: p.MustExist,
			Parents:   s.parents(p, vals),
		}
		if p.Target == "dirent" {
			pa.Target = gate.Dirent
		}
		if s.recursive(p, vals) {
			switch p.Recursion {
			case RecursionFilterBefore:
				pa.Recursion = gate.FilterBefore
			case RecursionFilterAfter:
				pa.Recursion = gate.FilterAfter
			default:
				pa.Recursion = gate.AllOrNothing
			}
		}
		args = append(args, pa)
	}
	return gate.Request{
		Tool:       s.Name,
		SideEffect: s.effectiveSideEffect(vals),
		Paths:      args,
		Argv: func(canonical map[string][]string) []string {
			paths, err := s.destinations(canonical)
			if err != nil {
				return nil
			}
			argv, err := s.render(l.Bin, v, vals, paths)
			if err != nil {
				return nil
			}
			return argv
		},
	}, args
}

func (e *Engine) authorize(ctx context.Context, req gate.Request) (gate.Grant, error) {
	if e.Authorizer == nil {
		return gate.Grant{}, &gate.Error{Kind: gate.KindDenied, Message: "no authorizer configured"}
	}
	grant, err := e.Authorizer.Authorize(ctx, req)
	if err == nil {
		return grant, nil
	}
	var ge *gate.Error
	if errors.As(err, &ge) {
		return gate.Grant{}, ge
	}
	return gate.Grant{}, &gate.Error{Kind: gate.KindDenied, Message: err.Error()}
}

func denied(param, msg string) *gate.Error {
	return &gate.Error{Kind: gate.KindDenied, Param: param, Message: msg}
}

// destinations returns the argv path tokens for a grant. An into_dir
// param the authorizer mapped to one dst/<base(src)> path per source
// (sources: path params declared before it) is passed as that
// directory, so cp/mv place each source exactly where it was checked.
// Several sources without such a mapping fail closed.
func (s *Spec) destinations(canonical map[string][]string) (map[string][]string, error) {
	out := make(map[string][]string, len(canonical))
	for k, v := range canonical {
		out[k] = v
	}
	var srcs []string
	for _, p := range s.Params {
		if p.Type != TypePath {
			continue
		}
		canon := canonical[p.Name]
		if p.IntoDir && len(srcs) > 0 {
			if dir, ok := mappedDir(canon, srcs); ok {
				out[p.Name] = []string{dir}
			} else if len(srcs) > 1 || len(canon) != 1 {
				return nil, denied(p.Name, fmt.Sprintf("destination not mapped into a directory for %d sources", len(srcs)))
			}
		}
		srcs = append(srcs, canon...)
	}
	return out, nil
}

// mappedDir reports the directory D when canon[i] == D/<base(srcs[i])>
// for every source and D is an existing directory.
func mappedDir(canon, srcs []string) (string, bool) {
	if len(canon) != len(srcs) || len(canon) == 0 {
		return "", false
	}
	dir := filepath.Dir(canon[0])
	for i, c := range canon {
		if c != filepath.Join(dir, filepath.Base(srcs[i])) {
			return "", false
		}
	}
	fi, err := os.Stat(dir)
	return dir, err == nil && fi.IsDir()
}

// checkClobber refuses a call whose destination already exists unless
// the param's clobber_when matches, and always refuses an existing
// directory as the destination entry (cp/mv would nest inside it,
// somewhere the authorizer never checked). It runs after authorization,
// so it never probes a denied path.
func (s *Spec) checkClobber(vals values, canonical map[string][]string) error {
	for _, p := range s.Params {
		if p.ClobberWhen == nil {
			continue
		}
		allowed := s.matches(p.ClobberWhen, vals)
		op := s.effectiveOp(p, vals)
		for _, c := range canonical[p.Name] {
			fi, err := os.Lstat(c)
			switch {
			case err != nil:
			case fi.IsDir():
				return &gate.Error{Kind: gate.KindInvalidArgs, Param: p.Name, Path: c, Op: op, Message: "an existing directory is in the way"}
			case !allowed:
				return &gate.Error{Kind: gate.KindInvalidArgs, Param: p.Name, Path: c, Op: op, Message: "exists; " + whenText(p.ClobberWhen) + " replaces it"}
			}
		}
	}
	return nil
}

func whenText(when map[string]any) string {
	parts := make([]string, 0, len(when))
	for k, v := range when {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

// checkKind enforces a param's kind (file/dir) on granted paths that
// exist. It runs after authorization so it never probes a denied path.
func checkKind(p *Param, pa gate.PathArg, canon []string) error {
	if p.Kind == "" || p.Kind == "any" || pa.Recursion != gate.NoRecursion {
		return nil
	}
	for _, c := range canon {
		fi, err := os.Stat(c)
		if err != nil {
			continue
		}
		if p.Kind == "file" && fi.IsDir() {
			return &gate.Error{Kind: gate.KindInvalidArgs, Param: p.Name, Path: c, Op: pa.Op, Message: "is a directory; want a file"}
		}
		if p.Kind == "dir" && !fi.IsDir() {
			return &gate.Error{Kind: gate.KindInvalidArgs, Param: p.Name, Path: c, Op: pa.Op, Message: "is not a directory"}
		}
	}
	return nil
}

// argvs renders the call's argv, split into chunks when a filter-before
// file list would overflow the argv budget.
func (e *Engine) argvs(l *Loaded, v *Variant, vals values, paths map[string][]string, filterBefore string) ([][]string, error) {
	s := l.Spec
	if filterBefore == "" {
		argv, err := s.render(l.Bin, v, vals, paths)
		if err != nil {
			return nil, err
		}
		return [][]string{argv}, nil
	}
	files := paths[filterBefore]
	if len(files) == 0 {
		return nil, nil
	}
	paths[filterBefore] = []string{}
	base, err := s.render(l.Bin, v, vals, paths)
	if err != nil {
		return nil, err
	}
	budget := e.ArgBudget
	if budget <= 0 {
		budget = defaultArgBudget
	}
	var out [][]string
	for _, c := range chunk(files, argvSize(base), budget) {
		paths[filterBefore] = c
		argv, err := s.render(l.Bin, v, vals, paths)
		if err != nil {
			return nil, err
		}
		out = append(out, argv)
	}
	paths[filterBefore] = files
	return out, nil
}

func (e *Engine) exec(ctx context.Context, l *Loaded, v *Variant, argvs [][]string, grant gate.Grant) (*Result, error) {
	s := l.Spec
	ok := s.OkExitCodes
	if v != nil && len(v.OkExitCodes) > 0 {
		ok = v.OkExitCodes
	}
	maxOut, maxErr := e.MaxStdout, e.MaxStderr
	if maxOut <= 0 {
		maxOut = DefaultMaxStdout
	}
	if maxErr <= 0 {
		maxErr = DefaultMaxStderr
	}
	environ := os.Environ
	if e.Environ != nil {
		environ = e.Environ
	}

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	out := newRunOut(maxOut, maxErr, grant.Allow, s.Output == OutputPaths0)
	start := time.Now()
	for _, argv := range argvs {
		if err := out.run(ctx, argv, e.Cwd, environ()); err != nil {
			if errors.Is(err, errTimeout) {
				return nil, &gate.Error{Kind: KindTimeout, Message: fmt.Sprintf("%s did not finish within %s; output discarded", s.Name, s.timeout)}
			}
			return nil, &gate.Error{Kind: KindExecFailed, Message: err.Error()}
		}
	}

	res := &Result{
		ExitCode:        mergeExit(out.exitCodes, ok),
		StdoutBytes:     out.stdout.total,
		StdoutTruncated: out.stdout.truncated(),
		Stderr:          out.stderr.buf.String(),
		StderrBytes:     out.stderr.total,
		StderrTruncated: out.stderr.truncated(),
		DurationMS:      time.Since(start).Milliseconds(),
	}
	res.OK = containsInt(ok, res.ExitCode)
	if out.filter != nil {
		res.Filtered = out.filter.filtered
	}
	if s.Output == OutputText && isBinary(out.stdout.buf.Bytes(), res.StdoutTruncated) {
		res.StdoutBinary = true
	} else {
		str := out.stdout.buf.String()
		if res.StdoutTruncated {
			str = string(trimPartialRune(out.stdout.buf.Bytes()))
		}
		res.Stdout = &str
	}
	return res, nil
}

// argvs0 reports what ran: the argv itself when there is one of modest
// size, otherwise the call rendered with its roots (the prompt form).
func argvs0(argvs [][]string, roots func() []string) []string {
	if len(argvs) == 1 && len(argvs[0]) <= maxArgvReport+16 {
		return argvs[0]
	}
	return roots()
}

func reports(args []gate.PathArg, grant gate.Grant) []PathReport {
	out := []PathReport{}
	for _, pa := range args {
		canon := grant.Canonical[pa.Param]
		if len(pa.Values) == 1 && len(canon) > 1 { // into_dir mapped per source
			for _, c := range canon {
				out = append(out, PathReport{Param: pa.Param, Given: pa.Values[0], Resolved: c, Op: opString(pa.Op)})
			}
			continue
		}
		for i, given := range pa.Values {
			r := PathReport{Param: pa.Param, Given: given, Op: opString(pa.Op)}
			if i < len(canon) {
				r.Resolved = canon[i]
			}
			out = append(out, r)
		}
	}
	return out
}

func opString(op scope.Op) string {
	var parts []string
	if op&scope.Read != 0 {
		parts = append(parts, "read")
	}
	if op&scope.Write != 0 {
		parts = append(parts, "write")
	}
	if op&scope.Exec != 0 {
		parts = append(parts, "exec")
	}
	return strings.Join(parts, ",")
}
