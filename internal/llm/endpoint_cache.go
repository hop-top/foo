// Endpoint inventory caching for `foo model list --endpoint`.
//
// The two caches on this command answer to opposite clocks and must not
// be conflated. The aim catalog (models.dev) is a published census that
// moves on a daily cadence, and aim already caches it at 24h with
// ETag/304 revalidation — foo adds nothing there, it only reports what
// aim did (see [CatalogProvenance]). A live endpoint's inventory is
// local mutable state: `ollama pull` changes it in the time it takes to
// download a file, and there is no ETag to revalidate against because
// the response carries no validator.
//
// So this cache is deliberately short. It exists to make a second
// `foo model list` in the same minute instant, not to avoid talking to
// the server for a day.
//
// What is cached is the projected []ModelEntry rather than the raw HTTP
// response. Caching at the transport (kit's httpcache) would key on the
// request and store whatever came back, including a 200 whose body is a
// proxy error page — the exact failure endpointCatalog.ListModels
// diagnoses by parse error. Caching the projection means only a
// successfully parsed inventory is ever written, which is what makes
// the "never cache a failure" rule structural rather than a check
// someone has to remember.

package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"hop.top/kit/go/storage/kv"

	// kit v0.5 splits kv backends into separately-imported packages;
	// the blank import runs the init() that registers "sqlite".
	_ "hop.top/kit/go/storage/kv/sqlite"
)

// endpointCacheDB is the sqlite filename under the resolved cache
// directory. Named for what it holds rather than for the command, so a
// second endpoint-backed surface shares one store.
const endpointCacheDB = "model-endpoint-cache.db"

// endpointCacheTool is the XDG tool segment, i.e. the cache lands at
// <XDG_CACHE_HOME>/foo/model-endpoint-cache.db by default.
const endpointCacheTool = "foo"

// endpointCacheKeyPrefix namespaces entries inside a store that
// FOO_CACHE may point at a directory shared with other foo caches.
const endpointCacheKeyPrefix = "model-endpoint:"

// DefaultEndpointCacheTTL is how long a cached endpoint inventory stays
// fresh.
//
// Five minutes, chosen against the thing that invalidates it. A local
// runtime's model list changes when the operator runs `ollama pull`,
// `ollama rm`, or restarts a server with a different model directory —
// all interactive acts, all followed within a minute or two by the
// operator looking at `foo model list` to confirm. A 24h TTL (the
// catalog's figure) would show them the pre-pull list and read as a bug
// in foo. A 30s TTL would not survive the pause between reading the
// list and re-reading it after piping through a filter.
//
// Five minutes also bounds the staleness a user has to reason about:
// "did I pull that in the last five minutes" is answerable, and
// --refresh is right there when the answer is yes. The probe it saves
// is cheap but not free — 25ms to 115ms locally, more over an ssh
// tunnel — and the win is a listing that feels instant on repeat.
const DefaultEndpointCacheTTL = 5 * time.Minute

// cachedEndpointCatalog wraps a CatalogSource with a TTL store keyed by
// base URL.
//
// It is a decorator rather than a field on endpointCatalog so the
// uncached path stays byte-identical and so a test can cache any
// source, including one that fails on demand.
type cachedEndpointCatalog struct {
	inner   CatalogSource
	baseURL string
	store   kv.TTLStore
	ttl     time.Duration

	// refresh skips the read but not the write, so `--refresh` leaves
	// a warm cache behind rather than a stale one.
	refresh bool
}

// endpointCacheEntry is the stored payload. The base URL is recorded
// alongside the rows so a key collision (two stores merged, a prefix
// changed) surfaces as a miss rather than as another server's
// inventory.
type endpointCacheEntry struct {
	BaseURL   string       `json:"base_url"`
	FetchedAt time.Time    `json:"fetched_at"`
	Models    []ModelEntry `json:"models"`
}

