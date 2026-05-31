# Manage schemas

Save, list, show, compile, and delete named JSON schemas for
structured-output prompts.

## Use this when

- You ask the model for structured JSON often and want to give
  the contract a short name.
- You want to preview what a DSL string compiles to before
  storing it.
- You want to ship a JSON Schema file and store it as-is.

## Before you begin

You need:

- Write access to `$XDG_STATE_HOME/foo/`.
- A schema either as a DSL shorthand (see
  [reference/schema-dsl.md](../reference/schema-dsl.md)) or as a
  JSON Schema file.

## Outcome

After this guide you will be able to:

- Save a schema from DSL or from a JSON file.
- Reference it from `--schema` or `--schema-multi`.
- Inspect, list, compile, and delete schemas.

## Quick path

```sh
# Save from DSL
foo schema create person "name, age int, email"

# Apply
foo --schema person "extract: Jane Doe, 30, jane@x.com"
```

## Steps

### 1. Save a schema from DSL

```sh
foo schema create person "name, age int, email"
# schema "person" saved
```

### 2. Save a schema from a JSON Schema file

```sh
foo schema create event --file ./schemas/event.json
# schema "event" saved from ./schemas/event.json
```

The file contents are stored verbatim, with no DSL compilation.

### 3. Preview a DSL string without saving

```sh
foo schema compile "title, tags []str, score float"
```

Expected: the compiled JSON Schema in table/JSON/YAML form
according to `--format`.

### 4. List stored schemas

```sh
foo schema list
```

### 5. Show one schema (DSL + compiled JSON)

```sh
foo schema show person
```

### 6. Use a stored schema

```sh
foo --schema person "extract: Jane, 30, jane@x.com"
foo --schema-multi person "extract all people from this text"
```

`--schema` requests one object; `--schema-multi` requests a JSON
array where each element matches the schema.

### 7. Delete a schema

```sh
foo schema delete person --confirm=yes
# schema "person" deleted
```

`delete` is destructive — see
[confirm-destructive-ops.md](confirm-destructive-ops.md).

## Common issues

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `schema not found and not valid DSL` | Value passed to `--schema` is neither a saved name nor parseable DSL | Run `foo schema list`, or fix DSL syntax |
| `provide DSL string or --file` | `schema create <name>` with no DSL and no `--file` | Pass a DSL body or `--file <path>` |
| `read file: ...` | `--file` path missing | Check the path |

## How it works

`schema create <name> <dsl>` compiles the DSL to JSON Schema and
stores both alongside the name. `schema create <name> --file path`
stores the file content verbatim. Either way the persisted
schema is what gets appended to your prompt when you reference it
by name.

`--schema name` looks up the name first; if not found it falls
back to compiling the value as DSL. That is why a typo can
manifest as a DSL parse error rather than a "not found" error.

Schemas live in `$XDG_STATE_HOME/foo/schemas.db` (SQLite).

## Options

| Subcommand | Args | Notes |
|------------|------|-------|
| `list` | (none) | Names only |
| `show` | `<name>` | DSL + compiled Schema |
| `create` | `<name> [dsl]` or `<name> --file <path>` | Inline DSL or JSON Schema file |
| `compile` | `<shorthand>` | DSL → JSON Schema, no persistence |
| `delete` | `<name>` | Remove |

## Related docs

- [Reference: schema DSL grammar](../reference/schema-dsl.md) — type table and rules.
- [Quickstart](../quickstart.md) — schema DSL used inline.
- [Reference: commands](../reference/commands.md#schema) — full surface.
