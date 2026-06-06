# Route across models

Send a prompt to one model by default, fall back to a second
model when the first fails, or hand the choice to a RouteLLM
server that picks strong-vs-weak per query.

## Use this when

- A single provider is your norm but you want a safety net for
  rate limits, transient 5xx, or quota exhaustion.
- You want one cheap model for routine prompts and a stronger
  one only when the query warrants it, using RouteLLM as the
  picker.
- You are evaluating foo for a workflow that cannot tolerate
  hard-stopping on a provider outage.

If you are looking for capability-based or budget-based pool
routing (`pool: [...]` with weights and constraint matching),
see the "Not yet built" section at the bottom.

## Before you begin

You need:

- foo built from a recent commit (the fallback chain wiring
  landed mid-cycle; if your `foo` predates it, `LLM_FALLBACK`
  is silently ignored).
- A provider key per model you plan to route to
  (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GOOGLE_API_KEY`).
- For RouteLLM only: a RouteLLM server reachable at
  `http://localhost:6060` or wherever `ROUTELLM_BASE_URL`
  points.

## Outcome

After this guide you will be able to:

- Pin a single default model the way most users do.
- Configure a fallback chain in `~/.config/hop/llm.yaml` or via
  `LLM_FALLBACK`, and have foo retry the next entry when the
  primary call returns a fallbackable error.
- Send a prompt through a RouteLLM router with a chosen
  threshold using the `router-<name>:<threshold>` model prefix.

## Quick path

```sh
# 1. Single default. The 90% case.
foo model default claude-3-5-sonnet-latest
foo "summarize this"

# 2. Add a fallback. Tried in order on retriable failure.
export LLM_FALLBACK="openai://gpt-4o-mini,anthropic://claude-3-haiku-20240307"
foo "summarize this"

# 3. RouteLLM picks strong vs weak per query.
foo -m router-mf:0.7 "explain the diff between B-tree and LSM"
```

## Steps

### 1. Pin the default

```sh
foo model default claude-3-5-sonnet-latest
foo model current
```

Precedence (highest first): `-m`/`--model` flag, `FOO_MODEL`
env, `model` in `config.yaml`, hard-coded
`claude-3-5-sonnet-latest`. See
[configure-models.md](configure-models.md) for the full
walkthrough.

### 2. Configure a fallback chain

Two ways to declare fallbacks. Env wins.

Via `~/.config/hop/llm.yaml`:

```yaml
default: anthropic://claude-3-5-sonnet-latest
fallback:
  - openai://gpt-4o-mini
  - anthropic://claude-3-haiku-20240307
```

Via env:

```sh
export LLM_FALLBACK="openai://gpt-4o-mini,anthropic://claude-3-haiku-20240307"
```

Run a normal prompt:

```sh
foo "tell me a joke"
```

When the primary completion returns a fallbackable error
(network failure, 429, 5xx, etc.), kit's `Complete` walks the
chain in order and retries against each fallback URI until one
succeeds or all are exhausted. Non-fallbackable errors (4xx
auth, malformed request) short-circuit immediately without
trying the next entry — by design.

You'll know foo tried a fallback because the eventual error,
if all entries fail, names every URI it attempted.

### 3. Use RouteLLM strong/weak routing

Stand up a RouteLLM server (out of scope here; see the
upstream RouteLLM docs). Point foo at it:

```sh
export ROUTELLM_BASE_URL=http://localhost:6060
export ROUTELLM_STRONG_MODEL=gpt-4o
export ROUTELLM_WEAK_MODEL=gpt-4o-mini
```

Or put the equivalent in `~/.config/hop/llm.yaml`:

```yaml
providers:
  routellm:
    routellm:
      base_url: http://localhost:6060
      strong_model: gpt-4o
      weak_model: gpt-4o-mini
      routers: [mf, bert]
```

Then route a prompt with `router-<name>:<threshold>`:

```sh
foo -m router-mf:0.7 "explain the diff between B-tree and LSM"
```

The prefix is a passthrough: foo turns `router-mf:0.7` into
the URI `routellm://mf:0.7`. Kit's routellm adapter reads the
router name (`mf`), validates the threshold (`0.7`, must be in
`[0, 1]`), and forwards the completion to your RouteLLM
server, which responds with whichever underlying model it
picked for the query.

## Common issues

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| Fallback chain set but the second URI is never tried | The first error was not fallbackable (e.g. 401 auth, 400 bad request). Kit only walks the chain on retriable errors. | Verify the primary URI works in isolation; fix the auth or request shape. |
| `LLM_FALLBACK` and `fallback:` both set, only env applies | By design. Env wins over the config file for fallbacks. | Unset `LLM_FALLBACK` to fall through to the config-file list. |
| `routellm: threshold X out of range [0, 1]` | The `:<threshold>` portion of `router-<name>:<X>` is not a float in `[0, 1]`. | Use a value like `router-mf:0.7`. |
| `routellm: ...connect: connection refused` | No RouteLLM server is running at `ROUTELLM_BASE_URL`. | Start the server or point at an existing one. |
| `missing OPENAI_API_KEY for model "router-..."` (or similar) | Some prefixes still hit foo's precheck. | The router-prefix path skips foo's key precheck — if you see this, the model name did not start with `router-`. Verify the `-m` value. |

## How it works

foo's `internal/llm.NewClient` performs four steps:

1. Inspect the model string and pick a provider scheme. Known
   prefixes (`gpt-`, `o1-`, `o3-`, `claude-`, `gemini-`,
   `llama`/`mistral`/`deepseek-r1`, `router-`) map directly;
   anything unknown defaults to openai-compatible.
2. Build a primary URI of the form `<scheme>://<model>` (with
   `?api_key=...` for keyed providers) and check that the
   required env var is set, returning a precise error if not.
3. Call `kit/llm.LoadConfig(uri)` to merge the URI with
   `~/.config/hop/llm.yaml` and the `LLM_FALLBACK` env var,
   yielding a `ResolvedConfig.Fallbacks` list.
4. Resolve the primary URI to a provider via
   `kit/llm.Resolve`, resolve each fallback URI the same way,
   and construct a `kit/llm.Client` with
   `llm.WithFallback(...)` for each fallback. Kit's
   `Complete`/`Stream`/`CallWithTools` methods walk the chain
   on fallbackable errors.

Three things to know about the boundary:

- An explicit `-m` still pins the call to a single model. The
  fallback chain comes from the kit config layer, which the
  `-m` override does not re-shape.
- The `router-<router>:<threshold>` prefix is a thin
  passthrough; full routellm config (base URL, strong/weak
  model, routers list) lives in kit's `~/.config/hop/llm.yaml`
  under `providers.routellm.routellm` or the `ROUTELLM_*` env
  vars. foo does not surface a parallel set of flags.
- foo's API-key precheck runs against the primary URI's
  scheme only. A fallback to a different provider must have
  its own key exported, or the fallback call will fail (which
  is itself fallbackable, so the chain marches on).

## Options

| Surface | Default | Purpose |
|---------|---------|---------|
| `-m, --model` | config value | Pin a model for one invocation; bypasses the env/config default but not the fallback chain. |
| `FOO_MODEL` (env) | (unset) | Default model id. |
| `model` (foo config key) | `claude-3-5-sonnet-latest` | Default model id. |
| `LLM_FALLBACK` (env) | (unset) | Comma-separated provider URIs tried in order on retriable failure. Overrides `fallback:` in `llm.yaml`. |
| `fallback:` (`~/.config/hop/llm.yaml`) | (none) | Same list, set in config rather than env. |
| `router-<name>:<threshold>` model prefix | n/a | Send the call through RouteLLM with `<threshold>` in `[0, 1]`. |
| `ROUTELLM_BASE_URL` (env) | `http://localhost:6060` | RouteLLM server endpoint. |
| `ROUTELLM_STRONG_MODEL` (env) | (from kit config) | Strong model RouteLLM will pick when the router score is above threshold. |
| `ROUTELLM_WEAK_MODEL` (env) | (from kit config) | Weak model RouteLLM will pick below threshold. |
| `ROUTELLM_ROUTERS` (env) | (from kit config) | Comma-separated router names enabled on the server. |

## Not yet built

**Capability- and budget-based pool routing.** Kit's
`config.go` ships a `pool:` block in `llm.yaml` with
per-entry `weight`, `enabled`, and alias fields, and exposes
`LoadPool()` for consumers. foo does not consume that block
today: there is no `--budget` flag, no capability filter
plumbed through to the picker, and no way to declare "pick the
cheapest tool-calling model from this pool" from a `foo`
invocation. The pieces exist in kit; foo has not wired them.

Until that lands:

- For "always cheap unless I say otherwise," use a fallback
  chain that puts the cheap model first.
- For "decide per query," use RouteLLM with a threshold.
- For "must support tool calling," pin the model explicitly
  with `-m`.

See [reference/compatibility.md](../reference/compatibility.md)
for the running list of features that are scoped but not yet
shipped.

## Related docs

- [Configure models](configure-models.md) — set the default
  model, override with `-m`, inspect provider auth.
- [Reference: config](../reference/config.md) — full config
  key list including `fallback:` and the `routellm:` extras
  block.
- [Reference: compatibility](../reference/compatibility.md) —
  versioning, deprecations, the not-yet-shipped surface.
- [Concepts: how foo works](../concepts.md) — where model
  selection fits in the prompt assembly pipeline.