// NewCachedEndpointCatalog returns a CatalogSource that lists baseURL's
// models through a short-lived on-disk cache.
//
// Caching is skipped — the inner source is returned unwrapped — when
// refresh is set, when the resolved TTL is non-positive, or when the
// store cannot be opened. Every skip is a correctness-preserving
// fallback: the command still works, it just talks to the server.
//
// refresh=true bypasses the read but still writes, so `--refresh`
// leaves a warm cache behind rather than a stale one.
//
// One consequence of a TTL store worth knowing: the window is bound to
// an entry when it is written, not applied when it is read. Lowering
// FOO_CACHE_TTL therefore does not shorten entries already on disk —
// verified by hand, a 5m entry survives a subsequent 1ns run. --refresh
// is the way past an entry written under a longer window.
func NewCachedEndpointCatalog(baseURL string, client *http.Client, refresh bool) CatalogSource {
	inner := NewEndpointCatalog(baseURL, client)

	ttl := resolveCacheTTL(DefaultEndpointCacheTTL)
	if ttl <= 0 {
		// A zero TTL means caching OFF. It must not reach a TTL store
		// as a duration: kit's httpcache reads non-positive as "never
		// expire", and an entry that never expires is the worst
		// possible answer for an inventory that changes on a pull.
		return inner
	}

	store, err := openEndpointCacheStore()
	if err != nil {
		slog.Warn("model.endpoint.cache.open.failed", slog.Any("err", err))
		return inner
	}
	return &cachedEndpointCatalog{
		inner:   inner,
		baseURL: baseURL,
		store:   store,
		ttl:     ttl,
		refresh: refresh,
	}
}

// openEndpointCacheStore resolves the path and opens the TTL store.
func openEndpointCacheStore() (kv.TTLStore, error) {
	path, err := resolveCachePath(endpointCacheTool, endpointCacheDB)
	if err != nil {
		return nil, fmt.Errorf("resolve cache path: %w", err)
	}
	store, err := kv.Open(kv.Config{Backend: "sqlite", Path: path})
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	ttlStore, ok := store.(kv.TTLStore)
	if !ok {
		_ = store.Close()
		return nil, fmt.Errorf("%s: backend does not support TTL", path)
	}
	return ttlStore, nil
}

// ListModels serves from cache when a fresh entry exists, else fetches
// and stores.
//
// An error from the inner source is returned as-is and nothing is
// written. This is the rule the whole file exists to hold: an
// unreachable endpoint must stay an error naming the URL, because "this
// server has no models" and "this server did not answer" are opposite
// facts and a down ssh tunnel is the common case. An empty-but-
// successful response is a different thing and is cached normally — a
// server that genuinely serves nothing said so.
func (c *cachedEndpointCatalog) ListModels(ctx context.Context) ([]ModelEntry, error) {
	key := endpointCacheKeyPrefix + c.baseURL

	if !c.refresh {
		if entry, ok := c.read(ctx, key); ok {
			return entry.Models, nil
		}
	}

	models, err := c.inner.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	c.write(ctx, key, models)
	return models, nil
}

// read returns a stored entry for key, or ok=false on any miss.
//
// Every failure mode — absent key, unreadable store, undecodable
// payload, a payload recorded against a different base URL — is a miss,
// never an error: a broken cache must degrade to a fetch, not to a
// failed command.
func (c *cachedEndpointCatalog) read(ctx context.Context, key string) (endpointCacheEntry, bool) {
	raw, found, err := c.store.Get(ctx, key)
	if err != nil || !found {
		return endpointCacheEntry{}, false
	}
	var entry endpointCacheEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return endpointCacheEntry{}, false
	}
	if entry.BaseURL != c.baseURL {
		return endpointCacheEntry{}, false
	}
	return entry, true
}

// write stores models under key with the configured TTL. Failures are
// logged and swallowed: a cache that cannot be written is a slower
// command, not a broken one.
func (c *cachedEndpointCatalog) write(ctx context.Context, key string, models []ModelEntry) {
	payload, err := json.Marshal(endpointCacheEntry{
		BaseURL:   c.baseURL,
		FetchedAt: time.Now(),
		Models:    models,
	})
	if err != nil {
		slog.Warn("model.endpoint.cache.encode.failed", slog.Any("err", err))
		return
	}
	if err := c.store.PutWithTTL(ctx, key, payload, c.ttl); err != nil {
		slog.Warn("model.endpoint.cache.write.failed", slog.Any("err", err))
	}
}
