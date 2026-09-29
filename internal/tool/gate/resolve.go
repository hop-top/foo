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
	r, err := physical(abs, 0, nil)
	if err != nil {
		return "", err
	}
	return r.path, nil
}

// Anchor makes raw absolute the way Canonical reads it ("~" and "~/..."
// are the home directory, other relative paths are taken against cwd)
// without resolving or cleaning it.
func Anchor(cwd, raw string) (string, error) { return anchor(cwd, raw) }

// resolved is a physically resolved path.
type resolved struct {
	path string
	// missing counts trailing components that do not exist.
	missing int
	// seen lists the directories in which a component the model named
	// turned out to be a symlink: resolving it read something there
	// that the path as written does not say.
	seen []string
}

// climbError is a ".." the model wrote that leaves a directory the
// caller does not let it climb out of, cancelling a component looked
// up where it may not look either: resolving it would tell whether
// that component exists, and what it is.
type climbError struct{ dir string }

func (e *climbError) Error() string { return fmt.Sprintf("%q: \"..\" climbs out of it", e.dir) }

// modelFrom is the index in abs where the part the model wrote begins:
// the working directory and the home directory anchor() puts in front
// of it are foo's, not the model's.
func modelFrom(abs, raw string) int {
	switch {
	case filepath.IsAbs(raw):
		return 0
	case raw == "~" || strings.HasPrefix(raw, "~/"):
		return len(abs) - len(raw[1:])
	default:
		return len(abs) - len(raw)
	}
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
//
// from is the index where the model's part of abs begins (modelFrom).
// A ".." in that part cancels the model's last component; unless
// climb allows the directory the ".." leaves (missing tail included)
// or the directory that component was looked up in, it stops with
// *climbError. A ".." cancelling part of the working directory is
// allowed; a nil climb allows every "..". On an error the returned
// resolved is how far resolution got.
func physical(abs string, from int, climb func(dir string) bool) (resolved, error) {
	dest := "/"
	rest := abs
	links := 0
	// model is the length of the suffix of rest the model wrote; link
	// targets spliced in front of it are the filesystem's.
	model := len(abs) - from
	// looked holds, for each of the model's components still in the
	// path, the directory it was looked up in.
	var tail, seen, looked []string
	partial := func() resolved {
		return resolved{path: filepath.Join(append([]string{dest}, tail...)...), missing: len(tail), seen: seen}
	}
	for {
		rest = strings.TrimLeft(rest, "/")
		model = min(model, len(rest))
		if rest == "" {
			break
		}
		byModel := len(rest) <= model
		comp := rest
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			comp, rest = rest[:i], rest[i:]
		} else {
			rest = ""
		}
		model = min(model, len(rest))
		if byModel && comp == ".." && len(looked) > 0 {
			in := looked[len(looked)-1]
			looked = looked[:len(looked)-1]
			if climb != nil && !climb(partial().path) && !climb(in) {
				return partial(), &climbError{dir: partial().path}
			}
		} else if byModel && comp != "." && comp != ".." {
			looked = append(looked, partial().path)
		}
		switch {
		case comp == ".":
			continue
		case comp == ".." && tail != nil:
			return partial(), fmt.Errorf("%q: \"..\" follows the missing component %q", abs, tail[0])
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
			return partial(), err
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			dest = next
			continue
		}
		links++
		if links > maxLinks {
			return partial(), fmt.Errorf("%q: too many levels of symbolic links", abs)
		}
		target, err := os.Readlink(next)
		if err != nil {
			return partial(), err
		}
		if byModel {
			seen = append(seen, dest)
		}
		if filepath.IsAbs(target) {
			dest = "/"
		}
		rest = target + "/" + rest
	}
	return partial(), nil
}

// isMissing reports a component that does not exist, including a
// lookup through a non-directory.
func isMissing(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// dirent resolves abs as a directory entry: the parent is resolved
// physically and the final component kept, so a final symlink names
// the link itself. A final "." or ".." or a trailing slash names the
// resolved directory, as the kernel would. from and climb are as for
// physical; on an error entry is how far resolution got.
func dirent(abs string, from int, climb func(string) bool) (entry, parent resolved, err error) {
	base := abs[strings.LastIndexByte(abs, '/')+1:]
	if base == "" || base == "." || base == ".." {
		entry, err = physical(abs, from, climb)
		return entry, parentOf(entry), err
	}
	parent, err = physical(abs[:len(abs)-len(base)], min(from, len(abs)-len(base)), climb)
	if err != nil {
		return parent, parent, err
	}
	entry = resolved{path: filepath.Join(parent.path, base), missing: parent.missing, seen: parent.seen}
	if _, lerr := os.Lstat(entry.path); lerr != nil {
		if !isMissing(lerr) {
			return parent, parent, lerr
		}
		entry.missing++
	}
	return entry, parent, nil
}

// parentOf is the directory holding r.
func parentOf(r resolved) resolved {
	p := resolved{path: filepath.Dir(r.path), seen: r.seen}
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
