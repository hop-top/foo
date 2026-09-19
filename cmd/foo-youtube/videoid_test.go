package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// TestNormalizeVideoArg pins the accepted argument shapes: a bare
// 11-character video ID normalizes to the canonical watch URL, every
// previously-accepted full URL passes through verbatim, and everything
// else — including 10- and 12-character near-misses — is rejected.
func TestNormalizeVideoArg(t *testing.T) {
	tests := []struct {
		name  string
		arg   string
		want  string
		wantO bool
	}{
		{
			name:  "bare id normalizes to canonical watch url",
			arg:   "dQw4w9WgXcQ",
			want:  "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
			wantO: true,
		},
		{
			name:  "bare id with underscore and dash",
			arg:   "a_b-c1D2e3F",
			want:  "https://www.youtube.com/watch?v=a_b-c1D2e3F",
			wantO: true,
		},
		{
			name:  "watch url passes through",
			arg:   "https://youtube.com/watch?v=dQw4w9WgXcQ",
			want:  "https://youtube.com/watch?v=dQw4w9WgXcQ",
			wantO: true,
		},
		{
			name:  "www watch url passes through",
			arg:   "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
			want:  "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
			wantO: true,
		},
		{
			name:  "youtu.be url passes through",
			arg:   "https://youtu.be/dQw4w9WgXcQ",
			want:  "https://youtu.be/dQw4w9WgXcQ",
			wantO: true,
		},
		{
			name:  "shorts url passes through",
			arg:   "https://www.youtube.com/shorts/dQw4w9WgXcQ",
			want:  "https://www.youtube.com/shorts/dQw4w9WgXcQ",
			wantO: true,
		},
		{
			name: "invalid garbage rejected",
			arg:  "not a video at all",
		},
		{
			name: "non-youtube url rejected",
			arg:  "https://vimeo.com/123456789",
		},
		{
			name: "ten char near-miss rejected",
			arg:  "dQw4w9WgXc",
		},
		{
			name: "twelve char near-miss rejected",
			arg:  "dQw4w9WgXcQZ",
		},
		{
			name: "eleven chars with illegal rune rejected",
			arg:  "dQw4w9WgXc!",
		},
		{
			name: "empty rejected",
			arg:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := normalizeVideoArg(tt.arg)
			if ok != tt.wantO {
				t.Fatalf("normalizeVideoArg(%q) ok = %v, want %v", tt.arg, ok, tt.wantO)
			}
			if ok && got != tt.want {
				t.Errorf("normalizeVideoArg(%q) = %q, want %q", tt.arg, got, tt.want)
			}
		})
	}
}

// TestBareIDAndURLShareCacheEntry proves the acceptance criterion that
// matters most, and proves it through run() rather than through the
// normalizer alone: run() normalizes before any fetch, so the bare-ID
// and watch-URL forms of the same video produce identical yt-dlp argv
// and therefore the same cache key — one runner call for both.
func TestBareIDAndURLShareCacheEntry(t *testing.T) {
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		t.Skip("yt-dlp not on PATH; run() gates on it before fetching")
	}

	var calls atomic.Int64
	var seen []string
	prev := ytRunner
	ytRunner = func(_ context.Context, args []string) ([]byte, error) {
		calls.Add(1)
		seen = append(seen, args[len(args)-1])
		return []byte(`{"title":"X","channel":"Y"}`), nil
	}
	t.Cleanup(func() { ytRunner = prev })

	// Point the cache openYTCache() creates at a temp db so both runs
	// share one real store and nothing leaks into the user's XDG cache.
	t.Setenv("FOO_YOUTUBE_CACHE", filepath.Join(t.TempDir(), "yt.db"))
	t.Cleanup(func() { ytCache = nil })

	for _, arg := range []string{
		"dQw4w9WgXcQ",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
	} {
		cmd := newRoot().Cmd
		cmd.SetOut(&strings.Builder{})
		cmd.SetErr(&strings.Builder{})
		cmd.SetContext(context.Background())
		if err := run(cmd, []string{arg}, runOpts{metadata: true}); err != nil {
			t.Fatalf("run(%q): %v", arg, err)
		}
	}

	if got := calls.Load(); got != 1 {
		t.Errorf("runner calls = %d, want 1 (bare id and watch url must share a cache entry); argv urls seen: %v", got, seen)
	}
}

// TestRunRejectsInvalidArg keeps the usage-error path pinned: anything
// that is neither a supported URL nor a canonical bare ID is still
// refused before yt-dlp is consulted.
func TestRunRejectsInvalidArg(t *testing.T) {
	ytCache = nil // rejection must happen before any cache/runner work
	t.Cleanup(func() { ytCache = nil })

	for _, arg := range []string{"not a video", "dQw4w9WgXc", "dQw4w9WgXcQZ"} {
		cmd := newRoot().Cmd
		cmd.SetOut(&strings.Builder{})
		cmd.SetErr(&strings.Builder{})
		cmd.SetContext(context.Background())
		err := run(cmd, []string{arg}, runOpts{metadata: true})
		if err == nil {
			t.Fatalf("run(%q) = nil, want usage error", arg)
		}
		if !strings.Contains(err.Error(), "invalid YouTube URL") {
			t.Errorf("run(%q) error = %q, want an invalid-URL usage error", arg, err)
		}
	}
}
