# Route across models

foo picks which model receives a prompt via three complementary
mechanisms: the **pool** (the recommended path: a curated set of
models scored at invocation time against your budget and the
capabilities the prompt needs), the **fallback chain** (retry the
next model when one fails), and **RouteLLM** (delegate the strong-
vs-weak decision to an external router per query).

## Use this when

- You want foo to pick the cheapest model that supports the
  capabilities your prompt requires (tools, structured output).
- You have a budget posture — "always cheap unless I say otherwise" —
  and want foo to respect it without naming a specific model on
  every invocation.
- A single provider is your norm but you want a safety net for rate
  limits, transient 5xx, or quota exhaustion.
- You are evaluating foo for a workflow that cannot tolerate hard-
  stopping on a provider outage.

## Before you begin

You need:

- foo built from a recent commit with kit's pool routing primitives
  (`hop.top/kit v0.4.0-alpha.7` or later).
- A provider key per model you plan to route to
  (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GOOGLE_API_KEY`).
- For RouteLLM only: a RouteLLM server reachable at
  `http://localhost:6060` or wherever `ROUTELLM_BASE_URL` points.

## Outcome

After this guide you will be able to:

- Configure a pool of candidate models in `~/.config/hop/llm.yaml`.
- Use `--budget cheap|balanced|premium` to pick within the pool.
- Pin a single model with `-m` when you want to bypass the picker.
- Configure a fallback chain layered on top of any picked model.
- Route per-query via RouteLLM when the strong/weak decision needs
  prompt-content awareness the pre-flight picker can't deliver.

## Pool routing with --budget

Pool routing is foo's recommended path for "let me declare a budget
posture, then forget about it." On first run foo seeds a default
pool block into `~/.config/hop/llm.yaml`; edit it to match what you
have keys for and how aggressively you want to spend.

### Quick path

```sh
# 1. Seed and inspect the default pool. (foo seeds it on first run;
#    this just shows where it lives.)
foo --help | head -1
cat ~/.config/hop/llm.yaml

# 2. Pick the cheapest qualifying model.
foo --budget cheap "summarize this"

# 3. Pick the most capable.
foo --budget premium "explain the diff between B-tree and LSM"

# 4. Constrain the pick to JSON-mode-capable entries.
foo --schema "name str, age int" --budget cheap "extract: bob is 42"

# 5. Constrain the pick to tool-call-capable entries.
foo -T foo_time --budget balanced "what time is it?"
```

### How the picker decides

The picker pulls every model the pool authorizes, drops any whose
context window can't hold the request, drops any that don't satisfy
the capability filters (`--schema` → JSON mode; `-T` → tool calling),
then ranks the survivors:

| Tier | Ranking |
|------|---------|
| `cheap` | weighted USD price ascending: `0.75·input + 0.25·output`. |
| `balanced` | same price sort, picks the **median** survivor (upper-middle on even-sized lists). |
| `premium` | largest context window; ties break on highest input price; final tiebreak on alphabetical (provider, id). |

Ties always break on `(provider, id)` alphabetically — deterministic
across runs.

### Editing the pool

The pool lives at `~/.config/hop/llm.yaml` (or
`$XDG_CONFIG_HOME/hop/llm.yaml`). Each entry has four fields:

```yaml
pool:
  - alias: cheap-openai     # optional shorthand for LLM_POOL_DISABLE
    scheme: openai          # required: URI scheme in kit's registry
    model: gpt-4o-mini      # required: model id in models.dev
    # enabled: false        # optional, default true
    # weight: 2.0           # optional, default 1.0 (future load-distribution)
```

To temporarily disable an entry without removing it, set
`enabled: false` or use the env var:

```sh
export LLM_POOL_DISABLE="cheap-openai,openai:gpt-4o"
foo --budget cheap "..."
```

The env list matches against `alias` or `<scheme>:<model>`.

### When to override with -m

`-m` (and `FOO_MODEL`, and `model:` in `config.yaml`) pins the call
to one model and **bypasses the picker entirely**. Use it when:

- You're A/B-comparing two models and want to control for everything
  else.
- You're driving a model whose key is the only one set in your
  environment.
- A specific model is the only one that produces the answer shape
  you need, and you've already done the comparison work.

The fallback chain still wraps the pinned model — `-m` skips the
picker, not kit's `WithFallback` wiring.

**`--budget` is silently ignored when `-m` is set.** Since the picker
is bypassed, the budget tier has nothing to filter against; foo skips
even the value validation. `foo -m gpt-4o --budget cheap "..."` runs
gpt-4o without complaint or warning. If you want budget-driven
selection, drop `-m`.

### Reading picker decisions

`--picker-debug` sets `LLM_PICKER_TRACE=1`, which makes kit emit one
structured slog line per pick on stderr:

```sh
$ foo --budget cheap --picker-debug "hi"
level=INFO msg=llm.pick picker.budget=cheap picker.outcome=matched
  picker.chosen.provider=openai picker.chosen.model=gpt-4o-mini
  picker.candidate_count=9 picker.eliminated_count=0
```

Stable attributes: `picker.budget`, `picker.filter.*`,
`picker.profile.max_input_tokens`, `picker.candidate_count`,
`picker.eliminated_count`, `picker.outcome` (`matched` / `no_match`),
and on a match `picker.chosen.provider` / `picker.chosen.model`.

### Worked examples

```sh
# Cheap with no constraints — picks the lowest weighted-price entry
# from the pool. Today's default pool: gpt-4o-mini, claude-3-5-haiku,
# gemini-2.0-flash. The exact pick depends on current models.dev
# pricing.
foo --budget cheap "what is 2+2?"

# Premium with a long prompt — picks the largest context window the
# pool offers (a Claude Opus or a high-context Gemini). Reads
# pricing as a tiebreaker so the most expensive of two equally-
# capacious models wins.
foo --budget premium "$(cat very_long_doc.md) summarize"

# Cheap + schema — drops every entry that doesn't advertise JSON-
# mode, then picks cheapest of the survivors. Useful when you need
# structured output but don't care about model quality otherwise.
foo --schema "name str, role str" --budget cheap "extract: jane is a vp"

# Balanced + tools — drops every entry that doesn't advertise tool-
# calling, then picks the median-priced survivor. The sweet spot
# for agentic workflows where both quality and cost matter.
foo -T foo_time -T foo_version --budget balanced "what's the time and your version?"
```

### Pool routing vs router-X

Both surfaces route across models, but they answer different
questions:

| Aspect | `--budget` (pool routing) | `router-<name>:<threshold>` (RouteLLM) |
|--------|---------------------------|----------------------------------------|
| When the decision happens | **Pre-flight**: before the prompt is sent. | **Per-request**: RouteLLM scores the prompt then dispatches. |
| What it scores on | Static metadata (price, context window, capabilities). | Prompt content + a complexity score. |
| Who runs the logic | Kit's picker, in-process in foo. | A separate RouteLLM HTTP server. |
| Outcome scope | One pick per invocation, used for every retry in the chain. | One pick per turn (RouteLLM re-decides on each request). |
| Operational deps | None — the pool config is local. | A RouteLLM server reachable via `ROUTELLM_BASE_URL`. |

Use **pool routing** when "this kind of workload always wants tier X"
captures your intent — most CLI usage falls here. Use **RouteLLM**
when the prompt itself should drive strong/weak escalation. They
**compose**: a `router-mf:0.7` pin uses RouteLLM's scoring per turn,
and the kit fallback chain wraps either choice.

## Other routing mechanisms

### Fallback chain

A fallback chain is the safety net under whichever model was picked
(by `-m`, the pool, or RouteLLM). Kit retries the next URI on
fallbackable errors (network, 429, 5xx). Non-fallbackable errors
(401, 400) short-circuit by design.

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

When the primary completion returns a fallbackable error, kit's
`Complete` walks the chain in order and retries each fallback until
one succeeds or all are exhausted. The eventual error names every
URI it attempted.

### RouteLLM strong/weak routing

Stand up a RouteLLM server (out of scope here; see the upstream
RouteLLM docs). Point foo at it:

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

The prefix is a passthrough: foo turns `router-mf:0.7` into the URI
`routellm://mf:0.7`. Kit's routellm adapter reads the router name
(`mf`), validates the threshold (`0.7`, must be in `[0, 1]`), and
forwards the completion to your RouteLLM server, which responds
with whichever underlying model it picked for the query.

## Common issues

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `--budget` chose a model whose key isn't set | The picker doesn't know about your env. | Either export the missing key, set `enabled: false` on the offending pool entry, or use `LLM_POOL_DISABLE`. |
| `foo: seeded default pool config at ...` printed once and never again | First-run seeding succeeded; subsequent invocations are no-ops. | Working as intended — edit the seeded file. |
| Picker picks the "wrong" model under `cheap` | Models with no `cost` in models.dev are treated as 0 (helps local/open-weight models). | Inspect with `--picker-debug`. If undesired, set `enabled: false` on the offending entry. |
| Fallback chain set but the second URI is never tried | The first error was not fallbackable (e.g. 401 auth, 400 bad request). Kit only walks the chain on retriable errors. | Verify the primary URI works in isolation; fix the auth or request shape. |
| `LLM_FALLBACK` and `fallback:` both set, only env applies | By design. Env wins over the config file for fallbacks. | Unset `LLM_FALLBACK` to fall through to the config-file list. |
| `routellm: threshold X out of range [0, 1]` | The `:<threshold>` portion of `router-<name>:<X>` is not a float in `[0, 1]`. | Use a value like `router-mf:0.7`. |
| `routellm: ...connect: connection refused` | No RouteLLM server is running at `ROUTELLM_BASE_URL`. | Start the server or point at an existing one. |

## How it works

foo's `internal/llm.NewClient` runs three paths:

1. **Pool path** (default when no `-m`/`FOO_MODEL`/`config.model`
   is set). `kit/llm.LoadPool` reads the pool from
   `~/.config/hop/llm.yaml`; `kit/llm.PickProviderInPool` filters
   the candidate set by the request profile, scores survivors by
   budget, returns the winner. foo derives the profile from flag
   state (`--schema` → JSON mode, `-T` → tool calling, prompt
   length → `MaxInputTokens`).
2. **Explicit pin** (`-m <model>`). Scheme is detected from the
   model id; key precheck runs; URI assembled and resolved.
3. **RouteLLM passthrough** (`-m router-<name>:<threshold>`). The
   prefix is stripped and forwarded as the `routellm://` URI body.

Every path runs the same fallback wiring: `kit/llm.LoadConfig` reads
the `fallback:` list plus `LLM_FALLBACK`, and each entry is added
via `WithFallback`.

## Options

| Surface | Default | Purpose |
|---------|---------|---------|
| `--budget` | `balanced` | Pool routing tier (`cheap` / `balanced` / `premium`). |
| `FOO_BUDGET` (env) | (unset) | Fallback for `--budget`. CLI wins. |
| `--picker-debug` | off | Sets `LLM_PICKER_TRACE=1` for the process. |
| `-m, --model` | config value | Pin one model; **bypasses the picker**. |
| `FOO_MODEL` (env) | (unset) | Default model id. |
| `model` (foo config key) | `claude-3-5-sonnet-latest` | Default model id. |
| `pool:` (`~/.config/hop/llm.yaml`) | seeded by foo | Candidate set for the picker. |
| `LLM_POOL_DISABLE` (env) | (unset) | Comma list of `alias` or `<scheme>:<model>` to mute. |
| `LLM_FALLBACK` (env) | (unset) | Comma-separated provider URIs tried in order on retriable failure. Overrides `fallback:` in `llm.yaml`. |
| `fallback:` (`~/.config/hop/llm.yaml`) | (none) | Same list, set in config rather than env. |
| `router-<name>:<threshold>` model prefix | n/a | Send the call through RouteLLM with `<threshold>` in `[0, 1]`. |
| `ROUTELLM_BASE_URL` (env) | `http://localhost:6060` | RouteLLM server endpoint. |
| `ROUTELLM_STRONG_MODEL` (env) | (from kit config) | Strong model RouteLLM will pick when the router score is above threshold. |
| `ROUTELLM_WEAK_MODEL` (env) | (from kit config) | Weak model RouteLLM will pick below threshold. |
| `ROUTELLM_ROUTERS` (env) | (from kit config) | Comma-separated router names enabled on the server. |

## Related docs

- [Configure models](configure-models.md) — set the default model,
  override with `-m`, inspect provider auth.
- [Reference: config](../reference/config.md) — full config key list
  including the `pool:` block schema, `fallback:`, and the
  `routellm:` extras block.
- [Reference: compatibility](../reference/compatibility.md) —
  versioning, deprecations.
- [Concepts: how foo works](../concepts.md) — where model selection
  fits in the prompt assembly pipeline.
