package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// countingSource records how often the inner source was consulted, so a
// cache hit is proven by the fetch NOT happening rather than by the rows
// merely matching — identical rows would come back either way.
type countingSource struct {
	calls   atomic.Int64
	entries []ModelEntry
	err     error
}

func (s *countingSource) ListModels(context.Context) ([]ModelEntry, error) {
	s.calls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	return s.entries, nil
}

// withCacheDir points FOO_CACHE at a fresh temp dir so tests never read
// or write the developer's real cache, and so each test starts cold.
func withCacheDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("FOO_CACHE", dir)
	t.Setenv("FOO_CACHE_TTL", "")
	return dir
}

// newTestCache builds a cache around src without going through
// NewCachedEndpointCatalog, which would construct its own HTTP-backed
// inner source. ttl and refresh are passed explicitly so a test can pin
// them rather than route through env.
func newTestCache(t *testing.T, src CatalogSource, baseURL string, ttl time.Duration, refresh bool) *cachedEndpointCatalog {
	t.Helper()
	store, err := openEndpointCacheStore()
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &cachedEndpointCatalog{inner: src, baseURL: baseURL, store: store, ttl: ttl, refresh: refresh}
}

func sampleEndpointRows() []ModelEntry {
	return []ModelEntry{
		{Source: SourceEndpoint, Provider: "127.0.0.1:11434", ID: "llama3:8b", Routable: true},
		{Source: SourceEndpoint, Provider: "127.0.0.1:11434", ID: "qwen3:4b", Routable: true},
	}
}

// TestEndpointCache_SecondCallServesFromCache is the headline behavior:
// within the TTL the endpoint is probed once, not twice.
func TestEndpointCache_SecondCallServesFromCache(t *testing.T) {
	withCacheDir(t)
	src := &countingSource{entries: sampleEndpointRows()}
	c := newTestCache(t, src, "http://127.0.0.1:11434/v1", time.Minute, false)

	first, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("second: %v", err)
	}

	if n := src.calls.Load(); n != 1 {
		t.Errorf("inner source called %d time(s), want 1 (second call must hit the cache)", n)
	}
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("rows: first=%d second=%d, want 2 and 2", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("row %d differs between fetch and cache hit: %+v vs %+v", i, first[i], second[i])
		}
	}
}

// TestEndpointCache_NeverCachesAFailure is the rule the whole file
// exists to hold. A down ssh tunnel must stay an error on every call,
// and must never be written as an empty inventory that a later call
// serves as "this server has no models".
func TestEndpointCache_NeverCachesAFailure(t *testing.T) {
	dir := withCacheDir(t)
	wantErr := errors.New("foo: list models from http://127.0.0.1:11500/v1/models: connection refused")
	src := &countingSource{err: wantErr}
	c := newTestCache(t, src, "http://127.0.0.1:11500/v1", time.Minute, false)

	for i := 1; i <= 2; i++ {
		got, err := c.ListModels(context.Background())
		if !errors.Is(err, wantErr) {
			t.Fatalf("call %d: err = %v, want the transport error", i, err)
		}
		if got != nil {
			t.Errorf("call %d: returned %d row(s) alongside an error", i, len(got))
		}
	}
	if n := src.calls.Load(); n != 2 {
		t.Errorf("inner source called %d time(s), want 2: a failure must not be cached", n)
	}

	// The negative is worth asserting directly: nothing may be stored
	// under the key at all. A later success must be what populates it.
	store, err := openEndpointCacheStore()
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer store.Close()
	if _, found, _ := store.Get(context.Background(),
		endpointCacheKeyPrefix+"http://127.0.0.1:11500/v1"); found {
		t.Errorf("a failed probe wrote a cache entry in %s", dir)
	}
}

// TestEndpointCache_EmptyButSuccessfulIsCached separates the two facts
// the previous test keeps apart. A server that genuinely serves nothing
// said so, and that answer is as cacheable as any other.
func TestEndpointCache_EmptyButSuccessfulIsCached(t *testing.T) {
	withCacheDir(t)
	src := &countingSource{entries: []ModelEntry{}}
	c := newTestCache(t, src, "http://127.0.0.1:11434/v1", time.Minute, false)

	for i := 1; i <= 2; i++ {
		got, err := c.ListModels(context.Background())
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if len(got) != 0 {
			t.Errorf("call %d: got %d rows, want 0", i, len(got))
		}
	}
	if n := src.calls.Load(); n != 1 {
		t.Errorf("inner source called %d time(s), want 1: an empty success is cacheable", n)
	}
}

