# Use strategies

Apply a meta-prompting wrapper (chain-of-thought, tree-of-thought,
ReAct, scratchpad) to a foo invocation.

## Use this when

- You want the model to reason step-by-step before answering.
- You want to A/B different reasoning patterns over the same
  pattern + prompt.
- You want to author a custom wrapper and reuse it.

## Before you begin

You need:

- A list of available strategies (`foo strategy list`).
- Optionally, a `.md` file under `$XDG_CONFIG_HOME/foo/strategies/`
  for custom wrappers.

## Outcome

After this guide you will be able to:

- List strategies.
- Apply one with `--strategy`.
- Author a custom strategy.

## Quick path

```sh
foo strategy list
foo --strategy cot -p reviewer "review this code"
```

## Steps

### 1. List available strategies

```sh
foo strategy list
```

Expected: a table with `NAME` and `DESCRIPTION` columns.

### 2. Apply a strategy

`-s`/`--strategy` wraps the assembled system prompt:

```sh
foo --strategy cot "solve: how many ways to color a 4-vertex graph with 3 colors?"
```

Combine with a pattern; the strategy wraps the pattern body:

```sh
foo -p reviewer --strategy cot "review this code"
```

### 3. Author a custom strategy

Drop a file at `$XDG_CONFIG_HOME/foo/strategies/<name>.md`. The
format is:

```
prefix text that goes BEFORE the existing system prompt
---
suffix text that goes AFTER the existing system prompt
```

Both halves are optional. `foo strategy list` picks up the new
strategy on next invocation.

### 4. Verify the wrap

Use `--dry-run` to confirm the assembled prompt without calling
the model:

```sh
foo --dry-run --strategy cot -p reviewer "review this code"
```

Expected: the system block prints the strategy prefix, then the
pattern body, then the strategy suffix.

## Common issues

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `strategy ... not found` | Typo or file not yet picked up | Run `foo strategy list`; verify file path |
| Strategy seems to have no effect | Pattern body overrides the framing | Test with `--dry-run` to see actual assembly |

## How it works

A strategy is a prefix/suffix decoration applied to the system
prompt at assembly time. It runs *after* `-p` resolves the
pattern, so the strategy wraps whatever the pattern produced.

Built-in strategies are compiled into foo (chain-of-thought,
tree-of-thought, ReAct, scratchpad — `foo strategy list` is the
authoritative list). Custom strategies live as plain `.md` files
in the user strategies dir and are concatenated with the
delimiter `---`.

## Options

| Flag | Default | Purpose |
|------|---------|---------|
| `-s, --strategy` | (none) | Strategy name |
| `--dry-run` | `false` | Print assembled prompt instead of calling model |

## Related docs

- [Manage patterns](manage-patterns.md) — system prompts that strategies wrap.
- [Concepts: strategies](../concepts.md#patterns-strategies-fragments-schemas) — how strategies fit the assembly pipeline.
- [Reference: commands](../reference/commands.md#strategy) — strategy subcommand surface.
