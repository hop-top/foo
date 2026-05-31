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
| `pattern ... not found` | Wrong alias, or pattern is project-local in a different cwd | Run `foo pattern list`; see [Scope and cwd](#scope-and-cwd) |
| `pattern "X" saved` but `-p X` reports not found from elsewhere | Pattern in `.foo/patterns/` is cwd-bound | Import it: `foo pattern import .foo/patterns/X/system.md X` |
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

### Scope and cwd

`-p <name>` resolves in this order:

1. `./.foo/patterns/<name>/system.md` (project-local, cwd-relative)
2. `<patterns_path>/<name>/system.md` (user-global; defaults to
   `$XDG_CONFIG_HOME/foo/patterns/<name>/system.md`)

`foo pattern list` checks both locations and de-duplicates by
name. **A pattern is only usable from a directory where at least
one of the two lookups resolves.** This is the cause of the most
common pattern surprise: you create a pattern while in a project
directory (so it lands in `.foo/patterns/`), then try to use it
from somewhere else and get `pattern "<name>" not found`.

To make a project-local pattern available everywhere, import it
into the user store:

```sh
foo pattern import .foo/patterns/<name>/system.md <name>
# pattern imported
```

`foo pattern create` always writes to the user store. `foo pattern
import` does the same. Neither writes into `.foo/patterns/` —
checking a project-local pattern into a repo is done by editing
files directly (not via `pattern create`).

## Re-use fabric patterns

Foo's pattern format (`<patterns_path>/<name>/system.md`) is the
same on-disk shape as
[fabric](https://github.com/danielmiessler/fabric)'s pattern
library. Fabric ships ~239 curated patterns (`extract_wisdom`,
`summarize`, `analyze_paper`, etc.); you can use any of them with
foo without conversion.

If fabric is already installed, its patterns live at
`~/.config/fabric/patterns/`. You can either point foo at them
directly or copy individual patterns into foo's store.

### Option 1: point foo at fabric's directory

Set `FOO_PATTERNS_PATH` to fabric's pattern root for one
invocation:

```sh
FOO_PATTERNS_PATH=~/.config/fabric/patterns \
  foo -p extract_wisdom "https://example.com/article-text"
```

For a persistent override, add it to foo's config:

```sh
# ~/.config/foo/config.yaml
patterns_path: ~/.config/fabric/patterns
```

Trade-off: foo's own user-store patterns at
`~/.config/foo/patterns/` become invisible until you switch back.
`.foo/patterns/` (project-local) still wins regardless because
foo checks it first.

### Option 2: import selected patterns into foo's store

If you want a mixed store — your patterns plus a few fabric ones
— import what you need:

```sh
# One pattern
foo pattern import ~/.config/fabric/patterns/extract_wisdom/system.md extract_wisdom

# Bulk import (every fabric pattern)
for d in ~/.config/fabric/patterns/*/; do
  name=$(basename "$d")
  foo pattern import "$d/system.md" "$name"
done
```

Patterns land in `$XDG_CONFIG_HOME/foo/patterns/<name>/system.md`
and are visible from any cwd. Re-importing a pattern overwrites
the prior version, so the loop is idempotent.

### Option 3: clone fabric's patterns without fabric

If you don't have fabric installed, the patterns repo can be
shallow-cloned and imported the same way:

```sh
git clone --depth 1 https://github.com/danielmiessler/fabric.git /tmp/fabric
for d in /tmp/fabric/patterns/*/; do
  name=$(basename "$d")
  foo pattern import "$d/system.md" "$name"
done
rm -rf /tmp/fabric
```

Fabric's patterns are MIT-licensed; check fabric's repo for the
current license before redistributing.

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
