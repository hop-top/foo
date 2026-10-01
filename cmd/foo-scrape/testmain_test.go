package main

import (
	"fmt"
	"os"
	"testing"

	"hop.top/foo/internal/testutil"
)

// cacheEnv is every variable that moves or tunes the scrape cache. A
// developer's own setting must never steer a test.
var cacheEnv = []string{
	"FOO_CACHE", "FOO_CACHE_TTL", "FOO_SCRAPE_CACHE", "FOO_SCRAPE_CACHE_TTL", "FOO_SCRAPE_BUS_PEERS",
}

// TestMain points HOME and the XDG base dirs at a throwaway tree and
// drops the cache variables above, so a test that does not name its own
// cache file writes the default one there: never into the developer's
// cache, and never into a store a concurrent test run is using.
func TestMain(m *testing.M) {
	cleanup, err := testutil.IsolateUserDirs()
	if err != nil {
		fmt.Fprintln(os.Stderr, "isolate user dirs:", err)
		os.Exit(1)
	}
	for _, name := range cacheEnv {
		_ = os.Unsetenv(name)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}
