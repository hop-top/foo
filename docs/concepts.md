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
shape and where they go in the prompt:

| Concept | Shape | Goes into | Stored at |
|---------|-------|-----------|-----------|
| Pattern | A named system prompt | System role | `$XDG_CONFIG_HOME/foo/patterns/<name>/system.md` |
| Strategy | A wrapper that decorates the system prompt | System role | Built-in (some user-extensible) |
| Fragment | A named text snippet | User role (`-f`) or system role (`--system-fragment`) | Workspace store (WSM) |
| Schema | A structured-output JSON contract | System role (appended) | Local SQLite under XDG state dir |

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
`--chain-limit` iterations (default 5).

Two built-in tools ship with foo (`foo_time`, `foo_version`).
Additional tools are discovered as external binaries on `$PATH`
matching `foo-tool-<name>`. They speak the same `--ext-info`
metadata protocol as plugin commands.

## Plugins via PATH discovery

`foo <name>` dispatches to a binary on `$PATH` named `foo-<name>`
when no built-in subcommand matches. This is the Git extension
convention: write a plugin in any language, drop it on the PATH,
and it becomes a foo subcommand. Implement `--ext-info` to return
metadata in the standard JSON shape.

Plugins typically emit markdown on stdout, which composes with
foo's stdin reader: `foo youtube ... | foo -p summarize`.

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
  introspect profile, env, workspace, auth, and config.
- **Config layering.** `-c/--config` (a kit global) layers extra
  config files or key=value overrides on top of the discovered
  user/project config.

The frozen acceptance snapshot for this contract is
[reference/kit-conformance-baseline.md](reference/kit-conformance-baseline.md).

## Where state lives

foo follows the XDG base-directory spec:

| What | Where |
|------|-------|
| Config | `$XDG_CONFIG_HOME/foo/config.yaml` (default `~/.config/foo/config.yaml`) |
| Patterns | `$XDG_CONFIG_HOME/foo/patterns/` |
| Embeddings DB | `$XDG_STATE_HOME/foo/embeddings.db` |
| Schemas DB | `$XDG_STATE_HOME/foo/schemas.db` |
| Workspace events (fragments included) | WSM workspace store under `$XDG_STATE_HOME` |

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
