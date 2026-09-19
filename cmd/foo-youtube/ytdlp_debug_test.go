package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// fakeYTDLP builds a tiny helper binary standing in for yt-dlp: it
// writes a known marker to stderr and a payload to stdout, so a test
// can tell whether the child's stderr reached the parent's.
func fakeYTDLP(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := dir + "/fake.go"
	const prog = `package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "YTDLP-RAW-NOISE [download] 42% of 10MiB")
	fmt.Fprint(os.Stdout, ` + "`" + `{"title":"T"}` + "`" + `)
}
`
	if err := os.WriteFile(src, []byte(prog), 0o644); err != nil {
		t.Fatalf("write fake: %v", err)
	}
	bin := dir + "/yt-dlp"
	build := exec.Command("go", "build", "-buildvcs=false", "-o", bin, src)
	build.Env = append(os.Environ(), "GOFLAGS=-buildvcs=false")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake yt-dlp: %v\n%s", err, out)
	}
	return dir
}

// runExecYTDLP runs execYTDLP with the fake yt-dlp first on PATH and a
// captured stderr, returning (stdout, capturedStderr).
func runExecYTDLP(t *testing.T, debug bool) (string, string) {
	t.Helper()
	dir := fakeYTDLP(t)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	prevDebug := ytDebug
	ytDebug = debug
	t.Cleanup(func() { ytDebug = prevDebug })

	var errBuf bytes.Buffer
	prevErr := ytStderr
	ytStderr = &errBuf
	t.Cleanup(func() { ytStderr = prevErr })

	out, err := execYTDLP(context.Background(), []string{"--dump-json"})
	if err != nil {
		t.Fatalf("execYTDLP: %v (stderr=%q)", err, errBuf.String())
	}
	return string(out), errBuf.String()
}

// TestExecYTDLP_DebugGate is the acceptance criterion for the raw
// passthrough: silent by default, verbatim under --debug.
func TestExecYTDLP_DebugGate(t *testing.T) {
	tests := []struct {
		name      string
		debug     bool
		wantNoise bool
	}{
		{"default run suppresses raw yt-dlp stderr", false, false},
		{"debug run passes raw yt-dlp stderr through", true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr := runExecYTDLP(t, tc.debug)

			if !strings.Contains(stdout, `"title":"T"`) {
				t.Errorf("stdout = %q, want the yt-dlp payload", stdout)
			}
			gotNoise := strings.Contains(stderr, "YTDLP-RAW-NOISE")
			if gotNoise != tc.wantNoise {
				t.Errorf("raw noise on stderr = %v, want %v (stderr=%q)",
					gotNoise, tc.wantNoise, stderr)
			}
		})
	}
}

// TestExecYTDLP_FailureSurfacesStderr proves suppressing the stream by
// default does not make a failing yt-dlp silent: the captured stderr
// must be folded into the returned error.
func TestExecYTDLP_FailureSurfacesStderr(t *testing.T) {
	dir := t.TempDir()
	src := dir + "/fake.go"
	const prog = `package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "ERROR: video unavailable")
	os.Exit(1)
}
`
	if err := os.WriteFile(src, []byte(prog), 0o644); err != nil {
		t.Fatalf("write fake: %v", err)
	}
	bin := dir + "/yt-dlp"
	if out, err := exec.Command("go", "build", "-buildvcs=false", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("build fake yt-dlp: %v\n%s", err, out)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	prevDebug := ytDebug
	ytDebug = false // the DEFAULT path: stderr is captured, not streamed
	t.Cleanup(func() { ytDebug = prevDebug })

	var errBuf bytes.Buffer
	prevErr := ytStderr
	ytStderr = &errBuf
	t.Cleanup(func() { ytStderr = prevErr })

	_, err := execYTDLP(context.Background(), []string{"--dump-json"})
	if err == nil {
		t.Fatal("expected an error from a failing yt-dlp")
	}
	if !strings.Contains(err.Error(), "video unavailable") {
		t.Errorf("error = %q, want the captured yt-dlp stderr folded in", err)
	}
}
