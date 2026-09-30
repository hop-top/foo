# How foo works

A mental model for foo: what it is, what it is not, and how the
pieces fit together. Read this before going deep into any how-to.

## Use this when

- You are evaluating whether foo solves a problem you have.
- You want to understand the vocabulary before reading task guides.
- A command surprised you and you want to know why it did what it did.

## What foo is

foo is a terminal-first LLM workflow tool. It assembles a prompt
from local inputs, sends it to a model provider, and returns the
response on stdout. Everything else — patterns, strategies,
fragments, schemas, embeddings, tools — is a way to control the
assembled prompt or the response shape.

foo is not a long-running daemon, not a chat backend, and not a
local model runtime. It calls remote provider APIs (Anthropic,
OpenAI, or OpenAI-compatible endpoints).

## The prompt assembly pipeline

Every foo invocation produces one prompt:

```
inputs → assembly → LLM → output
```

Inputs come from four sources, in priority order:

1. The positional argument (`foo "explain quicksort"`).
2. Piped stdin (`cat code.go | foo "find bugs"`).
3. A named pattern selected with `-p` (system prompt).
4. Fragments selected with `-f` (user-prompt addenda) or
   `--system-fragment` (system-prompt addenda).

A strategy wrapper (`-s`) can re-shape the system prompt — for
example chain-of-thought wraps it with reasoning scaffolding.

A schema selection (`--schema` or `--schema-multi`) appends a JSON
contract to the system prompt so the model returns structured
output.

`--dry-run` prints the fully assembled prompt and exits without
calling the model. Use it to verify what foo will actually send.

## Patterns, strategies, fragments, schemas

These are foo's four reusable inputs. They differ in what they
shape and where they go in the prompt. Picking which model
receives the assembled prompt is its own surface — pool routing
under `--budget` (the recommended path), a fallback chain
layered underneath any pick, or RouteLLM strong/weak routing
per request — covered in
[how-to: route across models](how-to/route-across-models.md).

| Concept | Shape | Goes into | Stored at |
|---------|-------|-----------|-----------|
| Pattern | A named system prompt | System role | `$XDG_CONFIG_HOME/foo/patterns/<name>/system.md` |
| Strategy | A wrapper that decorates the system prompt | System role | Built-in (some user-extensible) |
| Fragment | A named text snippet | User role (`-f`) or system role (`--system-fragment`) | Workspace store (WSM) |
| Schema | A structured-output JSON contract | System role (appended) | `$XDG_STATE_HOME/foo/schemas.db` (SQLite) |

Each can be authored, listed, shown, and (where applicable)
deleted via its grouped subcommand. See the matching how-to:

- [Manage patterns](how-to/manage-patterns.md)
- [Use strategies](how-to/use-strategies.md)
- [Manage fragments](how-to/manage-fragments.md)
- [Manage schemas](how-to/manage-schemas.md)

## Embeddings: a local semantic index

`foo embed` builds a local vector index from text or files. The
provider's embedding API turns each input into a vector; foo
stores vectors in a SQLite database under the XDG state directory.

A search request (`foo embed search`) embeds the query, finds the
nearest neighbors in the named collection, and returns ranked
matches. The result set is the index of relevant content — foo
does not feed it back into the model on its own. Compose with the
main prompt path explicitly when you want retrieval-augmented
generation.

Collections are namespaces over the same store. List them with
`foo embed collection list`; delete them with
`foo embed collection delete`.

Embeddings live entirely on disk. Nothing is shipped to the model
provider beyond the embedding API call itself.

## Tools: agentic dispatch

foo supports function calling via `-T <name>`. With `-T` set, foo
runs an agentic dispatch loop: the model can request a tool call,
foo runs the tool, and the result is fed back. The loop bounds at
`--chain-limit` iterations (default 5). `foo tool list` shows every
name `-T` accepts and where each one comes from; an unknown name is
an error, never silently dropped.

Tools come from three places, and a name has one owner, in this
order:

| Kind | Examples | Runs | Paths checked |
|------|----------|------|---------------|
| Go built-ins | `foo_time`, `foo_version` | In foo | No paths |
| OS tools from specs | `ls`, `cat`, `grep`, `rm`, `sed`, … and your own | The pinned OS binary, started by foo | Yes, against `scope.yaml` |
| Plugins | `foo-tool-<name>` on `$PATH` | The plugin binary | No: the plugin opens what it likes |

