# Compatibility

Versioning policy, dependency requirements, and deprecations.

## Versioning

foo follows Semantic Versioning.

- **Major** bumps signal incompatible CLI surface changes:
  removed or renamed commands, removed or renamed flags, output
  format changes that scripted consumers depend on.
- **Minor** bumps add features without breaking existing
  invocations: new commands, new flags, new providers.
- **Patch** bumps are fixes only — no surface change.

Pre-1.0 releases (`0.y.z`) follow the same rules but reserve
minor for "anything goes" changes per common Go module practice.

## Kit dependency

foo requires `hop.top/kit` at version `v0.4.0-alpha.6` or newer.
Earlier versions lack:

- The strict CLI validator (`Root.Validate()` strict gate).
- The closed sets for `kit/side-effect` and `kit/idempotent`
  annotations consumed by foo's leaf commands.
- The `--confirm` global flag and its `auto | yes | no | prompt`
  values.

If a build mixes an older kit with the current foo source, the
`Execute`-time validator will reject the root with
`cli validation failed:` and a bucket diagnostic. See
[kit-conformance-baseline.md](kit-conformance-baseline.md) for
the catalog of buckets and what each annotation does.

## Confirm policy is kit-shipped

`--confirm` ships with kit. foo classifies each destructive leaf
with `kitcli.SetSideEffect(..., SideEffectDestructiveLocal)`; kit
handles the policy enforcement and prompting. The flag values:

| Value | Behavior |
|-------|----------|
| `auto` | TTY → `prompt`; no TTY → `no`. Default. |
| `yes` | Proceed without prompting |
| `no` | Refuse with `UNAUTHORIZED` |
| `prompt` | Ask, even off-TTY |

foo has never shipped legacy `--force` or `--yes` flags, so
there is no migration to perform.

## Status surface is kit-shipped

`foo status` is mounted via `kitcli.WithStatus(kitcli.StatusConfig{})`.
It boots cleanly even when the rest of foo is offline. Treat it as
the canonical health check in CI and operator workflows.

## Plugin contract

The plugin discovery convention (`foo <name>` → `foo-<name>` on
`$PATH`) is part of the kit `ext` package. Plugin metadata is
exposed via `--ext-info` and follows the documented JSON shape
(`{"name", "version", "description", "capabilities"}`).

Tool extensions follow the same shape under the `foo-tool-`
prefix and are discovered at tool-dispatch time.

## Deprecations

None.

This section is reserved for surface that is scheduled to be
removed in a future major. When a flag, command, or config key
is deprecated, it will appear here with the deprecation version,
the removal version, and the recommended replacement.

## Local-model status

Local model runtimes (Ollama, LM Studio, llama.cpp) are not yet
supported. There is no `base_url` config to point at a local
endpoint. Track [hop-top/foo](https://github.com/hop-top/foo) for
release notes on this.

Workaround: use any OpenAI-compatible HTTP gateway. Unknown model
schemes fall back to the OpenAI scheme; with `OPENAI_API_KEY` set,
that fallback works with gateways such as OpenRouter, Groq, and
Together.

## Related docs

- [Reference: kit-conformance baseline](kit-conformance-baseline.md) — frozen audit snapshot.
- [How to: confirm destructive ops](../how-to/confirm-destructive-ops.md) — the user-facing payoff of the kit contract.
- [How to: configure models](../how-to/configure-models.md) — fallback scheme details.
