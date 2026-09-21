// Cache path and TTL resolution for foo's own host-level caches.
//
// The env namespace is settled across the whole foo family: the
// unprefixed FOO_CACHE / FOO_CACHE_TTL are foo's own host-level
// settings, which every extension inherits as its default, and the
// FOO_<EXT>_ prefixed form belongs to one extension and overrides it.
// Precedence is FOO_<EXT>_* > FOO_* > built-in default.
//
// cmd/foo-scrape and cmd/foo-youtube implement the same two rules
// inline. They are not refactored onto this file and must not import
// it: both are standalone sidecar binaries whose file header states
// they import zero foo internal packages, which is what lets them ship
// and version independently of the host. This file is the host
// binary's copy — the first one FOO_CACHE affects directly rather than
// through an extension — and the rules, not the code, are what is
// shared. Any host-side cache added later reuses this file rather than
// writing a fourth copy.

package llm

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"hop.top/kit/go/core/xdg"
)

// hostCacheDirEnv is foo's own host-level cache path setting. It names
// a directory; an extension-specific variable names a file outright.
const hostCacheDirEnv = "FOO_CACHE"

// hostCacheTTLEnv is foo's own host-level freshness window.
const hostCacheTTLEnv = "FOO_CACHE_TTL"

// resolveCachePath picks a cache file path: FOO_CACHE as a directory to
// join dbName under, else the XDG cache dir for tool.
//
// There is no extension-specific variable in the host's own precedence
// ladder — the host is what FOO_CACHE belongs to. The signature stays
// shaped like the sidecars' (tool, dbName) so the three resolve to
// comparable paths and a reader moving between them is not surprised.
//
// kv.Open creates a missing parent directory, so no MkdirAll happens
// here.
func resolveCachePath(tool, dbName string) (string, error) {
	if dir := strings.TrimSpace(os.Getenv(hostCacheDirEnv)); dir != "" {
		return filepath.Join(dir, dbName), nil
	}
	return xdg.CacheFile(tool, dbName)
}

// resolveCacheTTL reads FOO_CACHE_TTL, falling back to def.
//
// Values are Go duration strings ("10h", "90m", "5m"), never raw
// seconds: "300" does not parse and falls through to def rather than
// being read as five minutes, which is the safe direction for an
// ambiguous value.
//
// A parsed zero ("0", "0s") is honoured and means caching OFF. Callers
// must treat a non-positive result as "skip the cache entirely" and
// must never hand it to kit's httpcache.WithTTL, which reads
// non-positive as "no expiry" — the exact opposite — and would make
// every entry immortal.
func resolveCacheTTL(def time.Duration) time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(os.Getenv(hostCacheTTLEnv))); err == nil {
		return d
	}
	return def
}
