// Spec-backed shim tools: the in-process engine behind -T for OS
// commands (wc, …), the foo-tool-<name> multi-call entry point, and
// `foo tool install|uninstall`.

package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"hop.top/foo/internal/tool"
	"hop.top/foo/internal/tool/builtin"
	"hop.top/foo/internal/tool/gate"
	"hop.top/foo/internal/tool/shim"
	extdiscover "hop.top/kit/go/ai/ext/discover"
	"hop.top/kit/go/core/xdg"
)

// shimAccess is how a run's shim calls may reach the user.
type shimAccess struct {
	// confirm asks approval questions; nil when nobody can be asked
	// (foo-tool-<name> run by another host), so any call that needs
	// approval is refused.
	confirm gate.Confirmer
	// approveAll asks before every call (--tools-approve).
	approveAll bool
}

// newShimAuthorizer is the one place shim calls get their authorizer,
// for in-process -T runs and for foo-tool-<name> links run by other
// hosts: the path gate over foo's scope.yaml and side-effect policy
// (kit's table, foo's overlay, the user's tool-policy.yaml), with
// relative paths resolved against cwd, the directory captured when
// the run started.
//
// A scope.yaml or tool-policy.yaml that cannot be loaded is an error,
// never a silent deny-all or allow-all: the caller fails the run with
// it, the way foo fails on a broken config file.
func newShimAuthorizer(cwd string, access shimAccess) (gate.Authorizer, error) {
	sc, err := gate.LoadScope(scopeTool)
	if err != nil {
		return nil, fmt.Errorf("load tool scope policy: %w", err)
	}
	tbl, err := gate.LoadPolicy(scopeTool)
	if err != nil {
		return nil, fmt.Errorf("load tool side-effect policy: %w", err)
	}
	opts := []gate.Option{
		gate.WithScope(sc),
		gate.WithCwd(cwd),
		gate.WithPolicy(tbl),
		gate.WithApproveAll(access.approveAll),
	}
	if access.confirm != nil {
		opts = append(opts, gate.WithConfirmer(access.confirm))
	}
	return gate.New(opts...)
}

// newShimEngine builds the engine for this run, without an authorizer
// (every call denied) until one is attached. The working directory is
// captured once: it is the base for every relative path the model
// sends and where commands run.
func newShimEngine() *shim.Engine {
	cwd, _ := os.Getwd()
	e := &shim.Engine{Cwd: cwd}
	if dir, err := xdg.StateDir("foo"); err == nil {
		e.Flavors = shim.NewFlavors(filepath.Join(dir, "tool-flavors.json"))
	}
	return e
}

