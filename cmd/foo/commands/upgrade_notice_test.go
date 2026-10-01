package commands

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hop.top/kit/go/core/upgrade"
)

// releaseFeed stands in for the release source: a loopback server
// answering kit's custom-feed shape with a version newer than any test
// build, after delay. It counts the checks that reach it.
func releaseFeed(t *testing.T, delay time.Duration) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"version":"99.0.0","url":"","notes":""}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// openNoticeGates puts a run where the notice is allowed — stderr a
// terminal, no opt-out, not CI — and points the check at feed instead
// of GitHub.
func openNoticeGates(t *testing.T, feed string) {
	t.Helper()
	t.Setenv(upgradeNoticeOptOutEnv, "")
	t.Setenv("CI", "")
	prevTTY, prevSrc := stderrIsTerminal, upgradeSource
	stderrIsTerminal = func(io.Writer) bool { return true }
	upgradeSource = upgrade.WithReleaseURL(feed)
	t.Cleanup(func() { stderrIsTerminal, upgradeSource = prevTTY, prevSrc })
}

// With every gate open the notice checks the feed once and prints.
// This is the control for the skip cases below: the same run, one gate
// closed, must not reach the feed.
func TestUpgradeNotice_ShownWhenAllowed(t *testing.T) {
	env := newToolTestEnv(t)
	feed, hits := releaseFeed(t, 0)
	openNoticeGates(t, feed.URL)

	_, stderr, _, err := runFooArgs(t, env, "tool", "list")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if hits.Load() != 1 {
		t.Errorf("feed hit %d times; want 1", hits.Load())
	}
	if !strings.Contains(stderr, "update available") {
		t.Errorf("stderr %q carries no update notice", stderr)
	}
}

// Each of these runs must not check for an update at all: not dial,
// not print. The notice is for a person at a terminal who has not said
// otherwise.
func TestUpgradeNotice_SkippedWithoutDialing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		setup func(t *testing.T)
	}{
		{name: "--offline", args: []string{"--offline", "tool", "list"}},
		{name: "--offline after the command", args: []string{"tool", "list", "--offline"}},
		{name: "--quiet", args: []string{"--quiet", "tool", "list"}},
		{name: "opt-out env", args: []string{"tool", "list"}, setup: func(t *testing.T) {
			t.Setenv(upgradeNoticeOptOutEnv, "1")
		}},
		{name: "CI", args: []string{"tool", "list"}, setup: func(t *testing.T) {
			t.Setenv("CI", "true")
		}},
		{name: "stderr not a terminal", args: []string{"tool", "list"}, setup: func(t *testing.T) {
			stderrIsTerminal = func(io.Writer) bool { return false }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newToolTestEnv(t)
			feed, hits := releaseFeed(t, 0)
			openNoticeGates(t, feed.URL)
			if tc.setup != nil {
				tc.setup(t)
			}

			_, stderr, _, err := runFooArgs(t, env, tc.args...)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if n := hits.Load(); n != 0 {
				t.Errorf("feed hit %d times; want no check", n)
			}
			if strings.Contains(stderr, "update available") {
				t.Errorf("stderr %q carries an update notice", stderr)
			}
		})
	}
}

// A release source that never answers must not hold the command
// hostage: the notice gives up after upgradeNoticeTimeout, not kit's
// 10s default, and the command still succeeds.
func TestUpgradeNotice_BoundedWait(t *testing.T) {
	env := newToolTestEnv(t)
	feed, hits := releaseFeed(t, time.Minute)
	openNoticeGates(t, feed.URL)
	prev := upgradeNoticeTimeout
	upgradeNoticeTimeout = 100 * time.Millisecond
	t.Cleanup(func() { upgradeNoticeTimeout = prev })

	start := time.Now()
	_, _, _, err := runFooArgs(t, env, "tool", "list")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("feed hit %d times; want 1", hits.Load())
	}
	if elapsed > 5*time.Second {
		t.Errorf("command took %s behind an unanswered update check; want it bounded by %s", elapsed, upgradeNoticeTimeout)
	}
}

// The production terminal test: a buffer or a pipe is not a terminal.
func TestStderrIsTerminal_NonTerminals(t *testing.T) {
	if stderrIsTerminal(&bytes.Buffer{}) {
		t.Error("a bytes.Buffer reported as a terminal")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if stderrIsTerminal(w) {
		t.Error("a pipe reported as a terminal")
	}
}
