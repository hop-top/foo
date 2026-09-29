# Write a foo plugin

Author a new executable that foo discovers and dispatches. Two
integration scopes are available; pick the one that matches what
your plugin produces.

## Use this when

- You want to extend foo without forking it.
- You have output (markdown, JSON, text) that should compose with
  `foo -p <pattern>` downstream.
- You want the LLM to call your binary as a tool during chain-of-
  thought execution.

## Before you begin

You need:

- Any language that can produce an executable on `$PATH`. The two
  shipped plugins are in Go; nothing in foo requires Go for a
  third-party plugin.
- A clear answer to "is this output for the user or for the LLM?"
  — that decides which scope you pick.

## Outcome

After this guide you will:

- Know the two plugin scopes (`foo-<name>` and `foo-tool-<name>`)
  and when to use each.
- Have the `--ext-info` JSON contract.
- Be able to drop a `foo-<name>` binary on `$PATH` and see it in
  `foo --help` immediately.

## Two scopes at a glance

| Scope | Naming | Visible as | Output target |
|-------|--------|------------|---------------|
| Subcommand | `foo-<name>` | `foo <name>` in `--help` (PLUGINS group) | stdout, usually markdown, piped to downstream `foo` or saved |
| LLM tool | `foo-tool-<name>` | Tool call invoked by the LLM during `foo -T <name> ...` | JSON-on-stdout, consumed by the model |

A subcommand is for **user-facing** workflows (`foo youtube "..."
| foo -p summarize`). An LLM tool is **model-facing** — the model
decides when to call it, structured args go in, structured result
comes out.

## Quick path — subcommand plugin

```sh
# 1. Create the binary
cat > /tmp/foo-hello <<'EOF'
#!/bin/sh
if [ "$1" = "--ext-info" ]; then
  cat <<JSON
{"name":"hello","version":"0.1.0","description":"Print a greeting","capabilities":["discover"]}
JSON
  exit 0
fi
printf "# Hello\n\nFrom plugin: %s\n" "${1:-world}"
EOF
chmod +x /tmp/foo-hello

# 2. Put it on PATH
mv /tmp/foo-hello "$(go env GOPATH)/bin/"

# 3. Use it
foo --help | grep hello
foo hello "stranger" | foo -p summarize
```

## Subcommand plugin — full contract

