package shim

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"hop.top/foo/internal/tool/gate"
	"hop.top/kit/go/core/scope"
)

// readAuth mirrors the real gate's read-side semantics closely enough
// to drive the built-in read specs against real binaries: canonical
// paths from gate.Canonical (physical, vs cwd), must_exist, a deny list
// of name globs matched per path component (like kit's **/.env,
// **/secrets*), filter-before walks that skip links and withhold denied
// files and whole denied directories, and a filter-after Allow that
// checks the entry and its physical target.
type readAuth struct {
	cwd   string
	deny  []string
	calls int
	last  gate.Request
}

func (a *readAuth) denied(p string) bool {
	for _, part := range strings.Split(p, "/") {
		for _, g := range a.deny {
			if ok, _ := path.Match(g, part); ok {
				return true
			}
		}
	}
	return false
}

func (a *readAuth) Authorize(_ context.Context, req gate.Request) (gate.Grant, error) {
	a.calls++
	a.last = req
	g := gate.Grant{Canonical: map[string][]string{}, Files: map[string][]string{}}
	var after bool
	for _, pa := range req.Paths {
		for _, v := range pa.Values {
			c, err := gate.Canonical(a.cwd, v)
			if err != nil {
				return gate.Grant{}, &gate.Error{Kind: gate.KindInvalidArgs, Param: pa.Param, Path: v, Op: pa.Op, Message: err.Error()}
			}
			if _, err := os.Lstat(c); pa.MustExist && errors.Is(err, fs.ErrNotExist) {
				return gate.Grant{}, &gate.Error{Kind: gate.KindNotFound, Param: pa.Param, Path: c, Op: pa.Op, Message: "no such file or directory"}
			}
			if a.denied(c) {
				return gate.Grant{}, &gate.Error{Kind: gate.KindDenied, Param: pa.Param, Path: c, Op: pa.Op, Message: "denied by scope"}
			}
			g.Canonical[pa.Param] = append(g.Canonical[pa.Param], c)
		}
		switch pa.Recursion {
		case gate.FilterBefore:
			g.Files[pa.Param] = []string{}
			for _, root := range g.Canonical[pa.Param] {
				a.walkFiles(root, pa.Param, &g)
			}
		case gate.FilterAfter:
			after = true
		}
	}
	if after {
		g.Allow = func(abs string) bool {
			if !filepath.IsAbs(abs) {
				return false
			}
			target, err := gate.Canonical("/", abs)
			return err == nil && !a.denied(abs) && !a.denied(target)
		}
	}
	return g, nil
}

func (a *readAuth) walkFiles(root, param string, g *gate.Grant) {
	fi, err := os.Lstat(root)
	if err != nil {
		return
	}
	if !fi.IsDir() {
		if fi.Mode().IsRegular() {
			g.Files[param] = append(g.Files[param], root)
		}
		return
	}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		switch {
		case d.IsDir() && a.denied(p):
			g.Filtered++
			return fs.SkipDir
		case d.Type().IsRegular() && a.denied(p):
			g.Filtered++
		case d.Type().IsRegular():
			g.Files[param] = append(g.Files[param], p)
		}
		return nil
	})
}

