# Schema DSL reference

Compact shorthand for JSON Schema, used by `--schema`,
`--schema-multi`, and `foo schema {create,compile,show}`.

## Grammar

```
schema  := field ("," field)*
field   := name (" " type)?
name    := identifier
type    := "str" | "int" | "float" | "bool"
         | "[]str" | "[]int"
```

- Fields are comma-separated.
- Whitespace around commas and types is optional.
- The default type when omitted is `str`.

## Type table

| DSL type | JSON Schema | Example DSL |
|----------|-------------|-------------|
| `str` (default) | `{"type": "string"}` | `name` or `name str` |
| `int` | `{"type": "integer"}` | `age int` |
| `float` | `{"type": "number"}` | `score float` |
| `bool` | `{"type": "boolean"}` | `active bool` |
| `[]str` | `{"type": "array", "items": {"type": "string"}}` | `tags []str` |
| `[]int` | `{"type": "array", "items": {"type": "integer"}}` | `scores []int` |

## Compile example

DSL:

```
name, age int, tags []str
```

Compiled JSON Schema:

```json
{
  "type": "object",
  "properties": {
    "name": {"type": "string"},
    "age":  {"type": "integer"},
    "tags": {"type": "array", "items": {"type": "string"}}
  },
  "required": ["name", "age", "tags"]
}
```

Preview interactively:

```sh
foo schema compile "name, age int, tags []str"
```

## Validation rules

- **All fields are required.** There is no optional marker.
- **Default type is `str`.** Omitting the type after a field name
  yields a string field.
- **Object-only.** The DSL describes a single object schema. For
  array-of-object output use `--schema-multi` (foo wraps the
  schema in an array contract at prompt-assembly time).
- **No nesting.** The DSL does not express nested objects or
  arrays of objects. For those, ship a JSON Schema file via
  `foo schema create <name> --file <path>`.
- **Unknown types fail at compile time.** `--schema "x foo"`
  errors before the model is called.

## Where DSL is accepted

- `--schema "<dsl>"` (inline, single-object output).
- `--schema-multi "<dsl>"` (inline, array output).
- `foo schema create <name> "<dsl>"` (persisted).
- `foo schema compile "<dsl>"` (preview only).

When `--schema` or `--schema-multi` is set, foo first looks up the
value as a saved schema name. If that lookup fails, foo tries to
compile the value as DSL. A typo therefore manifests as a DSL
parse error, not a "not found" error.

## Related docs

- [Manage schemas](../how-to/manage-schemas.md) — save, list, delete.
- [Quickstart](../quickstart.md) — DSL used inline for first structured output.
- [Troubleshooting: DSL parse failure](../troubleshooting.md#schema-dsl-parse-failure) — common traps.
