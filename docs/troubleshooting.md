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
| `missing X_API_KEY for model "..."` | No key for that scheme in `X_API_KEY`, the secret store, `LLM_API_KEY` or `providers.<scheme>.api_key` in llm.yaml | [Set an API key](#api-key-missing-or-invalid) |
| `auth error (provider "...")` | API key set but rejected by the provider | [Check your key + provider config](#api-key-missing-or-invalid) |
| 401 `Missing Authentication header` from OpenRouter, Groq or another gateway | No key reached the provider; its key is in `OPENAI_API_KEY`, or the URI has an empty `?api_key=` | [Export the scheme's own key](#api-key-missing-or-invalid) |
| `auth error (provider "...")` from a fallback, or a provider you have no key for, while `LLM_API_KEY` is set | `LLM_API_KEY` goes to every keyed scheme without its own key | [Export that scheme's own key](#api-key-missing-or-invalid) |
| `llm.fallback.dropped: fallback has no API key; skipping it fallback=... missing=X_API_KEY` | A fallback entry's scheme has no key; foo runs without that fallback | [Set the fallback's key or remove the entry](#api-key-missing-or-invalid) |
| `model "..." not available (provider "openai")` for an id you never meant to send to OpenAI | Unrecognised model id; foo assumed an OpenAI-compatible provider | [Point foo at the right endpoint](#unknown-model-id-routed-to-openai) |
| `pattern ... not found` | Wrong name / wrong scope | `foo pattern list`; check scope |
| `fragment ... not found` | Wrong alias | `foo fragment list` |
| `unknown tool "..."` | `-T` name is not a built-in, a tool spec or a `foo-tool-*` on `$PATH` | `foo tool list` |
| `tool "..." is unavailable: tool spec ...` | Your tool spec with that name fails to load | [Fix the spec](how-to/write-tool-specs.md#common-issues) |
| Tool call `denied: no scope policy: .../scope.yaml does not exist` | No `scope.yaml`, so no path is granted to tools | [Create it](#tool-calls-denied-or-declined) |
| Tool call `denied: no scope allow rule covers read here` | The path is outside your scope, or resolves outside it | `foo scope check <path>`; [details](#tool-calls-denied-or-declined) |
| Tool call `declined: approval required but cannot be asked: no terminal ...` | A write, delete or `mode: prompt` call with no terminal to ask on | [Run on a terminal or auto-allow](#tool-calls-denied-or-declined) |
| Tool output shows `/private/tmp/...` or `/private/var/...` (macOS) | foo reports canonical paths; `/tmp` and `/var` link into `/private` | Nothing to fix; scope rules written as `/tmp/**` still match |
| Tool call `timeout: ... did not finish within 30s` | The command outran its limit | [Narrow the call](#tool-calls-denied-or-declined) |
| `[foo] warning: skipping tool plugin ...` | The plugin's `--ext-info` `parameters` is not a JSON Schema object of type `object`, or its `foo_tool` annotations cannot be enforced | [Fix the plugin's schema](how-to/write-plugins.md#--ext-info-for-tool-plugins) or [its `foo_tool`](how-to/write-plugins.md#gate-your-plugin-declare-foo_tool) |
| `schema not found and not valid DSL` | `--schema` value is neither saved nor valid DSL | [Fix DSL parse failures](#schema-dsl-parse-failure) |
| `interactive REPL requires a terminal` | `foo repl` invoked without a TTY | [Provide a prompt or run on a TTY](#repl-launched-without-tty) |
| `no prompt provided (stdin was empty)` | `foo` got an empty pipe and no positional prompt | [Diagnose the upstream pipe](#empty-pipe-into-foo) |
| `pattern "X" not found` despite `foo pattern list` showing it elsewhere | Pattern is project-local in another cwd | [Make the pattern global](#pattern-found-here-not-there) |
| `OFFLINE: --offline refused <host>` (exit 10) | `--offline` and a model on a remote endpoint | [Use a local model or drop `--offline`](#--offline-refused-the-model-endpoint) |
| `UNAUTHORIZED` from a `delete` command | Destructive command refused off-TTY | [Use `--confirm=yes`](#destructive-command-refused-with-unauthorized) |
| Empty output or visible garbled bytes | Streaming hiccup | [Disable streaming](#streaming-garbled-or-truncated) |
| `embed: ... 401 Unauthorized` | `OPENAI_API_KEY` missing | Export it |
| `--file is required` | `foo embed file` missing `--file` | Add `--file <path>` |
| `foo status` shows degraded | One or more subsystems missing keys/state | [Read the status surface](#foo-status-shows-degraded-health) |
| `no source provided and stdin is a terminal` | `foo fragment create <alias>` with no source on a TTY | Pipe content or pass a file/URL |

## API key missing or invalid

Two distinct failure modes:

- **Env var unset** — foo refuses to call the provider with an empty
  key and reports `missing <ENV_VAR> for model "<model>" (provider
  <name>); export <ENV_VAR>=... and retry, or switch models with
  foo model default <model>`. The fix is named in the message.
  Each scheme has its own variable — `OPENROUTER_API_KEY` for
  `openrouter://`, `GROQ_API_KEY` for `groq://`, and so on
  ([full list](reference/config.md#provider-keys)). A key in
  `OPENAI_API_KEY` is used only for the `openai` scheme.
- **Key set but rejected** — the provider returns an `auth error
  (provider "<name>")` with the upstream HTTP body. The key is
  malformed, expired, or scoped to a different org/project.

Fix:

```sh
export ANTHROPIC_API_KEY=sk-ant-...
# or
export OPENAI_API_KEY=sk-...
# or, for an OpenRouter model
export OPENROUTER_API_KEY=sk-or-...

foo provider show anthropic   # status should be "configured"
foo "hello"
```

A URI-form `--model` (`-m 'openrouter://openai/gpt-4.1-nano'`)
gets its scheme's key the same way a bare id does; `?api_key=` on
the URI overrides it.

`LLM_API_KEY` also satisfies the check, for any keyed scheme that
has no key of its own — including fallback entries. So does
`providers.<scheme>.api_key` in `$XDG_CONFIG_HOME/hop/llm.yaml`,
for that scheme only. Order: `?api_key=` on the URI, the scheme's
variable, `LLM_API_KEY`, then the file
([key precedence](reference/config.md#key-precedence)).

Fallback entries (`LLM_FALLBACK`, llm.yaml `fallback:`) get their
own scheme's key the same way. A fallback whose key is missing does
not fail the run: foo drops that entry and warns once on stderr
(`llm.fallback.dropped ... missing=OPENROUTER_API_KEY`). Set the
named variable, or remove the entry.

`foo provider list` shows registered schemes; `foo provider show
<scheme>` shows whether a run would find a key, from any of the
sources above, and `--format json` adds `key_source` naming which
one. See [how-to/configure-models.md](how-to/configure-models.md).

## Unknown model id routed to OpenAI

foo maps a bare `--model` id to a provider by prefix: `gpt-`/`o1`/`o3`
to OpenAI, `claude-` to Anthropic, `gemini-` to Google, `llama`/
`mistral`/`deepseek-r1` to Ollama, `router-` to RouteLLM. An id
matching none of these is assumed to be OpenAI-compatible, because most
aggregators (OpenRouter, Groq, Together) are.

A RouteLLM tier name is the common surprise. `private`, `coding`,
`deep` and `fast` are legitimate ids on a RouteLLM server, but they
match no prefix, so foo sends them to OpenAI and the failure names a
provider you never asked for:

```sh
foo -m private "hi"
# model "private" not available (provider "openai"); "private" matched no
# known model prefix, so foo assumed an OpenAI-compatible provider ...
```

The request never reached your router. foo cannot tell a tier name from
a typo without asking a server, so name the scheme yourself:

```sh
foo -m 'routellm://private' "hi"
```

The `routellm://` scheme takes a tier name or the classic
`router-<name>:<threshold>` form; the server resolves the router and
threshold from its own config either way. Its endpoint comes from
`providers.routellm.routellm.base_url` in `llm.yaml` (default
`http://localhost:6060`) — see
[Route across models](how-to/route-across-models.md).

If the id belongs to some other OpenAI-compatible server rather than a
router, point foo at that endpoint instead with `?base_url=`,
`LLM_BASE_URL`, or `providers.openai.base_url` — see
[Use a local endpoint](how-to/use-a-local-endpoint.md).

To confirm which ids foo can actually reach, run `foo model list`.

When the same message names an id you *did* mean for OpenAI (say
`gpt-4o-mini` misspelled), no hint is appended: the prefix matched, so
foo made no assumption to explain. Check the spelling against
`foo model list`.

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

## REPL launched without TTY

Running plain `foo` with no positional argument and no piped
stdin attempts to open the REPL, which requires a terminal.

If you got `interactive REPL requires a terminal`, either:

- Supply a prompt: `foo "hello"`.
- Pipe a prompt: `echo hello | foo`.
- Run from a real terminal (not a non-TTY subprocess).

## Empty pipe into foo

`no prompt provided (stdin was empty)` means `foo` was invoked
with no positional prompt and stdin was a closed or empty pipe.
Most often this is a cascade: the upstream command in the pipeline
failed and produced no output.

```sh
foo youtube "https://..." | foo -p summarize
# foo: no prompt provided (stdin was empty)
```

Diagnose by running the upstream alone and watching stderr:

```sh
foo youtube "https://..." 2>&1 1>/dev/null
# look for the actual error
```

If the upstream succeeds, the pipe is fine and the receiving
`foo` has its own issue (missing pattern, missing key) — check
those independently.

## Pattern found here, not there

You ran `foo pattern list` in one directory and saw `summarize`.
You ran `foo -p summarize "..."` in another directory and got
`pattern "summarize" not found`. The pattern is **project-local**:
it lives at `./.foo/patterns/summarize/system.md` in the first
cwd, not in the user-global store.

To make a pattern available from any cwd, import it into the user
store:

```sh
# from the directory that has the project-local pattern
foo pattern import .foo/patterns/summarize/system.md summarize
# pattern imported
```

After import the pattern lives at
`$XDG_CONFIG_HOME/foo/patterns/summarize/system.md` and resolves
regardless of cwd. See
[manage-patterns.md](how-to/manage-patterns.md#scope-and-cwd) for
the full lookup rules.

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

## Tool calls denied or declined

With `-T`, foo checks every path an OS tool (`ls`, `cat`, `rm`, …)
would touch before the command starts. The refusal goes back to the
model; `--tools-debug` prints it on stderr too:

```
[tool] error: ls: denied: path /: no scope allow rule covers read here
```

- **`no scope policy: <path>/scope.yaml does not exist`**: every call
  is denied until you create that file:
  `$XDG_CONFIG_HOME/foo/scope.yaml` (`XDG_CONFIG_HOME` unset:
  `~/.config` on Linux, `~/Library/Application Support` on macOS);
  `foo scope show` prints it.
- **`no scope allow rule covers <op> here`** or **`matches a scope
  deny rule`**: run `foo scope check <path> --op <op>` from the same
  directory. It checks the path the way the call did (relative to
  where foo ran, symlinks and `..` followed, a `..` or link through a
  directory outside the scope refused) and shows where it resolves and
  which rule or refusal decides. Secrets (`.env`, keys, `~/.ssh`, …)
  are always denied.
- **`declined: approval required but cannot be asked: no terminal`**:
  the call writes or deletes (or `mode: prompt` wants to ask) and foo
  has no terminal to ask on (CI, cron, a `foo-tool-*` link run by
  another host). Answers are never read from stdin. Run on a
  terminal, or auto-allow that side effect in `tool-policy.yaml`.
- **`declined: the user declined this call`**: you answered no.
- **`timeout`**: OS tools stop after 30 seconds (60 for `find`, `grep`,
  `cp`, `mv` and `rm`), plugins after 30; partial output is discarded.
  A FIFO or a huge tree is the usual cause: narrow the path, set
  `maxdepth`, or use `head` instead of `cat`.

Walkthrough: [Let the model use OS commands safely](how-to/use-os-tools.md).

## `foo status` shows degraded health

`foo status` is the kit-shipped health probe. It boots cleanly
even when the rest of foo is offline and reports the state of
profile, env, workspace, auth, and config.

A degraded entry usually means one of:

- Missing env var the active subsystem expects (e.g.
  `ANTHROPIC_API_KEY` for the Anthropic provider).
- Workspace store not yet initialized.
- Config file at a path foo cannot read.

Ask for a structured format to read the detail:

```sh
foo status --format json
```

`--format json` and `--format yaml` are currently the only formats
that print anything. The default table format renders nothing,
because the status payload carries no column tags for the table
renderer to pick up — a kit-side gap, not a foo misconfiguration.
`--verbose` (`-V`) does not work around it.

Address the specific entry and re-run; status is purely
informational and never mutates state.

## `--offline` refused the model endpoint

`--offline` only lets foo reach model endpoints on this machine: a
`base_url` host of `localhost`, `127.0.0.0/8` or `[::1]`. Any other
endpoint, including a provider's default
(`api.openai.com`, …), fails before the request is sent:

```
OFFLINE: --offline refused api.openai.com: only loopback endpoints (localhost, 127.0.0.0/8, ::1) are reachable offline
```

The exit code is 5. Fix it one of two ways:

- Point the call at a model you serve locally, e.g.
  `LLM_BASE_URL=http://127.0.0.1:11434/v1 foo --offline -m llama3 "hi"`
  ([details](how-to/use-a-local-endpoint.md)).
- Drop `--offline` if the call is meant to reach the provider.

A host name that resolves to loopback still counts as remote,
because resolving it would touch the network. Write the address
instead (`127.0.0.1`, `[::1]`) or `localhost`.

If a local primary model fails and a remote fallback is configured,
the refusal names the fallback's host. Check that the local server
is up.

## Local endpoint: wrong host, or a nonsensical context-length error

Two symptoms when pointing foo at a self-hosted OpenAI-compatible
server.

**Requests still reach `api.openai.com`.** The `base_url` never
resolved. It belongs under `providers:` → `openai:` in
`$XDG_CONFIG_HOME/hop/llm.yaml` (not at the top level):

```yaml
providers:
  openai:
    base_url: http://127.0.0.1:8000/v1
```

`LLM_BASE_URL` overrides the file, and `?base_url=` on `--model`
overrides both.

**A context-length error that does not match your prompt**, e.g.
"maximum context length is 8192 tokens, however your messages
resulted in at least 19 tokens". The server is asking for an
explicit cap, not reporting a real overflow:

```sh
foo --max-tokens 512 -m my-local-model "hello"
```

**`model "..." not available`** against a local server is kit
mapping a 404. Check the id against `foo model list --endpoint
http://127.0.0.1:8000/v1 --refresh`, and check that `base_url` is
not doubling the prefix (`/v1/v1/chat/completions`).

Full walkthrough:
[how-to/use-a-local-endpoint.md](how-to/use-a-local-endpoint.md).

## Related docs

- [How to: confirm destructive ops](how-to/confirm-destructive-ops.md)
- [How to: configure models](how-to/configure-models.md)
- [How to: use a local endpoint](how-to/use-a-local-endpoint.md)
- [How to: let the model use OS commands safely](how-to/use-os-tools.md)
- [Reference: schema DSL](reference/schema-dsl.md)
- [Reference: config](reference/config.md)
