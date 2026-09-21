package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hop.top/kit/go/console/progress"
	"hop.top/kit/go/storage/kv"
	_ "hop.top/kit/go/storage/kv/sqlite"
)

// TestResolveCachePath covers the path precedence:
// FOO_<EXT>_CACHE -> foo's host-level FOO_CACHE (dir + per-ext db) -> XDG.
func TestResolveCachePath(t *testing.T) {
	t.Run("specific env wins (exact path)", func(t *testing.T) {
		t.Setenv("FOO_SCRAPE_CACHE", "/tmp/custom.db")
		t.Setenv("FOO_CACHE", "/should/be/ignored")
		got, err := resolveCachePath("foo-scrape", scrapeCacheDB, "FOO_SCRAPE_CACHE")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got != "/tmp/custom.db" {
			t.Errorf("got %q, want /tmp/custom.db", got)
		}
	})

	t.Run("host FOO_CACHE is a dir, db filed under it", func(t *testing.T) {
		t.Setenv("FOO_SCRAPE_CACHE", "")
		t.Setenv("FOO_CACHE", "/shared/cache")
		got, err := resolveCachePath("foo-scrape", scrapeCacheDB, "FOO_SCRAPE_CACHE")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		want := filepath.Join("/shared/cache", scrapeCacheDB)
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("whitespace-only env is treated as unset", func(t *testing.T) {
		t.Setenv("FOO_SCRAPE_CACHE", "   ")
		t.Setenv("FOO_CACHE", "/shared")
		got, err := resolveCachePath("foo-scrape", scrapeCacheDB, "FOO_SCRAPE_CACHE")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got != filepath.Join("/shared", scrapeCacheDB) {
			t.Errorf("blank specific env should fall through to FOO_CACHE; got %q", got)
		}
	})

	t.Run("no env falls back to XDG default with per-tool db name", func(t *testing.T) {
		t.Setenv("FOO_SCRAPE_CACHE", "")
		t.Setenv("FOO_CACHE", "")
		t.Setenv("XDG_CACHE_HOME", t.TempDir())
		got, err := resolveCachePath("foo-scrape", scrapeCacheDB, "FOO_SCRAPE_CACHE")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if !strings.HasSuffix(got, filepath.Join("foo-scrape", scrapeCacheDB)) {
			t.Errorf("XDG default should end in foo-scrape/%s; got %q", scrapeCacheDB, got)
		}
	})

	// The host dir must yield distinct files per extension, or two
	// sidecars inheriting one FOO_CACHE would fight over one sqlite file.
	t.Run("host dir keeps sibling extensions distinct", func(t *testing.T) {
		t.Setenv("FOO_SCRAPE_CACHE", "")
		t.Setenv("FOO_CACHE", "/shared")
		scrapePath, err := resolveCachePath("foo-scrape", scrapeCacheDB, "FOO_SCRAPE_CACHE")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		ytPath, err := resolveCachePath("foo-youtube", "ytdlp-cache.db", "FOO_YOUTUBE_CACHE")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if scrapePath == ytPath {
			t.Errorf("sibling tools collided on %q", scrapePath)
		}
	})
}

// TestResolveCacheTTL covers the TTL precedence:
// FOO_<EXT>_CACHE_TTL -> host FOO_CACHE_TTL -> per-extension default.
func TestResolveCacheTTL(t *testing.T) {
	t.Run("specific env wins", func(t *testing.T) {
		t.Setenv("FOO_SCRAPE_CACHE_TTL", "1h")
		t.Setenv("FOO_CACHE_TTL", "9h")
		if got := resolveCacheTTL("FOO_SCRAPE_CACHE_TTL", scrapeCacheTTLDefault); got != time.Hour {
			t.Errorf("got %v, want 1h", got)
		}
	})

	t.Run("falls back to the host FOO_CACHE_TTL", func(t *testing.T) {
		t.Setenv("FOO_SCRAPE_CACHE_TTL", "")
		t.Setenv("FOO_CACHE_TTL", "30m")
		if got := resolveCacheTTL("FOO_SCRAPE_CACHE_TTL", scrapeCacheTTLDefault); got != 30*time.Minute {
			t.Errorf("got %v, want 30m", got)
		}
	})

	t.Run("unparseable extension value falls through to the host", func(t *testing.T) {
		t.Setenv("FOO_SCRAPE_CACHE_TTL", "not-a-duration")
		t.Setenv("FOO_CACHE_TTL", "2h")
		if got := resolveCacheTTL("FOO_SCRAPE_CACHE_TTL", scrapeCacheTTLDefault); got != 2*time.Hour {
			t.Errorf("got %v, want 2h (bad specific should fall through)", got)
		}
	})

	// Raw seconds are not a duration string; "600" must not silently
	// become 600ns or 10m. It falls through like any other bad value.
	t.Run("bare integer is not a duration", func(t *testing.T) {
		t.Setenv("FOO_SCRAPE_CACHE_TTL", "600")
		t.Setenv("FOO_CACHE_TTL", "")
		if got := resolveCacheTTL("FOO_SCRAPE_CACHE_TTL", scrapeCacheTTLDefault); got != scrapeCacheTTLDefault {
			t.Errorf("got %v, want the %v default", got, scrapeCacheTTLDefault)
		}
	})

	t.Run("no env defaults to 10h", func(t *testing.T) {
		t.Setenv("FOO_SCRAPE_CACHE_TTL", "")
		t.Setenv("FOO_CACHE_TTL", "")
		if got := resolveCacheTTL("FOO_SCRAPE_CACHE_TTL", scrapeCacheTTLDefault); got != 10*time.Hour {
			t.Errorf("got %v, want 10h", got)
		}
	})

	t.Run("zero parses to zero (caller reads it as off)", func(t *testing.T) {
		t.Setenv("FOO_SCRAPE_CACHE_TTL", "0s")
		t.Setenv("FOO_CACHE_TTL", "9h")
		if got := resolveCacheTTL("FOO_SCRAPE_CACHE_TTL", scrapeCacheTTLDefault); got != 0 {
			t.Errorf("got %v, want 0 (an explicit zero must not fall through)", got)
		}
	})
}

// TestHTTPClient_CachesByDefault proves caching is ON with no env at
// all: the client must come back wired to an observedStore.
func TestHTTPClient_CachesByDefault(t *testing.T) {
	isolateCacheEnv(t)

	client, obs := httpClient(false)
	if obs == nil {
		t.Fatal("no observedStore; caching is off by default")
	}
	if client == http.DefaultClient {
		t.Error("got the default client; the cache transport was not wired")
	}
}

// TestHTTPClient_NoCacheBypasses proves --no-cache takes the uncached
// path: default client, no store.
func TestHTTPClient_NoCacheBypasses(t *testing.T) {
	isolateCacheEnv(t)

	client, obs := httpClient(true)
	if obs != nil {
		t.Error("observedStore wired despite --no-cache")
	}
	if client != http.DefaultClient {
		t.Error("want the default client under --no-cache")
	}
}

// TestHTTPClient_ZeroTTLDisablesCache is the guard on kit's httpcache
// semantics: a non-positive TTL there means "never expire", so a zero
// must never reach WithTTL. foo intercepts it and skips the cache.
func TestHTTPClient_ZeroTTLDisablesCache(t *testing.T) {
	for _, ttl := range []string{"0", "0s", "0h"} {
		t.Run(ttl, func(t *testing.T) {
			isolateCacheEnv(t)
			t.Setenv("FOO_SCRAPE_CACHE_TTL", ttl)

			client, obs := httpClient(false)
			if obs != nil {
				t.Error("observedStore wired at TTL=0; entries would never expire")
			}
			if client != http.DefaultClient {
				t.Error("want the default client at TTL=0")
			}
		})
	}
}

// TestScrape_ZeroTTLWritesNothing is the behavioral half of the zero-TTL
// guard: two scrapes at TTL=0 must hit the origin twice and leave the
// store empty. A cache wired with a non-positive TTL would instead store
// an entry with no expiry and serve the second scrape from it forever.
func TestScrape_ZeroTTLWritesNothing(t *testing.T) {
	isolateCacheEnv(t)

	var origin int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		origin++
		_, _ = w.Write([]byte(scrapePage))
	}))
	t.Cleanup(srv.Close)

	path := filepath.Join(t.TempDir(), "zero.db")
	t.Setenv("FOO_SCRAPE_CACHE", path)
	t.Setenv("FOO_SCRAPE_CACHE_TTL", "0s")

	for i := range 2 {
		var out strings.Builder
		ctx := progress.WithReporter(context.Background(), &recorder{})
		if err := scrape(scrapeCmd(ctx, &out), srv.URL, "readability", false); err != nil {
			t.Fatalf("scrape %d: %v", i, err)
		}
	}

	if origin != 2 {
		t.Errorf("origin requests = %d, want 2 (TTL=0 must not serve from cache)", origin)
	}
	if n := countCacheKeys(t, path); n != 0 {
		t.Errorf("%d cache entries written at TTL=0, want 0", n)
	}
}