A subcommand plugin is any executable on `$PATH` whose filename
starts with `foo-` but not `foo-tool-` (that prefix is reserved
for [LLM tool plugins](#llm-tool-plugin--foo-tool-name); a name
like `foo-toolbox` is still a subcommand). foo discovers it on
every `foo` invocation via `hop.top/kit/go/ai/ext/dispatch`,
registers it as a cobra subcommand under the PLUGINS group, and
forwards argv with `DisableFlagParsing: true` (you handle flags
yourself).

### Required: `--ext-info`

When invoked as `<binary> --ext-info`, the plugin must write a
single JSON object to stdout and exit 0:

```json
{
  "name": "youtube",
  "version": "0.4.0",
  "description": "YouTube transcript and metadata extraction",
  "capabilities": ["discover"]
}
```

Fields:

| Field | Required | Notes |
|-------|----------|-------|
| `name` | yes | The verb users will type after `foo`. Lowercase, no hyphens preferred. |
| `version` | yes | Semver-ish. Foo doesn't gate on it but shows it in metadata. |
| `description` | yes | Becomes both `Short` and `Long` in `foo --help`. Foo strict-validates the presence of `Long`, so an empty description means a generic placeholder. |
| `capabilities` | yes | Always include `"discover"` for subcommand plugins. |

Foo runs `--ext-info` once per `foo` invocation, at startup, to
fill in the help text, so plugins shouldn't do any heavy work in
this path. Plain JSON, exit 0.

### Argv passthrough

After dispatch, foo execs your binary with all remaining argv
intact. `foo youtube --timestamps "https://..."` arrives at
`foo-youtube` as `--timestamps https://...`. Plugin owns flag
parsing — use whatever idiom your language prefers.

### Output composes downstream

The convention is **markdown on stdout**. When a user pipes
`foo <plugin> ... | foo -p <pattern>`, foo's stdin reader treats
the piped bytes as the user prompt. Markdown is what patterns
expect; binary or terminal-escape output won't compose.

Write user-facing diagnostics to stderr. Exit non-zero on failure.

### Strict-validation behavior

Subcommand plugins inherit a conservative annotation set:
side-effect `interactive`, idempotency `no`, top-level-verb, and
passthrough. You don't apply these — foo's dispatcher stamps them
after `Register` returns. This keeps the strict-validation gate
armed even when arbitrary plugins are present.

## LLM tool plugin — `foo-tool-<name>`

Tool plugins are NOT cobra subcommands: subcommand discovery
skips every `foo-tool-*` binary, so none appears in `foo --help`
and none is run at startup. They register with foo's internal
tool registry instead, and the model decides when to call them
during `foo -T <name> "..."` execution.

| Aspect | Subcommand plugin | LLM tool plugin |
|--------|-------------------|-----------------|
| Filename prefix | `foo-` | `foo-tool-` |
| Discovery scanner | `dispatch.Register` (cobra) | `tool.Registry` (LLM dispatcher) |
| User invocation | `foo <name> [argv]` | `foo -T <name> "<prompt>"` (model invokes) |
| Output | markdown on stdout | JSON on stdout (function-call result shape) |
| `--ext-info` JSON | Same shape; `capabilities: ["discover"]` | Same shape, plus optional `parameters` |

Tool plugins are usually short, single-purpose, side-effect-free
(or at least clearly scoped). The built-in tools `foo_time` and
`foo_version` in `internal/tool/builtin/` are the in-process
version of the same contract.

### Example: a weather tool the model passes arguments to

The plugin declares `city` and `days` in `parameters`; the model
fills them in from the prompt. The script uses `jq` to read its
input.

```sh
cat > foo-tool-weather <<'SH'
#!/bin/sh
if [ "$1" = "--ext-info" ]; then
  cat <<'JSON'
{
  "name": "weather",
  "version": "0.1.0",
  "description": "Daily forecast for a city",
  "capabilities": ["discover"],
  "parameters": {
    "type": "object",
    "properties": {
      "city": {"type": "string", "description": "City name, e.g. Toronto"},
      "days": {"type": "integer", "description": "Days to forecast, 1-7"}
    },
    "required": ["city"]
  }
}
JSON
  exit 0
fi
# stdin: {"name":"weather","arguments":{"city":"Toronto","days":3}}
req=$(cat)
city=$(printf '%s' "$req" | jq -r '.arguments.city')
days=$(printf '%s' "$req" | jq -r '.arguments.days // 1')
# ...look up the real forecast here...
printf '{"result":{"city":"%s","days":%s,"high_c":[21,19,17]}}\n' "$city" "$days"
SH
chmod +x foo-tool-weather
mv foo-tool-weather "$(go env GOPATH)/bin/"

foo tool list    # weather appears with PARAMS true
foo -T weather --tools-debug "What should I pack for a 3-day trip to Toronto?"
# [tool] call: weather (id=...) args={"city":"Toronto","days":3}
```

foo offers the model a `weather` tool whose arguments are the
`parameters` schema. When the model calls it, foo runs
`foo-tool-weather` with no argv and writes the call to its stdin;
the model folds the result into its answer. `--tools-debug` prints
each call and result on stderr.

### `--ext-info` for tool plugins

The subcommand fields apply, with two differences:

| Field | Notes |
|-------|-------|
| `name` | The value `-T` takes. Empty or missing falls back to the filename after `foo-tool-`. |
| `parameters` | Optional. JSON Schema for the tool's arguments, passed to the model as-is. Must be a JSON object with `"type": "object"`; describe each argument under `properties` and list mandatory ones in `required`. Missing or `null` means the tool takes no arguments. |

If `parameters` is not a JSON object, lacks `"type": "object"`, or
has a `properties` that is not an object, foo skips the plugin. It
does not appear in `foo tool list` or `-T`, and foo prints why on
stderr, from `foo tool list` and from any `-T` run that names it:

```
[foo] warning: skipping tool plugin /usr/local/bin/foo-tool-weather: --ext-info "parameters" must declare "type": "object"
```

foo skips the plugin instead of offering it with no arguments,
because the model would then call it without the inputs it said
it needs. A binary whose `--ext-info` fails outright still
registers, under its filename-derived name and with no arguments.

### Call protocol

| Direction | Payload |
|-----------|---------|
| stdin, from foo | `{"name": "<tool name>", "arguments": <object the model sent>}` |
| stdout, success | `{"result": <any JSON>}` |
| stdout, failure | `{"error": "<message>"}` |

foo passes `arguments` through as the model sent them; validate
them in the plugin. Each call has a 30-second deadline. A non-zero
exit or non-JSON stdout reaches the model as a tool error.

## Common issues

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| Plugin missing from `foo --help` | Wrong PATH or wrong filename | `which foo-<name>`; rename binary; check it's executable |
| Description shows as generic placeholder | `--ext-info` errored or returned non-JSON | Run `foo-<name> --ext-info` directly and validate the JSON |
| `pipe broken` when running `foo <plugin> | foo` | Plugin wrote binary to stdout | Emit markdown; diagnostics to stderr |
| `foo <name>` is treated as a prompt arg | Binary not on PATH at all | `go install` the plugin or `chmod +x` after copying |
| Tool plugin missing from `foo tool list` | Wrong PATH, wrong filename, or not executable | `which foo-tool-<name>`; check the `foo-tool-` prefix and `chmod +x` |
| `unknown tool "<name>"` | `-T` value differs from the listed name (`--ext-info` `name` overrides the filename) | Use the NAME column of `foo tool list` |
| Tool plugin never invoked | Did you pass `-T <name>`? Model declines to call | Confirm `--tools-debug` shows the tool offered to the model |
| `[foo] warning: skipping tool plugin ...` | `parameters` in `--ext-info` is not a JSON Schema object of type `object` | Fix the schema; check it with `foo-tool-<name> --ext-info \| jq .parameters` |
| Model calls the tool with `{}` | No `parameters` declared (`PARAMS` is false in `foo tool list`) | Add a `parameters` schema to `--ext-info` |

## How it works

At foo startup, `commands.New` calls
`registerExtPlugins(rootCmd)`. That helper scans `$PATH` for
`foo-*` executables via kit's `discover.Scanner`, registers each
discovered binary as a passthrough cobra subcommand, drops the
`foo-tool-*` ones, and stamps the kit annotations the strict
validator requires on the rest. Each remaining plugin's
`--ext-info` description is read once, right there at startup,
and assigned to `cmd.Short` and `cmd.Long`.

Tool-plugin discovery happens separately in `buildRegistry`
(`cmd/foo/commands/root.go`), which scans for `foo-tool-*`, runs
each binary's `--ext-info` once, and adds it to the LLM
dispatcher's registry as an `ExternalTool`
(`internal/tool/external.go`) carrying its `parameters` schema.
A tool plugin is invoked only when (a) the user passed
`-T <name>`, AND (b) the model decides to call it.

## Reference

| Topic | Where |
|-------|-------|
| Subcommand plugin example | `cmd/foo-youtube/main.go`, `cmd/foo-scrape/main.go` |
| Tool plugin example | [The weather example](#example-a-weather-tool-the-model-passes-arguments-to); built-ins in `internal/tool/builtin/` |
| Tool plugin loader | `internal/tool/external.go` |
| Dispatcher source | `hop.top/kit/go/ai/ext/dispatch` |
| Discovery source | `hop.top/kit/go/ai/ext/discover` |

## Related docs

- [Use plugins](use-plugins.md) — installing and running plugins.
- [Concepts](../concepts.md#plugins-via-path-discovery) — the
  PATH-discovery model.
- [Reference: commands](../reference/commands.md) — the full CLI
  surface plugins extend.
