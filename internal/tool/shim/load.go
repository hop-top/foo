package shim

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"hop.top/kit/go/core/xdg"
)

//go:embed specs/*.yaml
var builtinSpecs embed.FS

// Source kinds, in rising precedence.
const (
	SourceBuiltin = "builtin"
	SourceSystem  = "system"
	SourceUser    = "user"
)

// SystemDir is where system-wide specs live (kit's /etc/xdg convention).
const SystemDir = "/etc/xdg/foo/tools"

// Source says where a spec was read from.
type Source struct {
	Kind string
	Path string // empty for builtin
}

func (s Source) String() string {
	if s.Kind == SourceBuiltin {
		return SourceBuiltin
	}
	return s.Kind + ":" + s.Path
}

// Loaded is a valid spec ready to run.
type Loaded struct {
	Spec *Spec
	// Bin is the pinned binary: the first command.bin candidate that
	// exists, chosen at load and never looked up on PATH.
	Bin    string
	Source Source
	// Overrides is set when a user or system spec replaces a built-in.
	Overrides bool
	// Raw is the spec source.
	Raw []byte
	// Warnings are lint findings that do not reject the spec.
	Warnings []string
}

// SourceLabel is the SOURCE column of `foo tool list`.
func (l *Loaded) SourceLabel() string {
	if l.Overrides {
		return l.Source.String() + " (overrides builtin)"
	}
	return l.Source.String()
}

// Invalid is a spec that won its name but cannot be used. Its name is
// not registered: a broken override never falls back to the built-in.
type Invalid struct {
	Name   string
	Source Source
	Err    error
}

func (i *Invalid) Error() string {
	return fmt.Sprintf("tool spec %s (%s): %v", i.Name, i.Source, i.Err)
}

// Catalog is every spec visible to foo.
type Catalog struct {
	Specs   []*Loaded
	Invalid []*Invalid
}

// Lookup finds a name among valid and invalid specs.
func (c *Catalog) Lookup(name string) (*Loaded, *Invalid) {
	for _, l := range c.Specs {
		if l.Spec.Name == name {
			return l, nil
		}
	}
	for _, i := range c.Invalid {
		if i.Name == name {
			return nil, i
		}
	}
	return nil, nil
}

// LoadOptions selects spec directories.
type LoadOptions struct {
	// UserDir and SystemDir are scanned for <name>.yaml; empty skips.
	UserDir, SystemDir string
	// Builtin overrides the embedded specs (tests); its specs live
	// under specs/.
	Builtin fs.FS
}

// DefaultLoadOptions uses the foo config dir and /etc/xdg/foo.
func DefaultLoadOptions() LoadOptions {
	opts := LoadOptions{SystemDir: SystemDir}
	if dir, err := xdg.ConfigDir("foo"); err == nil {
		opts.UserDir = filepath.Join(dir, "tools")
	}
	return opts
}

type candidate struct {
	name   string
	source Source
	data   []byte
	err    error
}

// Load reads built-in, system and user specs. For each name the
// highest-precedence file wins (user > system > builtin), valid or not.
func Load(opts LoadOptions) *Catalog {
	builtin := opts.Builtin
	if builtin == nil {
		builtin = builtinSpecs
	}
	layers := [][]candidate{
		readFS(builtin, "specs", Source{Kind: SourceBuiltin}),
		readDir(opts.SystemDir, SourceSystem),
		readDir(opts.UserDir, SourceUser),
	}
	builtinNames := map[string]bool{}
	for _, c := range layers[0] {
		builtinNames[c.name] = true
	}
	winners := map[string]candidate{}
	for _, layer := range layers {
		for _, c := range layer {
			winners[c.name] = c
		}
	}
	names := make([]string, 0, len(winners))
	for n := range winners {
		names = append(names, n)
	}
	sort.Strings(names)

	cat := &Catalog{}
	for _, n := range names {
		c := winners[n]
		l, err := load(c, builtinNames)
		if err != nil {
			cat.Invalid = append(cat.Invalid, &Invalid{Name: n, Source: c.source, Err: err})
			continue
		}
		cat.Specs = append(cat.Specs, l)
	}
	return cat
}

func load(c candidate, builtinNames map[string]bool) (*Loaded, error) {
	if c.err != nil {
		return nil, c.err
	}
	s, err := Parse(c.data)
	if err != nil {
		return nil, err
	}
	if s.Name != c.name {
		return nil, fmt.Errorf("name %q does not match file name %q", s.Name, c.name+".yaml")
	}
	bin, err := pinBinary(s.Command.Bin)
	if err != nil {
		return nil, err
	}
	l := &Loaded{
		Spec:      s,
		Bin:       bin,
		Source:    c.source,
		Overrides: c.source.Kind != SourceBuiltin && builtinNames[c.name],
		Raw:       c.data,
	}
	if c.source.Kind != SourceBuiltin && !builtinNames[c.name] {
		for _, p := range s.Params {
			if p.Type == TypeString {
				l.Warnings = append(l.Warnings, fmt.Sprintf(
					"param %q: strings are not path-gated; make sure %s cannot read a path from it", p.Name, s.Name))
			}
		}
	}
	return l, nil
}

// pinBinary returns the first candidate that is an executable file.
func pinBinary(bins []string) (string, error) {
	for _, b := range bins {
		fi, err := os.Stat(b)
		if err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return b, nil
		}
	}
	return "", fmt.Errorf("no executable among command.bin %v", bins)
}

func readFS(fsys fs.FS, dir string, src Source) []candidate {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil
	}
	var out []candidate
	for _, e := range entries {
		name, ok := specName(e)
		if !ok {
			continue
		}
		data, err := fs.ReadFile(fsys, dir+"/"+e.Name())
		out = append(out, candidate{name: name, source: src, data: data, err: err})
	}
	return out
}

func readDir(dir, kind string) []candidate {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []candidate
	for _, e := range entries {
		name, ok := specName(e)
		if !ok {
			continue
		}
		p := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		out = append(out, candidate{name: name, source: Source{Kind: kind, Path: p}, data: data, err: err})
	}
	return out
}

func specName(e fs.DirEntry) (string, bool) {
	n := e.Name()
	if e.IsDir() || strings.HasPrefix(n, ".") || !strings.HasSuffix(n, ".yaml") {
		return "", false
	}
	return strings.TrimSuffix(n, ".yaml"), true
}