// TestEndpointCache_RefreshBypassesTheRead covers --refresh's endpoint
// half: the stored entry is ignored, and the fresh result replaces it.
func TestEndpointCache_RefreshBypassesTheRead(t *testing.T) {
	withCacheDir(t)
	const url = "http://127.0.0.1:11434/v1"

	warm := &countingSource{entries: sampleEndpointRows()}
	if _, err := newTestCache(t, warm, url, time.Minute, false).ListModels(context.Background()); err != nil {
		t.Fatalf("warm: %v", err)
	}

	// A different inventory behind the same URL, as after `ollama pull`.
	pulled := append(sampleEndpointRows(),
		ModelEntry{Source: SourceEndpoint, Provider: "127.0.0.1:11434", ID: "mistral:7b", Routable: true})
	fresh := &countingSource{entries: pulled}

	got, err := newTestCache(t, fresh, url, time.Minute, true).ListModels(context.Background())
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if fresh.calls.Load() != 1 {
		t.Errorf("--refresh did not reach the endpoint: %d call(s)", fresh.calls.Load())
	}
	if len(got) != 3 {
		t.Fatalf("refresh returned %d rows, want the 3 fresh ones", len(got))
	}

	// --refresh writes as well as reads, so the next plain call sees
	// the new inventory rather than the pre-pull one.
	after := &countingSource{entries: nil}
	cached, err := newTestCache(t, after, url, time.Minute, false).ListModels(context.Background())
	if err != nil {
		t.Fatalf("after refresh: %v", err)
	}
	if after.calls.Load() != 0 {
		t.Errorf("--refresh left no warm cache: inner called %d time(s)", after.calls.Load())
	}
	if len(cached) != 3 {
		t.Errorf("cache still holds %d rows, want the 3 refreshed ones", len(cached))
	}
}

