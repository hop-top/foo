package shim

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Output caps (§6).
const (
	DefaultMaxStdout = 64 << 10
	DefaultMaxStderr = 8 << 10
	// binarySniff is how much of stdout is checked for NUL bytes.
	binarySniff = 8 << 10
)

// childPath is the only PATH a command sees.
const childPath = "/usr/bin:/bin"

// childEnv keeps the variables a command needs to behave the same for
// every caller and drops everything else (GREP_OPTIONS, POSIXLY_CORRECT,
// CLICOLOR, LS_COLORS, …).
func childEnv(environ []string) []string {
	out := []string{"PATH=" + childPath}
	for _, kv := range environ {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch {
		case k == "HOME", k == "LANG", k == "TZ", k == "TMPDIR", strings.HasPrefix(k, "LC_"):
			out = append(out, kv)
		}
	}
	return out
}

// capWriter keeps the first max bytes and counts every byte. It never
// fails, so the child's output keeps draining past the cap and the
// child never blocks on a full pipe.
type capWriter struct {
	max   int
	buf   *bytes.Buffer
	total int64
}

func (w *capWriter) Write(p []byte) (int, error) {
	w.total += int64(len(p))
	if room := w.max - w.buf.Len(); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		w.buf.Write(p[:room])
	}
	return len(p), nil
}

func (w *capWriter) truncated() bool { return w.total > int64(w.buf.Len()) }

// pathFilter turns NUL-separated path output into newline-separated
// lines, dropping every entry allow rejects. Entries holding a newline
// are quoted so each line stays one entry.
type pathFilter struct {
	allow    func(string) bool
	sink     *capWriter
	pending  []byte
	skipping bool // current entry overflowed maxEntry; drop it
	filtered int
}

// maxEntry bounds one buffered entry; longer ones are dropped.
const maxEntry = 16 << 10

func (f *pathFilter) Write(p []byte) (int, error) {
	f.pending = append(f.pending, p...)
	for {
		i := bytes.IndexByte(f.pending, 0)
		if i < 0 {
			break
		}
		if f.skipping {
			f.filtered++
			f.skipping = false
		} else {
			f.emit(string(f.pending[:i]))
		}
		f.pending = f.pending[i+1:]
	}
	if len(f.pending) > maxEntry {
		f.pending = f.pending[:0]
		f.skipping = true
	}
	return len(p), nil
}

func (f *pathFilter) flush() {
	switch {
	case f.skipping:
		f.filtered++
	case len(f.pending) > 0:
		f.emit(string(f.pending))
	}
	f.pending, f.skipping = nil, false
}

func (f *pathFilter) emit(entry string) {
	if !strings.HasPrefix(entry, "/") || f.allow == nil || !f.allow(entry) {
		f.filtered++
		return
	}
	if strings.ContainsAny(entry, "\n\r") {
		entry = strconv.Quote(entry)
	}
	_, _ = f.sink.Write([]byte(entry + "\n"))
}

// runOut accumulates the output of one call across its chunks.
type runOut struct {
	stdout, stderr *capWriter
	filter         *pathFilter
	exitCodes      []int
}

func newRunOut(maxOut, maxErr int, allow func(string) bool, paths0 bool) *runOut {
	o := &runOut{
		stdout: &capWriter{max: maxOut, buf: &bytes.Buffer{}},
		stderr: &capWriter{max: maxErr, buf: &bytes.Buffer{}},
	}
	if paths0 {
		o.filter = &pathFilter{allow: allow, sink: o.stdout}
	}
	return o
}

// errTimeout reports a call that ran out of time.
var errTimeout = errors.New("timeout")

// run executes one argv: no shell, pinned binary, fixed environment,
// stdin from /dev/null, own process group killed as a whole when ctx
// ends. A nonzero exit is recorded, not returned.
func (o *runOut) run(ctx context.Context, argv []string, dir string, environ []string) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Args = argv
	cmd.Dir = dir
	cmd.Env = childEnv(environ)
	cmd.Stdin = nil
	if o.filter != nil {
		cmd.Stdout = o.filter
	} else {
		cmd.Stdout = o.stdout
	}
	cmd.Stderr = o.stderr
	setProcessGroup(cmd)
	cmd.WaitDelay = 2 * time.Second

	err := cmd.Run()
	if o.filter != nil {
		o.filter.flush()
	}
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return errTimeout
		}
		return ctx.Err()
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		o.exitCodes = append(o.exitCodes, 0)
	case errors.As(err, &exitErr):
		o.exitCodes = append(o.exitCodes, exitErr.ExitCode())
	default:
		return err
	}
	return nil
}

// exitCode merges chunk exit codes: the first code outside ok wins;
// otherwise the lowest (grep: any chunk matched → 0).
func mergeExit(codes, ok []int) int {
	if len(codes) == 0 {
		return ok[len(ok)-1]
	}
	low := codes[0]
	for _, c := range codes {
		if !containsInt(ok, c) {
			return c
		}
		if c < low {
			low = c
		}
	}
	return low
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// isBinary reports output the model should not receive as text: a NUL
// in the first 8 KiB, or invalid UTF-8 (a rune cut by the cap excepted).
func isBinary(b []byte, truncated bool) bool {
	sniff := b
	if len(sniff) > binarySniff {
		sniff = sniff[:binarySniff]
	}
	if bytes.IndexByte(sniff, 0) >= 0 {
		return true
	}
	if truncated {
		b = trimPartialRune(b)
	}
	return !utf8.Valid(b)
}

func trimPartialRune(b []byte) []byte {
	for i := 1; i <= utf8.UTFMax && i <= len(b); i++ {
		if utf8.RuneStart(b[len(b)-i]) {
			if !utf8.FullRune(b[len(b)-i:]) {
				return b[:len(b)-i]
			}
			break
		}
	}
	return b
}
