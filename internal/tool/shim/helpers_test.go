package shim

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"hop.top/foo/internal/tool/gate"
)

// fakeAuth is a stand-in for the real gate: it canonicalizes paths
// physically against cwd and denies any path with a component in deny.
type fakeAuth struct {
	cwd  string
	deny map[string]bool
	// rewrite replaces the canonical paths of a param, to prove argv
	// uses the grant and not the model's raw values.
	rewrite map[string][]string
	calls   int
	last    gate.Request
}

func (a *fakeAuth) denied(p string) bool {
	for _, part := range strings.Split(p, "/") {
		if a.deny[part] {
			return true
		}
	}
	return false
}

func canonical(base, raw string) string {
	abs := raw
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(base, raw)
	}
	if c, err := filepath.EvalSymlinks(abs); err == nil {
		return c
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return filepath.Clean(abs)
	}
	return filepath.Join(dir, filepath.Base(abs))
}

func (a *fakeAuth) Authorize(_ context.Context, req gate.Request) (gate.Grant, error) {
	a.calls++
	a.last = req
	g := gate.Grant{Canonical: map[string][]string{}, Files: map[string][]string{}}
	var srcs []string
	for _, pa := range req.Paths {
		for _, v := range pa.Values {
			c := canonical(a.cwd, v)
			if a.denied(c) {
				return gate.Grant{}, &gate.Error{Kind: gate.KindDenied, Param: pa.Param, Path: c, Op: pa.Op, Message: "not in scope"}
			}
			g.Canonical[pa.Param] = append(g.Canonical[pa.Param], c)
		}
		// IntoDir as the real gate maps it: an existing directory
		// receives dst/<base(src)> per source declared before it.
		if pa.IntoDir && len(srcs) > 0 {
			dst := g.Canonical[pa.Param][0]
			if fi, err := os.Stat(dst); err == nil && fi.IsDir() {
				g.Canonical[pa.Param] = nil
				for _, s := range srcs {
					g.Canonical[pa.Param] = append(g.Canonical[pa.Param], filepath.Join(dst, filepath.Base(s)))
				}
			} else if len(srcs) > 1 {
				return gate.Grant{}, &gate.Error{Kind: gate.KindInvalidArgs, Param: pa.Param, Path: dst, Message: "must be an existing directory"}
			}
		}
		srcs = append(srcs, g.Canonical[pa.Param]...)
		if r, ok := a.rewrite[pa.Param]; ok {
			g.Canonical[pa.Param] = r
		}
		switch pa.Recursion {
		case gate.FilterBefore:
			files := []string{}
			for _, root := range g.Canonical[pa.Param] {
				_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
					if err != nil || !d.Type().IsRegular() {
						return nil
					}
					if a.denied(p) {
						g.Filtered++
						return nil
					}
					files = append(files, p)
					return nil
				})
			}
			g.Files[pa.Param] = files
		case gate.FilterAfter:
			g.Allow = func(p string) bool { return !a.denied(p) }
		}
	}
	return g, nil
}

// tempDir returns a fresh directory in canonical form (/private/var on
// macOS), so expectations match what the authorizer grants.
func tempDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// mustSpec parses a fixture spec and pins its binary.
func mustSpec(t *testing.T, src string) *Loaded {
	t.Helper()
	s, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("fixture spec rejected: %v", err)
	}
	bin, err := pinBinary(s.Command.Bin)
	if err != nil {
		t.Fatalf("fixture spec: %v", err)
	}
	return &Loaded{Spec: s, Bin: bin, Source: Source{Kind: SourceBuiltin}, Raw: []byte(src)}
}

func builtinWC(t *testing.T) *Loaded {
	t.Helper()
	l, inv := Load(LoadOptions{}).Lookup("wc")
	if l == nil {
		t.Fatalf("builtin wc missing (invalid: %v)", inv)
	}
	return l
}

func call(t *testing.T, e *Engine, l *Loaded, args string) (*Result, error) {
	t.Helper()
	return e.Call(context.Background(), l, json.RawMessage(args))
}

func mustCall(t *testing.T, e *Engine, l *Loaded, args string) *Result {
	t.Helper()
	res, err := call(t, e, l, args)
	if err != nil {
		t.Fatalf("call %s: %v", args, err)
	}
	return res
}

func wantKind(t *testing.T, err error, kind gate.Kind) *gate.Error {
	t.Helper()
	ge, ok := err.(*gate.Error)
	if !ok {
		t.Fatalf("error = %v (%T); want *gate.Error kind %s", err, err, kind)
	}
	if ge.Kind != kind {
		t.Fatalf("error kind = %s (%v); want %s", ge.Kind, ge, kind)
	}
	return ge
}

func stdout(r *Result) string {
	if r.Stdout == nil {
		return ""
	}
	return *r.Stdout
}

func indexOf(list []string, v string) int { return slices.Index(list, v) }
