# Embed content

Add text or files to a local vector store so you can search them
later with `foo embed search`.

## Use this when

- You have notes, docs, or transcripts you want to query semantically.
- You want a local index over a set of files, with no cloud
  vector DB.
- You are building a retrieval step that feeds into a later `foo`
  invocation.

## Before you begin

You need:

- `OPENAI_API_KEY` exported. foo's embedder talks to the OpenAI
  embeddings API; an Anthropic-only setup will not work for this
  command. See [troubleshooting.md](../troubleshooting.md) for the
  exact error.
- Write access to `$XDG_STATE_HOME/foo/` (default
  `~/.local/state/foo/`).

## Outcome

After this guide, you will have:

- One or more named collections populated with text or file chunks.
- The ability to run `foo embed search` against those collections.

## Quick path

```sh
# Embed a snippet into the default collection
foo embed add "kit is the shared CLI library used by foo"

# Embed a file, chunked, into a named collection
foo embed file --file README.md --collection foo-docs
```

## Steps

### 1. Embed a literal string

```sh
foo embed add "foo plugins are PATH-discovered binaries named foo-*"
# embedded 01HZX... into "default"
```

The line emitted is the storage ID plus the destination collection.
The default collection name is `default`.

### 2. Embed into a named collection

Use `-c/--collection` to choose where the row goes:

```sh
foo embed add "anthropic provider uses ANTHROPIC_API_KEY" -c providers
# embedded 01HZY... into "providers"
```

A collection is created on first write; there is no separate
"create collection" step.

### 3. Embed a file as chunks

```sh
foo embed file --file ./docs/concepts.md --collection foo-docs
# embedded N chunks from ./docs/concepts.md into "foo-docs"
```

The file is split into chunks before embedding. Each chunk is
stored with `source` (the file path) and `chunk` (the `i/N`
counter) metadata so search results can be traced back.

### 4. Verify the collection exists

```sh
foo embed collection list
```

Expected: a table with one row per collection and its row count.

## Common issues

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `embed: ... 401 Unauthorized` | `OPENAI_API_KEY` missing or invalid | Export a valid key |
| `embed: ... no embedding API` | Provider does not expose one | Use OpenAI or an OpenAI-compatible endpoint |
| `--file is required` | Forgot `--file` on `foo embed file` | Add `--file <path>` |
| `state dir: ...` permission error | Cannot write XDG state dir | Check `$XDG_STATE_HOME` and dir perms |

## How it works

`foo embed add` calls the embedder once for the literal string and
inserts one row keyed by a fresh ULID. Re-running with the same
text mints a new row — the store does not deduplicate by content
hash automatically.

`foo embed file` reads the file, chunks it, calls the embedder
once per chunk, and inserts a row per chunk. Re-running with the
same file inserts a fresh batch (idempotency is declared
`conditional` precisely because of this).

Rows live in `$XDG_STATE_HOME/foo/embeddings.db` (SQLite). See
[concepts.md](../concepts.md#embeddings-a-local-semantic-index)
for the wider model.

## Options

| Flag | Applies to | Default | Purpose |
|------|------------|---------|---------|
| `-c, --collection` | `add`, `file` | `default` | Target collection |
| `--file` | `file` | (required) | File to chunk and embed |

## Related docs

- [Search semantically](search-semantically.md) — query what you just embedded.
- [Concepts: embeddings](../concepts.md#embeddings-a-local-semantic-index) — mental model.
- [Reference: commands](../reference/commands.md#embed) — full command surface.
