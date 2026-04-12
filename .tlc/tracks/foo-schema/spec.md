# foo-schema — Named JSON Schemas Plugin

## Overview

Init()-registered extension (`CapRegistry`) compiled into foo binary.
Named JSON schemas for structured LLM output. Includes a DSL
shorthand that compiles to full JSON Schema. Storage via
`workspace.StateStore`.

## Extension Registration

```
package schema

func init() {
    registry.Register(&Schema{})
}

// Schema implements ext.Extension
// Meta() → {Name: "schema", Version: "0.1.0", ...}
// Capabilities() → ext.CapRegistry
// Init(ctx) → loads schema index from StateStore
// Close() → no-op
```

## Schema DSL

Shorthand string → JSON Schema compiler.

### Syntax

```
"field1, field2 type, field3 type, ..."
```

- Bare name → `{"type": "string"}` (default)
- `name type` → mapped type
- Supported types: `str`, `int`, `float`, `bool`, `[]str`, `[]int`

### Examples

```
"name, age int, email"
→ {
    "type": "object",
    "properties": {
      "name":  {"type": "string"},
      "age":   {"type": "integer"},
      "email": {"type": "string"}
    },
    "required": ["name", "age", "email"]
  }

"title, tags []str, score float"
→ {
    "type": "object",
    "properties": {
      "title": {"type": "string"},
      "tags":  {"type": "array", "items": {"type": "string"}},
      "score": {"type": "number"}
    },
    "required": ["title", "tags", "score"]
  }
```

### Type mapping

| DSL | JSON Schema |
|-----|-------------|
| (bare) | `{"type": "string"}` |
| `str` | `{"type": "string"}` |
| `int` | `{"type": "integer"}` |
| `float` | `{"type": "number"}` |
| `bool` | `{"type": "boolean"}` |
| `[]str` | `{"type": "array", "items": {"type": "string"}}` |
| `[]int` | `{"type": "array", "items": {"type": "integer"}}` |

## Storage Model

StateStore key prefix: `schema:<name>`. Value = JSON:

```json
{
  "name": "<schema-name>",
  "dsl": "<original shorthand or empty>",
  "schema": { ... },
  "created_at": "<rfc3339>",
  "updated_at": "<rfc3339>"
}
```

Schemas can be created from DSL or raw JSON Schema. If created
from DSL, both `dsl` and compiled `schema` are persisted.

## CLI Surface

```
foo schema list                    # tabular: name, field count
foo schema show <name>             # print JSON Schema to stdout
foo schema set <name> <dsl>        # compile DSL, store as named
foo schema set <name> --file s.json  # store raw JSON Schema
foo schema remove <name>           # delete schema
foo schema dsl <shorthand>         # compile + print (no save)
```

## Prompt Usage

### Single object (`--schema`)

```
foo --schema person "extract person info from: John, 30, j@x.com"
```

Sets LLM `response_format` to JSON Schema mode. Response parsed
and validated against schema before output.

### Array output (`--schema-multi`)

```
foo --schema-multi person "extract all people from this text"
```

Wraps schema in `{"type": "array", "items": <schema>}`.
Response expected as JSON array.

### Resolution

1. Parse `--schema` / `--schema-multi` flag value as schema name
2. Lookup `schema:<name>` in StateStore
3. Extract `schema` field → pass to LLM as `response_format`
4. Fail fast if schema name unresolvable

## Dependencies

- `hop.top/kit/ext` — Extension interface, CapRegistry
- `hop.top/kit/ext/registry` — init()-based plugin registry
- `workspace.StateStore` — schema persistence
- **kit/llm `response_format` support** — required for structured
  output; not yet implemented. This plugin's prompt-usage features
  are blocked until kit/llm exposes `response_format` in its
  request builder. CLI management commands (list/show/set/dsl)
  can ship independently.

## Open Questions

- Nested object support in DSL? e.g. `address.city, address.zip`
- Optional fields syntax? e.g. `name, age? int`
- Schema composition/inheritance? e.g. `--schema base+extension`
