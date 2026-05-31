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
starts with `foo-`. foo discovers it on every `foo` invocation
via `hop.top/kit/go/ai/ext/dispatch`, registers it as a hidden
cobra subcommand under the PLUGINS group, and forwards argv with
`DisableFlagParsing: true` (you handle flags yourself).

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

Foo invokes `--ext-info` lazily at help-render time, so plugins
shouldn't do any heavy work in this path. Plain JSON, exit 0.

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

Tool plugins are NOT cobra subcommands. They register with foo's
internal tool registry instead, and the model decides when to
call them during `foo -T <name> "..."` execution.

| Aspect | Subcommand plugin | LLM tool plugin |
|--------|-------------------|-----------------|
| Filename prefix | `foo-` | `foo-tool-` |
| Discovery scanner | `dispatch.Register` (cobra) | `tool.Registry` (LLM dispatcher) |
| User invocation | `foo <name> [argv]` | `foo -T <name> "<prompt>"` (model invokes) |
| Output | markdown on stdout | JSON on stdout (function-call result shape) |
| `--ext-info` JSON | Same shape; `capabilities: ["discover"]` | Same shape |

Tool plugins are usually short, single-purpose, side-effect-free
(or at least clearly scoped). The two built-in tools `foo_time`
and `foo_version` are the canonical templates — they live in
`internal/tool/builtin/` and show the minimal contract.

For a third-party tool plugin, follow the same `--ext-info`
contract but match the `foo-tool-` prefix:

```sh
# foo-tool-weather emits a forecast JSON blob for a given city
foo -T weather "What should I pack for a 3-day business trip to Toronto?"
# Model invokes foo-tool-weather(city=Toronto, days=3),
# gets back the forecast, then composes a packing list grounded
# in the actual high/low and precipitation.
```

The model decides whether to invoke `weather` based on the
prompt; on call, foo execs `foo-tool-weather` with structured
args derived from the prompt. The tool returns JSON; the model
folds it into its response. Pass `--tools-debug` to see the call
and result on stderr.

## Common issues

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| Plugin missing from `foo --help` | Wrong PATH or wrong filename | `which foo-<name>`; rename binary; check it's executable |
| Description shows as generic placeholder | `--ext-info` errored or returned non-JSON | Run `foo-<name> --ext-info` directly and validate the JSON |
| `pipe broken` when running `foo <plugin> | foo` | Plugin wrote binary to stdout | Emit markdown; diagnostics to stderr |
| `foo <name>` is treated as a prompt arg | Binary not on PATH at all | `go install` the plugin or `chmod +x` after copying |
| Tool plugin never invoked | Did you pass `-T <name>`? Model declines to call | Confirm `--tools-debug` shows the tool offered to the model |

## How it works

At foo startup, `commands.New` calls
`registerExtPlugins(rootCmd)`. That helper scans `$PATH` for
`foo-*` executables via kit's `discover.Scanner`, registers each
discovered binary as a passthrough cobra subcommand, and stamps
the kit annotations the strict validator requires. The plugin's
`--ext-info` description is read once (lazily, at help render)
and assigned to `cmd.Long`.

Tool-plugin discovery happens separately in `buildRegistry`
(`cmd/foo/commands/root.go`), which scans for `foo-tool-*` and
adds each as an `ExternalTool` in the LLM dispatcher's registry.
A tool plugin is invoked only when (a) the user passed
`-T <name>`, AND (b) the model decides to call it.

## Reference

| Topic | Where |
|-------|-------|
| Subcommand plugin example | `cmd/foo-youtube/main.go`, `cmd/foo-scrape/main.go` |
| Tool plugin example | `internal/tool/builtin/time.go`, `internal/tool/builtin/version.go` |
| Dispatcher source | `hop.top/kit/go/ai/ext/dispatch` |
| Discovery source | `hop.top/kit/go/ai/ext/discover` |

## Related docs

- [Use plugins](use-plugins.md) — installing and running plugins.
- [Concepts](../concepts.md#plugins-via-path-discovery) — the
  PATH-discovery model.
- [Reference: commands](../reference/commands.md) — the full CLI
  surface plugins extend.