// TestScrape_NoCacheWritesNothing pins --no-cache the same way: a real
// store path is configured but the flag must keep every byte out of it.
func TestScrape_NoCacheWritesNothing(t *testing.T) {
	isolateCacheEnv(t)

	var origin int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		origin++
		_, _ = w.Write([]byte(scrapePage))
	}))
	t.Cleanup(srv.Close)

	path := filepath.Join(t.TempDir(), "nocache.db")
	t.Setenv("FOO_SCRAPE_CACHE", path)

	for i := range 2 {
		var out strings.Builder
		ctx := progress.WithReporter(context.Background(), &recorder{})
		if err := scrape(scrapeCmd(ctx, &out), srv.URL, "readability", true); err != nil {
			t.Fatalf("scrape %d: %v", i, err)
		}
	}

	if origin != 2 {
		t.Errorf("origin requests = %d, want 2 (--no-cache must always refetch)", origin)
	}
	if n := countCacheKeys(t, path); n != 0 {
		t.Errorf("%d cache entries written under --no-cache, want 0", n)
	}
}

// TestScrape_CachesWithNoEnvConfigured is the default-on behavior end to
// end: with no FOO_SCRAPE_CACHE set, the XDG-resolved store still serves
// the second scrape and the origin is hit once.
func TestScrape_CachesWithNoEnvConfigured(t *testing.T) {
	isolateCacheEnv(t)

	var origin int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		origin++
		_, _ = w.Write([]byte(scrapePage))
	}))
	t.Cleanup(srv.Close)

	for i := range 2 {
		var out strings.Builder
		ctx := progress.WithReporter(context.Background(), &recorder{})
		if err := scrape(scrapeCmd(ctx, &out), srv.URL, "readability", false); err != nil {
			t.Fatalf("scrape %d: %v", i, err)
		}
	}

	if origin != 1 {
		t.Errorf("origin requests = %d, want 1 (caching is on by default)", origin)
	}
}

