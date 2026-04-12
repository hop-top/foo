# foo-scrape Plugin Spec

## Overview

External binary plugin for URL-to-markdown conversion.
Discoverable via kit/ext PATH prefix scan (`foo-` prefix).
Converts web pages to clean markdown with optional readability mode.

## Discovery

- Binary name: `foo-scrape`
- Discovery: `discover.Scanner{Prefix: "foo-"}` scans `$PATH`
- Capability: `CapDiscover` (external binary)
- Interrogation: `foo-scrape --ext-info` returns JSON metadata

### --ext-info Response

```json
{
  "name": "scrape",
  "version": "0.1.0",
  "description": "URL to markdown conversion with readability",
  "capabilities": ["discover"]
}
```

## CLI Interface

```
foo-scrape [flags] <url>
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--readability` | `true` | Clean text extraction (strips nav, ads, etc.) |
| `--raw` | `false` | Full HTML to markdown (preserves all content) |
| `--ext-info` | — | Print extension JSON metadata and exit |
| `--help` | — | Print usage |

`--readability` and `--raw` are mutually exclusive.
`--readability` is default when neither specified.

### Output

Markdown to stdout:

```markdown
# Page Title

[clean article content as markdown]
```

In `--raw` mode, full HTML structure converted to markdown
including nav, sidebars, footers.

## Dependencies

- No external binary dependencies
- Uses Go HTTP client + HTML parser internally
- No runtime dependency on foo core

## Composition

Pipeable into foo for LLM processing:

```sh
foo-scrape "https://example.com/article" | foo -p extract_wisdom
foo-scrape "https://docs.example.com" | foo -p summarize
foo-scrape --raw "https://example.com" | foo -p analyze
```

## Error Handling

- Invalid URL: exit 1 + stderr message
- Network failure / timeout: exit 1 + stderr message
- Non-HTML response: exit 1 + stderr message
- Empty body after readability: exit 0 + stderr warning

## Build

- Language: Go
- Single binary, zero foo-core imports
- Install: `go install` or copy to `$PATH`
