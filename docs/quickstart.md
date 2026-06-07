# Quickstart: foo in five minutes

A short, end-to-end first session. Run a prompt, save a pattern,
extract structured output, embed a file, and search it back.

## Use this when

- You have foo installed and an API key set.
- You want a guided walkthrough before reading any reference page.

## Before you begin

You need:

- foo installed (`go install hop.top/foo@latest`).
- `ANTHROPIC_API_KEY` or `OPENAI_API_KEY` exported in your shell.
- A terminal with stdin/stdout (no TTY needed for any step here).

## Outcome

By the end of this guide, you will have:

- Run a one-shot prompt and a piped prompt.
- Saved and applied a reusable system prompt (a pattern).
- Extracted structured JSON via the schema DSL.
- Embedded a file and run a semantic search against it.

## Steps

### 1. Send a one-shot prompt

```sh
foo "explain quicksort in two sentences"
```

Expected: a streamed model response on stdout, two sentences long.

### 2. Pipe stdin into a prompt

```sh
echo "func add(a, b int) int { return a - b }" | foo "find the bug"
```

Expected: a model response identifying the subtraction where addition
was intended.

### 3. Save a reusable pattern

Patterns are named system prompts. Save one called `reviewer`:

```sh
foo pattern create reviewer "You are a strict senior Go reviewer. Reply with a numbered list of issues."
# pattern "reviewer" saved
```

Apply it:

```sh
echo "func add(a, b int) int { return a - b }" | foo -p reviewer
```

Expected: a numbered list of issues from the model.

### 4. Extract structured JSON

Use the schema DSL inline:

```sh
foo --schema "name, age int, role" "extract from: Jane, 32, engineer"
```

Expected output (whitespace may differ):

```json
{"name":"Jane","age":32,"role":"engineer"}
```

The DSL is documented in
[reference/schema-dsl.md](reference/schema-dsl.md).

### 5. Embed a file

Embed the foo README into a named collection so you can search it:

```sh
foo embed file --file README.md --collection foo-docs
# embedded N chunks from README.md into "foo-docs"
```

### 6. Search the index

Ask a question against the collection you just built:

```sh
foo embed search "how do plugins work?" --collection foo-docs --count 3
```

Expected: a table with up to three rows, each showing an ID, a
similarity score, the source path, and a chunk indicator. The
top result should point at the README's "Plugins" section.

### 7. Verify the session worked

List your patterns and collections to confirm state persisted:

```sh
foo pattern list
foo embed collection list
```

Expected: `reviewer` in the pattern list; `foo-docs` in the
collection list with a non-zero row count.

## Common issues

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `Error creating LLM client` | No API key set | Export `ANTHROPIC_API_KEY` or `OPENAI_API_KEY` |
| `pattern ... not found` | Typo or pattern not saved | Run `foo pattern list` |
| `schema not found and not valid DSL` | Bad DSL syntax | See [reference/schema-dsl.md](reference/schema-dsl.md) |
| Empty output, no error | Streaming hiccup | Re-run with `--no-stream` |

Full table: [troubleshooting.md](troubleshooting.md).

## How it works

Each step in this guide stresses a different part of foo's prompt
assembly pipeline:

- Step 1–2 exercise the bare assembly path (positional + stdin).
- Step 3–4 layer named inputs (pattern, schema).
- Step 5–6 use the embeddings subsystem, which is a local index
  decoupled from the main LLM call path.

The conceptual overview is in [concepts.md](concepts.md).

## Next steps

You ran every command above against the default `balanced` pool
tier. Two things worth learning next:

- `foo --budget cheap "explain quicksort"` — same prompt routed
  through a cheaper model. Useful for bulk work and CI scripts.
- `foo --budget premium --schema "name, summary" "..."` — premium
  tier with structured output. The picker only considers entries
  that support JSON mode.

See [route across models](how-to/route-across-models.md) for the
full picker model, pool editing, and the `-m` explicit-pin
escape hatch.

## Related docs

- [Embed content](how-to/embed-content.md) — add text and files in detail.
- [Search semantically](how-to/search-semantically.md) — tune relevance and result count.
- [Manage patterns](how-to/manage-patterns.md) — author and share patterns.
- [Manage schemas](how-to/manage-schemas.md) — save and reuse JSON contracts.
- [Route across models](how-to/route-across-models.md) — `--budget`, pool, fallback, RouteLLM.