// TestScrape_SharedFooCacheServesHit proves the host FOO_CACHE dir is
// a real store and not just a resolved string: the second scrape under
// it comes from cache.
func TestScrape_SharedFooCacheServesHit(t *testing.T) {
	isolateCacheEnv(t)

	var origin int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		origin++
		_, _ = w.Write([]byte(scrapePage))
	}))
	t.Cleanup(srv.Close)

	// A not-yet-existing subdirectory: kv.Open creates the parent.
	dir := filepath.Join(t.TempDir(), "nested", "shared")
	t.Setenv("FOO_CACHE", dir)

	for i := range 2 {
		var out strings.Builder
		ctx := progress.WithReporter(context.Background(), &recorder{})
		if err := scrape(scrapeCmd(ctx, &out), srv.URL, "readability", false); err != nil {
			t.Fatalf("scrape %d: %v", i, err)
		}
	}

	if origin != 1 {
		t.Errorf("origin requests = %d, want 1 (FOO_CACHE dir must back a real store)", origin)
	}
	if n := countCacheKeys(t, filepath.Join(dir, scrapeCacheDB)); n == 0 {
		t.Error("no entries under the host FOO_CACHE dir")
	}
}

// isolateCacheEnv clears every cache env this binary reads and points
// XDG at a temp dir, so a test never inherits the developer's real cache
// or another test's leftovers.
func isolateCacheEnv(t *testing.T) {
	t.Helper()
	t.Setenv("FOO_SCRAPE_CACHE", "")
	t.Setenv("FOO_SCRAPE_CACHE_TTL", "")
	t.Setenv("FOO_CACHE", "")
	t.Setenv("FOO_CACHE_TTL", "")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

// countCacheKeys reports how many foo-scrape entries the store at path
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
	keys, err := store.List(context.Background(), "foo-scrape:")
	if err != nil {
		t.Fatalf("listing keys: %v", err)
	}
	return len(keys)
}

// TestRoot_NoCacheFlagReachesFetch drives the real cobra root so the
// --no-cache flag is exercised through registration, parsing and the
// RunE hand-off. The unit tests above prove httpClient honors its
// parameter; only this one proves the flag is connected to it at all.
func TestRoot_NoCacheFlagReachesFetch(t *testing.T) {
	isolateCacheEnv(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(scrapePage))
	}))
	t.Cleanup(srv.Close)

	var seen []bool
	prev := httpClientFor
	httpClientFor = func(noCache bool) (*http.Client, *observedStore) {
		seen = append(seen, noCache)
		return http.DefaultClient, nil
	}
	t.Cleanup(func() { httpClientFor = prev })

	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{"flag before url", []string{"--no-cache", srv.URL}, true},
		{"flag after url", []string{srv.URL, "--no-cache"}, true},
		{"flag absent", []string{srv.URL}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen = nil
			root := newRoot()
			root.Cmd.SetArgs(tc.args)
			root.Cmd.SetOut(&strings.Builder{})
			root.Cmd.SetErr(&strings.Builder{})
			if err := root.Cmd.ExecuteContext(context.Background()); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if len(seen) != 1 {
				t.Fatalf("seam consulted %d times, want 1", len(seen))
			}
			if seen[0] != tc.want {
				t.Errorf("noCache at the fetch seam = %v, want %v", seen[0], tc.want)
			}
		})
	}
}
