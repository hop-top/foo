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

## Configuration keys

| Key | Type | Default | Description | Env equivalent |
|-----|------|---------|-------------|----------------|
| `model` | string | `claude-3-5-sonnet-latest` | Default LLM model id | `FOO_MODEL` |
| `patterns_path` | string | `$XDG_CONFIG_HOME/foo/patterns` | Directory for pattern files | `FOO_PATTERNS_PATH` |
| `accent` | string | `#E040FB` | TUI accent color (hex) | `FOO_ACCENT` |
| `budget` | string | `""` (= `balanced` at use) | Persistent pool routing tier. Env wins. | `FOO_BUDGET` |
| `secrets.backend` | string | `env` | Secret store backend id | `FOO_SECRETS_BACKEND` |
| `secrets.prefix` | string | `""` | Prefix applied to secret key lookups | `FOO_SECRETS_PREFIX` |
| `secrets.service` | string | `""` | Service identifier for the backend | `FOO_SECRETS_SERVICE` |

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
# set. foo seeds a default block on first run; edit to taste.
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
| `providers.<scheme>.api_key` | string | Provider key. Overridden by the per-provider env var (e.g. `OPENAI_API_KEY`). |
| `providers.<scheme>.base_url` | string | Custom base URL. Overridden by `LLM_BASE_URL` env, and by a `?base_url=` param on `--model`. See [use a local endpoint](../how-to/use-a-local-endpoint.md). |
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
| `fallback` | list of URIs | Tried in order on retriable primary failure. Overridden by `LLM_FALLBACK` env. Each entry gets its own scheme's key, resolved as for the primary model ([key precedence](#provider-keys)); an entry whose key is missing is dropped with one stderr warning, and the run continues. |

End-to-end walkthrough:
[how-to: route across models](../how-to/route-across-models.md).

## Environment variables

### Provider keys

| Variable | Scheme | Required for |
|----------|--------|--------------|
| `ANTHROPIC_API_KEY` | `anthropic` | Claude models |
| `OPENAI_API_KEY` | `openai` | GPT/o-series models, all embeddings, and bare model ids foo does not recognise (sent to the `openai` scheme, e.g. a [local endpoint](../how-to/use-a-local-endpoint.md)) |
| `GOOGLE_API_KEY` | `google`, `gemini` | Gemini models |
| `OPENROUTER_API_KEY` | `openrouter` | OpenRouter models (`openrouter://<vendor>/<model>`) |
| `GROQ_API_KEY` | `groq` | Groq models |
| `XAI_API_KEY` | `xai` | xAI (Grok) models |
| `TOGETHER_API_KEY` | `together` | Together AI models |
| `FIREWORKS_API_KEY` | `fireworks` | Fireworks AI models |
| `DEEPSEEK_API_KEY` | `deepseek` | DeepSeek hosted models |
| `MISTRAL_API_KEY` | `mistral` | Mistral hosted models |

Each scheme reads its own variable. An OpenRouter or Groq
model never borrows `OPENAI_API_KEY`, so a real OpenAI key is
never sent to another provider. `ollama`, `lmstudio` and
`routellm` are local and take no key.

#### Key precedence

For a keyed scheme, foo resolves the key in this order and stops
at the first hit:

| # | Source | Example |
|---|--------|---------|
| 1 | `?api_key=` on the URI | `-m 'openrouter://openai/gpt-4.1-nano?api_key=sk-or-...'` |
| 2 | The scheme's own key: secret store, then env var | `OPENROUTER_API_KEY` |
| 3 | `LLM_API_KEY` | universal key |

This mirrors kit: kit's `Resolve` sends only the URI's
`api_key`, so foo appends the resolved key to the URI and the
key it prechecks is the key sent. Tiers 2–3 follow kit's
`SecretFor` order (provider key before `LLM_API_KEY`).

`LLM_API_KEY` is not tied to a provider: it goes to every keyed
scheme that has no key of its own, fallback entries included
(kit's `LoadConfig` applies it to every URI). With more than one
provider in play, prefer per-scheme variables so one provider's
key is never sent to another host.

The same key applies whether you pick the model by bare id, by
pool, or as a URI: `-m 'openrouter://openai/gpt-4.1-nano'` gets
`OPENROUTER_API_KEY` appended. A URI that already carries
`?api_key=` is sent as written. A missing key fails before any
request with `missing OPENROUTER_API_KEY for model "..."`.

Fallback entries (`LLM_FALLBACK`, llm.yaml `fallback:`) get their
own scheme's key the same way. A fallback whose key is missing is
dropped rather than failing the run — the primary may be fine —
and foo warns once on stderr:
`llm.fallback.dropped: fallback has no API key; skipping it
fallback=openrouter://... missing=OPENROUTER_API_KEY`.

`foo provider show <scheme>` reports each scheme's expected key
and whether foo can see it.

### Kit routing env vars

These are consumed by `kit/llm` (not foo directly), but they
take effect on every `foo` invocation.

| Variable | Used by | Purpose |
|----------|---------|---------|
| `LLM_PROVIDER` | `LoadConfig` | Default URI when no model is specified. Overrides `default:` in `llm.yaml`. |
| `LLM_API_KEY` | foo key precheck, `LoadConfig` | Universal key for any keyed scheme with no key of its own, fallbacks included; satisfies foo's precheck. A per-scheme variable outranks it ([key precedence](#key-precedence)). |
| `LLM_BASE_URL` | `LoadConfig` | Custom base URL for the resolved provider. Overrides `providers.<scheme>.base_url`; a `?base_url=` param on `--model` overrides both. |
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
| `FOO_SECRETS_BACKEND` | `secrets.backend` | Secret backend |
| `FOO_SECRETS_PREFIX` | `secrets.prefix` | Secret key prefix |
| `FOO_SECRETS_SERVICE` | `secrets.service` | Secret service id |
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
foo -c "secrets={backend: keyring, service: foo}" "some prompt"
```

## Related docs

- [Configure models](../how-to/configure-models.md) — set/override the model.
- [Concepts: where state lives](../concepts.md#where-state-lives) — directory layout.
- [Compatibility](compatibility.md) — kit version requirements for `-c`.
