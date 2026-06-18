# foo-youtube

External plugin binary for the [@../../README.md](../../README.md) CLI: extracts a
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
[@../../docs/how-to/use-plugins.md](../../docs/how-to/use-plugins.md).

## CLI

```
foo-youtube [flags] <url>
```

`<url>` is required: a `youtube.com/watch`, `youtu.be`, or
`youtube.com/shorts` URL.

| Flag | Default | Description |
|------|---------|-------------|
| `--metadata` | `true` | Include video metadata (title, channel, date, duration, views, likes) |
| `--no-metadata` | — | Skip metadata |
| `--transcript` | `true` | Extract transcript |
| `--no-transcript` | — | Skip transcript |
| `--timestamps` | `false` | Prefix transcript lines with timestamps |
| `--comments` | `false` | Include up to 20 top comments |
| `--ext-info` | — | Print discovery JSON and exit (host-facing; hidden) |

Flags are parsed by this binary, not by foo — the host forwards argv
verbatim. See [@../../docs/how-to/write-plugins.md](../../docs/how-to/write-plugins.md)
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

Diagnostics go to stderr; data goes to stdout. This markdown-on-stdout
convention is what foo's stdin reader treats as the user prompt — see
[@../../docs/concepts.md](../../docs/concepts.md#plugins-via-path-discovery).

## Discovery

The host runs `foo-youtube --ext-info` and JSON-decodes stdout. The
contract: emit only the JSON object, exit 0.

```json
{
  "name": "youtube",
  "version": "dev",
  "description": "YouTube transcript and metadata extraction",
  "capabilities": ["discover"]
}
```

`version` is stamped at build time via ldflags (`dev` in source builds).
The full discovery/dispatch mechanism lives in
[@../../docs/how-to/use-plugins.md](../../docs/how-to/use-plugins.md#how-it-works).

## Exit codes

Follows the kit cross-tool convention (§8.1):

| Code | Meaning |
|------|---------|
| `0` | Success |
| `1` | Fetch failure (metadata/transcript against a valid request) |
| `2` | Usage error (missing/invalid URL, bad flags) |
| `5` | Missing dependency (`yt-dlp` not on `$PATH`) |

Comments are best-effort: a fetch failure there warns on stderr and
continues (does not fail the run). A non-zero exit produces no stdout,
which downstream surfaces as `no prompt provided (stdin was empty)` —
see [@../../docs/troubleshooting.md](../../docs/troubleshooting.md#empty-pipe-into-foo).

## Events

When peers are configured, capture events publish onto the kit bus:

| Mutation | Topic |
|----------|-------|
| metadata extracted | `foo-youtube.capture.metadata.fetched` |
| transcript extracted | `foo-youtube.capture.transcript.fetched` |

Peers come from `FOO_YOUTUBE_BUS_PEERS` (comma-separated `ws://` URLs);
unset means events stay in-process. Auth token from `FOO_BUS_TOKEN` /
`BUS_TOKEN` when present. Topic notation:
[@../../docs/reference/event-topics.md](../../docs/reference/event-topics.md).

## See also

- [@../../docs/how-to/use-plugins.md](../../docs/how-to/use-plugins.md) — install, verify, pipe
- [@../../docs/how-to/write-plugins.md](../../docs/how-to/write-plugins.md) — plugin contract
- [@../../docs/concepts.md](../../docs/concepts.md#plugins-via-path-discovery) — PATH-discovery model
- [@../../docs/troubleshooting.md](../../docs/troubleshooting.md) — pipeline failures
