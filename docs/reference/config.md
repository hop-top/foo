# Config reference

Configuration sources, precedence, keys, and environment-variable
equivalents.

## Sources and precedence

foo loads configuration in layers; later layers override earlier
ones.

| Layer | Path | Order |
|-------|------|-------|
| Built-in defaults | (compiled) | Lowest |
| System | `/etc/foo/config.yaml` | |
| User | `$XDG_CONFIG_HOME/foo/config.yaml` ([defaults](#xdg-variables-kit-shared)) | |
| Project | `.foo.yaml` (in the current directory) | |
| Extra `-c` files | Per `-c <path>` invocation | |
| Environment | `FOO_*` variables | |
| `-c key=value` | Per `-c key=value` invocation | Highest |

`-c` is supplied by kit, not foo. Its semantics:

- `-c <path>` loads an additional config file after the
  discovered ones.
- `-c key=value` overrides a single value (dotted keys for
  nesting; the value is parsed as YAML, then falls back to a
  literal string).
- `-c` flags win over any file layer.

### What foo writes

foo reads every layer but writes only the user file, and only the
key a command sets: `foo model default <id>` writes `model` and
leaves the rest of `$XDG_CONFIG_HOME/foo/config.yaml` as it is —
other keys, comments, blank lines and order. Values from the
project file, `FOO_*` variables, `-c` and `--profile` are never
copied into it. A missing file is created holding just that key
(mode 0644: the file names a secret backend, never a secret); an
existing file keeps its mode and, if it is a symlink, stays one. A
file that does not parse is reported and left untouched.

## Configuration keys

| Key | Type | Default | Description | Env equivalent |
|-----|------|---------|-------------|----------------|
| `model` | string | `claude-3-5-sonnet-latest` | Default LLM model id | `FOO_MODEL` |
| `patterns_path` | string | `$XDG_CONFIG_HOME/foo/patterns` | Directory for pattern files | `FOO_PATTERNS_PATH` |
| `accent` | string | `#E040FB` | TUI accent color (hex) | `FOO_ACCENT` |
| `budget` | string | `""` (= `balanced` at use) | Persistent pool routing tier. Env wins. | `FOO_BUDGET` |
| `secrets.backend` | string | `env` | Secret store backend: `env` or `keyring` ([secret store](#secret-store)) | `FOO_SECRETS_BACKEND` |
| `secrets.prefix` | string | `""` | Env var prefix for `env` lookups; ignored by `keyring` | `FOO_SECRETS_PREFIX` |
| `secrets.service` | string | `""` (= `foo` at use) | Keychain service name for `keyring`; `--profile` replaces it; ignored by `env` | `FOO_SECRETS_SERVICE` |

### Example `config.yaml`

```yaml
model: claude-3-5-sonnet-latest
patterns_path: /home/me/prompts/patterns
accent: "#E040FB"

secrets:
  backend: env
  prefix: ""
  service: ""
```

### Secret store

`secrets:` configures where foo looks for provider API keys. One
store serves every key lookup: a run's key check and its fallback
entries, `foo provider show`, `foo model list` and the `foo embed`
commands' OpenAI key. A key the listing reports as configured is
the key a run uses.

A provider's key is stored under its env var's name in lowercase
(`openrouter_api_key` for `OPENROUTER_API_KEY`). The store is
asked under each of the provider's key names first, then the env
vars of those names ([key precedence](#key-precedence)). A backend
error counts as "not in the store": the env var still answers.

| Backend | Reads | Settings |
|---------|-------|----------|
| `env` (default) | The env var the name maps to: `openrouter_api_key` reads `OPENROUTER_API_KEY`; with `prefix: FOO_`, `FOO_OPENROUTER_API_KEY` | `prefix` |
| `keyring` | The OS keychain (macOS Keychain, Secret Service on Linux, Windows Credential Manager): item with service `service`, account the key name | `service`; empty uses `foo` |

`--profile <name>` sets `service` to `<name>` in place of `foo`, so
each aps profile keeps its own keys; it has no effect on `env`.

Store a key for the `keyring` backend with the OS tool; foo only
reads it:

```sh
# macOS (prompts for the value)
security add-generic-password -s foo -a openrouter_api_key -w
# Linux (Secret Service)
secret-tool store --label='foo openrouter' service foo username openrouter_api_key
```

```yaml
secrets:
  backend: keyring   # service defaults to foo
```

If foo cannot open the configured backend — any name other than
`env` or `keyring` — it warns on stderr (`secrets.store.unavailable`)
and reads keys from env vars only.

## Kit `llm.yaml` (routing surface)

Model routing lives in kit's config file, not foo's. Path:
`$XDG_CONFIG_HOME/hop/llm.yaml` ([defaults](#xdg-variables-kit-shared)).
foo reads this file via `kit/llm.LoadConfig` when constructing
the LLM client, so the keys below shape every `foo` invocation
that talks to a model.

```yaml
# Default URI used when foo's --model / FOO_MODEL / model
# config key are all unset.
default: anthropic://claude-3-5-sonnet-latest

# Per-scheme provider overrides. Extras (anything under a
# scheme but not the four typed fields) are passed through to
# the adapter — used by routellm below.
providers:
  anthropic:
    api_key: sk-ant-...
    base_url: ""
    model: ""
  openai:
    api_key: sk-...
  routellm:
    routellm:
      base_url: http://localhost:6060
      strong_model: gpt-4o
      weak_model: gpt-4o-mini
      routers: [mf, bert]

# Candidate pool the budget picker scores when no --model / -m is
# set. foo writes a default block when it creates llm.yaml on first
# run (mode 0600: api_key values live in this file); it never edits
# an existing llm.yaml. Edit to taste.
pool:
  - alias: cheap-openai     # optional shorthand for LLM_POOL_DISABLE
    scheme: openai          # required: URI scheme in kit's registry
    model: gpt-4o-mini      # required: model id in models.dev
    # enabled: false        # optional, default true
    # weight: 2.0           # optional, default 1.0
  - alias: balanced-anthropic
    scheme: anthropic
    model: claude-3-5-sonnet-latest

# Ordered list of fallback URIs tried by kit's Client when the
# primary completion returns a fallbackable error.
fallback:
  - openai://gpt-4o-mini
  - anthropic://claude-3-haiku-20240307
```

| Field | Type | Purpose |
|-------|------|---------|
| `default` | string | URI used when no model is specified. Overridden by `LLM_PROVIDER` env. |
| `providers.<scheme>.api_key` | string | Provider key for that scheme only; satisfies foo's key precheck. Outranks the scheme's env var (e.g. `OPENAI_API_KEY`), the secret store and `LLM_API_KEY`; only `?api_key=` on the URI beats it ([key precedence](#key-precedence)). |
| `providers.<scheme>.api_key_env` | string | Name of the variable holding that scheme's key, for a key kept under another name (`MY_OR_KEY`). Looked up before the scheme's own names, in the secret store and the environment ([key precedence](#key-precedence)). |
| `providers.<scheme>.base_url` | string | Custom base URL for that scheme: bare id, URI-form `--model`, pool pick and fallback entries alike; `providers.openai.base_url` also serves `foo embed`. Overridden by `LLM_BASE_URL` (primary's scheme only) and by a `?base_url=` param on the URI ([base URL precedence](#base-url-precedence)). See [use a local endpoint](../how-to/use-a-local-endpoint.md). |
| `providers.<scheme>.model` | string | Default model for this scheme; URI model wins when set. |
| `providers.routellm.routellm.base_url` | string | RouteLLM server URL. Overridden by `ROUTELLM_BASE_URL`. |
| `providers.routellm.routellm.strong_model` | string | Strong-tier model RouteLLM picks above threshold. Overridden by `ROUTELLM_STRONG_MODEL`. |
| `providers.routellm.routellm.weak_model` | string | Weak-tier model RouteLLM picks below threshold. Overridden by `ROUTELLM_WEAK_MODEL`. |
| `providers.routellm.routellm.routers` | list | Enabled router names. Overridden by comma-separated `ROUTELLM_ROUTERS`. |
| `pool` | list of entries | Candidate set the budget picker scores; see fields below. |
| `pool[].alias` | string | Optional shorthand; entries with an alias can be muted via `LLM_POOL_DISABLE` by name. |
| `pool[].scheme` | string | URI scheme (e.g. `openai`, `anthropic`, `google`, `ollama`). |
| `pool[].model` | string | Model id as it appears on models.dev. |
| `pool[].enabled` | bool | Default `true`. Set `false` to keep the entry but mute it. |
| `pool[].weight` | float | Default `1.0`. Reserved for future load-distribution policy. |
| `fallback` | list of URIs | Tried in order on retriable primary failure. Overridden by `LLM_FALLBACK` env. Each entry gets its own scheme's key, resolved as for the primary model ([key precedence](#key-precedence)); an entry whose key is missing is dropped with one stderr warning, and the run continues. Each entry also gets its scheme's configured base URL ([base URL precedence](#base-url-precedence)). |

End-to-end walkthrough:
[how-to: route across models](../how-to/route-across-models.md).

## Environment variables

### Provider keys

| Variable | Scheme | Required for |
|----------|--------|--------------|
| `ANTHROPIC_API_KEY` | `anthropic` | Claude models |
| `OPENAI_API_KEY` | `openai` | GPT/o-series models, all embeddings, and bare model ids foo does not recognise (sent to the `openai` scheme, e.g. a [local endpoint](../how-to/use-a-local-endpoint.md)) |
| `GOOGLE_API_KEY`, then `GEMINI_API_KEY` | `google`, `gemini` | Gemini models |
| `OPENROUTER_API_KEY` | `openrouter` | OpenRouter models (`openrouter://<vendor>/<model>`) |
| `GROQ_API_KEY` | `groq` | Groq models |
| `XAI_API_KEY` | `xai` | xAI (Grok) models |
| `TOGETHER_API_KEY` | `together`, `togetherai` | Together AI models |
| `FIREWORKS_API_KEY` | `fireworks`, `fireworks-ai` | Fireworks AI models |
| `DEEPSEEK_API_KEY` | `deepseek` | DeepSeek hosted models |
| `MISTRAL_API_KEY` | `mistral` | Mistral hosted models |
| `OLLAMA_API_KEY`, `ROUTELLM_API_KEY` | `ollama`, `routellm` | Optional: sent when set (an authenticating proxy), never required |

Kit decides which variables a scheme reads (`kitllm.ApplyAPIKey`);
foo passes the scheme through. Each scheme reads its own
variables. An OpenRouter or Groq model never borrows
`OPENAI_API_KEY`, so a real OpenAI key is never sent to another
provider. `ollama`, `lmstudio` and `routellm` are local and need
no key.

A models.dev provider id works as a scheme too, with the
provider's own key: `fireworks-ai://...` and `togetherai://...`
reach the `fireworks` and `together` adapters, and any other
catalog provider speaking an OpenAI-compatible protocol reaches
the `openai` adapter at the catalog's base URL, reading the key
variable the catalog lists (`digitalocean://...` reads
`DIGITALOCEAN_ACCESS_TOKEN`). Catalog facts come from the cached
catalog only (`foo model list` fetches it when stale, `--refresh`
forces it); a run never fetches it. Without a cached catalog, only the schemes above and
their aliases resolve.

#### Key precedence

For a keyed scheme, foo resolves the key through kit
(`kitllm.ApplyAPIKey`) in this order and stops at the first hit:

| # | Source | Example |
|---|--------|---------|
| 1 | `?api_key=` on the URI | `-m 'openrouter://openai/gpt-4.1-nano?api_key=sk-or-...'` |
| 2 | `providers.<scheme>.api_key` in `$XDG_CONFIG_HOME/hop/llm.yaml` | `providers: {openrouter: {api_key: sk-or-...}}` |
| 3 | The scheme's key names — the variable `providers.<scheme>.api_key_env` names first, then the scheme's own — looked up in the secret store, then in the environment | `OPENROUTER_API_KEY`; `GOOGLE_API_KEY` then `GEMINI_API_KEY`; `providers: {openrouter: {api_key_env: MY_OR_KEY}}` |
| 4 | `LLM_API_KEY` | universal key |

Kit's `Resolve` sends only the URI's `api_key`, so foo appends
the resolved key to the URI and the key it prechecks is the key
sent. A file key belongs to its scheme:
`providers.openai.api_key` never satisfies an `openrouter` model.
An alias scheme without a block of its own uses its adapter's
(`fireworks-ai://` reads `providers.fireworks`).

`LLM_API_KEY` is not tied to a provider: it goes to every keyed
scheme that has no key of its own, fallback entries included. It
is never sent to a local runtime (`ollama`, `lmstudio`,
`routellm`). With more than one provider in play, prefer
per-scheme variables so one provider's key is never sent to
another host.

Earlier foo versions ranked the llm.yaml key last, below the env
var and `LLM_API_KEY`; it now outranks both. `google`/`gemini`
also accept `GEMINI_API_KEY`, and a set `OLLAMA_API_KEY` /
`ROUTELLM_API_KEY` is sent to that runtime.

The same key applies whether you pick the model by bare id, by
pool, or as a URI: `-m 'openrouter://openai/gpt-4.1-nano'` gets
`OPENROUTER_API_KEY` appended. A URI that already carries
`?api_key=` is sent as written. A missing key fails before any
request (exit 5) with `missing OPENROUTER_API_KEY for model
"..."`, naming the highest-precedence variable.

Fallback entries (`LLM_FALLBACK`, llm.yaml `fallback:`) get their
own scheme's key the same way. A fallback whose key is missing is
dropped rather than failing the run — the primary may be fine —
and foo warns once on stderr:
`llm.fallback.dropped: fallback has no API key; skipping it
fallback=openrouter://... missing=OPENROUTER_API_KEY`.

`foo provider show <scheme>` reports each scheme's expected key
and whether a run would find it; it takes a catalog provider id
(`fireworks-ai`) as well as a scheme.

#### Base URL precedence

Every URI foo sends — bare id, URI-form `--model`, pool pick,
each fallback entry, and the `openai` URI behind the `foo embed`
commands — resolves its endpoint the same way, stopping at the
first hit:

| # | Source | Applies to |
|---|--------|------------|
| 1 | `?base_url=` on the URI | That URI; never replaced |
| 2 | `LLM_BASE_URL` | The primary model, and fallbacks on the primary's scheme |
| 3 | `providers.<scheme>.base_url` in llm.yaml | Any URI of that scheme |
| 4 | The adapter's default | Public API for hosted schemes; local default for `ollama`, `lmstudio` |

A host-form URI (`scheme://host:port/model`) names its endpoint
and is sent as written.

`LLM_BASE_URL` is one server for any scheme, so it stops at the
primary's scheme. With `LLM_BASE_URL=http://127.0.0.1:8000/v1`,
`-m my-model` (openai scheme) and `LLM_FALLBACK=anthropic://claude-...`,
the fallback goes to Anthropic, or to `providers.anthropic.base_url`
if set — never to the local server, which would receive the
Anthropic key. An `openai://` fallback in the same run does go to
the local server. Pin a fallback elsewhere with `?base_url=` on
its entry.

`foo embed add|file|search` make one call on one model,
`openai://text-embedding-3-small`, which is that call's primary:
`LLM_BASE_URL` applies, then `providers.openai.base_url`, then
OpenAI's API. The base is the API root, as for a run; foo posts to
`<base>/embeddings` (`http://127.0.0.1:8000/v1` receives
`/v1/embeddings`; the default is
`https://api.openai.com/v1/embeddings`).

### Kit routing env vars

These are consumed by `kit/llm` (not foo directly), but they
take effect on every `foo` invocation.

| Variable | Used by | Purpose |
|----------|---------|---------|
| `LLM_PROVIDER` | `LoadConfig` | Default URI when no model is specified. Overrides `default:` in `llm.yaml`. |
| `LLM_API_KEY` | foo key precheck, `LoadConfig` | Universal key for any keyed scheme with no key of its own, fallbacks included; satisfies foo's precheck. A per-scheme variable outranks it ([key precedence](#key-precedence)). |
| `LLM_BASE_URL` | foo, `LoadConfig` | Custom base URL for the primary model, for fallback entries on the primary's scheme, and for `foo embed`'s embeddings call. Overrides `providers.<scheme>.base_url` there; a `?base_url=` param on the URI overrides both. Never applied to a fallback on another scheme ([base URL precedence](#base-url-precedence)). |
| `LLM_FALLBACK` | `LoadConfig` | Comma-separated fallback URIs. Overrides `fallback:` in `llm.yaml`. |
| `LLM_POOL_DISABLE` | `LoadPool` | Comma list of `alias` or `<scheme>:<model>` entries to mute without removing. |
| `LLM_PICKER_TRACE` | picker | When set to a truthy value (`1`, `true`, `on`, `yes`) emits one structured slog line per pick on stderr. `--picker-debug` sets this implicitly. |
| `ROUTELLM_BASE_URL` | routellm adapter | RouteLLM server endpoint. |
| `ROUTELLM_STRONG_MODEL` | routellm adapter | Strong-tier model id. |
| `ROUTELLM_WEAK_MODEL` | routellm adapter | Weak-tier model id. |
| `ROUTELLM_ROUTERS` | routellm adapter | Comma-separated router names. |

### foo-specific env vars

| Variable | Overrides | Notes |
|----------|-----------|-------|
| `FOO_MODEL` | `model` | Default model id |
| `FOO_BUDGET` | `--budget` | Pool routing tier fallback (`cheap` / `balanced` / `premium`). |
| `FOO_PATTERNS_PATH` | `patterns_path` | Directory for patterns |
| `FOO_ACCENT` | `accent` | TUI accent color |
| `FOO_SECRETS_BACKEND` | `secrets.backend` | Secret backend (`env`, `keyring`) |
| `FOO_SECRETS_PREFIX` | `secrets.prefix` | Env var prefix (`env` backend) |
| `FOO_SECRETS_SERVICE` | `secrets.service` | Keychain service (`keyring` backend; default `foo`) |
| `FOO_CACHE` | `$XDG_CACHE_HOME/foo` | Directory holding foo's own caches, e.g. `foo model list --endpoint`'s inventory store. |
| `FOO_CACHE_TTL` | (per-cache default) | Freshness window for foo's own caches, as a Go duration (`30s`, `1h`). `0` disables caching. Does not affect the aim catalog's 24h window, which aim owns. |

### XDG variables (kit-shared)

foo follows XDG base directories. The defaults below are what
applies when the variable is unset.

| Variable | Linux default | macOS default | Holds |
|----------|---------------|---------------|-------|
| `XDG_CONFIG_HOME` | `~/.config` | `~/Library/Application Support` | `foo/config.yaml`, `foo/patterns/`, `foo/strategies/`, tool files `foo/scope.yaml`, `foo/tool-policy.yaml`, `foo/tools/`; kit's `hop/llm.yaml` |
| `XDG_STATE_HOME` | `~/.local/state` | `~/Library/Application Support` | `foo/embeddings.db`, `foo/schemas.db`, `foo/workspace.db` (WSM workspace store, fragments included), upgrade state, `foo/tool-shims.json` (`foo tool install` links), `foo/tool-flavors.json` (detected BSD/GNU/busybox flavor per binary) |
| `XDG_DATA_HOME` | `~/.local/share` | `~/Library/Application Support` | `wsm/machine-id` (WSM's machine identity) |
| `XDG_CACHE_HOME` | `~/.cache` | `~/Library/Caches` | `foo/model-endpoint-cache.db`, `hop/aim/` (models.dev catalog) |
| `XDG_BIN_HOME` | `~/.local/bin` | `~/.local/bin` | `foo/` (`foo tool install` links) |

## Kit `-c/--config` interaction

`-c` and `-c key=value` are applied last, after env. This is
intentional: command-line overrides always win, so a CI job that
sets `-c model=gpt-4o-mini` does not silently lose to a
project-local `.foo.yaml`.

Examples:

```sh
# Layer an extra config file on top of the discovered ones
foo -c ./ci.foo.yaml "some prompt"

# Single-value override (dotted key for nesting)
foo -c secrets.backend=keyring "some prompt"

# YAML-parsed value
foo -c "secrets={backend: keyring, service: foo-ci}" "some prompt"
```

## Related docs

- [Configure models](../how-to/configure-models.md) — set/override the model.
- [Concepts: where state lives](../concepts.md#where-state-lives) — directory layout.
- [Compatibility](compatibility.md) — kit version requirements for `-c`.
