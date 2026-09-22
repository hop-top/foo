package main

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
)

// --- flag default regression -------------------------------------------------

// TestFlagDefaults_MetadataAndTranscriptOn is the regression test for the
// double-bind defect: binding --no-X to the same variable as --X wrote
// the negation's `false` default through that pointer at registration,
// so a bare invocation silently did no work while --help still
// advertised `true`.
//
// Asserting the parsed flag values is not enough — the old code left
// `--metadata` reporting true from its own registration. The assertion
// has to be on what run() actually receives.
func TestFlagDefaults_MetadataAndTranscriptOn(t *testing.T) {
	got := captureRunOpts(t, []string{"https://youtu.be/VID123"})
	if !got.metadata {
		t.Error("metadata defaulted to false; a bare invocation must include metadata")
	}
	if !got.transcript {
		t.Error("transcript defaulted to false; a bare invocation must extract the transcript")
	}
}

// TestFlagNegations proves each --no-* switch still turns its section
// off, and turns off only its own.
func TestFlagNegations(t *testing.T) {
	tests := []struct {
		name           string
		args           []string
		wantMetadata   bool
		wantTranscript bool
	}{
		{"no-metadata", []string{"--no-metadata"}, false, true},
		{"no-transcript", []string{"--no-transcript"}, true, false},
		{"both", []string{"--no-metadata", "--no-transcript"}, false, false},
		{"explicit true", []string{"--metadata", "--transcript"}, true, true},
		{"explicit false", []string{"--metadata=false"}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := captureRunOpts(t, append(tt.args, "https://youtu.be/VID123"))
			if got.metadata != tt.wantMetadata {
				t.Errorf("metadata = %v, want %v", got.metadata, tt.wantMetadata)
			}
			if got.transcript != tt.wantTranscript {
				t.Errorf("transcript = %v, want %v", got.transcript, tt.wantTranscript)
			}
		})
	}
}

// TestFlagPassthrough covers the remaining switches so the negation fix
// cannot quietly break them.
func TestFlagPassthrough(t *testing.T) {
	got := captureRunOpts(t, []string{"--timestamps", "--comments", "--no-cache", "https://youtu.be/VID123"})
	if !got.timestamps {
		t.Error("--timestamps did not reach runOpts")
	}
	if !got.comments {
		t.Error("--comments did not reach runOpts")
	}
	if !got.noCache {
		t.Error("--no-cache did not reach runOpts")
	}
}

// captureRunOpts parses args through the real root command and returns
// the runOpts that run() receives.
//
// It intercepts at the runFunc seam rather than replacing RunE, so the
// flag registration AND the production RunE reconciliation are both
// under test. Re-deriving the opts in the test instead would only prove
// the test's own copy of the logic — the double-bind defect lives in
// registration, which a reimplementation cannot observe.
func captureRunOpts(t *testing.T, args []string) runOpts {
	t.Helper()

	var got runOpts
	prev := runFunc
	runFunc = func(_ *cobra.Command, _ []string, opts runOpts) error {
		got = opts
		return nil
	}
	t.Cleanup(func() { runFunc = prev })

	root := newRoot()
	root.Cmd.SetArgs(args)
	root.Cmd.SetOut(&bytes.Buffer{})
	root.Cmd.SetErr(&bytes.Buffer{})
	if err := root.Cmd.Execute(); err != nil {
		t.Fatalf("Execute(%v): %v", args, err)
	}
	return got
}