// TestEndpointCache_ExpiredEntryRefetches proves the TTL is load-bearing
// and not merely stored. A one-nanosecond window is expired by the time
// the second call runs.
func TestEndpointCache_ExpiredEntryRefetches(t *testing.T) {
	withCacheDir(t)
	src := &countingSource{entries: sampleEndpointRows()}
	c := newTestCache(t, src, "http://127.0.0.1:11434/v1", time.Nanosecond, false)

	if _, err := c.ListModels(context.Background()); err != nil {
		t.Fatalf("first: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := c.ListModels(context.Background()); err != nil {
		t.Fatalf("second: %v", err)
	}
	if n := src.calls.Load(); n != 2 {
		t.Errorf("inner source called %d time(s), want 2: an expired entry must refetch", n)
	}
}

// TestEndpointCache_KeyedByBaseURL: two servers listed in one process
// must not see each other's inventory.
func TestEndpointCache_KeyedByBaseURL(t *testing.T) {
	withCacheDir(t)
	a := &countingSource{entries: sampleEndpointRows()}
	b := &countingSource{entries: []ModelEntry{
		{Source: SourceEndpoint, Provider: "127.0.0.1:11500", ID: "gpt-oss:20b", Routable: true},
	}}

	if _, err := newTestCache(t, a, "http://127.0.0.1:11434/v1", time.Minute, false).
		ListModels(context.Background()); err != nil {
		t.Fatalf("a: %v", err)
	}
	got, err := newTestCache(t, b, "http://127.0.0.1:11500/v1", time.Minute, false).
		ListModels(context.Background())
	if err != nil {
		t.Fatalf("b: %v", err)
	}
	if b.calls.Load() != 1 {
		t.Errorf("second endpoint served from the first one's entry")
	}
	if len(got) != 1 || got[0].ID != "gpt-oss:20b" {
		t.Errorf("second endpoint returned %+v", got)
	}
}

// TestEndpointCache_ZeroTTLWritesNothing is the documented trap. kit's
// httpcache reads a non-positive TTL as "no expiry", so a zero that
// leaks through would make every entry immortal — the opposite of off.
// The assertion is on cache writes, not on the returned rows, because
// the rows are identical either way.
func TestEndpointCache_ZeroTTLWritesNothing(t *testing.T) {
	for _, ttl := range []string{"0", "0s"} {
		t.Run(ttl, func(t *testing.T) {
			dir := withCacheDir(t)
			t.Setenv("FOO_CACHE_TTL", ttl)

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"data":[{"id":"llama3:8b"}]}`))
			}))
			defer srv.Close()

			src := NewCachedEndpointCatalog(srv.URL+"/v1", srv.Client(), false)
			if _, ok := src.(*cachedEndpointCatalog); ok {
				t.Fatal("TTL=0 must return the uncached source, not a cache with a zero window")
			}
			for i := 0; i < 2; i++ {
				if _, err := src.ListModels(context.Background()); err != nil {
					t.Fatalf("call %d: %v", i, err)
				}
			}

			// Nothing may exist on disk: not an empty db, not an
			// entry with no expiry.
			if matches, _ := filepath.Glob(filepath.Join(dir, "*")); len(matches) != 0 {
				t.Errorf("TTL=0 wrote %v; want zero cache writes", matches)
			}
		})
	}
}

// TestEndpointCache_RefreshStillWritesAtZeroTTL: --refresh must not
// resurrect caching that the TTL turned off.
func TestEndpointCache_RefreshStillWritesNothingAtZeroTTL(t *testing.T) {
	dir := withCacheDir(t)
	t.Setenv("FOO_CACHE_TTL", "0s")

	src := NewCachedEndpointCatalog("http://127.0.0.1:11434/v1", nil, true)
	if _, ok := src.(*cachedEndpointCatalog); ok {
		t.Fatal("TTL=0 with --refresh must still return the uncached source")
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "*")); len(matches) != 0 {
		t.Errorf("wrote %v at TTL=0", matches)
	}
}

// TestEndpointCache_TTLFromEnv proves FOO_CACHE_TTL reaches the cache as
// a parsed duration rather than being ignored.
func TestEndpointCache_TTLFromEnv(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		want      time.Duration
	}{
		{"unset falls back to the default", "", DefaultEndpointCacheTTL},
		{"minutes", "90m", 90 * time.Minute},
		{"hours", "2h", 2 * time.Hour},
		{"raw seconds do not parse", "300", DefaultEndpointCacheTTL},
		{"garbage falls through", "soon", DefaultEndpointCacheTTL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FOO_CACHE_TTL", tc.env)
			if got := resolveCacheTTL(DefaultEndpointCacheTTL); got != tc.want {
				t.Errorf("resolveCacheTTL = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestResolveCachePath covers the host-level ladder: FOO_CACHE names a
// directory to join under, and its absence falls to XDG.
func TestResolveCachePath(t *testing.T) {
	t.Run("FOO_CACHE joins the db name", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("FOO_CACHE", dir)
		got, err := resolveCachePath("foo", "x.db")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if want := filepath.Join(dir, "x.db"); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("falls back to XDG", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("FOO_CACHE", "")
		t.Setenv("XDG_CACHE_HOME", home)
		got, err := resolveCachePath("foo", "x.db")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if want := filepath.Join(home, "foo", "x.db"); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

// TestEndpointCache_UnreachableEndpointNamesTheURL is the end-to-end
// version of the never-cache-a-failure rule, through the real
// constructor and a closed port rather than a stub error.
func TestEndpointCache_UnreachableEndpointNamesTheURL(t *testing.T) {
	withCacheDir(t)

	// A server closed immediately hands us a port nothing listens on.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL + "/v1"
	srv.Close()

	src := NewCachedEndpointCatalog(url, &http.Client{Timeout: 2 * time.Second}, false)
	got, err := src.ListModels(context.Background())
	if err == nil {
		t.Fatalf("unreachable endpoint returned %d row(s) and no error", len(got))
	}
	if got != nil {
		t.Errorf("rows returned alongside an error: %+v", got)
	}
	if !strings.Contains(err.Error(), url) {
		t.Errorf("error does not name the endpoint: %v", err)
	}
}
