# Configure models

Pick the default model, override it per-invocation, and inspect
provider auth.

> **First check whether you really want this page.** Foo's default
> model-selection mechanism is the pool picker driven by
> `--budget` — see
> [route across models](route-across-models.md). The right page
> for "use a cheaper model for a small task" is `--budget cheap`,
> not `-m`. Read on if you actually need an explicit, picker-
> bypassing model pin (A/B comparison, debugging a specific
> model, the only key you have is one provider's).

## Use this when

- You do not know any model ids and need to find one.
- You want to pin one specific model id for an invocation,
  bypassing the pool picker.
- You want to change the foo `model` config key that the picker
  uses as the fallback when no pool is loaded.
- You need to verify which provider keys foo can see.

## Before you begin

You need:

- A provider key exported for the model family you intend to use
  (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`).
- foo installed and `foo provider list` working.

## Outcome

After this guide you will be able to:

- Find a model id without knowing one first.
- Show + set the default model.
- Override the model on a single call.
- Confirm a provider's credentials are visible to foo.

## Quick path

```sh
# Find an id, then make it the default
foo model list --provider=anthropic
foo model default claude-sonnet-5

# Show + set default
foo model current
foo model default claude-3-5-sonnet-latest

# One-shot override
foo -m gpt-4o "draft a release note"

# Check auth status
foo provider show anthropic
```

## Steps

### 1. Find a model id

`foo model default` wants an id. `foo model list` is where ids come
from — it reads the aim catalog (models.dev), thousands of models
across hundreds of providers:

```sh
foo model list
```

```
PROVIDER    ID                             CONTEXT  TOOLS  REASONING  RELEASED
anthropic   claude-fable-5-1               1000000  true   true       2026-09-01
deepseek    deepseek-flash                 1000000  true   true       2026-09-10
google      gemini-3.8-flash               1048576  true   true       2026-09-02
groq        qwen/qwen3.8-27b               131042   true   true       2026-08-14
lmstudio    openai/gpt-oss-20b             131072   true   true       2025-08-05
mistral     zai-glm-5-3                    1000000  true   true       2026-08-14
openai      gpt-6-astra                    1050000  true   true       2026-09-04
```

The default view is 20 rows, rotating across providers so the first
screenful is each provider's current flagship rather than one
aggregator's back catalogue. A stderr line reports how many were
cut:

```
7843 more model(s) not shown; raise --limit or pass --limit=0 for all
```

Pass an id from the `ID` column straight to `foo model default`.

> Only the provider whose key you have exported will actually
> answer. The catalog lists what exists, not what you can reach —
> check with `foo provider show <scheme>` (step 8).

### 2. Narrow the list

Filters apply before truncation and combine with AND. Narrow by
provider first — it is the filter that maps onto the key you hold:

```sh
foo model list --provider=anthropic --limit=5
```

```
PROVIDER   ID                CONTEXT  TOOLS  REASONING  RELEASED
anthropic  claude-fable-5-1  1000000  true   true       2026-09-01
anthropic  claude-opus-5     1000000  true   true       2026-07-24
anthropic  claude-sonnet-5   1000000  true   true       2026-06-29
anthropic  claude-fable-5    1000000  true   true       2026-06-07
anthropic  claude-opus-4-8   1000000  true   true       2026-05-28
```

Then add a capability. Capability flags are three-state — omit the
flag for no filtering, pass it for models that have the capability,
pass `=false` for models that do not:

```sh
foo model list --provider=openai --tool-call --limit=5
```

```
PROVIDER  ID             CONTEXT  TOOLS  REASONING  RELEASED
openai    gpt-6-astra    1050000  true   true       2026-09-04
openai    gpt-5.6        1050000  true   true       2026-07-09
openai    gpt-5.6-luna   1050000  true   true       2026-07-09
openai    gpt-5.6-sol    1050000  true   true       2026-07-09
openai    gpt-5.6-terra  1050000  true   true       2026-07-09
```

That is 48 OpenAI models down to 39 with tool calling. The other
filters are `--family`, `--input` / `--output` (repeatable
modalities, every listed one required), `--open-weights`,
`--reasoning` and `--structured-output` — see the
[command reference](../reference/commands.md#model) for the full
set.

`--query` is the escape hatch for one expression instead of several
flags:

```sh
foo model list --query 'provider:anthropic reasoning:true'
```

Its keys are underscored and use `in`/`out` for modalities
(`provider`, `family`, `in`, `out`, `tool_call`, `reasoning`,
`open_weights`, `structured_output`, `temperature`); an unknown key
is an error naming the key. Where a query key names the same thing
as an explicit flag, the flag wins — modality lists merge instead.

### 3. Pipe it somewhere

Hints and the cache footer go to stderr, so stdout stays a clean
stream:

```sh
foo model list --limit=0 --format json > models.json
```

Under `--format json` or `yaml` the cache provenance rides along as
a `_meta` object beside `data` rather than as a footer.

> On this command `--output` is the output-modality filter and
> shadows kit's global `--output` destination path. Redirect stdout
> as above instead of passing `--output models.json`.

### 4. Show the current default

```sh
foo model current
```

Expected: a one-row table with `CURRENT` set to the model id from
config (defaults to `claude-3-5-sonnet-latest` if never set).

### 5. Set a new default

```sh
foo model default claude-3-5-sonnet-latest
# default model set to "claude-3-5-sonnet-latest"
```

The value is written to `$XDG_CONFIG_HOME/foo/config.yaml` under
`model`. You can also set it ad hoc via `FOO_MODEL` in env.

### 6. Override on one call

`-m`/`--model` takes precedence over both env and config:

```sh
foo -m gpt-4o "draft a release note"
```

### 7. List registered providers

```sh
foo provider list
```

Expected: schemes registered in the current build (e.g.
`anthropic`, `openai`, `google`).

### 8. Inspect provider auth

```sh
foo provider show anthropic
```

Expected: a row with `SCHEME`, `AUTH`, and `STATUS`. `STATUS` is
one of `configured`, `missing`, `available` (no auth required), or
`unknown`.

## Common issues

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `Error creating LLM client` | No matching provider key in env | Export `ANTHROPIC_API_KEY` or `OPENAI_API_KEY` |
| `provider show <X>` returns `status: missing` | Key var is empty | Re-export the key, then re-run `provider show` |
| `model default ...` no-op | Filesystem permission on `$XDG_CONFIG_HOME` | Check directory perms |

## How it works

Model selection precedence (highest first):

1. `-m`/`--model` on the command line.
2. `FOO_MODEL` in env.
3. `model` in user config (`config.yaml`).
4. Hard-coded default: `claude-3-5-sonnet-latest`.

foo dispatches by scheme prefix. Known prefixes route to the
matching provider; unknown prefixes fall back to the OpenAI
scheme. With `OPENAI_API_KEY` set, that fallback works with
OpenAI-compatible endpoints (OpenRouter, Groq, Together, etc.).

### Local and self-hosted models

The same fallback reaches a server you run yourself — llama.cpp,
vLLM, LM Studio, Ollama's OpenAI shim — once you point foo at its
endpoint. Set `providers.openai.base_url` in `llm.yaml`, export
`LLM_BASE_URL`, or attach `?base_url=` to `-m`; the param wins,
then the env var, then the file.

```sh
foo -m "my-local-model?base_url=http://127.0.0.1:8000/v1" "hello"
```

See [use a local endpoint](use-a-local-endpoint.md) for the full
walkthrough, including model-id discovery and the `--max-tokens`
requirement some servers impose.

## Options

| Flag / setting | Default | Purpose |
|----------------|---------|---------|
| `-m, --model` | `cfg.Model` | Override for one invocation |
| `model` (config key) | `claude-3-5-sonnet-latest` | Default model id |
| `FOO_MODEL` (env) | (unset) | Default model id override |

## Related docs

- [Route across models](route-across-models.md) — pool routing with `--budget` (the primary mechanism), fallback chains, RouteLLM strong/weak routing.
- [Use a local endpoint](use-a-local-endpoint.md) — point foo at a self-hosted OpenAI-compatible server.
- [Reference: config](../reference/config.md) — model + secrets config keys.
- [Reference: commands](../reference/commands.md#model) — `model` and `provider` surface.
- [Concepts: assembly pipeline](../concepts.md#the-prompt-assembly-pipeline) — how the model fits in.
