package main

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hop.top/kit/go/storage/kv"
	xrr "hop.top/xrr"
	xexec "hop.top/xrr/adapters/exec"
)

// cassetteDir holds committed xrr exec cassettes capturing yt-dlp output,
// so the suite replays recorded subprocess responses instead of shelling
// out to a live yt-dlp + YouTube. Re-record with -update.
const cassetteDir = "testdata/cassettes"

// xrrRunner wraps ytRunner's seam around an xrr session: each call is
// fingerprinted by argv and replayed from (or recorded to) the cassette.
// The exec adapter's Cwd is left empty to keep the canonical argv-only
// fingerprint (cross-runtime replay safe).
func xrrRunner(mode xrr.Mode) func(context.Context, []string) ([]byte, error) {
	sess := xrr.NewSession(mode, xrr.NewFileCassette(cassetteDir))
	adapter := xexec.NewAdapter()
	return func(ctx context.Context, args []string) ([]byte, error) {
		req := &xexec.Request{Argv: append([]string{"yt-dlp"}, args...)}
		resp, err := sess.Record(ctx, adapter, req, func() (xrr.Response, error) {
			// do() only runs in record mode; replay never calls it.
			out, eerr := execYTDLP(ctx, args)
			return &xexec.Response{Stdout: string(out)}, eerr
		})
		if err != nil {
			return nil, err
		}
		switch r := resp.(type) {
		case *xexec.Response:
			return []byte(r.Stdout), nil
		case *xrr.RawResponse:
			// Replay returns RawResponse with the YAML payload decoded
			// into a map; pull stdout back out.
			if s, ok := r.Payload["stdout"].(string); ok {
				return []byte(s), nil
			}
			return nil, nil
		default:
			return nil, nil
		}
	}
}

// withReplay swaps ytRunner for the cassette-backed runner for the test's
// duration and restores it after.
func withReplay(t *testing.T) {
	t.Helper()
	prev := ytRunner
	ytRunner = xrrRunner(xrr.ModeReplay)
	t.Cleanup(func() { ytRunner = prev })
}

