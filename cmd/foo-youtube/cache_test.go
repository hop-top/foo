package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hop.top/kit/go/storage/kv"
	_ "hop.top/kit/go/storage/kv/sqlite"
)

// TestResolveCachePath covers the path precedence:
// FOO_<EXT>_CACHE -> foo's host-level FOO_CACHE (dir + per-ext db) -> XDG.
// Mirrors foo-scrape's test of the same-shaped resolver.
func TestResolveCachePath(t *testing.T) {
	t.Run("specific env wins (exact path)", func(t *testing.T) {
		t.Setenv("FOO_YOUTUBE_CACHE", "/tmp/custom.db")
		t.Setenv("FOO_CACHE", "/should/be/ignored")
		got, err := resolveCachePath("foo-youtube", youtubeCacheDB, "FOO_YOUTUBE_CACHE")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got != "/tmp/custom.db" {
			t.Errorf("got %q, want /tmp/custom.db", got)
		}
	})

	t.Run("host FOO_CACHE is a dir, db filed under it", func(t *testing.T) {
		t.Setenv("FOO_YOUTUBE_CACHE", "")
		t.Setenv("FOO_CACHE", "/shared/cache")
		got, err := resolveCachePath("foo-youtube", youtubeCacheDB, "FOO_YOUTUBE_CACHE")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		want := filepath.Join("/shared/cache", youtubeCacheDB)
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("whitespace-only env is treated as unset", func(t *testing.T) {
		t.Setenv("FOO_YOUTUBE_CACHE", "   ")
		t.Setenv("FOO_CACHE", "/shared")
		got, err := resolveCachePath("foo-youtube", youtubeCacheDB, "FOO_YOUTUBE_CACHE")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got != filepath.Join("/shared", youtubeCacheDB) {
			t.Errorf("blank specific env should fall through to FOO_CACHE; got %q", got)
		}
	})

	t.Run("no env falls back to XDG default with per-tool db name", func(t *testing.T) {
		t.Setenv("FOO_YOUTUBE_CACHE", "")
		t.Setenv("FOO_CACHE", "")
		t.Setenv("XDG_CACHE_HOME", t.TempDir())
		got, err := resolveCachePath("foo-youtube", youtubeCacheDB, "FOO_YOUTUBE_CACHE")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if !strings.HasSuffix(got, filepath.Join("foo-youtube", youtubeCacheDB)) {
			t.Errorf("XDG default should end in foo-youtube/%s; got %q", youtubeCacheDB, got)
		}
	})
}

// TestResolveCacheTTL covers the TTL precedence:
// FOO_<EXT>_CACHE_TTL -> host FOO_CACHE_TTL -> per-extension default (24h).
func TestResolveCacheTTL(t *testing.T) {
	t.Run("specific env wins", func(t *testing.T) {
		t.Setenv("FOO_YOUTUBE_CACHE_TTL", "1h")
		t.Setenv("FOO_CACHE_TTL", "9h")
		if got := resolveCacheTTL("FOO_YOUTUBE_CACHE_TTL", youtubeCacheTTLDefault); got != time.Hour {
			t.Errorf("got %v, want 1h", got)
		}
	})

	t.Run("falls back to the host FOO_CACHE_TTL", func(t *testing.T) {
		t.Setenv("FOO_YOUTUBE_CACHE_TTL", "")
		t.Setenv("FOO_CACHE_TTL", "30m")
		if got := resolveCacheTTL("FOO_YOUTUBE_CACHE_TTL", youtubeCacheTTLDefault); got != 30*time.Minute {
			t.Errorf("got %v, want 30m", got)
		}
	})

	t.Run("unparseable extension value falls through to the host", func(t *testing.T) {
		t.Setenv("FOO_YOUTUBE_CACHE_TTL", "not-a-duration")
		t.Setenv("FOO_CACHE_TTL", "2h")
		if got := resolveCacheTTL("FOO_YOUTUBE_CACHE_TTL", youtubeCacheTTLDefault); got != 2*time.Hour {
			t.Errorf("got %v, want 2h (bad specific should fall through)", got)
		}
	})

	// foo-youtube keeps its 24h default; only foo-scrape is 10h.
	t.Run("no env defaults to 24h", func(t *testing.T) {
		t.Setenv("FOO_YOUTUBE_CACHE_TTL", "")
		t.Setenv("FOO_CACHE_TTL", "")
		if got := resolveCacheTTL("FOO_YOUTUBE_CACHE_TTL", youtubeCacheTTLDefault); got != 24*time.Hour {
			t.Errorf("got %v, want 24h", got)
		}
	})

	t.Run("zero parses to zero (caller reads it as off)", func(t *testing.T) {
		t.Setenv("FOO_YOUTUBE_CACHE_TTL", "0s")
		t.Setenv("FOO_CACHE_TTL", "9h")
		if got := resolveCacheTTL("FOO_YOUTUBE_CACHE_TTL", youtubeCacheTTLDefault); got != 0 {
			t.Errorf("got %v, want 0 (an explicit zero must not fall through)", got)
		}
	})
}

// TestOpenYTCache_ZeroTTLDisablesCache is the guard on the "no expiry"
// trap: at TTL=0 the store must never be opened, so runYTDLP execs every
// time instead of storing an entry that outlives the process forever.
func TestOpenYTCache_ZeroTTLDisablesCache(t *testing.T) {
	for _, ttl := range []string{"0", "0s", "0h"} {
		t.Run(ttl, func(t *testing.T) {
			isolateCacheEnv(t)
			path := filepath.Join(t.TempDir(), "zero.db")
			t.Setenv("FOO_YOUTUBE_CACHE", path)
			t.Setenv("FOO_YOUTUBE_CACHE_TTL", ttl)

			ytCache = nil
			t.Cleanup(func() { ytCache = nil })
			openYTCache()
			if ytCache != nil {
				t.Fatal("cache opened at TTL=0; entries would never expire")
			}

			var calls atomic.Int64
			prev := ytRunner
			ytRunner = func(_ context.Context, _ []string) ([]byte, error) {
				calls.Add(1)
				return []byte("{}"), nil
			}
			t.Cleanup(func() { ytRunner = prev })

			args := []string{"--dump-json", "https://youtu.be/zero"}
			ctx := context.Background()
			if _, err := runYTDLP(ctx, args); err != nil {
				t.Fatalf("run1: %v", err)
			}
			if _, err := runYTDLP(ctx, args); err != nil {
				t.Fatalf("run2: %v", err)
			}
			if got := calls.Load(); got != 2 {
				t.Errorf("runner calls = %d, want 2 (TTL=0 must not serve from cache)", got)
			}
			if n := countCacheKeys(t, path); n != 0 {
				t.Errorf("%d cache entries written at TTL=0, want 0", n)
			}
		})
	}
}

// TestOpenYTCache_OpensByDefault pins the positive case the zero-TTL
// guard must not swallow: with no TTL env, the store opens.
func TestOpenYTCache_OpensByDefault(t *testing.T) {
	isolateCacheEnv(t)
	t.Setenv("FOO_YOUTUBE_CACHE", filepath.Join(t.TempDir(), "default.db"))

	ytCache = nil
	t.Cleanup(func() { ytCache = nil })
	openYTCache()
	if ytCache == nil {
		t.Fatal("cache not opened with no TTL env; caching should be on")
	}
	if ytCacheTTL != youtubeCacheTTLDefault {
		t.Errorf("ytCacheTTL = %v, want %v", ytCacheTTL, youtubeCacheTTLDefault)
	}
}

// TestOpenYTCache_SharedFooCache proves the host dir is honored and
// backs a real store under the per-tool db name.
func TestOpenYTCache_SharedFooCache(t *testing.T) {
	isolateCacheEnv(t)
	dir := filepath.Join(t.TempDir(), "nested", "shared")
	t.Setenv("FOO_CACHE", dir)

	ytCache = nil
	t.Cleanup(func() { ytCache = nil })
	openYTCache()
	if ytCache == nil {
		t.Fatal("cache not opened under the host FOO_CACHE dir")
	}

	prev := ytRunner
	ytRunner = func(_ context.Context, _ []string) ([]byte, error) { return []byte("{}"), nil }
	t.Cleanup(func() { ytRunner = prev })
	if _, err := runYTDLP(context.Background(), []string{"--dump-json", "x"}); err != nil {
		t.Fatalf("run: %v", err)
	}

	if n := countCacheKeys(t, filepath.Join(dir, youtubeCacheDB)); n == 0 {
		t.Error("no entries under the host FOO_CACHE dir")
	}
}

// isolateCacheEnv clears every cache env this binary reads and points
// XDG at a temp dir, so a test never inherits the developer's real cache
// or another test's leftovers.
func isolateCacheEnv(t *testing.T) {
	t.Helper()
	t.Setenv("FOO_YOUTUBE_CACHE", "")
	t.Setenv("FOO_YOUTUBE_CACHE_TTL", "")
	t.Setenv("FOO_CACHE", "")
	t.Setenv("FOO_CACHE_TTL", "")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	// openYTCache mutates these package globals; restore them so a test
	// never leaks a TTL or an open store into the next one.
	prevTTL, prevCache := ytCacheTTL, ytCache
	t.Cleanup(func() { ytCacheTTL, ytCache = prevTTL, prevCache })
}

// countCacheKeys reports how many foo-youtube entries the store at path
// holds. A store that was never created counts as zero — kv.Open would
// happily create an empty one, so the file is checked first.
func countCacheKeys(t *testing.T, path string) int {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		return 0
	}
	store, err := kv.Open(kv.Config{Backend: "sqlite", Path: path})
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	defer store.Close()
	keys, err := store.List(context.Background(), "foo-youtube:")
	if err != nil {
		t.Fatalf("listing keys: %v", err)
	}
	return len(keys)
}
