package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestResolveCachePath covers the path precedence:
// plugin-specific env -> shared FOO_CACHE (dir + per-tool db) -> XDG default.
func TestResolveCachePath(t *testing.T) {
	t.Run("specific env wins (exact path)", func(t *testing.T) {
		t.Setenv("FOO_SCRAPE_CACHE", "/tmp/custom.db")
		t.Setenv("FOO_CACHE", "/should/be/ignored")
		got, err := resolveCachePath("foo-scrape", "FOO_SCRAPE_CACHE")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got != "/tmp/custom.db" {
			t.Errorf("got %q, want /tmp/custom.db", got)
		}
	})

	t.Run("shared FOO_CACHE is a dir, db filed under it", func(t *testing.T) {
		t.Setenv("FOO_SCRAPE_CACHE", "")
		t.Setenv("FOO_CACHE", "/shared/cache")
		got, err := resolveCachePath("foo-scrape", "FOO_SCRAPE_CACHE")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		want := filepath.Join("/shared/cache", "foo-scrape-cache.db")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("whitespace-only env is treated as unset", func(t *testing.T) {
		t.Setenv("FOO_SCRAPE_CACHE", "   ")
		t.Setenv("FOO_CACHE", "/shared")
		got, err := resolveCachePath("foo-scrape", "FOO_SCRAPE_CACHE")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got != filepath.Join("/shared", "foo-scrape-cache.db") {
			t.Errorf("blank specific env should fall through to FOO_CACHE; got %q", got)
		}
	})

	t.Run("no env falls back to XDG default with per-tool db name", func(t *testing.T) {
		t.Setenv("FOO_SCRAPE_CACHE", "")
		t.Setenv("FOO_CACHE", "")
		got, err := resolveCachePath("foo-scrape", "FOO_SCRAPE_CACHE")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if !strings.HasSuffix(got, filepath.Join("foo-scrape", "foo-scrape-cache.db")) {
			t.Errorf("XDG default should end in foo-scrape/foo-scrape-cache.db; got %q", got)
		}
	})
}

// TestResolveCacheTTL covers the TTL precedence:
// plugin-specific env -> shared FOO_CACHE_TTL -> 24h default.
func TestResolveCacheTTL(t *testing.T) {
	t.Run("specific env wins", func(t *testing.T) {
		t.Setenv("FOO_SCRAPE_CACHE_TTL", "1h")
		t.Setenv("FOO_CACHE_TTL", "9h")
		if got := resolveCacheTTL("FOO_SCRAPE_CACHE_TTL"); got != time.Hour {
			t.Errorf("got %v, want 1h", got)
		}
	})

	t.Run("falls back to shared FOO_CACHE_TTL", func(t *testing.T) {
		t.Setenv("FOO_SCRAPE_CACHE_TTL", "")
		t.Setenv("FOO_CACHE_TTL", "30m")
		if got := resolveCacheTTL("FOO_SCRAPE_CACHE_TTL"); got != 30*time.Minute {
			t.Errorf("got %v, want 30m", got)
		}
	})

	t.Run("unparseable specific falls through to shared", func(t *testing.T) {
		t.Setenv("FOO_SCRAPE_CACHE_TTL", "not-a-duration")
		t.Setenv("FOO_CACHE_TTL", "2h")
		if got := resolveCacheTTL("FOO_SCRAPE_CACHE_TTL"); got != 2*time.Hour {
			t.Errorf("got %v, want 2h (bad specific should fall through)", got)
		}
	})

	t.Run("no env defaults to 24h", func(t *testing.T) {
		t.Setenv("FOO_SCRAPE_CACHE_TTL", "")
		t.Setenv("FOO_CACHE_TTL", "")
		if got := resolveCacheTTL("FOO_SCRAPE_CACHE_TTL"); got != 24*time.Hour {
			t.Errorf("got %v, want 24h", got)
		}
	})

	t.Run("zero duration is honored (no expiry)", func(t *testing.T) {
		t.Setenv("FOO_SCRAPE_CACHE_TTL", "0s")
		if got := resolveCacheTTL("FOO_SCRAPE_CACHE_TTL"); got != 0 {
			t.Errorf("got %v, want 0", got)
		}
	})
}
