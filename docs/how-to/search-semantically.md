# Search content semantically

Find the most similar entries in a local embedding collection by
running a natural-language query through `foo embed search`.

## Use this when

- You have already populated a collection (see
  [embed-content.md](embed-content.md)).
- You want ranked matches against a free-text query, not exact
  substring matches.
- You are building a retrieval step that feeds another `foo`
  invocation.

## Before you begin

You need:

- `OPENAI_API_KEY` exported (the query is embedded the same way
  the stored content was).
- At least one row in the target collection — verify with
  `foo embed collection list`.

## Outcome

After this guide, you will have a ranked list of matches for a
query, scoped to a specific collection.

## Quick path

```sh
foo embed search "how do plugins work?" --collection foo-docs --count 3
```

## Steps

### 1. Run a search

```sh
foo embed search "kit config layering rules" -c foo-docs
```

Expected output (table format, default):

```text
ID            SCORE   SOURCE                 CHUNK
01HZ...       0.812   ./docs/concepts.md     3/8
01HZ...       0.764   ./docs/concepts.md     5/8
...
```

### 2. Tune the result count

`-n/--count` controls how many neighbors come back. Default 5.

```sh
foo embed search "destructive confirm policy" -c foo-docs -n 10
```

### 3. Switch output format for piping

The result is `output.Dispatch`-rendered, so kit's format flags
apply. JSON is useful when chaining into another foo call:

```sh
foo embed search "schema dsl grammar" -c foo-docs --format json
```

Expected: a JSON array; each element has `id`, `score`, `source`,
`chunk`.

## Common issues

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| Empty result table | Collection is empty or wrong name | Run `foo embed collection list` |
| Low scores everywhere | Query and content used different embedding contexts | Re-embed against a single model |
| `embed query: ... 401` | `OPENAI_API_KEY` missing | Export the key |

## How it works

Relevance is cosine similarity over the embedding vectors. foo
embeds the query string once, asks the store for the top-N closest
vectors in the chosen collection, and returns them ranked by
descending score. Scores are bounded in `[-1, 1]` in theory; in
practice values cluster around `0.6`–`0.85` for relevant chunks.

Search is a pure read — it never mutates the store. That is why
`foo embed search` is classified as `read` and is safe to script.

For the wider mental model see
[concepts.md](../concepts.md#embeddings-a-local-semantic-index).

## Options

| Flag | Default | Purpose |
|------|---------|---------|
| `-c, --collection` | `default` | Collection to search |
| `-n, --count` | `5` | Number of neighbors to return |
| `--format` (kit global) | `table` | `json`, `yaml`, `text`, etc. |

## Related docs

- [Embed content](embed-content.md) — populate a collection first.
- [Concepts: embeddings](../concepts.md#embeddings-a-local-semantic-index) — relevance scoring background.
- [Reference: commands](../reference/commands.md#embed) — every embed subcommand.
