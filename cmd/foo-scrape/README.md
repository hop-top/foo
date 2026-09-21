# foo-scrape

External plugin binary for the [foo](../../README.md) CLI:
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
walkthrough, see [Use plugins](../../docs/how-to/use-plugins.md).

## CLI

```
foo-scrape [flags] <url>
```

`<url>` is required — exactly one positional argument.

| Flag | Default | Description |
|------|---------|-------------|
| `--mode` | `readability` | `readability` (main article content) or `raw` (full HTML) |
| `--quiet` | `false` | Suppress progress output on stderr |
| `--format` | `table` | Output format for kit-rendered output (`csv`, `human`, `json`, `table`, `text`, `yaml`) |
| `--ext-info` | — | Print discovery JSON and exit (host-facing probe) |

Flags are parsed by this binary, not by foo — the host forwards argv
verbatim. See [Write plugins](../../docs/how-to/write-plugins.md)
for the argv-passthrough contract.

## Output

Markdown to stdout. `--mode readability` extracts the main article and
drops navigation, scripts, and chrome; `--mode raw` converts the full
HTML document.

Diagnostics and progress go to stderr; data goes to stdout. This
markdown-on-stdout convention is what foo's stdin reader treats as the
user prompt — see
[Concepts](../../docs/concepts.md#plugins-via-path-discovery).

## Progress on stderr

The scrape reports progress on **stderr** while **stdout** stays
reserved for the markdown payload. That split is what makes the pipe
work: `foo scrape <url> | foo -p extract_wisdom` shows progress in the
terminal while only markdown reaches the downstream prompt.

Human lines (the default):

```
[fetch] https://example.com
[fetch] https://example.com (fetched) (0.5 KiB)
[done] https://example.com in 144ms, ~47 tokens (0.2 KiB) ok
```

| Phase | Meaning |
|-------|---------|
| `fetch` | Work started, or completed against the network (`source: fetched`) |
| `cache` | Payload served from the cache (`source: cache`) |
| `done` | Terminal event: elapsed time, markdown size, estimated tokens |

The `cache` phase appears only when caching is enabled (see below); with
caching off every run reports `(fetched)`.

`--format json` emits the same sequence as JSONL on stderr, with
`phase`, `item`, `bytes` and an `extra` object carrying `source` on the
fetch/cache events and `elapsed` / `est_tokens` on `done`:

```json
{"phase":"fetch","at":"...","item":"https://example.com"}
{"phase":"fetch","at":"...","item":"https://example.com (fetched)","bytes":559,"extra":{"source":"fetched"}}
{"phase":"done","at":"...","item":"https://example.com in 219ms, ~47 tokens","bytes":188,"ok":true,"extra":{"elapsed":"219ms","est_tokens":47}}
```

`--quiet` silences stderr entirely (zero bytes), leaving stdout
untouched. The token figure is a four-characters-per-token estimate for
operator telemetry — never an input to billing or truncation.

The event vocabulary (phase names, `extra` keys, `source` values) is
identical to [foo-youtube](../foo-youtube/README.md)'s, so one consumer
reads both sidecars' JSONL.

## Discovery

The host runs `foo-scrape --ext-info` and JSON-decodes stdout. The
contract: emit only the JSON object, exit 0. It is intercepted before
flag parsing, so discovery never depends on argument validation.

```json
{"name":"scrape","version":"dev","description":"URL to markdown conversion with readability","capabilities":["discover"]}
```

`version` is stamped at build time via ldflags (`dev` in source builds).
The full discovery/dispatch mechanism lives in
[Use plugins](../../docs/how-to/use-plugins.md#how-it-works).

## Caching

Caching is **opt-in**: with no cache environment set, every scrape goes
to the network. Set `FOO_SCRAPE_CACHE` to a writable db path and fetches
go through a caching `http.RoundTripper`
(`hop.top/kit/go/storage/httpcache`) backed by a sqlite `kv` store, so
repeated scrapes of the same URL skip the network.

| Setting | Default | Effect |
|---------|---------|--------|
| `FOO_SCRAPE_CACHE` | — (unset: caching off) | Cache db **path** (file); enables caching |
| `FOO_SCRAPE_CACHE_TTL` | `24h` | Freshness window (Go duration) |

```sh
FOO_SCRAPE_CACHE=~/.cache/foo-scrape/cache.db foo scrape "https://example.com"
```

Caching is best-effort: a store-open failure falls back to a direct
fetch and never fails a scrape. There is no per-run bypass flag — unset
`FOO_SCRAPE_CACHE` for a guaranteed fresh pull.

## Exit codes

Follows the kit cross-tool convention (§8.1):

| Code | Meaning |
|------|---------|
| `0` | Success |
| `1` | Fetch failure (network error, non-200 response) |
| `2` | Usage error (missing/extra argument, invalid `--mode`, bad flags) |

A non-zero exit produces no stdout, which downstream surfaces as
`no prompt provided (stdin was empty)` —
see [Troubleshooting](../../docs/troubleshooting.md#empty-pipe-into-foo).

## Events

When peers are configured, a capture event publishes onto the kit bus:

| Mutation | Topic |
|----------|-------|
| page scraped | `foo-scrape.capture.page.scraped` |

Peers come from `FOO_SCRAPE_BUS_PEERS` (comma-separated `ws://` URLs);
unset means events stay in-process. Auth token from `FOO_BUS_TOKEN` /
`BUS_TOKEN` when present. Topic notation:
[Event topics](../../docs/reference/event-topics.md).

## See also

- [Use plugins](../../docs/how-to/use-plugins.md) — install, verify, pipe
- [Write plugins](../../docs/how-to/write-plugins.md) — plugin contract
- [Concepts](../../docs/concepts.md#plugins-via-path-discovery) — PATH-discovery model
- [Troubleshooting](../../docs/troubleshooting.md) — pipeline failures
- [foo-youtube](../foo-youtube/README.md) — sibling plugin (YouTube transcripts)
