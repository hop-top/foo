package gate_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/log/v2"
	"hop.top/foo/internal/tool"
	"hop.top/foo/internal/tool/gate"
	"hop.top/kit/go/core/scope"
)

// fsEnv is a throwaway tree with HOME and XDG_CONFIG_HOME inside it.
// root is canonical (macOS /var → /private/var), so expectations built
// from it compare equal to what the gate resolves.
type fsEnv struct {
	root string
	home string
	cfg  string // XDG_CONFIG_HOME
}

func newFS(t *testing.T) *fsEnv {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &fsEnv{root: root, home: filepath.Join(root, "home"), cfg: filepath.Join(root, "cfg")}
	e.mkdir(t, "home")
	e.mkdir(t, "cfg")
	t.Setenv("HOME", e.home)
	t.Setenv("XDG_CONFIG_HOME", e.cfg)
	return e
}

// p joins rel onto the root.
func (e *fsEnv) p(rel string) string { return filepath.Join(e.root, rel) }

func (e *fsEnv) mkdir(t *testing.T, rel string) string {
	t.Helper()
	dir := e.p(rel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func (e *fsEnv) file(t *testing.T, rel, content string) string {
	t.Helper()
	path := e.p(rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// link creates rel as a symlink to target (absolute, or relative to
// the link's directory).
func (e *fsEnv) link(t *testing.T, target, rel string) string {
	t.Helper()
	path := e.p(rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	return path
}

// scopeYAML writes foo's per-user scope.yaml. Patterns may use {root}.
func (e *fsEnv) scopeYAML(t *testing.T, yaml string) {
	t.Helper()
	e.file(t, "cfg/foo/scope.yaml", strings.ReplaceAll(yaml, "{root}", e.root))
}

// confirmer records every question and answers from a script.
type confirmer struct {
	answers []bool
	err     error
	asked   []string
}

func (c *confirmer) Confirm(q string) (bool, error) {
	c.asked = append(c.asked, q)
	if c.err != nil {
		return false, c.err
	}
	if len(c.answers) == 0 {
		return false, nil
	}
	a := c.answers[0]
	c.answers = c.answers[1:]
	return a, nil
}

// yes answers every question with yes.
func yes() *confirmer { return &confirmer{answers: []bool{true, true, true, true}} }

// newGate loads foo's scope and policy the way production does and
// builds a gate rooted at cwd. Warnings land in the returned buffer.
func newGate(t *testing.T, cwd string, opts ...gate.Option) (*gate.Gate, *bytes.Buffer) {
	t.Helper()
	sc, err := gate.LoadScope("foo")
	if err != nil {
		t.Fatalf("LoadScope: %v", err)
	}
	tbl, err := gate.LoadPolicy("foo")
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	var logBuf bytes.Buffer
	base := []gate.Option{
		gate.WithScope(sc),
		gate.WithPolicy(tbl),
		gate.WithCwd(cwd),
		gate.WithLogger(log.New(&logBuf)),
	}
	g, err := gate.New(append(base, opts...)...)
	if err != nil {
		t.Fatalf("gate.New: %v", err)
	}
	return g, &logBuf
}

// readCall is a read-only single-path call (ls/cat style).
func readCall(tool string, values ...string) gate.Request {
	return gate.Request{
		Tool:       tool,
		SideEffect: "read",
		Paths:      []gate.PathArg{{Param: "path", Values: values, Op: scope.Read}},
	}
}

func authorize(t *testing.T, g *gate.Gate, req gate.Request) (gate.Grant, *gate.Error) {
	t.Helper()
	grant, err := g.Authorize(context.Background(), req)
	if err == nil {
		return grant, nil
	}
	var ge *gate.Error
	if !errors.As(err, &ge) {
		t.Fatalf("Authorize error %v (%T) is not a *gate.Error", err, err)
	}
	return grant, ge
}

func mustAllow(t *testing.T, g *gate.Gate, req gate.Request) gate.Grant {
	t.Helper()
	grant, ge := authorize(t, g, req)
	if ge != nil {
		t.Fatalf("want allowed, got %v", ge)
	}
	return grant
}

func mustRefuse(t *testing.T, g *gate.Gate, req gate.Request, kind gate.Kind) *gate.Error {
	t.Helper()
	_, ge := authorize(t, g, req)
	if ge == nil {
		t.Fatalf("want %s, got allowed", kind)
	}
	if ge.Kind != kind {
		t.Fatalf("kind = %s; want %s (%v)", ge.Kind, kind, ge)
	}
	return ge
}

func wantCanonical(t *testing.T, grant gate.Grant, param string, want ...string) {
	t.Helper()
	got := grant.Canonical[param]
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("Canonical[%s] = %q; want %q", param, got, want)
	}
}

// prompter builds the real tool.Prompter over an in-memory answer
// source, or over an open that fails like a missing terminal.
func prompter(answers string, out io.Writer) *tool.Prompter {
	return tool.NewPrompter(func() (io.Reader, error) { return strings.NewReader(answers), nil }, out)
}

func noTTY(out io.Writer) *tool.Prompter {
	return tool.NewPrompter(func() (io.Reader, error) {
		return nil, errors.New("open /dev/tty: device not configured")
	}, out)
}