// RunToolShim serves foo started as foo-tool-<name>: the external plugin
// protocol, run through the same spec validation and authorization as
// in-process calls. Nobody can be asked for approval here, so a call
// that needs it is denied. main calls it before building the command
// tree.
func RunToolShim(ctx context.Context, name, v string, args []string) int {
	engine := newShimEngine()
	m := &shim.MultiCall{
		Name:    name,
		Version: v,
		Catalog: shim.Load(shim.DefaultLoadOptions()),
		Engine:  engine,
		Authorizer: func() (gate.Authorizer, error) {
			return newShimAuthorizer(engine.Cwd, shimAccess{})
		},
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
	return m.Run(ctx, args)
}

// toolSet is every tool foo can offer, plus the candidates that lost
// their name.
type toolSet struct {
	registry *tool.Registry
	// shadowed are tools not registered because an earlier source owns
	// the name: built-ins beat shim specs, specs beat PATH plugins.
	shadowed []toolRow
	// invalid are specs that won their name but failed to load.
	invalid map[string]*shim.Invalid
	// engine runs every shim tool of the set and authorizes gated
	// plugins; its authorizer is attached once a run selects either.
	engine *shim.Engine
}

// discoverTools assembles, in precedence order: foo's Go built-ins,
// shim specs (built-in, system, user), then foo-tool-* binaries on
// $PATH. A name keeps its first owner; later claimants are listed as
// shadowed. An invalid spec still owns its name, so a broken override
// never silently yields to a plugin. PATH entries that are links to
// foo itself are skipped.
func discoverTools(names []string, warn io.Writer) (*toolSet, error) {
	ts := &toolSet{registry: tool.NewRegistry(), invalid: map[string]*shim.Invalid{}}
	for _, t := range []tool.Tool{builtin.TimeTool{}, builtin.VersionTool{}} {
		if err := ts.registry.Register(t); err != nil {
			return nil, err
		}
	}
	listing := len(names) == 0
	cat := shim.Load(shim.DefaultLoadOptions())
	for _, inv := range cat.Invalid {
		ts.invalid[inv.Name] = inv
		if listing || slices.Contains(names, inv.Name) {
			_, _ = fmt.Fprintf(warn, "[foo] warning: skipping %v\n", inv)
		}
	}
	ts.engine = newShimEngine()
	for _, l := range cat.Specs {
		if listing {
			for _, w := range l.Warnings {
				_, _ = fmt.Fprintf(warn, "[foo] warning: tool spec %s (%s): %s\n", l.Spec.Name, l.Source, w)
			}
		}
		t := shim.NewTool(ts.engine, l)
		if ts.registry.Register(t) != nil {
			ts.shadow(t, "a built-in tool")
		}
	}

	scanner := &extdiscover.Scanner{Prefix: shim.LinkPrefix}
	found, err := scanner.Scan()
	if err != nil {
		return nil, err
	}
	self := selfExecutable()
	for i := range found {
		f := &found[i]
		if self != nil && sameFile(f.Path, self) {
			continue
		}
		if owner := ts.owner(f.Name); owner != "" {
			ts.shadowed = append(ts.shadowed, externalRow(f.Name, f.Path, "", owner))
			continue
		}
		ext, err := tool.ExternalToolFromFound(f)
		if err != nil {
			warnSkippedTool(warn, names, err)
			continue
		}
		ext.SetEngine(ts.engine)
		if owner := ts.owner(ext.Name()); owner != "" || ts.registry.Register(ext) != nil {
			if owner == "" {
				owner = tool.SourceOf(mustGet(ts.registry, ext.Name()))
			}
			ts.shadowed = append(ts.shadowed, externalRow(ext.Name(), f.Path, ext.Description(), owner))
		}
	}
	return ts, nil
}

// owner names what holds name, or "" when it is free.
func (ts *toolSet) owner(name string) string {
	if t, ok := ts.registry.Get(name); ok {
		return tool.SourceOf(t)
	}
	if inv, ok := ts.invalid[name]; ok {
		return inv.Source.String() + " (invalid)"
	}
	return ""
}

func (ts *toolSet) shadow(t *shim.Tool, by string) {
	row := toolRowOf(t)
	row.Status = statusShadowed
	row.Description = "shadowed by " + by
	ts.shadowed = append(ts.shadowed, row)
}

// gatedTool reports whether t's calls go through the path gate: every
// spec tool, and plugins that declare foo_tool annotations.
func gatedTool(t tool.Tool) bool {
	switch v := t.(type) {
	case *shim.Tool:
		return true
	case *tool.ExternalTool:
		return v.Gated()
	}
	return false
}

func mustGet(r *tool.Registry, name string) tool.Tool {
	t, _ := r.Get(name)
	return t
}

func externalRow(name, path, desc, owner string) toolRow {
	if desc != "" {
		desc += "; "
	}
	return toolRow{
		Name:        name,
		Source:      path,
		Description: desc + "shadowed by " + owner,
		SideEffect:  sideEffectUnknown,
		Paths:       pathsUngated,
		Status:      statusShadowed,
	}
}

// selfExecutable is foo's own binary, for skipping links to it.
func selfExecutable() os.FileInfo {
	p, err := os.Executable()
	if err != nil {
		return nil
	}
	fi, err := os.Stat(p)
	if err != nil {
		return nil
	}
	return fi
}

func sameFile(path string, fi os.FileInfo) bool {
	other, err := os.Stat(path)
	return err == nil && os.SameFile(other, fi)
}

// shimInstaller manages links in dir (default: foo's bin home).
func shimInstaller(dir string) (*shim.Installer, error) {
	if dir == "" {
		home, err := xdg.BinHome("foo")
		if err != nil {
			return nil, err
		}
		dir = home
	}
	target, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate foo executable: %w", err)
	}
	stateDir, err := xdg.StateDir("foo")
	if err != nil {
		return nil, err
	}
	return &shim.Installer{
		Dir:      dir,
		Target:   target,
		Manifest: filepath.Join(stateDir, "tool-shims.json"),
		Version:  version,
	}, nil
}

// onPath reports whether dir is a $PATH entry.
func onPath(dir string) bool {
	abs, _ := filepath.Abs(dir)
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if pa, err := filepath.Abs(p); err == nil && pa == abs {
			return true
		}
	}
	return false
}
