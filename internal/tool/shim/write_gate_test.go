package shim

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hop.top/foo/internal/tool/gate"
	"hop.top/kit/go/core/scope"
)

// scopeRule grants or denies ops on a directory and everything under it.
type scopeRule struct {
	root string
	ops  scope.Op
}

// scopeGate is a fake Authorizer that mirrors the real gate's path
// semantics (internal/tool/gate) over a simple rule list, so the write
// specs can be exercised without a scope.yaml:
//
//   - follow values resolve physically (gate.Canonical); dirent values
//     resolve the parent and keep the final component, so a final link
//     names the link;
//   - dirent needs write on the parent and the op on the entry, plus
//     the op on a final link's target;
//   - parents write-checks every missing ancestor;
//   - into_dir maps an existing directory to dir/<base(src)> per source
//     declared before it; several sources need an existing directory;
//   - all_or_nothing walks the tree without following links and
//     refuses the call on one denied entry; an into_dir destination
//     checks the source tree at its mapped place;
//   - combined ops are checked bit by bit.
//
// A deny rule wins; a path no allow rule covers is denied (strict).
type scopeGate struct {
	cwd   string
	allow []scopeRule
	deny  []scopeRule

	calls   int
	last    gate.Request
	checked []string // "op path" for every check, in order
}

func (g *scopeGate) covers(rules []scopeRule, path string, op scope.Op) bool {
	for _, r := range rules {
		if r.ops&op != 0 && (path == r.root || strings.HasPrefix(path, r.root+"/") || r.root == "/") {
			return true
		}
	}
	return false
}

func wOpName(op scope.Op) string {
	switch op {
	case scope.Read:
		return "read"
	case scope.Write:
		return "write"
	case scope.Exec:
		return "exec"
	}
	return "?"
}

// ok checks every bit of op on path.
func (g *scopeGate) ok(path string, op scope.Op) (scope.Op, bool) {
	for _, bit := range []scope.Op{scope.Read, scope.Write, scope.Exec} {
		if op&bit == 0 {
			continue
		}
		g.checked = append(g.checked, wOpName(bit)+" "+path)
		if g.covers(g.deny, path, bit) || !g.covers(g.allow, path, bit) {
			return bit, false
		}
	}
	return 0, true
}

type fakeValue struct {
	entry, parent string
	srcRoot       string
}

func wMissing(p string) bool {
	_, err := os.Lstat(p)
	return err != nil
}

func (g *scopeGate) resolve(pa gate.PathArg, abs, srcRoot string) (fakeValue, error) {
	v := fakeValue{srcRoot: srcRoot}
	base := abs[strings.LastIndexByte(abs, '/')+1:]
	var err error
	switch {
	case pa.Target != gate.Dirent:
		v.entry, err = gate.Canonical("/", abs)
	case base == "" || base == "." || base == "..":
		v.entry, err = gate.Canonical("/", abs)
		v.parent = filepath.Dir(v.entry)
	default:
		v.parent, err = gate.Canonical("/", abs[:len(abs)-len(base)])
		v.entry = filepath.Join(v.parent, base)
	}
	if err != nil {
		return v, &gate.Error{Kind: gate.KindInvalidArgs, Param: pa.Param, Path: abs, Op: pa.Op, Message: err.Error()}
	}
	if pa.MustExist && wMissing(v.entry) {
		return v, &gate.Error{Kind: gate.KindNotFound, Param: pa.Param, Path: v.entry, Op: pa.Op, Message: "no such file or directory"}
	}
	return v, nil
}

func (g *scopeGate) deniedErr(param, path string, op scope.Op, why string) error {
	return &gate.Error{Kind: gate.KindDenied, Param: param, Path: path, Op: op, Message: why}
}

