# foo-fragment — Reusable Text Snippets Plugin

## Overview

Init()-registered extension (`CapRegistry`) compiled into foo binary.
Fragments are reusable text snippets attached to prompts via CLI
flags. Content-addressed storage via `workspace.ArtifactStore`
(SHA-256).

## Extension Registration

```
package fragment

func init() {
    registry.Register(&Fragment{})
}

// Fragment implements ext.Extension
// Meta() → {Name: "fragment", Version: "0.1.0", ...}
// Capabilities() → ext.CapRegistry
// Init(ctx) → loads alias index from StateStore
// Close() → no-op
```

## Fragment Sources

| Source | Resolution |
|--------|------------|
| Alias | Named ref → artifact hash lookup via StateStore |
| URL | HTTP fetch → cache as artifact; alias optional |
| File | Read path → store as artifact; alias optional |
| Stdin | Pipe input → SHA-256 hash → ephemeral artifact |

All sources converge to ArtifactStore. Alias→hash mapping lives
in StateStore under key prefix `fragment:alias:<name>`.

## Storage Model

- **Content**: `ArtifactStore.SaveArtifact(workspaceID, name, []byte)`
  returns `ArtifactData` with content-addressed hash (SHA-256).
- **Index**: `StateStore.SetState(sessionID, key, value)` where
  key = `fragment:alias:<alias>` and value = JSON:
  ```json
  {
    "artifact_id": "<sha256>",
    "source": "alias|url|file|stdin",
    "source_ref": "<original path/url/empty>",
    "created_at": "<rfc3339>"
  }
  ```
- **URL cache**: Fetched URLs stored as artifacts; TTL metadata
  in StateStore under `fragment:url:<sha256(url)>`.

## CLI Surface

```
foo fragment list              # tabular: alias, source, hash prefix
foo fragment set <alias> [src] # src = file path, URL, or stdin
foo fragment show <alias>      # print content to stdout
foo fragment remove <alias>    # delete alias mapping (artifact GC later)
```

### `foo fragment set` behavior

1. If `src` starts with `http(s)://` → fetch, store artifact, map alias
2. If `src` is a file path → read, store artifact, map alias
3. If no `src` and stdin is a pipe → read stdin, store artifact, map alias
4. Error if no source resolvable

## Prompt Composition

### User fragments (`-f`)

```
foo -f greeting -f disclaimer "summarize this"
```

Appends fragment content after user message, separated by `\n---\n`.
Order preserved. Duplicates deduplicated by artifact hash.

### System fragments (`--sf`)

```
foo --sf persona --sf constraints "summarize this"
```

Appends fragment content to system prompt. Same ordering/dedup
rules as `-f`.

### Resolution order

1. Parse flag values as alias names
2. Lookup alias → artifact_id via StateStore
3. Fetch content via `ArtifactStore.GetArtifact(artifact_id)`
4. Fail fast if any alias unresolvable (no silent skip)

## Dependencies

- `hop.top/kit/ext` — Extension interface, CapRegistry
- `hop.top/kit/ext/registry` — init()-based plugin registry
- `workspace.ArtifactStore` — content-addressed blob storage
- `workspace.StateStore` — alias→hash index persistence

## Open Questions

- Fragment versioning: track previous versions of same alias?
- Max fragment size limit (guard against accidental large files)?
- `foo fragment edit <alias>` — open in $EDITOR, re-store on save?
