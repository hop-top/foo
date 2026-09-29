package shim

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Link actions reported by Install and Uninstall.
const (
	ActionCreated   = "created"
	ActionUnchanged = "unchanged"
	ActionReplaced  = "replaced"
	ActionSkipped   = "skipped"
	ActionRemoved   = "removed"
	ActionKept      = "kept"
)

// LinkResult is one row of `foo tool install|uninstall`.
type LinkResult struct {
	Name   string `json:"name" yaml:"name" table:"NAME,priority=9"`
	Link   string `json:"link" yaml:"link" table:"LINK,priority=8"`
	Action string `json:"action" yaml:"action" table:"ACTION,priority=9"`
	Reason string `json:"reason,omitempty" yaml:"reason,omitempty" table:"REASON,priority=5"`
}

// Manifest records the links foo created, so uninstall and regeneration
// touch nothing else.
type Manifest struct {
	Links []ManifestEntry `json:"links"`
}

// ManifestEntry is one created link.
type ManifestEntry struct {
	Name    string `json:"name"`
	Link    string `json:"link"`
	Target  string `json:"target"`
	Digest  string `json:"digest"`
	Version string `json:"foo_version"`
}

// Installer manages foo-tool-<name> links in one directory.
type Installer struct {
	// Dir holds the links.
	Dir string
	// Target is the foo executable the links point at (unresolved, so
	// a package-manager upgrade keeps them valid).
	Target string
	// Manifest is the JSON manifest path.
	Manifest string
	// Version is recorded per entry.
	Version string
}

func (in *Installer) dir() (string, error) {
	d, err := filepath.Abs(in.Dir)
	if err != nil {
		return "", err
	}
	return filepath.Clean(d), nil
}

// Install links every spec. Existing links to foo are kept or
// refreshed; anything else at a link path is left alone and reported
// skipped. Links this manifest created for specs that no longer exist
// are removed. Running it twice changes nothing.
func (in *Installer) Install(specs []*Loaded) ([]LinkResult, error) {
	dir, err := in.dir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	man := in.readManifest()
	recorded := man.byLink()
	keep := map[string]bool{}
	var out []LinkResult

	sorted := append([]*Loaded(nil), specs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Spec.Name < sorted[j].Spec.Name })
	for _, l := range sorted {
		name := l.Spec.Name
		link := filepath.Join(dir, LinkPrefix+name)
		res := LinkResult{Name: name, Link: link}
		fi, err := os.Lstat(link)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			err = in.symlink(link)
			res.Action = ActionCreated
		case err != nil:
		case fi.Mode()&fs.ModeSymlink == 0:
			res.Action, res.Reason = ActionSkipped, "exists and is not a link to foo"
		default:
			dest, _ := os.Readlink(link)
			switch {
			case dest == in.Target:
				res.Action = ActionUnchanged
			case in.isFooLink(link, recorded[link].Target):
				err = in.symlink(link)
				res.Action = ActionReplaced
			default:
				res.Action, res.Reason = ActionSkipped, "links to "+dest+", not foo"
			}
		}
		if err != nil {
			return out, fmt.Errorf("link %s: %w", link, err)
		}
		if res.Action != ActionSkipped {
			keep[link] = true
			recorded[link] = ManifestEntry{Name: name, Link: link, Target: in.Target, Digest: l.Spec.digest, Version: in.Version}
		}
		out = append(out, res)
	}

	for link, e := range recorded {
		if keep[link] || filepath.Dir(link) != dir {
			continue
		}
		res := LinkResult{Name: e.Name, Link: link, Action: ActionKept, Reason: "no longer a link to foo"}
		if in.isFooLink(link, e.Target) {
			if err := os.Remove(link); err != nil {
				return out, err
			}
			res.Action, res.Reason = ActionRemoved, "no spec named "+e.Name
		}
		delete(recorded, link)
		out = append(out, res)
	}
	return out, in.writeManifest(recorded)
}

// Uninstall removes the links in Dir that point at foo: the manifest's
// entries and any other foo-tool-* symlink resolving to foo. Files and
// links pointing elsewhere are never touched.
func (in *Installer) Uninstall() ([]LinkResult, error) {
	dir, err := in.dir()
	if err != nil {
		return nil, err
	}
	man := in.readManifest()
	recorded := man.byLink()
	candidates := map[string]string{} // link → name
	for link, e := range recorded {
		if filepath.Dir(link) == dir {
			candidates[link] = e.Name
		}
	}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if name, ok := strings.CutPrefix(e.Name(), LinkPrefix); ok && e.Type()&fs.ModeSymlink != 0 {
				candidates[filepath.Join(dir, e.Name())] = name
			}
		}
	}
	links := make([]string, 0, len(candidates))
	for l := range candidates {
		links = append(links, l)
	}
	sort.Strings(links)

	var out []LinkResult
	for _, link := range links {
		e, inManifest := recorded[link]
		res := LinkResult{Name: candidates[link], Link: link}
		switch {
		case in.isFooLink(link, e.Target):
			if err := os.Remove(link); err != nil {
				return out, err
			}
			res.Action = ActionRemoved
		case inManifest:
			res.Action, res.Reason = ActionKept, "no longer a link to foo"
		default:
			continue
		}
		delete(recorded, link)
		out = append(out, res)
	}
	return out, in.writeManifest(recorded)
}

// isFooLink reports whether link is a symlink to foo: it names Target
// or the target recorded when foo created it, or it resolves to the
// same file as Target.
func (in *Installer) isFooLink(link, recordedTarget string) bool {
	fi, err := os.Lstat(link)
	if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		return false
	}
	dest, err := os.Readlink(link)
	if err != nil {
		return false
	}
	if dest == in.Target || (recordedTarget != "" && dest == recordedTarget) {
		return true
	}
	a, errA := os.Stat(link)
	b, errB := os.Stat(in.Target)
	return errA == nil && errB == nil && os.SameFile(a, b)
}

// symlink points link at Target, replacing any existing link atomically.
func (in *Installer) symlink(link string) error {
	tmp := filepath.Join(filepath.Dir(link), fmt.Sprintf(".%s.tmp-%d", filepath.Base(link), os.Getpid()))
	_ = os.Remove(tmp)
	if err := os.Symlink(in.Target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (in *Installer) readManifest() Manifest {
	var m Manifest
	if data, err := os.ReadFile(in.Manifest); err == nil {
		_ = json.Unmarshal(data, &m)
	}
	return m
}

func (m Manifest) byLink() map[string]ManifestEntry {
	out := make(map[string]ManifestEntry, len(m.Links))
	for _, e := range m.Links {
		out[e.Link] = e
	}
	return out
}

func (in *Installer) writeManifest(entries map[string]ManifestEntry) error {
	if in.Manifest == "" {
		return nil
	}
	m := Manifest{Links: []ManifestEntry{}}
	for _, e := range entries {
		m.Links = append(m.Links, e)
	}
	sort.Slice(m.Links, func(i, j int) bool { return m.Links[i].Link < m.Links[j].Link })
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(in.Manifest), 0o755); err != nil {
		return err
	}
	tmp := in.Manifest + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, in.Manifest)
}