func (g *scopeGate) Authorize(ctx context.Context, req gate.Request) (gate.Grant, error) {
	g.calls++
	g.last = req
	g.checked = nil
	grant := gate.Grant{Canonical: map[string][]string{}}

	type argVals struct {
		pa   gate.PathArg
		vals []fakeValue
	}
	var args []argVals
	for _, pa := range req.Paths {
		av := argVals{pa: pa}
		for _, raw := range pa.Values {
			abs := raw
			if !filepath.IsAbs(abs) {
				abs = g.cwd + "/" + raw
			}
			if !pa.IntoDir {
				v, err := g.resolve(pa, abs, "")
				if err != nil {
					return gate.Grant{}, err
				}
				av.vals = append(av.vals, v)
				continue
			}
			type src struct {
				path string
				tree bool
			}
			var srcs []src
			for _, prev := range args {
				for _, v := range prev.vals {
					srcs = append(srcs, src{v.entry, prev.pa.Recursion == gate.AllOrNothing})
				}
			}
			rootOf := func(s src) string {
				if s.tree {
					return s.path
				}
				return ""
			}
			dir, err := gate.Canonical("/", abs)
			if err != nil {
				return gate.Grant{}, &gate.Error{Kind: gate.KindInvalidArgs, Param: pa.Param, Path: abs, Message: err.Error()}
			}
			fi, serr := os.Stat(dir)
			if serr != nil || !fi.IsDir() || len(srcs) == 0 {
				if len(srcs) > 1 {
					return gate.Grant{}, &gate.Error{Kind: gate.KindInvalidArgs, Param: pa.Param, Path: dir, Op: pa.Op,
						Message: fmt.Sprintf("must be an existing directory to receive %d sources", len(srcs))}
				}
				root := ""
				if len(srcs) == 1 {
					root = rootOf(srcs[0])
				}
				v, err := g.resolve(pa, abs, root)
				if err != nil {
					return gate.Grant{}, err
				}
				av.vals = append(av.vals, v)
				continue
			}
			for _, s := range srcs {
				v, err := g.resolve(pa, dir+"/"+filepath.Base(s.path), rootOf(s))
				if err != nil {
					return gate.Grant{}, err
				}
				av.vals = append(av.vals, v)
			}
		}
		args = append(args, av)
	}

	for _, av := range args {
		pa := av.pa
		for _, v := range av.vals {
			grant.Canonical[pa.Param] = append(grant.Canonical[pa.Param], v.entry)
			if pa.Target == gate.Dirent {
				if bit, ok := g.ok(v.parent, scope.Write); !ok {
					return gate.Grant{}, g.deniedErr(pa.Param, v.parent, bit, "parent not writable")
				}
			}
			if pa.Parents {
				start := v.entry
				if pa.Target == gate.Dirent {
					start = v.parent
				}
				for p := start; p != "/" && wMissing(p); p = filepath.Dir(p) {
					if bit, ok := g.ok(p, scope.Write); !ok {
						return gate.Grant{}, g.deniedErr(pa.Param, p, bit, "missing ancestor not writable")
					}
				}
			}
			if bit, ok := g.ok(v.entry, pa.Op); !ok {
				return gate.Grant{}, g.deniedErr(pa.Param, v.entry, bit, "not in scope")
			}
			if fi, err := os.Lstat(v.entry); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
				target, err := gate.Canonical("/", v.entry)
				if err != nil {
					return gate.Grant{}, g.deniedErr(pa.Param, v.entry, pa.Op, "link target unresolvable")
				}
				if bit, ok := g.ok(target, pa.Op); !ok {
					return gate.Grant{}, g.deniedErr(pa.Param, target, bit, "link target not in scope")
				}
			}
		}
	}

	for _, av := range args {
		if av.pa.Recursion != gate.AllOrNothing {
			continue
		}
		pa := av.pa
		for _, v := range av.vals {
			walkRoot, mapTo := v.entry, ""
			if pa.IntoDir {
				if v.srcRoot == "" {
					continue
				}
				walkRoot, mapTo = v.srcRoot, v.entry
			}
			var offenders []string
			err := filepath.WalkDir(walkRoot, func(p string, _ fs.DirEntry, err error) error {
				if p == walkRoot {
					return nil
				}
				if err != nil {
					offenders = append(offenders, p)
					return nil
				}
				if mapTo != "" {
					p = mapTo + p[len(walkRoot):]
				}
				if _, ok := g.ok(p, pa.Op); !ok {
					offenders = append(offenders, p)
					return nil
				}
				// kit's Check follows an existing link: its target
				// must pass too (fail-closed).
				if wIsLink(p) {
					target, err := gate.Canonical("/", p)
					if _, ok := g.ok(target, pa.Op); err != nil || !ok {
						offenders = append(offenders, p)
					}
				}
				return nil
			})
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return gate.Grant{}, err
			}
			if len(offenders) > 0 {
				return gate.Grant{}, g.deniedErr(pa.Param, v.entry, pa.Op,
					fmt.Sprintf("%d entries under it are not allowed, so nothing was changed: %s", len(offenders), strings.Join(offenders, ", ")))
			}
		}
	}
	return grant, nil
}

