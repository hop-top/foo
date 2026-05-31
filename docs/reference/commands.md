# Command reference

Every foo command, grouped by top-level verb. Synopses come from
the real `cobra.Command.Use` strings; descriptions come from
`Short`. For full task narrative, follow the linked how-to.

## Top-level

| Command | Synopsis | Description | How-to |
|---------|----------|-------------|--------|
| `foo [prompt]` | `foo [command] [prompt] [--flags]` | Run a one-shot prompt or open the REPL when no prompt given | [Quickstart](../quickstart.md) |
| `foo status` | `foo status [--flags]` | Show kit runtime status (profile, env, workspace, auth, config) | [Troubleshooting](../troubleshooting.md#foo-status-shows-degraded-health) |
| `foo shell` | `foo shell` | Open an interactive shell session (REPL) — TTY required | (No how-to; same as bare `foo` on a TTY) |
| `foo upgrade` | `foo upgrade` | Upgrade foo to the latest version | [Upgrade foo](../how-to/upgrade-foo.md) |
| `foo completion` | `foo completion [shell]` | Generate the autocompletion script for the specified shell | (kit-shipped) |

## Root flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--pattern` | `-p` | (none) | Pattern (system prompt) to apply |
| `--strategy` | `-s` | (none) | Strategy wrapper to apply |
| `--model` | `-m` | (config) | Model override for this call |
| `--no-stream` | | `false` | Wait for full response |
| `--dry-run` | | `false` | Print assembled prompt and exit |
| `--tool` | `-T` | (none) | Enable specific tools by name (repeatable) |
| `--chain-limit` | | `5` | Max tool-call iterations |
| `--tools-debug` | | `false` | Log tool calls + results to stderr |
| `--tools-approve` | | `false` | Confirm before each tool execution |
| `--fragment` | `-f` | (none) | Attach fragment(s) to the user prompt |
| `--system-fragment` | | (none) | Attach fragment(s) to the system prompt |
| `--schema` | | (none) | Structured JSON output (schema name or DSL) |
| `--schema-multi` | | (none) | Structured JSON array output (schema name or DSL) |

## Kit globals (apply everywhere)

| Flag | Purpose |
|------|---------|
| `-c, --config` | Layer extra config files or `key=value` overrides |
| `--confirm` | Confirm policy: `auto` (default), `yes`, `no`, `prompt` |
| `--format` | `csv`, `human`, `json`, `table`, `text`, `yaml` |
| `--cols` / `--columns` | Restrict columns (repeatable) |
| `-C, --chdir` | Change directory before running |
| `-o, --output` | Write output to path (`-` for stdout) |
| `--template` | Go text/template applied to results |
| `--format-help` | Show available formats |
| `--format-opt` | Per-format option as key=value |
| `--max-ops` | Cap mutating operations per invocation |
| `--no-color` | Disable ANSI color |
| `--no-hints` | Suppress next-step hints |
| `--quiet` | Suppress non-essential output |
| `--verbose` / `-V` | Increase log verbosity |
| `--api-version` | Request a specific CLI schema version |
| `--policy` | Named delegation policy |
| `--progress-format` | Progress output format (`human` or `json`) |

## `embed`

Manage a local vector store for embedding and semantic search.

| Subcommand | Synopsis | Description | How-to |
|------------|----------|-------------|--------|
| `add` | `foo embed add <text> [--flags]` | Embed text into a collection | [Embed content](../how-to/embed-content.md) |
| `file` | `foo embed file --file <path> [--flags]` | Embed a file as chunked vectors | [Embed content](../how-to/embed-content.md) |
| `search` | `foo embed search <query> [--flags]` | Search for similar embedded content | [Search semantically](../how-to/search-semantically.md) |
| `collection list` | `foo embed collection list` | List collections | [Embed content](../how-to/embed-content.md) |
| `collection delete` | `foo embed collection delete <name>` | Delete one collection | [Confirm destructive ops](../how-to/confirm-destructive-ops.md) |

Flags:

| Flag | Subcommands | Default | Purpose |
|------|-------------|---------|---------|
| `-c, --collection` | `add`, `file`, `search` | `default` | Collection name |
| `--file` | `file` | (required) | Path to file to embed |
| `-n, --count` | `search` | `5` | Number of neighbors |

## `fragment`

Manage reusable prompt fragments.

| Subcommand | Synopsis | Description | How-to |
|------------|----------|-------------|--------|
| `list` | `foo fragment list` | List fragment aliases | [Manage fragments](../how-to/manage-fragments.md) |
| `show` | `foo fragment show <alias>` | Show one fragment | [Manage fragments](../how-to/manage-fragments.md) |
| `create` | `foo fragment create <alias> [source]` | Create or replace a fragment (file, URL, stdin) | [Manage fragments](../how-to/manage-fragments.md) |
| `delete` | `foo fragment delete <alias>` | Delete a fragment alias | [Confirm destructive ops](../how-to/confirm-destructive-ops.md) |

## `pattern`

Manage reusable system prompt patterns.

| Subcommand | Synopsis | Description | How-to |
|------------|----------|-------------|--------|
| `list` | `foo pattern list` | List available patterns | [Manage patterns](../how-to/manage-patterns.md) |
| `show` | `foo pattern show <name>` | Show one pattern body | [Manage patterns](../how-to/manage-patterns.md) |
| `create` | `foo pattern create <name> [system-prompt]` | Create or replace a pattern | [Manage patterns](../how-to/manage-patterns.md) |
| `import` | `foo pattern import <path> [name]` | Import a pattern from a file | [Manage patterns](../how-to/manage-patterns.md) |
| `delete` | `foo pattern delete <name>` | Delete a pattern | [Confirm destructive ops](../how-to/confirm-destructive-ops.md) |

## `schema`

Manage JSON schemas.

| Subcommand | Synopsis | Description | How-to |
|------------|----------|-------------|--------|
| `list` | `foo schema list` | List stored schemas | [Manage schemas](../how-to/manage-schemas.md) |
| `show` | `foo schema show <name>` | Show DSL + compiled JSON Schema | [Manage schemas](../how-to/manage-schemas.md) |
| `create` | `foo schema create <name> [dsl] [--flags]` | Create or replace from DSL or `--file` | [Manage schemas](../how-to/manage-schemas.md) |
| `compile` | `foo schema compile <shorthand>` | Compile a DSL without storing it | [Schema DSL](schema-dsl.md) |
| `delete` | `foo schema delete <name>` | Delete a schema | [Confirm destructive ops](../how-to/confirm-destructive-ops.md) |

`create` flags:

| Flag | Purpose |
|------|---------|
| `--file` | JSON Schema file to store verbatim |

## `strategy`

List available prompt strategies.

| Subcommand | Synopsis | Description | How-to |
|------------|----------|-------------|--------|
| `list` | `foo strategy list` | List available strategies | [Use strategies](../how-to/use-strategies.md) |

## `model`

Manage the default model selection.

| Subcommand | Synopsis | Description | How-to |
|------------|----------|-------------|--------|
| `current` | `foo model current` | Show the current default model | [Configure models](../how-to/configure-models.md) |
| `default` | `foo model default <model>` | Set the default model | [Configure models](../how-to/configure-models.md) |

## `provider`

Inspect configured LLM providers.

| Subcommand | Synopsis | Description | How-to |
|------------|----------|-------------|--------|
| `list` | `foo provider list` | List registered providers | [Configure models](../how-to/configure-models.md) |
| `show` | `foo provider show <scheme>` | Show provider auth status | [Configure models](../how-to/configure-models.md) |

## Kit conformance annotations

Each leaf carries side-effect, idempotency, and verb annotations
consumed by the kit validator. The frozen audit baseline is
[kit-conformance-baseline.md](kit-conformance-baseline.md).

## Related docs

- [Config reference](config.md) — config keys + env vars.
- [Schema DSL reference](schema-dsl.md) — grammar for `--schema` values.
- [Compatibility](compatibility.md) — kit version requirements.
