package shim

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"hop.top/foo/internal/tool/gate"
)

// The root guard refuses protect_roots values that name "/", the home
// directory, an entry directly under "/", or what such an entry links
// to (macOS /tmp -> /private/tmp, merged-/usr /bin -> /usr/bin),
// whatever the scope. It runs in two phases so that it never answers
// about a path the scope does not let the call reach:
//
//   - guardLexical, before authorization: the value as written, "~"
//     expanded, anchored at the working directory and cleaned. It reads
//     nothing from the filesystem, so its answer is the same whatever
//     exists, and no prompt is shown for such a value.
//   - guardGranted, after authorization and before exec: the value as
//     the kernel and the gate resolve it, where a cp/mv destination
//     lands, and file identity. Only paths the scope granted (or the
//     user approved) get here; the rest got the gate's answer, alike
//     whether or not they alias a protected place.

// lexicalWhy says why the absolute path p, cleaned lexically, names a
// protected place; "" when it does not. home is the cleaned home
// directory, "" when unknown. It reads nothing from the filesystem.
func lexicalWhy(p, home string) string {
	c := filepath.Clean(p)
	switch {
	case c == "/":
		return "is the filesystem root"
	case filepath.Dir(c) == "/":
		return "is directly under /"
	case home != "" && c == home:
		return "is the home directory"
	}
	return ""
}

// lexicalHome is $HOME cleaned, "" when unknown. It reads the
// environment only.
func lexicalHome() string {
	if h, err := os.UserHomeDir(); err == nil && filepath.IsAbs(h) {
		return filepath.Clean(h)
	}
	return ""
}

// rootGuard is the filesystem-dependent part: the canonical home
// directory and the identities (device + inode) of "/", $HOME, every
// entry directly under "/" and what such an entry links to, which
// catch every other spelling: case on a case-insensitive filesystem,
// bind mounts, links.
type rootGuard struct {
	home  []string // cleaned and canonical $HOME; empty when unknown
	known []knownEntry
}

type knownEntry struct {
	fi   fs.FileInfo
	what string
}

func newRootGuard() *rootGuard {
	g := &rootGuard{}
	add := func(what string, fi fs.FileInfo, err error) {
		if err == nil {
			g.known = append(g.known, knownEntry{fi: fi, what: what})
		}
	}
	fi, err := os.Stat("/")
	add("is the filesystem root", fi, err)
	if h := lexicalHome(); h != "" {
		g.home = append(g.home, h)
		if c, err := gate.Canonical("/", h); err == nil {
			g.home = append(g.home, c)
		}
		fi, err := os.Lstat(h)
		add("is the home directory", fi, err)
		fi, err = os.Stat(h)
		add("is the home directory", fi, err)
	}
	ents, _ := os.ReadDir("/")
	for _, e := range ents {
		p := "/" + e.Name()
		fi, err := os.Lstat(p)
		add("is "+p+", an entry directly under /", fi, err)
		if e.Type()&fs.ModeSymlink != 0 {
			fi, err := os.Stat(p)
			add("is what "+p+" links to", fi, err)
		}
	}
	return g
}

// why says why the absolute path p is protected, "" when it is not. p
// is stat'ed as given, so the kernel resolves its links and "..".
func (g *rootGuard) why(p string) string {
	c := filepath.Clean(p)
	switch {
	case c == "/":
		return "is the filesystem root"
	case filepath.Dir(c) == "/":
		return "is directly under /"
	}
	for _, h := range g.home {
		if c == h {
			return "is the home directory"
		}
	}
	for _, stat := range []func(string) (fs.FileInfo, error){os.Lstat, os.Stat} {
		fi, err := stat(p)
		if err != nil {
			continue
		}
		for _, k := range g.known {
			if os.SameFile(fi, k.fi) {
				return k.what
			}
		}
	}
	return ""
}

func refuseRoot(s *Spec, p *Param, path, why string) error {
	return &gate.Error{
		Kind: gate.KindInvalidArgs, Param: p.Name, Path: path, Op: p.op,
		Message: fmt.Sprintf("%s; %s never acts on /, the home directory or an entry directly under / (nor what one links to), whatever the scope", why, s.Name),
	}
}

// cwd is the base for relative values.
func (e *Engine) cwd() string {
	if e.Cwd != "" {
		return e.Cwd
	}
	wd, _ := os.Getwd()
	return wd
}

// guardLexical refuses, before authorization, a protect_roots value
// that names a protected place as written. An into_dir param names a
// directory to move into, not the entry that changes; it is guarded
// where it lands, by guardGranted.
func (e *Engine) guardLexical(s *Spec, vals values) error {
	home, cwd := lexicalHome(), e.cwd()
	for _, p := range s.Params {
		if !p.ProtectRoots || p.IntoDir {
			continue
		}
		list, _ := vals[p.Name].([]string)
		for _, raw := range list {
			abs, err := gate.Anchor(cwd, raw)
			if err != nil {
				continue // the authorizer reports it
			}
			if why := lexicalWhy(abs, home); why != "" {
				return refuseRoot(s, p, raw, why)
			}
		}
	}
	return nil
}

// guardGranted refuses, after authorization and before exec, a granted
// protect_roots value that reaches a protected place once the
// filesystem is consulted: as written and stat'ed (the kernel resolves
// its links and ".."), as the gate resolves it, and the canonical paths
// that will run, including where an into_dir destination lands.
func (e *Engine) guardGranted(s *Spec, vals values, canonical map[string][]string) error {
	cwd := e.cwd()
	var g *rootGuard
	guard := func() *rootGuard {
		if g == nil {
			g = newRootGuard()
		}
		return g
	}
	for _, p := range s.Params {
		if !p.ProtectRoots || p.IntoDir {
			continue
		}
		list, _ := vals[p.Name].([]string)
		for _, raw := range list {
			abs, err := gate.Anchor(cwd, raw)
			if err != nil {
				continue
			}
			cands := []string{abs}
			if c, err := gate.Canonical(cwd, raw); err == nil {
				cands = append(cands, c)
			}
			for _, c := range cands {
				if why := guard().why(c); why != "" {
					return refuseRoot(s, p, raw, why)
				}
			}
		}
	}
	for _, p := range s.Params {
		if !p.ProtectRoots {
			continue
		}
		for _, c := range canonical[p.Name] {
			if why := guard().why(c); why != "" {
				return refuseRoot(s, p, c, why)
			}
		}
	}
	return nil
}
