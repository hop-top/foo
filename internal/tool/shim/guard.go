package shim

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"hop.top/foo/internal/tool/gate"
)

// rootGuard knows the entries a protect_roots param never names,
// whatever the scope: "/", the home directory, every entry directly
// under "/", and what such an entry links to (macOS /tmp → /private/tmp,
// merged-/usr /bin → /usr/bin). Names are compared lexically and after
// physical resolution; identities (device + inode) catch every other
// spelling: case on a case-insensitive filesystem, bind mounts, links.
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
	if h, err := os.UserHomeDir(); err == nil && filepath.IsAbs(h) {
		g.home = append(g.home, filepath.Clean(h))
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

// guardValues refuses a protect_roots value before authorization, so
// no prompt is ever shown for it: the value as written (anchored,
// lexically clean), as the kernel resolves it, and as foo resolves it.
// An into_dir param names a directory to move into, not the entry that
// changes; it is guarded where it lands, by guardGrant.
func (e *Engine) guardValues(s *Spec, vals values) error {
	var g *rootGuard
	for _, p := range s.Params {
		if !p.ProtectRoots || p.IntoDir {
			continue
		}
		list, _ := vals[p.Name].([]string)
		for _, raw := range list {
			if g == nil {
				g = newRootGuard()
			}
			cwd := e.Cwd
			if cwd == "" {
				cwd, _ = os.Getwd()
			}
			abs, err := gate.Anchor(cwd, raw)
			if err != nil {
				continue // the authorizer reports it
			}
			cands := []string{abs}
			if c, err := gate.Canonical(cwd, raw); err == nil {
				cands = append(cands, c)
			}
			for _, c := range cands {
				if why := g.why(c); why != "" {
					return refuseRoot(s, p, raw, why)
				}
			}
		}
	}
	return nil
}

// guardGrant refuses a granted protect_roots path: the canonical paths
// that will run, including where an into_dir destination lands.
func (e *Engine) guardGrant(s *Spec, canonical map[string][]string) error {
	var g *rootGuard
	for _, p := range s.Params {
		if !p.ProtectRoots {
			continue
		}
		for _, c := range canonical[p.Name] {
			if g == nil {
				g = newRootGuard()
			}
			if why := g.why(c); why != "" {
				return refuseRoot(s, p, c, why)
			}
		}
	}
	return nil
}
