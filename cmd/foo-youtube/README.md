# foo-youtube

External plugin binary for the [foo](../../README.md) CLI: extracts a
YouTube video's **transcript** and **metadata** as markdown on stdout,
ready to pipe into an LLM prompt. Wraps `yt-dlp`. Imports zero foo
internal packages.

```sh
foo youtube "https://youtu.be/<id>" | foo -p summarize
```

## Install

```sh
go install hop.top/foo/cmd/foo-youtube@latest
```

Requires **`yt-dlp`** on `$PATH` ([install](https://github.com/yt-dlp/yt-dlp)).
Once the binary is on `$PATH`, foo discovers it as `foo youtube`. For the
end-to-end setup and verification walkthrough, see
[Use plugins](../../docs/how-to/use-plugins.md).

## CLI

```
foo-youtube [flags] <url|id>
```

`<url|id>` is required: a `youtube.com/watch`, `youtu.be`, or
`youtube.com/shorts` URL, **or** a bare 11-character video ID such as
`dQw4w9WgXcQ`. A bare ID is expanded to
`https://www.youtube.com/watch?v=<id>` before yt-dlp sees it, so the two
forms are interchangeable:

```sh
foo youtube dQw4w9WgXcQ | foo -p summarize
foo youtube "https://www.youtube.com/watch?v=dQw4w9WgXcQ" | foo -p summarize
```

An ID must be exactly eleven characters from `[A-Za-z0-9_-]`; anything
else that is not a supported URL is a usage error (exit 2).

| Flag | Default | Description |
|------|---------|-------------|
| `--metadata` | `true` | Include video metadata (title, channel, date, duration, views, likes) |
| `--no-metadata` | — | Skip metadata |
| `--transcript` | `true` | Extract transcript |
| `--no-transcript` | — | Skip transcript |
| `--timestamps` | `false` | Prefix transcript lines with timestamps |
| `--comments` | `false` | Include up to 20 top comments |
| `--no-cache` | `false` | Bypass the yt-dlp output cache for this run |
| `-v`, `--debug` | `false` | Pass yt-dlp's raw stderr through for diagnosis |
| `--quiet` | `false` | Suppress progress output on stderr |
| `--format` | `table` | Output format for kit-rendered output (`csv`, `human`, `json`, `table`, `text`, `yaml`) |
| `--ext-info` | — | Print discovery JSON and exit (host-facing probe; hidden from `--help`) |

Flags are parsed by this binary, not by foo — the host forwards argv
verbatim. See [Write plugins](../../docs/how-to/write-plugins.md)
for the argv-passthrough contract.

## Output

Markdown to stdout. Sections appear in fixed order, gated by flags:

```markdown
# Video Title

## Metadata
- **Channel:** ...
- **Published:** YYYY-MM-DD
- **Duration:** ...
- **Views:** ...
- **Likes:** ...

## Transcript
[text, optionally `[m:ss]`-prefixed with --timestamps]

## Comments
- **Author:** comment
```

Diagnostics and progress go to stderr; data goes to stdout. This
markdown-on-stdout convention is what foo's stdin reader treats as the
user prompt — see
[Concepts](../../docs/concepts.md#plugins-via-path-discovery).

## Progress on stderr

The extraction reports progress on **stderr** while **stdout** stays
reserved for the markdown payload. That split is what makes the pipe
work: `foo youtube <url> | foo -p summarize` shows progress in the
terminal while only markdown reaches the downstream prompt.

Human lines (the default):

```
[fetch] https://www.youtube.com/watch?v=dQw4w9WgXcQ
[fetch] https://www.youtube.com/watch?v=dQw4w9WgXcQ (fetched) (643.3 KiB)
[done] https://www.youtube.com/watch?v=dQw4w9WgXcQ in 3.701s, ~52 tokens (0.2 KiB) ok
```

A warm run reports `[cache] <url> (cache)` in place of `(fetched)`. One
`fetch`/`cache` pair is emitted per `yt-dlp` invocation, so a run that
pulls metadata, transcript and comments reports each separately before
the single terminal `[done]`.

| Phase | Meaning |
|-------|---------|
| `fetch` | Work started, or completed against the network (`source: fetched`) |
| `cache` | Payload served from the cache (`source: cache`) |
| `done` | Terminal event: elapsed time, markdown size, estimated tokens |

`--format json` emits the same sequence as JSONL on stderr, with
`phase`, `item`, `bytes` and an `extra` object carrying `source` on the
fetch/cache events and `elapsed` / `est_tokens` on `done`:

```json
{"phase":"fetch","at":"...","item":"https://www.youtube.com/watch?v=dQw4w9WgXcQ"}
{"phase":"fetch","at":"...","item":"... (fetched)","bytes":658739,"extra":{"source":"fetched"}}
{"phase":"done","at":"...","item":"... in 3.701s, ~52 tokens","bytes":188,"ok":true,"extra":{"elapsed":"3.701s","est_tokens":52}}
```

`--quiet` silences stderr entirely (zero bytes), leaving stdout
untouched. The token figure is a four-characters-per-token estimate for
operator telemetry — never an input to billing or truncation.

`yt-dlp`'s own stderr is suppressed by default so its progress bars
never interleave with these lines; `-v`/`--debug` restores the raw
passthrough for diagnosis.

## Discovery

The host runs `foo-youtube --ext-info` and JSON-decodes stdout. The
contract: emit only the JSON object, exit 0. It is intercepted before
flag parsing, so discovery never depends on argument validation.

```json
{
  "name": "youtube",
  "version": "dev",
  "description": "YouTube transcript and metadata extraction",
  "capabilities": ["discover"]
}
```

`version` is stamped at build time via ldflags (`dev` in source builds).
The flag is hidden from `--help` because it is a host-facing probe, not
a user verb. The full discovery/dispatch mechanism lives in
[Use plugins](../../docs/how-to/use-plugins.md#how-it-works).

## Exit codes

Follows the kit cross-tool convention (§8.1):

| Code | Meaning |
|------|---------|
| `0` | Success |
| `1` | Fetch failure (metadata/transcript against a valid request) |
| `2` | Usage error (missing/invalid URL or video ID, bad flags) |
| `5` | Missing dependency (`yt-dlp` not on `$PATH`) |

Comments are best-effort: a fetch failure there warns on stderr and
continues (does not fail the run). A non-zero exit produces no stdout,
which downstream surfaces as `no prompt provided (stdin was empty)` —
see [Troubleshooting](../../docs/troubleshooting.md#empty-pipe-into-foo).

A video with **no English captions** is a fetch failure (exit 1), not an
empty `## Transcript` section:

```
$ foo youtube "https://youtu.be/<no-captions-id>"
GENERIC: fetching transcript: no English transcript available for this video
```

`yt-dlp` exits 0 for a captionless video, so the absence has to be
detected here. Reporting it as success would emit a document with
nothing in it — which downstream reads as an empty prompt rather than as
a missing transcript. Pass `--no-transcript` to extract metadata alone
from such a video.

## Caching

Raw `yt-dlp` output is cached **on by default**, keyed by a hash of the
full invocation argv — so metadata, transcript, and comments are cached
independently and repeated extractions of the same video skip the
subprocess and network. Backed by a sqlite `kv` store from
`hop.top/kit/go/storage/kv`.

| Setting | Default | Effect |
|---------|---------|--------|
| `--no-cache` | off | Bypass the cache for a single run |
| `FOO_YOUTUBE_CACHE` | — | Override the cache db **path** (file) |
| `FOO_YOUTUBE_CACHE_TTL` | `24h` | Freshness window (Go duration); `0` disables caching |
| `FOO_CACHE` | — | foo's own cache **directory**, inherited when `FOO_YOUTUBE_CACHE` is unset |
| `FOO_CACHE_TTL` | — | foo's own TTL, inherited when `FOO_YOUTUBE_CACHE_TTL` is unset |
| (default path) | `$XDG_CACHE_HOME/foo-youtube/ytdlp-cache.db` | when `FOO_YOUTUBE_CACHE` is unset |

Caching is best-effort: a path-resolve or store open/read/write failure
falls back to a live `yt-dlp` exec — it never fails a fetch. Use
`--no-cache` when a video changed and you need a fresh pull.

## Events

When peers are configured, capture events publish onto the kit bus:

| Mutation | Topic |
|----------|-------|
| metadata extracted | `foo-youtube.capture.metadata.fetched` |
| transcript extracted | `foo-youtube.capture.transcript.fetched` |

Peers come from `FOO_YOUTUBE_BUS_PEERS` (comma-separated `ws://` URLs);
unset means events stay in-process. Auth token from `FOO_BUS_TOKEN` /
`BUS_TOKEN` when present. Topic notation:
[Event topics](../../docs/reference/event-topics.md).

## See also

- [Use plugins](../../docs/how-to/use-plugins.md) — install, verify, pipe
- [Write plugins](../../docs/how-to/write-plugins.md) — plugin contract
- [Concepts](../../docs/concepts.md#plugins-via-path-discovery) — PATH-discovery model
- [Troubleshooting](../../docs/troubleshooting.md) — pipeline failures
- [foo-scrape](../foo-scrape/README.md) — sibling plugin (URL to markdown)