func newCacheStore(t *testing.T) kv.TTLStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "yt.db")
	s, err := kv.Open(kv.Config{Backend: "sqlite", Path: path})
	if err != nil {
		t.Fatalf("kv.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ttl, ok := s.(kv.TTLStore)
	if !ok {
		t.Fatal("sqlite store is not a TTLStore")
	}
	return ttl
}

// TestFetchMetadata_FromCassette proves fetchMetadata parses real yt-dlp
// --dump-json output replayed from the cassette — no live yt-dlp.
func TestFetchMetadata_FromCassette(t *testing.T) {
	withReplay(t)
	ytCache = nil // exercise the runner, not the cache

	md, err := fetchMetadata(context.Background(), "https://youtu.be/dQw4w9WgXcQ")
	if err != nil {
		t.Fatalf("fetchMetadata: %v", err)
	}
	if md.Title == "" {
		t.Error("expected a non-empty title from the cassette")
	}
	if md.Channel == "" {
		t.Error("expected a non-empty channel from the cassette")
	}
}

// TestRunYTDLP_CacheHitAvoidsRunner proves a second call for the same argv
// is served from the kv cache without invoking the runner at all.
func TestRunYTDLP_CacheHitAvoidsRunner(t *testing.T) {
	var calls atomic.Int64
	prev := ytRunner
	ytRunner = func(_ context.Context, _ []string) ([]byte, error) {
		calls.Add(1)
		return []byte(`{"title":"X","channel":"Y"}`), nil
	}
	t.Cleanup(func() { ytRunner = prev })

	ytCache = newCacheStore(t)
	t.Cleanup(func() { ytCache = nil })

	args := []string{"--dump-json", "https://youtu.be/abc"}
	ctx := context.Background()

	if _, err := runYTDLP(ctx, args); err != nil {
		t.Fatalf("run1: %v", err)
	}
	if _, err := runYTDLP(ctx, args); err != nil {
		t.Fatalf("run2: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("runner calls = %d, want 1 (second call must hit cache)", got)
	}
}

// TestRunYTDLP_DistinctArgvDistinctKey proves different argv miss
// independently (per-invocation cache granularity).
func TestRunYTDLP_DistinctArgvDistinctKey(t *testing.T) {
	var calls atomic.Int64
	prev := ytRunner
	ytRunner = func(_ context.Context, _ []string) ([]byte, error) {
		calls.Add(1)
		return []byte("{}"), nil
	}
	t.Cleanup(func() { ytRunner = prev })

	ytCache = newCacheStore(t)
	t.Cleanup(func() { ytCache = nil })

	ctx := context.Background()
	_, _ = runYTDLP(ctx, []string{"--dump-json", "https://youtu.be/a"})
	_, _ = runYTDLP(ctx, []string{"--dump-json", "https://youtu.be/b"})
	if got := calls.Load(); got != 2 {
		t.Errorf("runner calls = %d, want 2 (distinct argv must not collide)", got)
	}
}

// TestResolveCachePath covers the path precedence for foo-youtube, whose
// resolver takes an explicit dbName: plugin-specific env -> shared
// FOO_CACHE (dir + dbName) -> XDG default.
func TestResolveCachePath(t *testing.T) {
	const db = "ytdlp-cache.db"

	t.Run("specific env wins (exact path)", func(t *testing.T) {
		t.Setenv("FOO_YOUTUBE_CACHE", "/tmp/yt.db")
		t.Setenv("FOO_CACHE", "/ignored")
		got, err := resolveCachePath("foo-youtube", db, "FOO_YOUTUBE_CACHE")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got != "/tmp/yt.db" {
			t.Errorf("got %q, want /tmp/yt.db", got)
		}
	})

	t.Run("shared FOO_CACHE is a dir, dbName filed under it", func(t *testing.T) {
		t.Setenv("FOO_YOUTUBE_CACHE", "")
		t.Setenv("FOO_CACHE", "/shared/cache")
		got, err := resolveCachePath("foo-youtube", db, "FOO_YOUTUBE_CACHE")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if want := filepath.Join("/shared/cache", db); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("no env falls back to XDG default", func(t *testing.T) {
		t.Setenv("FOO_YOUTUBE_CACHE", "")
		t.Setenv("FOO_CACHE", "")
		got, err := resolveCachePath("foo-youtube", db, "FOO_YOUTUBE_CACHE")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if !strings.HasSuffix(got, filepath.Join("foo-youtube", db)) {
			t.Errorf("XDG default should end in foo-youtube/%s; got %q", db, got)
		}
	})
}

// TestResolveCacheTTL covers the TTL precedence:
// plugin-specific env -> shared FOO_CACHE_TTL -> 24h default.
func TestResolveCacheTTL(t *testing.T) {
	t.Run("specific env wins", func(t *testing.T) {
		t.Setenv("FOO_YOUTUBE_CACHE_TTL", "1h")
		t.Setenv("FOO_CACHE_TTL", "9h")
		if got := resolveCacheTTL("FOO_YOUTUBE_CACHE_TTL"); got != time.Hour {
			t.Errorf("got %v, want 1h", got)
		}
	})

	t.Run("falls back to shared FOO_CACHE_TTL", func(t *testing.T) {
		t.Setenv("FOO_YOUTUBE_CACHE_TTL", "")
		t.Setenv("FOO_CACHE_TTL", "30m")
		if got := resolveCacheTTL("FOO_YOUTUBE_CACHE_TTL"); got != 30*time.Minute {
			t.Errorf("got %v, want 30m", got)
		}
	})

	t.Run("unparseable specific falls through to shared", func(t *testing.T) {
		t.Setenv("FOO_YOUTUBE_CACHE_TTL", "nope")
		t.Setenv("FOO_CACHE_TTL", "2h")
		if got := resolveCacheTTL("FOO_YOUTUBE_CACHE_TTL"); got != 2*time.Hour {
			t.Errorf("got %v, want 2h", got)
		}
	})

	t.Run("no env defaults to 24h", func(t *testing.T) {
		t.Setenv("FOO_YOUTUBE_CACHE_TTL", "")
		t.Setenv("FOO_CACHE_TTL", "")
		if got := resolveCacheTTL("FOO_YOUTUBE_CACHE_TTL"); got != 24*time.Hour {
			t.Errorf("got %v, want 24h", got)
		}
	})
}
