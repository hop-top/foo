package gate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// maxLinks bounds symlink expansion per path, as the kernel does
// (ELOOP).
const maxLinks = 255

// Canonical resolves raw the way a command started in cwd would reach
// it, and returns the physical absolute path with no symlinks left:
// "~" and "~/..." expand to the home directory, other relative paths
// are taken against cwd, every symlink is resolved where it is met,
// and ".." climbs from the resolved directory, never lexically. A
// missing tail is kept as written below its deepest existing ancestor.
//
// "~user" and "$VAR" are not expanded; they are names relative to cwd.
func Canonical(cwd, raw string) (string, error) {
	abs, err := anchor(cwd, raw)
	if err != nil {
		return "", err
	}
	r, err := physical(abs)
	if err != nil {
		return "", err
	}
	return r.path, nil
}

// resolved is a physically resolved path.
type resolved struct {
	path string
	// missing counts trailing components that do not exist.
	missing int
}

// anchor makes raw absolute without cleaning it: cleaning before
// resolution would apply ".." lexically.
func anchor(cwd, raw string) (string, error) {
	switch {
	case raw == "":
		return "", errors.New("empty path")
	case strings.IndexByte(raw, 0) >= 0:
		return "", errors.New("path contains a NUL byte")
	case raw == "~" || strings.HasPrefix(raw, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve ~: %w", err)
		}
		return home + "/" + raw[1:], nil
	case filepath.IsAbs(raw):
		return raw, nil
	default:
		return cwd + "/" + raw, nil
	}
}

// physical walks abs one component at a time from "/". dest is always
// a symlink-free existing directory, so ".." is its parent. Once a
// component is missing the rest is kept as written; ".." after that
// point cannot be resolved and is rejected.
func physical(abs string) (resolved, error) {
	dest := "/"
	rest := abs
	links := 0
	var tail []string
	for {
		rest = strings.TrimLeft(rest, "/")
		if rest == "" {
			break
		}
		comp := rest
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			comp, rest = rest[:i], rest[i:]
		} else {
			rest = ""
		}
		switch {
		case comp == ".":
			continue
		case comp == ".." && tail != nil:
			return resolved{}, fmt.Errorf("%q: \"..\" follows the missing component %q", abs, tail[0])
		case comp == "..":
			dest = filepath.Dir(dest)
			continue
		case tail != nil:
			tail = append(tail, comp)
			continue
		}
		next := filepath.Join(dest, comp)
		fi, err := os.Lstat(next)
		if err != nil {
			if isMissing(err) {
				tail = []string{comp}
				continue
			}
			return resolved{}, err
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			dest = next
			continue
		}
		links++
		if links > maxLinks {
			return resolved{}, fmt.Errorf("%q: too many levels of symbolic links", abs)
		}
		target, err := os.Readlink(next)
		if err != nil {
			return resolved{}, err
		}
		if filepath.IsAbs(target) {
			dest = "/"
		}
		rest = target + "/" + rest
	}
	return resolved{path: filepath.Join(append([]string{dest}, tail...)...), missing: len(tail)}, nil
}

// isMissing reports a component that does not exist, including a
// lookup through a non-directory.
func isMissing(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// dirent resolves abs as a directory entry: the parent is resolved
// physically and the final component kept, so a final symlink names
// the link itself. A final "." or ".." or a trailing slash names the
// resolved directory, as the kernel would.
func dirent(abs string) (entry, parent resolved, err error) {
	base := abs[strings.LastIndexByte(abs, '/')+1:]
	if base == "" || base == "." || base == ".." {
		entry, err = physical(abs)
		if err != nil {
			return resolved{}, resolved{}, err
		}
		return entry, parentOf(entry), nil
	}
	parent, err = physical(abs[:len(abs)-len(base)])
	if err != nil {
		return resolved{}, resolved{}, err
	}
	entry = resolved{path: filepath.Join(parent.path, base), missing: parent.missing}
	if _, lerr := os.Lstat(entry.path); lerr != nil {
		if !isMissing(lerr) {
			return resolved{}, resolved{}, lerr
		}
		entry.missing++
	}
	return entry, parent, nil
}

// parentOf is the directory holding r.
func parentOf(r resolved) resolved {
	p := resolved{path: filepath.Dir(r.path)}
	if r.missing > 0 {
		p.missing = r.missing - 1
	}
	return p
}

// missingAncestors lists r's missing ancestors, nearest first,
// excluding r itself.
func missingAncestors(r resolved) []string {
	var out []string
	p := r.path
	for i := 1; i < r.missing; i++ {
		p = filepath.Dir(p)
		out = append(out, p)
	}
	return out
}
