# foo-scrape

External plugin binary for the [@../../README.md](../../README.md) CLI:
fetches a URL and converts the page to **markdown** on stdout, ready to
pipe into an LLM prompt. Imports zero foo internal packages.

```sh
foo scrape "https://example.com/article" | foo -p extract_wisdom
```

## Install

```sh
go install hop.top/foo/cmd/foo-scrape@latest
```

No external runtime dependency. Once the binary is on `$PATH`, foo
discovers it as `foo scrape`. For the end-to-end setup and verification
walkthrough, see [@../../docs/how-to/use-plugins.md](../../docs/how-to/use-plugins.md).

## CLI

```
foo-scrape [flags] <url>
```

`<url>` is required.

| Flag | Default | Description |
|------|---------|-------------|
| `--mode` | `readability` | `readability` (main article content) or `raw` (full HTML) |
| `--no-cache` | `false` | Bypass the page cache for this run |
| `--ext-info` | — | Print discovery JSON and exit (host-facing; hidden) |

Flags are parsed by this binary, not by foo — the host forwards argv
verbatim. See [@../../docs/how-to/write-plugins.md](../../docs/how-to/write-plugins.md)
for the argv-passthrough contract.

## Output

Markdown to stdout. `--mode readability` extracts the main article and
drops navigation, scripts, and chrome; `--mode raw` converts the full
HTML document.

Diagnostics go to stderr; data goes to stdout. This markdown-on-stdout
convention is what foo's stdin reader treats as the user prompt — see
[@../../docs/concepts.md](../../docs/concepts.md#plugins-via-path-discovery).

## Discovery

The host runs `foo-scrape --ext-info` and JSON-decodes stdout. The
contract: emit only the JSON object, exit 0.

```json
{
  "name": "scrape",
  "version": "dev",
  "description": "URL to markdown conversion with readability",
  "capabilities": ["discover"]
}
```

`version` is stamped at build time via ldflags (`dev` in source builds).
The full discovery/dispatch mechanism lives in
[@../../docs/how-to/use-plugins.md](../../docs/how-to/use-plugins.md#how-it-works).

## Caching

Fetched pages are cached **on by default**, keyed by request URL, via
`hop.top/kit/go/storage/httpcache` (a caching `http.RoundTripper`) backed
by a sqlite `kv` store — so repeated scrapes of the same URL skip the
network. Only GET + 2xx responses are cached, and `Cache-Control:
no-store` is honored.

| Setting | Default | Effect |
|---------|---------|--------|
| `--no-cache` | off | Bypass the cache for a single run |
| `FOO_SCRAPE_CACHE` | — | Override the cache db **path** (file) |
| `FOO_CACHE` | — | Shared cache **dir** for all foo plugins (db filed under it) |
| `FOO_SCRAPE_CACHE_TTL` | — | Freshness window (Go duration; `0` = no expiry) |
| `FOO_CACHE_TTL` | — | Shared TTL fallback for all foo plugins |
| (default) | `$XDG_CACHE_HOME/foo-scrape/foo-scrape-cache.db`, `24h` | when no env set |

Path precedence: `FOO_SCRAPE_CACHE` → `FOO_CACHE` → XDG default. TTL
precedence: `FOO_SCRAPE_CACHE_TTL` → `FOO_CACHE_TTL` → `24h`. The shared
`FOO_CACHE`/`FOO_CACHE_TTL` let one setting cover every foo plugin while
each keeps a distinct db file.

Caching is best-effort: a path-resolve or store-open failure falls back to
a direct fetch and never fails a scrape. Use `--no-cache` for a guaranteed
fresh pull.

## Exit codes

Follows the kit cross-tool convention (§8.1):

| Code | Meaning |
|------|---------|
| `0` | Success |
| `1` | Fetch failure (network error, non-200 response) |
| `2` | Usage error (missing/invalid URL or `--mode`, bad flags) |

A non-zero exit produces no stdout, which downstream surfaces as
`no prompt provided (stdin was empty)` —
see [@../../docs/troubleshooting.md](../../docs/troubleshooting.md#empty-pipe-into-foo).

## Events

When peers are configured, a capture event publishes onto the kit bus:

| Mutation | Topic |
|----------|-------|
| page scraped | `foo-scrape.capture.page.scraped` |

Peers come from `FOO_SCRAPE_BUS_PEERS` (comma-separated `ws://` URLs);
unset means events stay in-process. Auth token from `FOO_BUS_TOKEN` /
`BUS_TOKEN` when present. Topic notation:
[@../../docs/reference/event-topics.md](../../docs/reference/event-topics.md).

## See also

- [@../../docs/how-to/use-plugins.md](../../docs/how-to/use-plugins.md) — install, verify, pipe
- [@../../docs/how-to/write-plugins.md](../../docs/how-to/write-plugins.md) — plugin contract
- [@../../docs/concepts.md](../../docs/concepts.md#plugins-via-path-discovery) — PATH-discovery model
- [@../../docs/troubleshooting.md](../../docs/troubleshooting.md) — pipeline failures
- [@../foo-youtube/README.md](../foo-youtube/README.md) — sibling plugin (YouTube transcripts)