**OS tools.** Thirteen ship with foo (read: `ls`, `cat`, `head`,
`tail`, `wc`, `find`, `grep`, `stat`; write: `cp`, `mv`, `mkdir`,
`rm`, `sed`). Each is a YAML spec: typed parameters, a fixed argument
template, and which parameters are paths and what the command does to
them. The model fills in parameters; it never writes a command line.
Before the command starts, foo resolves every path, checks it against
your scope for that operation, and asks you when the call writes or
deletes. No `scope.yaml` means every call is denied. Walkthrough:
[Let the model use OS commands safely](how-to/use-os-tools.md). You
can add or replace specs
([Add or change an OS tool](how-to/write-tool-specs.md)) and expose
them to other hosts as `foo-tool-<name>` links
([Share foo's OS tools](how-to/share-os-tools.md)).

**Plugins.** `foo-tool-<name>` binaries speak the same `--ext-info`
metadata protocol as plugin commands, plus an optional `parameters`
JSON Schema that tells the model which arguments to pass
([Write plugins](how-to/write-plugins.md#--ext-info-for-tool-plugins)).
foo cannot see what a plugin does with its arguments, so it applies
no path scope to it; `--tools-approve` is the only gate.

## Plugins via PATH discovery

`foo <name>` dispatches to a binary on `$PATH` named `foo-<name>`
when no built-in subcommand matches. This is the Git extension
convention: write a plugin in any language, drop it on the PATH,
and it becomes a foo subcommand. `foo-tool-<name>` binaries are
the exception: they are LLM tools, never subcommands. Implement
`--ext-info` to return metadata in the standard JSON shape.

Plugins typically emit markdown on stdout, which composes with
foo's stdin reader: `foo youtube ... | foo -p summarize`. End-to-end
walkthrough: [Use plugins](how-to/use-plugins.md).

## The kit surface

foo is built on `hop.top/kit`'s CLI contract. That contract gives
every command a small set of guarantees you can rely on:

- **Side-effect classification.** Every leaf advertises whether it
  reads, writes locally, writes shared state, mutates destructively,
  or is interactive.
- **Idempotency.** Every leaf declares whether repeated invocations
  produce the same effect.
- **Confirmation policy.** Destructive leaves consult the global
  `--confirm` flag (provided by kit, not foo). On a TTY the default
  is `prompt`; without a TTY the default is `no`, refusing the
  action.
- **Status surface.** `foo status` is the kit-shipped health probe.
  It boots cleanly even when the rest of foo is offline; use it to
  introspect profile, env, workspace, auth, and config. Pass
  `--format json` or `--format yaml` — the default table format
  prints nothing today.
- **Config layering.** `-c/--config` (a kit global) layers extra
  config files or key=value overrides on top of the discovered
  user/project config.

The frozen acceptance snapshot for this contract is
[reference/kit-conformance-baseline.md](reference/kit-conformance-baseline.md).

## Where state lives

foo follows the XDG base-directory spec. With a variable unset,
`XDG_CONFIG_HOME` and `XDG_STATE_HOME` default to `~/.config` and
`~/.local/state` on Linux, and both to `~/Library/Application Support`
on macOS; see [XDG variables](reference/config.md#xdg-variables-kit-shared).

| What | Where |
|------|-------|
| Config | `$XDG_CONFIG_HOME/foo/config.yaml` |
| Model pool and providers (kit's, shared) | `$XDG_CONFIG_HOME/hop/llm.yaml` |
| Patterns | `$XDG_CONFIG_HOME/foo/patterns/` |
| Embeddings DB | `$XDG_STATE_HOME/foo/embeddings.db` |
| Schemas DB | `$XDG_STATE_HOME/foo/schemas.db` |
| Workspace events (fragments included) | WSM workspace store, `$XDG_STATE_HOME/foo/workspace.db` |
| Tool path scope | `$XDG_CONFIG_HOME/foo/scope.yaml` |
| Tool approval overrides | `$XDG_CONFIG_HOME/foo/tool-policy.yaml` |
| Your tool specs | `$XDG_CONFIG_HOME/foo/tools/<name>.yaml` |
| `foo tool install` link manifest | `$XDG_STATE_HOME/foo/tool-shims.json` |

Project-local overrides live next to the project root: `.foo.yaml`
for config, `.foo/patterns/` for patterns.

## What foo never does

- foo does not send anything to a provider unless you invoke a
  command that requires it.
- foo does not store prompt history or responses in a cloud
  service. Session events are recorded locally by WSM.
- foo does not collect telemetry.

Use `--dry-run` whenever you want to verify the exact bytes that
would leave your machine.

## Related docs

- [Quickstart](quickstart.md) — guided 5-minute walkthrough.
- [Reference: commands](reference/commands.md) — every verb at a glance.
- [Reference: config](reference/config.md) — config keys + env vars.
- [Reference: kit-conformance baseline](reference/kit-conformance-baseline.md) — frozen contract snapshot.