// rw grants read and write on each root.
func wRW(roots ...string) []scopeRule {
	out := make([]scopeRule, len(roots))
	for i, r := range roots {
		out[i] = scopeRule{root: r, ops: scope.Read | scope.Write}
	}
	return out
}

// ro grants read only on each root.
func wRO(roots ...string) []scopeRule {
	out := make([]scopeRule, len(roots))
	for i, r := range roots {
		out[i] = scopeRule{root: r, ops: scope.Read}
	}
	return out
}

// wTool loads a built-in spec by name, failing when it is missing
// or invalid.
func wTool(t *testing.T, name string) *Loaded {
	t.Helper()
	l, inv := Load(LoadOptions{}).Lookup(name)
	if inv != nil {
		t.Fatalf("builtin %s invalid: %v", name, inv.Err)
	}
	if l == nil {
		t.Fatalf("builtin %s missing", name)
	}
	return l
}

// wWithBin copies l with its pinned binary swapped, to exercise a flavor
// variant or record argv without running the real command.
func wWithBin(l *Loaded, bin string) *Loaded {
	c := *l
	c.Bin = bin
	return &c
}

// writeBox is a canonical temp tree: w/ is read+write, r/ is read-only,
// out/ is outside every rule.
type writeBox struct {
	root string
	gate *scopeGate
	eng  *Engine
}

func newWriteBox(t *testing.T) *writeBox {
	t.Helper()
	root := tempDir(t)
	for _, d := range []string{"w", "r", "out"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	w := filepath.Join(root, "w")
	g := &scopeGate{
		cwd:   w,
		allow: append(wRW(w), wRO(filepath.Join(root, "r"))...),
		deny:  []scopeRule{{root: filepath.Join(w, ".env"), ops: scope.Read | scope.Write | scope.Exec}},
	}
	return &writeBox{root: root, gate: g, eng: &Engine{Authorizer: g, Cwd: w, Flavors: &Flavors{}}}
}

func (s *writeBox) p(rel string) string { return filepath.Join(s.root, rel) }

func (s *writeBox) file(t *testing.T, rel, content string) string {
	t.Helper()
	return writeFile(t, s.p(rel), content)
}

func (s *writeBox) dir(t *testing.T, rel string) string {
	t.Helper()
	if err := os.MkdirAll(s.p(rel), 0o755); err != nil {
		t.Fatal(err)
	}
	return s.p(rel)
}

func (s *writeBox) link(t *testing.T, target, rel string) string {
	t.Helper()
	if err := os.Symlink(target, s.p(rel)); err != nil {
		t.Fatal(err)
	}
	return s.p(rel)
}

func wRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func wExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func wIsLink(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode()&fs.ModeSymlink != 0
}

// wWantArgv checks argv[0] is the pinned binary, a literal -- is
// present, and every token after it is a clean absolute path that the
// call granted.
func wWantArgv(t *testing.T, l *Loaded, res *Result, granted ...string) {
	t.Helper()
	if len(res.Argv) == 0 || res.Argv[0] != l.Bin {
		t.Fatalf("argv %q: want pinned binary %s first", res.Argv, l.Bin)
	}
	dd := indexOf(res.Argv, "--")
	if dd < 0 {
		t.Fatalf("argv %q has no --", res.Argv)
	}
	after := res.Argv[dd+1:]
	if strings.Join(after, "\n") != strings.Join(granted, "\n") {
		t.Fatalf("argv paths %q; want %q", after, granted)
	}
	for _, a := range after {
		if !filepath.IsAbs(a) || filepath.Clean(a) != a {
			t.Fatalf("argv path %q is not clean and absolute", a)
		}
	}
	for _, a := range res.Argv[1:dd] {
		if strings.HasPrefix(a, "/") {
			t.Fatalf("argv %q: path-like token %q before --", res.Argv, a)
		}
	}
}

func wWantOK(t *testing.T, res *Result) {
	t.Helper()
	if res.ExitCode != 0 || !res.OK {
		t.Fatalf("exit %d ok %v stderr %q argv %q", res.ExitCode, res.OK, res.Stderr, res.Argv)
	}
}

// wArgvRecorder is a stand-in binary that prints its argv, one
// bracketed token each, and answers --version with version.
func wArgvRecorder(t *testing.T, dir, name, version string) string {
	t.Helper()
	return writeScript(t, dir, name, `if [ "$1" = --version ]; then echo "`+version+`"; exit 0; fi
for a in "$@"; do printf '[%s]' "$a"; done`)
}
