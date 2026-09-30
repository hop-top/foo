//go:build xrr

// This file is compiled only with -tags xrr: the e2e suite builds foo
// that way so the binary can replay model calls from xrr cassettes. A
// release build has no such code path and never links xrr.
//
// With XRR_MODE unset the seam is inert even in a tagged build.

package main

import (
	"fmt"
	"net/http"
	"os"

	"hop.top/foo/internal/llmxrr"
	xrr "hop.top/xrr"
)

// Env the e2e harness sets alongside XRR_MODE and XRR_CASSETTE_DIR.
const (
	// envRoot is a scratch root whose path the cassettes hold as
	// {{root}}, so a recording replays from any temp dir.
	envRoot = "FOO_XRR_ROOT"
	// envElide drops tool result content from the fingerprint (see
	// llmxrr.Transport.ElideToolResults).
	envElide = "FOO_XRR_ELIDE_TOOL_RESULTS"
	// envJournal is a file each model exchange is appended to, for the
	// harness to read what foo sent and whether it went live.
	envJournal = "FOO_XRR_JOURNAL"
)

// init runs before main's cli.New installs kit's network guard over
// http.DefaultTransport, so the guard wraps this seam: --offline still
// refuses a remote provider exactly as it would without cassettes.
func init() {
	sess, err := xrr.SessionFromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "foo:", err)
		os.Exit(2)
	}
	if sess == nil {
		return
	}
	tr := &llmxrr.Transport{
		Session:          sess,
		Next:             http.DefaultTransport,
		ElideToolResults: os.Getenv(envElide) != "",
	}
	if sess.Mode() == xrr.ModeRecord {
		// Record only what has no recording yet: a suite that repeats
		// a call (same prompt, same tool) keeps one recording of it,
		// and adding a case records just that case. Delete a
		// recording to redo it.
		tr.Replay = xrr.NewSession(xrr.ModeReplay, xrr.NewFileCassette(os.Getenv(xrr.EnvCassetteDir)))
	}
	if root := os.Getenv(envRoot); root != "" {
		tr.Subst = []llmxrr.Subst{{Value: root, Placeholder: "{{root}}"}}
	}
	if path := os.Getenv(envJournal); path != "" {
		tr.OnExchange = llmxrr.Journal(path)
	}
	http.DefaultTransport = tr
}
