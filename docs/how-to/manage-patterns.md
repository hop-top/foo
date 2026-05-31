# Manage patterns

Create, list, show, import, and delete named system prompts.

## Use this when

- You repeatedly use the same role/instructions and want a short
  alias for it (e.g. `reviewer`, `summarize`, `extract_wisdom`).
- You want to share a pattern by checking it into a project
  directory.

## Before you begin

You need:

- Write access to `$XDG_CONFIG_HOME/foo/patterns/` (user-scoped),
  or to `.foo/patterns/` for a project-local override.

## Outcome

After this guide you will be able to:

- Save a pattern inline or from a file.
- Apply it with `-p <name>`.
- Inspect, list, and delete patterns.

## Quick path

```sh
# Inline
foo pattern create reviewer "You are a strict senior reviewer..."

# From a file
foo pattern import ./prompts/extract_wisdom.md extract_wisdom

# Apply
foo -p reviewer "review this code"
```

## Steps

### 1. Create a pattern inline

```sh
foo pattern create reviewer "You are a strict senior reviewer. Return numbered issues."
# pattern "reviewer" saved
```

### 2. Import a pattern from a file

```sh
foo pattern import ./prompts/extract_wisdom.md
# pattern imported
```

Pass an explicit alias if you want one other than the file
basename:

```sh
foo pattern import ./prompts/extract.md wisdom
```

### 3. List available patterns

```sh
foo pattern list
```

### 4. Show one pattern body

```sh
foo pattern show reviewer
```

### 5. Apply a pattern to a prompt

```sh
foo -p reviewer "review this code"
cat code.go | foo -p reviewer
```

### 6. Delete a pattern

```sh
foo pattern delete old-pattern --confirm=yes
# pattern "old-pattern" deleted
```

`delete` is destructive — see
[confirm-destructive-ops.md](confirm-destructive-ops.md).

## Common issues

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `pattern ... not found` | Wrong alias or wrong scope | Run `foo pattern list` |
| `pattern "X" saved` but `-p X` reports not found | Wrote to user scope, expected project scope, or vice versa | Check `patterns_path` config + `.foo/patterns/` |
| `permission denied` on create | XDG config dir not writable | Check `$XDG_CONFIG_HOME` |

## How it works

A pattern is a `system.md` file inside a per-name directory.
Default user scope: `$XDG_CONFIG_HOME/foo/patterns/<name>/system.md`.

When you set `patterns_path` in config (or `FOO_PATTERNS_PATH` in
env), foo reads from that path instead. A project-local
`.foo/patterns/` directory is the conventional override location
when checking shared patterns into a repository.

`-p <name>` loads the pattern body as the system prompt. Combined
with `--strategy` it is the strategy that wraps the pattern, not
the other way around.

## Options

| Subcommand | Args | Purpose |
|------------|------|---------|
| `list` | (none) | List names |
| `show` | `<name>` | Print body |
| `create` | `<name> [system-prompt]` | Save (body optional, empty allowed) |
| `import` | `<path> [name]` | Save from file |
| `delete` | `<name>` | Remove |

## Related docs

- [Use strategies](use-strategies.md) — wrap a pattern with reasoning scaffolding.
- [Manage fragments](manage-fragments.md) — smaller reusable snippets.
- [Reference: commands](../reference/commands.md#pattern) — full surface.
