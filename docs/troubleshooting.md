# Troubleshooting

Recover from common foo failure modes. Each entry is a symptom →
cause → fix; the longer sections below give the detail.

## Use this when

- A foo command failed and you want a fast diagnosis.
- A command silently produced no output.
- A script broke after switching to `--confirm=auto` defaults.

## Symptom → cause → fix

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `Error creating LLM client` | API key missing or invalid | [Set an API key](#api-key-missing-or-invalid) |
| `pattern ... not found` | Wrong name / wrong scope | `foo pattern list`; check scope |
| `fragment ... not found` | Wrong alias | `foo fragment list` |
| `schema not found and not valid DSL` | `--schema` value is neither saved nor valid DSL | [Fix DSL parse failures](#schema-dsl-parse-failure) |
| `interactive shell requires a terminal` | `foo` invoked with no args and no TTY | [Provide a prompt or run on a TTY](#interactive-shell-launched-without-tty) |
| `UNAUTHORIZED` from a `delete` command | Destructive command refused off-TTY | [Use `--confirm=yes`](#destructive-command-refused-with-unauthorized) |
| Empty output or visible garbled bytes | Streaming hiccup | [Disable streaming](#streaming-garbled-or-truncated) |
| `embed: ... 401 Unauthorized` | `OPENAI_API_KEY` missing | Export it |
| `--file is required` | `foo embed file` missing `--file` | Add `--file <path>` |
| `foo status` shows degraded | One or more subsystems missing keys/state | [Read the status surface](#foo-status-shows-degraded-health) |
| `no source provided and stdin is a terminal` | `foo fragment create <alias>` with no source on a TTY | Pipe content or pass a file/URL |

## API key missing or invalid

`Error creating LLM client` is foo telling you it could not build
a provider client. The two common causes:

- No key in env for the selected model's provider.
- A key is set but rejected by the provider.

Fix:

```sh
export ANTHROPIC_API_KEY=sk-ant-...
# or
export OPENAI_API_KEY=sk-...

foo provider show anthropic   # status should be "configured"
foo "hello"
```

`foo provider list` shows registered schemes; `foo provider show
<scheme>` shows whether the expected key is visible to foo. See
[how-to/configure-models.md](how-to/configure-models.md).

## Pattern / fragment / schema not found

foo uses three separate stores. Mixing up which store holds what
gives the same "not found" error from different commands.

- Patterns live as `system.md` files under
  `$XDG_CONFIG_HOME/foo/patterns/<name>/` (or `.foo/patterns/`).
- Fragments live in the WSM workspace store.
- Schemas live in `$XDG_STATE_HOME/foo/schemas.db`.

Run the matching `list` subcommand to confirm what's actually
stored:

```sh
foo pattern list
foo fragment list
foo schema list
```

A name shown by one list does not exist in another.

## interactive shell launched without TTY

Running plain `foo` with no positional argument and no piped
stdin attempts to open the interactive shell, which requires a
terminal.

If you got `interactive shell requires a terminal`, either:

- Supply a prompt: `foo "hello"`.
- Pipe a prompt: `echo hello | foo`.
- Run from a real terminal (not a non-TTY subprocess).

## Schema DSL parse failure

`schema not found and not valid DSL` means the value passed to
`--schema` or `--schema-multi` was not a stored schema name *and*
failed to parse as DSL.

Common DSL traps:

- Type names are exact: `int`, `float`, `bool`, `str`, `[]str`,
  `[]int`. `string` and `number` do not parse.
- Comma is the field separator. No trailing comma.
- All fields are required; there is no optional marker.

Preview a DSL string with:

```sh
foo schema compile "name, age int, tags []str"
```

Full grammar: [reference/schema-dsl.md](reference/schema-dsl.md).

## Streaming garbled or truncated

If output appears empty or as visible byte fragments, streaming
encountered an issue. To isolate:

```sh
foo --no-stream "your prompt"
```

If `--no-stream` works, the issue is in streaming transport
(provider, terminal, or pipe). Reasonable workarounds:

- Use `--no-stream` for the affected provider/model.
- Pipe to `cat` instead of `tee` if `tee` is buffering oddly.

## Destructive command refused with `UNAUTHORIZED`

foo's destructive commands consult kit's `--confirm` policy. Off
a TTY the default is `no`, which refuses. From a script, pass:

```sh
foo pattern delete old-pattern --confirm=yes
```

Full background:
[how-to/confirm-destructive-ops.md](how-to/confirm-destructive-ops.md).

## `foo status` shows degraded health

`foo status` is the kit-shipped health probe. It boots cleanly
even when the rest of foo is offline and reports the state of
profile, env, workspace, auth, and config.

A degraded entry usually means one of:

- Missing env var the active subsystem expects (e.g.
  `ANTHROPIC_API_KEY` for the Anthropic provider).
- Workspace store not yet initialized.
- Config file at a path foo cannot read.

Re-run with `--verbose` (`-V`) for the detail:

```sh
foo status -V
```

Address the specific entry and re-run; status is purely
informational and never mutates state.

## Related docs

- [How to: confirm destructive ops](how-to/confirm-destructive-ops.md)
- [How to: configure models](how-to/configure-models.md)
- [Reference: schema DSL](reference/schema-dsl.md)
- [Reference: config](reference/config.md)
