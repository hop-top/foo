# Configure models

Pick the default model, override it per-invocation, and inspect
provider auth.

## Use this when

- You want to change which model foo calls by default.
- You want a one-shot override (e.g. cheap model for a small task).
- You need to verify which provider keys foo can see.

## Before you begin

You need:

- A provider key exported for the model family you intend to use
  (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`).
- foo installed and `foo provider list` working.

## Outcome

After this guide you will be able to:

- Show + set the default model.
- Override the model on a single call.
- Confirm a provider's credentials are visible to foo.

## Quick path

```sh
# Show + set default
foo model current
foo model default claude-3-5-sonnet-latest

# One-shot override
foo -m gpt-4o "draft a release note"

# Check auth status
foo provider show anthropic
```

## Steps

### 1. Show the current default

```sh
foo model current
```

Expected: a one-row table with `CURRENT` set to the model id from
config (defaults to `claude-3-5-sonnet-latest` if never set).

### 2. Set a new default

```sh
foo model default claude-3-5-sonnet-latest
# default model set to "claude-3-5-sonnet-latest"
```

The value is written to `$XDG_CONFIG_HOME/foo/config.yaml` under
`model`. You can also set it ad hoc via `FOO_MODEL` in env.

### 3. Override on one call

`-m`/`--model` takes precedence over both env and config:

```sh
foo -m gpt-4o "draft a release note"
```

### 4. List registered providers

```sh
foo provider list
```

Expected: schemes registered in the current build (e.g.
`anthropic`, `openai`, `google`).

### 5. Inspect provider auth

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

### Local models are not yet supported

There is no `base_url` config to point at an Ollama, LM Studio, or
llama.cpp server. Local model support is tracked for a future
release. For now, point at an OpenAI-compatible HTTP gateway or
use a hosted provider.

## Options

| Flag / setting | Default | Purpose |
|----------------|---------|---------|
| `-m, --model` | `cfg.Model` | Override for one invocation |
| `model` (config key) | `claude-3-5-sonnet-latest` | Default model id |
| `FOO_MODEL` (env) | (unset) | Default model id override |

## Related docs

- [Route across models](route-across-models.md) — fallback chains and RouteLLM strong/weak routing.
- [Reference: config](../reference/config.md) — model + secrets config keys.
- [Reference: commands](../reference/commands.md#model) — `model` and `provider` surface.
- [Concepts: assembly pipeline](../concepts.md#the-prompt-assembly-pipeline) — how the model fits in.
