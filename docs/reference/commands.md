# Command reference

Every foo command, grouped by top-level verb. Synopses come from
the real `cobra.Command.Use` strings; descriptions come from
`Short`. For full task narrative, follow the linked how-to.

## Top-level

| Command | Synopsis | Description | How-to |
|---------|----------|-------------|--------|
| `foo [prompt]` | `foo [command] [prompt] [--flags]` | Run a one-shot prompt or open the REPL when no prompt given | [Quickstart](../quickstart.md) |
| `foo status` | `foo status [--flags]` | Show kit runtime status (profile, env, workspace, auth, config) | [Troubleshooting](../troubleshooting.md#foo-status-shows-degraded-health) |
| `foo repl` | `foo repl` | Open an interactive REPL session — TTY required (Enter sends; Ctrl+C/Esc exits) | (No how-to; same as bare `foo` on a TTY) |
| `foo upgrade` | `foo upgrade` | Upgrade foo to the latest version | [Upgrade foo](../how-to/upgrade-foo.md) |
| `foo completion` | `foo completion [shell]` | Generate the autocompletion script for the specified shell | (kit-shipped) |
| `foo <plugin>` | `foo <plugin> [argv...]` | Dispatch to `foo-<plugin>` binary on `$PATH` (PLUGINS group, descriptions from `--ext-info`) | [Use plugins](../how-to/use-plugins.md) |

## Root flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--pattern` | `-p` | (none) | Pattern (system prompt) to apply |
| `--strategy` | `-s` | (none) | Strategy wrapper to apply |
| `--model` | `-m` | (config) | Model override for this call (bare id, or a full `scheme://model` URI) |
| `--max-tokens` | | `0` | Cap completion length in tokens (`0` = provider default, field omitted) |
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

Discover model ids and manage the default model selection.

| Subcommand | Synopsis | Description | How-to |
|------------|----------|-------------|--------|
| `list` | `foo model list [--flags]` | List models from the model catalog | [Configure models](../how-to/configure-models.md#1-find-a-model-id) |
| `current` | `foo model current` | Show the current default model | [Configure models](../how-to/configure-models.md) |
| `default` | `foo model default <model>` | Set the default model | [Configure models](../how-to/configure-models.md) |

`list` flags:

| Flag | Default | Purpose |
|------|---------|---------|
| `--all` | `false` | Include models whose provider foo cannot reach (no adapter, or no API key configured) |
| `--limit` | `20` | Maximum models to list (`0` for no limit) |
| `--endpoint` | (none) | List models from this OpenAI-compatible base URL instead of the catalog |
| `--refresh` | `false` | Bypass the catalog and endpoint caches and refetch |
| `--provider` | (none) | Only models from this provider id (exact match) |
| `--family` | (none) | Only models in this family (exact match) |
| `--in` | (none) | Only models accepting this input modality (repeatable) |
| `--out` | (none) | Only models producing this output modality (repeatable) |
| `--tool-call` | (unset) | Only models with (`--tool-call`) or without (`--tool-call=false`) tool calling |
| `--reasoning` | (unset) | Only models with (`--reasoning`) or without (`--reasoning=false`) reasoning |
| `--open-weights` | (unset) | Only models with (`--open-weights`) or without (`--open-weights=false`) open weights |
| `--structured-output` | (unset) | Only models with (`--structured-output`) or without (`--structured-output=false`) structured output |
| `--query` | (none) | Catalog query expression, e.g. `"provider:openai reasoning:true"` |

By default the listing shows only models foo can actually call: the
provider must have a compiled-in adapter, and must either need no
credential (a local runtime) or have its API key present in the
configured secret store. Everything else is hidden, and a stderr
footer reports the count and names `--all`. The credential
requirement is read from the model catalog, which publishes the env
var names each provider accepts, so a provider accepting several
alternatives (`google` takes `GOOGLE_API_KEY`,
`GOOGLE_GENERATIVE_AI_API_KEY` or `GEMINI_API_KEY`) is satisfied by
any one of them. Keys resolve through the secret store, not
`os.Getenv`, so a keyring backend works.

`--all` disables that filtering. It widens the candidate set rather
than replacing the narrowing filters, so it combines with
`--provider`, `--query` and the rest — `--provider groq --all` is
how you browse a provider's catalogue before you have its key.

Reachability filtering never applies to `--endpoint`: those rows are
a server's own inventory, with no catalog provider behind them to
hold a credential requirement. `--all` is accepted there and does
nothing.

Capability flags are three-state: omit for no filtering, pass the
flag for models that have the capability, pass `=false` for models
that do not. Filters combine with AND and apply before `--limit`
truncates.

`--query` keys are `provider`, `family`, `in`, `out`, `tool_call`,
`reasoning`, `open_weights`, `structured_output`, `temperature` —
note `in`/`out` rather than the flags' `input`/`output`, and
underscores rather than hyphens. An unknown key is an error naming
the key. Where a query key names the same thing as an explicit
flag, the flag wins; modality lists merge instead.

Every filter flag is rejected against `--endpoint`: a live
`/v1/models` response carries ids and nothing to filter on. `--all`
is not a filter flag and is accepted.

Truncation hints, the `catalog: cached …` provenance footer and the
hidden-model footer all go to stderr, so `--format json` pipes
cleanly. Under `--format
json`/`yaml` the provenance is nested as a `_meta` object beside
`data` instead.

The modality filters are `--in` / `--out`, matching the `in:` and
`out:` keys `--query` accepts. `--output` keeps its family-wide
meaning here, so a listing writes to a file the usual way:

```sh
foo model list --limit=0 --format json --output models.json
```

## `provider`

Inspect configured LLM providers.

| Subcommand | Synopsis | Description | How-to |
|------------|----------|-------------|--------|
| `list` | `foo provider list` | List registered providers | [Configure models](../how-to/configure-models.md) |
| `show` | `foo provider show <scheme>` | Show provider auth status | [Configure models](../how-to/configure-models.md) |

`show` reports `status` as one of:

| Status | Meaning |
|--------|---------|
| `available` | The provider needs no credential (a local runtime). |
| `configured` | A required credential is present in the secret store. |
| `missing` | A required credential is absent. |

The requirement comes from the same model catalog `foo model list`
filters on, so the two surfaces always agree: a provider reported
`missing` here is a provider whose models the default listing hides.
`secret_key` names the secret-store key that satisfied the
requirement, or the first alternative when none did.

## Kit conformance annotations

Each leaf carries side-effect, idempotency, and verb annotations
consumed by the kit validator. The frozen audit baseline is
[kit-conformance-baseline.md](kit-conformance-baseline.md).

## Related docs

- [Config reference](config.md) — config keys + env vars.
- [Schema DSL reference](schema-dsl.md) — grammar for `--schema` values.
- [Compatibility](compatibility.md) — kit version requirements.
