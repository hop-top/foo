# Command reference

Every foo command, grouped by top-level verb. Synopses come from
the real `cobra.Command.Use` strings; descriptions come from
`Short`. For full task narrative, follow the linked how-to.

## Top-level

| Command | Synopsis | Description | How-to |
|---------|----------|-------------|--------|
| `foo` | `foo [command] [--flags]` | Run a subcommand, a one-shot prompt, or open the REPL — see [Invocation modes](#invocation-modes) | [Quickstart](../quickstart.md) |
| `foo status` | `foo status [--flags]` | Show kit runtime status (profile, env, workspace, auth, config) | [Troubleshooting](../troubleshooting.md#foo-status-shows-degraded-health) |
| `foo repl` | `foo repl` | Open an interactive REPL session — TTY required (Enter sends; Ctrl+C/Esc exits) | (No how-to; same as bare `foo` on a TTY) |
| `foo upgrade` | `foo upgrade` | Upgrade foo to the latest version | [Upgrade foo](../how-to/upgrade-foo.md) |
| `foo completion` | `foo completion [shell]` | Generate the autocompletion script for the specified shell | (kit-shipped) |
| `foo <plugin>` | `foo <plugin> [argv...]` | Dispatch to `foo-<plugin>` binary on `$PATH` (PLUGINS group, descriptions from `--ext-info`) | [Use plugins](../how-to/use-plugins.md) |

### Invocation modes

The bare `foo` command takes **either** a subcommand **or** a single
positional prompt — never both. With neither it opens the REPL. The
usage line cannot express that alternation (it renders one line), so the
three modes are:

```
foo "explain this stack trace"   # one-shot prompt
foo model list                   # subcommand
foo                              # REPL (TTY required)
```

Root flags such as `-m` / `--model` are local to the root command, so
they apply to the prompt form only. A subcommand rejects them wherever
they appear on the line:

```
foo -m gpt-4o-mini strategy list
# USAGE: unknown flag -m
# Cause: no flag named -m on foo strategy list
```

Only persistent flags (`--budget`, `--picker-debug`, and the kit
globals) are inherited by subcommands.

Piping with no positional prompt is an error rather than a REPL, since
stdin is not a terminal:

```
foo < /dev/null
# GENERIC: no prompt provided (stdin was empty); pass a positional prompt or pipe non-empty content
```

## Root flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--pattern` | `-p` | (none) | Pattern (system prompt) to apply |
| `--strategy` | `-s` | (none) | Strategy wrapper to apply |
| `--model` | `-m` | (config) | Model override for this call (bare id, or a full `scheme://model` URI) |
| `--max-tokens` | | `0` | Cap completion length in tokens (`0` = provider default, field omitted) |
| `--no-stream` | | `false` | Wait for full response |
| `--dry-run` | | `false` | Print assembled prompt and exit |
| `--tool` | `-T` | (none) | Enable tools by name (repeatable); names come from [`foo tool list`](#tool). An unknown name fails with exit 3 before any model call |
| `--chain-limit` | | `5` | Max tool-call iterations |
| `--tools-debug` | | `false` | Log tool calls + results to stderr |
| `--tools-approve` | | `false` | Confirm before each tool execution. Answers come from the terminal, never stdin. A no, or no terminal to ask on, returns the `declined` error to the model, with the reason |
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
| `--profile` | Namespace secret lookups under an aps profile — binds on the `keyring` secrets backend only (foo defaults to `env`, where it has no effect) |
| `--progress-format` | Progress output format (`human` or `json`) |
| `--offline` | Refuse network access; see [Offline runs](#offline-runs) |

### Offline runs

`--offline` refuses every request that would leave the machine. A
model call to a remote endpoint fails before anything is sent, with
code `OFFLINE` and exit 10 (foo's own code, so it never reads as a
missing API key, which is kit's exit 5); the same run against a model
served on loopback goes through. The flag works before or after the prompt.

| Endpoint | Under `--offline` |
|----------|-------------------|
| `localhost`, `127.0.0.0/8`, `[::1]` (as written in `base_url`) | Allowed |
| Any other host, including a DNS name that resolves to loopback | Refused: `OFFLINE`, exit 10 |
| Provider defaults (`api.openai.com`, `api.anthropic.com`, …) | Refused: `OFFLINE`, exit 10 |

It applies to plain prompts, streamed or not, to `-T` tool runs, and
to fallback providers. foo also skips the upgrade check and bus peers,
and `foo upgrade` refuses to run.

```
$ LLM_BASE_URL=https://api.example.com/v1 foo --offline -m gpt-4o "hi"
OFFLINE: --offline refused api.example.com: only loopback endpoints (localhost, 127.0.0.0/8, ::1) are reachable offline
Cause: Post "https://api.example.com/v1/chat/completions": POST https://api.example.com/v1/chat/completions: network disabled by --offline
Fix: use a model served on loopback (LLM_BASE_URL=http://127.0.0.1:<port>/v1, providers.<scheme>.base_url, or -m '<model>?base_url=...'), or drop --offline
$ echo $?
10
```

Exit 5 is kit's class for a call a policy forbids: running the same
command again cannot succeed. The `OFFLINE` code distinguishes it
from a missing or rejected API key (`UNAUTHORIZED`, also 5).

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
credential (a local runtime) or have an API key a run would find.
Everything else is hidden, and a stderr footer reports the count and
names `--all`.

A provider has an adapter when kit serves its id as a scheme:
registered (`openai`), under an alias (`fireworks-ai`,
`togetherai`), or, for a provider in the cached catalog, through the
protocol it speaks (most are OpenAI-compatible). "A key a run would
find" is the run's own key check, made through kit:
`providers.<scheme>.api_key` in llm.yaml, the provider's key names
(through the secret store, then the environment — `GOOGLE_API_KEY`
then `GEMINI_API_KEY` for `google`), or `LLM_API_KEY`
([key precedence](config.md#key-precedence)). A local runtime
(`ollama`, `lmstudio`, `routellm`) needs no key, even where the
catalog lists one for it.

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
| `configured` | A run would find a key: llm.yaml, the scheme's own, or `LLM_API_KEY`. |
| `missing` | No key from any of those sources. |

The verdict is the one `foo model list` filters on, and for a
provider an adapter serves it is the run's own key check, so a
`missing` provider is one whose models the default listing hides and
whose runs fail the key check. `<scheme>` may be a kit scheme
(`gemini`) or a catalog provider id (`fireworks-ai`). `secret_key`
names the provider's own secret-store key — the one that resolved,
else the first to set to give it a key of its own. With
`--format json` or `yaml`, `key_source` says where a `configured`
key was found (the source a run uses, highest precedence first);
the key itself is never printed:

| `key_source` | Key found in |
|--------------|--------------|
| `llm.yaml` | `providers.<scheme>.api_key` in `$XDG_CONFIG_HOME/hop/llm.yaml` |
| `secret_key` | The secret store, or the env var `secret_key` maps to |
| `LLM_API_KEY` | The universal `LLM_API_KEY` variable |

## `tool`

Inspect the tools `-T` / `--tool` can enable, and link foo's OS tools
for other hosts.

| Subcommand | Synopsis | Description | How-to |
|------------|----------|-------------|--------|
| `list` | `foo tool list` | List tools available to -T | [Use OS tools](../how-to/use-os-tools.md#1-find-the-tools) |
| `install` | `foo tool install [--dir <dir>]` | Link foo-tool-<name> plugins to foo for other hosts | [Share OS tools](../how-to/share-os-tools.md) |
| `uninstall` | `foo tool uninstall [--dir <dir>]` | Remove the foo-tool-<name> links install created | [Share OS tools](../how-to/share-os-tools.md#4-remove-the-links) |

`foo tool list` columns:

| Column | Values |
|--------|--------|
| `name` | The value `-T` takes |
| `source` | `builtin` (compiled into foo: `foo_time`, `foo_version` and the OS tools); `user:<path>` or `system:<path>` for a [tool spec](tool-spec.md) read from foo's config dir or `/etc/xdg/foo/tools`, with ` (overrides builtin)` when it replaces one; or the absolute path of a `foo-tool-<name>` binary on `$PATH` |
| `description` | What the model sees |
| `params` | `true` when the tool declares arguments for the model to fill in |
| `side_effect` | `read`, `write` or `destructive` for foo's tools and for a plugin that declares `foo_tool`; `unknown` for a plugin that declares none |
| `paths` | The path arguments foo checks, with their ops (`src:r dst:w`, `path:rw`); `ungated` for a plugin that declares no `foo_tool`: foo cannot check paths a plugin does not declare |
| `status` | `active`, or `shadowed`: another tool owns the name (Go built-ins, then specs, then `$PATH` plugins) and this one never runs |

A spec that fails to load is left out with a warning on stderr and
still owns its name: `-T` with that name exits 3 with the spec's
error. A plugin's name, description and parameter schema come from
its `--ext-info` output; listing runs each `foo-tool-*` binary once to
read it (links to foo itself and shadowed names are skipped). A binary
whose `parameters` is not a JSON Schema object of type `object`, or
whose `foo_tool` annotations foo cannot enforce, is left out of the
listing and of `-T`, with a warning on stderr from the
listing and from any `-T` run that names it (see
[Write plugins](../how-to/write-plugins.md#--ext-info-for-tool-plugins)).
Every `--format` works:

```sh
foo tool list --format json
```

`foo tool install` creates a `foo-tool-<name>` symlink to the foo
binary for every valid spec tool in `--dir` (default
`$XDG_BIN_HOME/foo`, where `XDG_BIN_HOME` defaults to `~/.local/bin`,
off `$PATH`; foo prints a note when the directory
is not on `$PATH`). Columns: `name`, `link`, `action` (`created`,
`unchanged`, `replaced`, `skipped`, `removed`, `kept`) and `reason`.
Files and links that do not point at foo are never touched. Run
through a link, foo applies the same scope and policy checks as `-T`
and refuses any call that would ask for approval. `-T` never needs the
links.

Tool calls that write or delete ask first; `tool-policy.yaml` beside
`scope.yaml` changes that per side effect
([how-to](../how-to/use-os-tools.md#7-let-writes-run-without-asking-optional)).

An unknown `-T` name fails before stdin is read or any model is
called, names the bad tool, and lists the valid ones. The exit code is
3 (not found), the same as an unknown `--pattern` or `--schema`:

```
foo -T foo_tme "what time is it?"
# NOT_FOUND: unknown tool "foo_tme"; did you mean "foo_time"? Available tools: cat, cp, find, foo_time, foo_version, grep, head, ls, mkdir, mv, rm, sed, stat, tail, wc (run `foo tool list` for details)
```

### Read tools

Built-in shim tools over the OS commands. Every path argument is
resolved to its canonical physical path, checked for `read` against
the path scope, and passed after a literal `--`; there is no globbing
or `$VAR` expansion. Flags that recurse where foo cannot filter,
follow links out of the checked tree, execute, write, take a path in
an option value, or never terminate cannot be expressed. BSD, GNU and
busybox binaries are all supported; the flavor is detected from the
pinned binary.

| Tool | Parameters | Recursion | Not offered |
|------|------------|-----------|-------------|
| `ls` | `path` (default `.`), `long`, `all`, `sort` (`name`/`time`/`size`), `reverse`, `directory` | none; walk trees with `find` | `-R`, `-L`, `-H`, color |
| `cat` | `path` (files, max 16), `number` | none | stdin (`-` is a file name) |
| `head` | `path` (files), `lines` (1..100000, default 10) or `bytes` (1..1 MiB) | none | — |
| `tail` | `path` (files), `lines` (1..100000, default 10) or `bytes` (1..1 MiB) | none | `-f`, `-F` (never terminate) |
| `wc` | `path` (files), `lines`, `words`, `bytes`, `chars` | none | `--files0-from` |
| `stat` | `path` | none | a format string: the output format is fixed per flavor; `-L` (paths already resolved) |
| `find` | `path` (default `.`), `maxdepth`/`mindepth` (0..64), `type` (`f`/`d`/`l`), `name`/`iname` (globs), `mtime_days` (-3650..3650) | `find -P` walks; foo drops every output path outside the scope and reports the count as `filtered` | `-exec`, `-execdir`, `-ok`, `-delete`, `-fprint*`, `-fls`, `-printf`, `-ls`, `-L`, `-H`, `-follow`, `-newer`, `-regex` |
| `grep` | `pattern` (always the argument of `-e`), `path`, `recursive`, `ignore_case`, `fixed`, `extended`, `word`, `invert`, `count`, `files_only`, `line_number` (default true), `max_count` (1..10000), `context` (0..20) | with `recursive=true` foo walks the tree without following links and passes grep only the permitted regular files; the rest are counted as `filtered`. Exit 1 (no match) is ok | `-r`, `-R`, `-f`, `--include`, `--exclude-from`, `-P` |

`ls` lists the names of every entry in a permitted directory, even
entries the scope denies reading; `find` leaves them out.

### Write tools

Built-in shim tools that change files. Every path argument is resolved
to its canonical path and checked against the path scope for the op
listed, then passed after a literal `--`. A `write` or `destructive`
call asks for confirmation before it runs.

| Tool | Parameters | Paths (op) | Side effect | Notes |
|------|------------|------------|-------------|-------|
| `cp` | `src[]`, `dst`, `recursive`, `overwrite` | `src` read, `dst` write | write; `overwrite=true` → destructive | Never replaces an existing file unless `overwrite=true`, never an existing directory. An existing directory `dst` receives each source under its own name. `recursive=true` checks every entry of the tree, at the source and at its destination; one denied entry refuses the whole call. Links inside a tree are copied as links. Root guard (below) on where `dst` lands: `cp x ~` is fine, `cp x /` is not |
| `mv` | `src[]`, `dst`, `overwrite` | `src` read + write, `dst` write | write; `overwrite=true` → destructive | The source needs write, since moving removes it from its directory, and read, since a move across filesystems copies its content. Same no-clobber, directory and whole-tree rules as `cp`. A link moves as the link. Root guard (below) on every source and on where `dst` lands: `mv x ~` is fine, `mv x /` is not |
| `mkdir` | `path[]`, `parents` | `path` write | write | `parents=true` also creates, and checks, every missing parent. No mode option |
| `rm` | `path[]`, `recursive`, `dir` | `path` write | destructive | Removes a link, never its target. Root guard (below). Removing an entry needs write on its directory, so the root of a scope grant cannot be removed unless its parent is writable too. `recursive=true` refuses the whole call if any entry under the tree is out of scope. No `-f`. `dir` is not offered with busybox `rm` |
| `sed` | `path[]`, `find`, `replace`, `backrefs`, `global`, `ignore_case`, `occurrence`, `extended`, `dry_run` | `path` read + write; `dry_run=true` → read | destructive; `dry_run=true` → read | One substitution per call: foo builds the `s` command from `find` and `replace`, so a sed script (`w`, `e`, `r`, addresses) can never be passed. `replace` is inserted literally: foo escapes `&` and `\`, so `a/b&c` or `\1` land as written on BSD, GNU and busybox sed. `backrefs=true` switches to sed replacement syntax (`&`, `\1`–`\9`, `\&`, `\\`); `replace` then may not end in a lone `\`. Newlines and `\x01` are rejected. `dry_run=true` prints the result and changes nothing, so it only needs read scope on the path: a read-only grant can be previewed. GNU sed runs with `--sandbox` |

**Root guard.** `rm`, `mv` and `cp` refuse `/`, the home directory and every
entry directly under `/`, whatever the scope, with `invalid_args`. The
check runs in two steps:

1. **As written, before the scope check.** The path with `~` expanded,
   taken from the working directory and cleaned (`/`, `//`, `/tmp/..`,
   `~`, `~/`, your home spelled out, `/usr`, `/tmp`) is refused before
   anything else, without looking at the disk and without a prompt.
2. **As it resolves, after the scope check and before the command runs.**
   A link to `/` or to home, what a top-level link points to
   (`/private/tmp` on macOS, `/usr/bin` where `/bin` links to it), and a
   match by file identity (another letter case on a case-insensitive
   disk). `mv` and `cp` check `dst` here too, where the entry lands after
   mapping into a directory, so `overwrite=true` cannot replace one.

Step 2 runs only on paths the scope grants or you approved, so a path
outside the scope gets the scope's `denied`, whatever it points to. A
call that needs approval may therefore ask first and be refused after.

## `scope`

Inspect the path policy tool calls are checked against: `scope.yaml`
in foo's config directory, `$XDG_CONFIG_HOME/foo/` (`XDG_CONFIG_HOME`
unset: `~/.config` on Linux, `~/Library/Application Support` on macOS), and
`/etc/xdg/foo/scope.yaml`, plus a built-in deny list for secrets on
every op: each of kit's secret patterns (`**/.env`, `**/secrets*`,
`**/credentials*`, `~/.ssh/**`, ...) and its descendant form
(`**/secrets*/**`), so a directory with a secret name hides its
contents as well as its own entry. Each credential directory kit
anchors to home is also denied at any depth, under its full
home-relative name (`**/.ssh`, `**/.aws`, `**/.azure`, `**/.gnupg`,
`**/.kube`, `**/.pki`, `**/.config/gcloud`; on macOS
`**/Library/Keychains`, `**/Library/Cookies`), as are the credential
files `**/.netrc`, `**/.pgpass`, `**/.pypirc` and `**/.my.cnf`, each
with its descendant form. `.npmrc` is denied only in home. With no `scope.yaml` every tool
call is denied, and `foo scope` prints the path it looked for. Writing one:
[Let the model use OS commands safely](../how-to/use-os-tools.md#2-write-scopeyaml).

| Subcommand | Synopsis | Description |
|------------|----------|-------------|
| `show` | `foo scope show` | Print the mode and every allow and deny rule |
| `check` | `foo scope check <path> [--op read\|write\|exec]` | Show what a tool call would do with one path |
| `test` | `foo scope test <path>... [--op read\|write\|exec]` | Show what tool calls would do with several paths |

`check` and `test` run each path through the gate's own checks for a
path argument a tool reads (`cat`, `ls`): resolved relative to the
working directory, `~` to home, symlinks (the last one too) and `..`
physically, and judged under the policy's mode, not by the raw rule
match. A path is also refused, as a tool call refuses it, when a `..`
in it climbs out of a directory no rule grants, or when it resolves
through a symlink in such a directory and no allow rule names it as
typed (`~/code/**` for a `~/code` link). `rm`, `mkdir` and a `cp`/`mv`
destination also need write on the parent directory; check that with
`--op write`. Columns: `path` (where the path resolves), `op`,
`decision`, `reason` (the rule, missing rule or refusal behind a
verdict other than `allowed`).

| `decision` | Meaning | Exit |
|------------|---------|------|
| `allowed` | An allow rule covers the path; the call runs | 0 |
| `denied` | `mode: strict` and a deny rule matches or no allow rule covers the path; also every path when no `scope.yaml` exists | 1 |
| `prompt` | `mode: prompt` and the same kind of path: the call asks for approval first, and is denied with no terminal | 8 |
| `warn` | `mode: warn` and the same kind of path: the call runs and logs one warning | 9 |

`test` exits with the most restrictive verdict among its paths
(`denied`, then `prompt`, then `warn`). A `scope.yaml` that does not
load, a bad `--op`, or a path that does not resolve where the scope
grants it (a symlink loop, a `..` after a missing directory) exits 2,
so a broken config never reads as a denial. One that does not resolve
outside the grant is refused like any path there:

```
$ foo scope check ./notes.txt
PATH                     OP    DECISION  REASON
/home/me/proj/notes.txt  read  prompt    no scope rule covers this path
SCOPE_PROMPT: path would prompt: a tool call would ask for approval first, and is denied without a terminal
$ echo $?
8
```

In warn mode a tool call logs one warning per call, naming how many
paths were let through and the first few, rather than one line per
file of a recursive walk.

## Kit conformance annotations

Each leaf carries side-effect, idempotency, and verb annotations
consumed by the kit validator. The frozen audit baseline is
[kit-conformance-baseline.md](kit-conformance-baseline.md).

## Related docs

- [Config reference](config.md) — config keys + env vars.
- [Schema DSL reference](schema-dsl.md) — grammar for `--schema` values.
- [Compatibility](compatibility.md) — kit version requirements.
