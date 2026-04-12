# foo-youtube Plugin Spec

## Overview

External binary plugin for YouTube transcript/metadata extraction.
Discoverable via kit/ext PATH prefix scan (`foo-` prefix).
Wraps `yt-dlp` for transcript extraction.

## Discovery

- Binary name: `foo-youtube`
- Discovery: `discover.Scanner{Prefix: "foo-"}` scans `$PATH`
- Capability: `CapDiscover` (external binary)
- Interrogation: `foo-youtube --ext-info` returns JSON metadata

### --ext-info Response

```json
{
  "name": "youtube",
  "version": "0.1.0",
  "description": "YouTube transcript and metadata extraction",
  "capabilities": ["discover"]
}
```

## CLI Interface

```
foo-youtube [flags] <youtube-url>
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--transcript` | `true` | Extract video transcript |
| `--timestamps` | `false` | Include timestamps in transcript |
| `--comments` | `false` | Include top-level comments |
| `--metadata` | `false` | Include video metadata (title, channel, date, etc.) |
| `--ext-info` | — | Print extension JSON metadata and exit |
| `--help` | — | Print usage |

### Output

Markdown to stdout. Sections appear in order based on flags:

```markdown
# Video Title

## Metadata
- **Channel:** ...
- **Published:** ...
- **Duration:** ...
- **Views:** ...

## Transcript
[transcript text, optionally with timestamps]

## Comments
- comment 1
- comment 2
```

## Dependencies

- `yt-dlp` must be in `$PATH`
- No runtime dependency on foo core

## Composition

Pipeable into foo for LLM processing:

```sh
foo-youtube "https://youtube.com/watch?v=xxx" | foo -p summarize
foo-youtube --comments "https://..." | foo -p analyze_sentiment
foo-youtube --metadata --transcript "https://..." | foo -p extract_wisdom
```

## Error Handling

- Missing `yt-dlp`: exit 1 + stderr message
- Invalid URL: exit 1 + stderr message
- Network failure: exit 1 + stderr message
- Private/unavailable video: exit 1 + stderr message
- No transcript available: exit 0 + stderr warning, skip section

## Build

- Language: Go
- Single binary, zero foo-core imports
- Install: `go install` or copy to `$PATH`