// readTree builds a small project with the files every read test uses:
// a readable file, a nested one, a .env and a secrets dir the fake
// denies, and a link.
func readTree(t *testing.T) string {
	t.Helper()
	dir := tempDir(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha\nbeta\ngamma\ndelta\n")
	writeFile(t, filepath.Join(dir, "sub", "c.go"), "package c\n// TODO nested\n")
	writeFile(t, filepath.Join(dir, ".env"), "TOKEN=hunter2 TODO\n")
	writeFile(t, filepath.Join(dir, "secrets", "key.txt"), "TODO private key\n")
	if err := os.Symlink("a.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	return dir
}

// readEngine is an engine over readTree's deny list.
func readEngine(dir string) (*Engine, *readAuth) {
	auth := &readAuth{cwd: dir, deny: []string{".env", "secrets*"}}
	return &Engine{Authorizer: auth, Cwd: dir}, auth
}

func builtinSpec(t *testing.T, name string) *Loaded {
	t.Helper()
	l, inv := Load(LoadOptions{}).Lookup(name)
	if l == nil {
		t.Fatalf("builtin %s missing (invalid: %v)", name, inv)
	}
	return l
}

func flavorOf(l *Loaded) string { return (&Flavors{}).Detect(l.Bin) }

// wantArgv asserts the exact argv: the pinned binary, opts, a literal
// --, then the canonical paths. Nothing the model sent as a path
// reaches argv except through the grant.
func wantArgv(t *testing.T, res *Result, l *Loaded, opts []string, paths ...string) {
	t.Helper()
	want := append([]string{l.Bin}, opts...)
	want = append(want, "--")
	want = append(want, paths...)
	if !slices.Equal(res.Argv, want) {
		t.Fatalf("argv = %q\n want %q", res.Argv, want)
	}
}

// wantRefused asserts a refusal of kind and that nothing ran.
func wantRefused(t *testing.T, e *Engine, l *Loaded, args string, kind gate.Kind, param string) *gate.Error {
	t.Helper()
	res, err := call(t, e, l, args)
	if res != nil {
		t.Fatalf("%s: ran (argv %q); want %s refusal", args, res.Argv, kind)
	}
	ge := wantKind(t, err, kind)
	if ge.Param != param {
		t.Errorf("%s: refused param %q; want %q (%v)", args, ge.Param, param, ge)
	}
	return ge
}

// forbidden lists, per built-in read tool, argv tokens no spec
// construct may ever emit: flags that recurse where foo cannot filter,
// follow links out of the checked tree, execute, write, read a path
// from an option value, or never terminate.
var forbidden = map[string][]string{
	"ls":   {"-R", "-L", "-H", "--recursive", "--dereference"},
	"cat":  {"-"},
	"head": {"-"},
	"tail": {"-f", "-F", "-r", "--follow", "--retry", "-"},
	"wc":   {"--files0-from", "-"},
	"find": {"-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprint0", "-fprintf", "-fls", "-printf", "-ls", "-L", "-H", "-follow", "-newer", "-anewer", "-cnewer", "-samefile", "-regex", "-iregex", "-f"},
	"grep": {"-r", "-R", "-d", "-D", "--recursive", "--dereference-recursive", "--directories", "-f", "--file", "--include", "--exclude", "--exclude-from", "--exclude-dir", "-P", "-"},
	"stat": {"-L", "--dereference"},
}

// specTokens is every literal token a spec can put in argv on any
// flavor: template literals, bool/enum fragments, value fragments and
// variant overrides and prefixes.
func specTokens(s *Spec) []string {
	toks := []string{}
	for _, tok := range s.Argv {
		if _, ph := placeholder(tok); !ph {
			toks = append(toks, tok)
		}
	}
	for _, p := range s.Params {
		for _, frag := range p.boolArgv {
			toks = append(toks, frag...)
		}
		for _, frag := range p.Values {
			toks = append(toks, frag...)
		}
		toks = append(toks, p.fragment...)
	}
	for _, v := range s.Command.Variants {
		toks = append(toks, v.Prefix...)
		for _, o := range v.Params {
			for _, frag := range o.boolArgv {
				toks = append(toks, frag...)
			}
			for _, frag := range o.Values {
				toks = append(toks, frag...)
			}
			toks = append(toks, o.fragment...)
		}
	}
	return toks
}

// hasFlag reports tok as flag f: exactly, as --long=value, or as a
// short flag inside a cluster (-lR).
func hasFlag(tok, f string) bool {
	if tok == f || strings.HasPrefix(tok, f+"=") {
		return true
	}
	if len(f) == 2 && f[0] == '-' && f[1] != '-' && len(tok) > 2 && len(tok) <= 5 && tok[0] == '-' && tok[1] != '-' &&
		!strings.ContainsAny(tok, " %=") {
		return strings.ContainsRune(tok[1:], rune(f[1]))
	}
	return false
}

// The read set: every tool read-only, every path param read + follow,
// no forbidden flag anywhere, and string params only where the command
// takes them as a pattern, never as a path.
func TestReadSpecs_Audit(t *testing.T) {
	strings_ := map[string][]string{"grep": {"pattern"}, "find": {"name", "iname"}}
	cat := Load(LoadOptions{})
	for name, bad := range forbidden {
		t.Run(name, func(t *testing.T) {
			l, inv := cat.Lookup(name)
			if l == nil {
				t.Fatalf("builtin %s missing (invalid: %v)", name, inv)
			}
			s := l.Spec
			if s.SideEffect != "read" || len(s.SideEffectIf) != 0 || s.Network != "none" {
				t.Errorf("side effect %q (+%d escalations) network %q; want read, none", s.SideEffect, len(s.SideEffectIf), s.Network)
			}
			for _, tok := range specTokens(s) {
				for _, f := range bad {
					if hasFlag(tok, f) {
						t.Errorf("spec can emit %q (forbidden %s)", tok, f)
					}
				}
			}
			var strs []string
			for _, p := range s.Params {
				switch p.Type {
				case TypePath:
					if p.Ops() != scope.Read || !slices.Equal(p.Op, []string{"read"}) || p.Target != "follow" || p.IntoDir {
						t.Errorf("path %q: op %v target %s; want [read] follow", p.Name, p.Op, p.Target)
					}
				case TypeString:
					strs = append(strs, p.Name)
					if p.MaxLen > 1024 || p.Newline {
						t.Errorf("string %q: max_len %d newline %v", p.Name, p.MaxLen, p.Newline)
					}
				case TypeInt:
					if p.Min == nil || p.Max == nil || *p.Max > 1<<20 {
						t.Errorf("int %q unbounded or too large", p.Name)
					}
				}
			}
			if !slices.Equal(strs, strings_[name]) {
				t.Errorf("string params %v; want %v", strs, strings_[name])
			}
		})
	}
}
