# Manage fragments

Create, list, show, and delete reusable prompt fragments stored in
your workspace.

## Use this when

- You have a snippet you want to attach to multiple prompts —
  persona, constraints, glossary, style guide.
- You want to keep that snippet versioned in the workspace rather
  than re-typed.
- You want to attach the snippet to either the user or system role.

## Before you begin

You need:

- A workspace store that foo can write to (WSM initializes one on
  first use under `$XDG_STATE_HOME`).
- A name (alias) for the fragment and either an inline source
  (file path, URL) or content piped on stdin.

## Outcome

After this guide you will be able to:

- Save a fragment from a file, URL, or stdin.
- Attach it to a prompt via `-f` (user role) or
  `--system-fragment` (system role).
- Inspect and delete fragments.

## Quick path

```sh
# From stdin
echo "Keep answers under 200 words." | foo fragment create constraints

# From a file
foo fragment create persona ./prompts/senior-go-reviewer.md

# Attach
foo -f constraints -f persona "review this diff"
```

## Steps

### 1. Create a fragment

Three input sources are accepted:

```sh
# File on disk
foo fragment create persona ./prompts/persona.md

# URL
foo fragment create style-guide https://example.com/style.md

# Stdin (no source argument; stdin must not be a TTY)
echo "Reply in bullet points." | foo fragment create bullets
```

Expected: `fragment "<alias>" saved`.

### 2. List fragments

```sh
foo fragment list
```

Expected: a table with `ALIAS`, `SOURCE`, and a short `ARTIFACT`
id (the workspace-store artifact identifier).

### 3. Show one fragment

```sh
foo fragment show persona
```

Expected: the resolved content of the fragment.

### 4. Attach a fragment to a prompt

`-f` attaches to the user prompt; `--system-fragment` attaches to
the system prompt. Both accept multiple values.

```sh
# User-role attachment
foo -f constraints "summarize this PR"

# System-role attachment
foo --system-fragment persona "review this code"
```

Multiple `-f` values are concatenated and appended to your prompt
text with a `---` separator.

### 5. Delete a fragment

```sh
foo fragment delete bullets --confirm=yes
# fragment "bullets" deleted
```

`delete` is destructive — see
[confirm-destructive-ops.md](confirm-destructive-ops.md) for the
confirm policy.

## Common issues

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `no source provided and stdin is a terminal` | Ran `fragment create` with no source and a TTY stdin | Pipe content or pass a source argument |
| `init workspace: ...` | WSM cannot write its store | Check `$XDG_STATE_HOME` perms |
| `fragment ... not found` | Wrong alias | Run `foo fragment list` |

## How it works

A fragment alias points to an artifact in the workspace store. The
content is fetched lazily when the alias is resolved. Sources
(file paths or URLs) are recorded as metadata so `fragment list`
can show provenance.

`-f` injects the resolved content into the user message; foo
joins it to the user prompt with a `---` separator.
`--system-fragment` injects it into the system prompt instead, so
the model treats it as instruction rather than data.

## Options

| Flag | Default | Purpose |
|------|---------|---------|
| `-f, --fragment` | (none) | Attach fragment(s) to the user prompt |
| `--system-fragment` | (none) | Attach fragment(s) to the system prompt |

## Related docs

- [Manage patterns](manage-patterns.md) — full named system prompts.
- [Concepts: patterns vs fragments](../concepts.md#patterns-strategies-fragments-schemas) — when to pick which.
- [Reference: commands](../reference/commands.md#fragment) — subcommand list.
