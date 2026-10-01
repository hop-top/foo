package shim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// gnuRe matches the "(GNU coreutils)"/"(GNU grep)" tag GNU tools print
// in --version. A bare "GNU" is not enough: macOS grep prints
// "grep (BSD grep, GNU compatible)".
var gnuRe = regexp.MustCompile(`\(GNU [A-Za-z]+\)`)

// ClassifyVersion maps a binary's --version output to a flavor.
func ClassifyVersion(out string) string {
	switch {
	case strings.Contains(out, "BusyBox"):
		return FlavorBusyBox
	case gnuRe.MatchString(out):
		return FlavorGNU
	default:
		return FlavorBSD
	}
}

// Flavors detects and caches binary flavors (§8). Detection runs
// `<bin> --version` once per (path, size, mtime); the cache persists in
// File when set.
type Flavors struct {
	// File is the JSON cache path; empty keeps the cache in memory only.
	File string

	mu     sync.Mutex
	loaded bool
	cache  map[string]string
}

// NewFlavors returns a detector backed by the cache file at path.
func NewFlavors(path string) *Flavors { return &Flavors{File: path} }

// Detect returns the flavor of bin.
func (f *Flavors) Detect(bin string) string {
	fi, err := os.Stat(bin)
	if err != nil {
		return FlavorBSD
	}
	key := fmt.Sprintf("%s|%d|%d", bin, fi.Size(), fi.ModTime().UnixNano())

	f.mu.Lock()
	defer f.mu.Unlock()
	f.load()
	if fl, ok := f.cache[key]; ok {
		return fl
	}
	out, done := versionOutput(bin)
	fl := ClassifyVersion(out)
	// A busybox applet is a link to the multi-call binary. Not every
	// applet names BusyBox in --version: sed prints "This is not GNU
	// sed version 4.0".
	if real, err := filepath.EvalSymlinks(bin); err == nil && filepath.Base(real) == "busybox" {
		fl, done = FlavorBusyBox, true
	}
	// A probe that did not finish says nothing about the binary: its
	// fallback is not cached, so the next call probes again.
	if done {
		f.cache[key] = fl
		f.save()
	}
	return fl
}

func (f *Flavors) load() {
	if f.loaded {
		return
	}
	f.loaded = true
	f.cache = map[string]string{}
	if f.File == "" {
		return
	}
	if data, err := os.ReadFile(f.File); err == nil {
		_ = json.Unmarshal(data, &f.cache)
	}
}

func (f *Flavors) save() {
	if f.File == "" {
		return
	}
	data, err := json.Marshal(f.cache)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(f.File), 0o755); err != nil {
		return
	}
	tmp := f.File + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		_ = os.Rename(tmp, f.File)
	}
}

// versionTimeout bounds a --version probe.
var versionTimeout = 2 * time.Second

// versionOutput runs `bin --version` in an empty scratch directory with
// no stdin and a short timeout, so a binary that takes --version for a
// file name cannot touch anything that matters. done is false when the
// probe did not run to an exit of its own: it could not start, or it
// timed out. A nonzero exit is an answer (BSD tools reject --version).
func versionOutput(bin string) (out string, done bool) {
	dir, err := os.MkdirTemp("", "foo-flavor-")
	if err != nil {
		return "", false
	}
	defer func() { _ = os.RemoveAll(dir) }()

	ctx, cancel := context.WithTimeout(context.Background(), versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--version")
	cmd.Dir = dir
	cmd.Env = childEnv(os.Environ())
	var buf bytes.Buffer
	w := &capWriter{max: 4096, buf: &buf}
	cmd.Stdout, cmd.Stderr = w, w
	setProcessGroup(cmd)
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	var exit *exec.ExitError
	done = ctx.Err() == nil && (err == nil || errors.As(err, &exit))
	return buf.String(), done
}
