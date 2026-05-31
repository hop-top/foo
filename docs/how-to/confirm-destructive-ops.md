# Confirm destructive operations

Run delete-style foo commands safely from scripts and CI using
kit's global `--confirm` flag.

## Use this when

- You need to script `foo pattern delete`, `foo schema delete`,
  `foo fragment delete`, or `foo embed collection delete`.
- You hit `UNAUTHORIZED` from a destructive command in a
  non-interactive shell.
- You want to understand which foo commands are classified as
  destructive.

## Before you begin

You need:

- foo built against `hop.top/kit v0.4.0-alpha.6` or newer (the
  current go.mod pins this). `--confirm` is supplied by kit, not
  by foo.
- An understanding that destructive here means
  *destructive-local*: local state loss only. foo has no
  destructive-shared commands today.

## Outcome

After this guide you will be able to:

- Identify destructive foo commands.
- Run them safely on a TTY (interactive confirm) or in a script
  (`--confirm=yes`).
- Understand why a script breaks with `UNAUTHORIZED`.

## Quick path

```sh
# Interactive (TTY): prompt is shown
foo pattern delete old-pattern

# Scripted (no TTY): bypass prompt
foo pattern delete old-pattern --confirm=yes

# CI sanity check: refuse explicitly
foo pattern delete old-pattern --confirm=no
```

## Steps

### 1. Identify the destructive commands

These commands mutate local state and remove it irreversibly:

| Command | What it removes |
|---------|-----------------|
| `foo pattern delete <name>` | One pattern directory under the patterns dir |
| `foo schema delete <name>` | One row from the schema store |
| `foo fragment delete <alias>` | One alias + its index entry |
| `foo embed collection delete <name>` | All embeddings in one collection |

Each is annotated `destructive-local` (see
[reference/kit-conformance-baseline.md](../reference/kit-conformance-baseline.md)).

### 2. Run one interactively

On a TTY, no `--confirm` flag needed — kit prompts:

```sh
foo pattern delete reviewer
# Are you sure? [y/N]
```

Answer `y` to proceed, anything else to abort.

### 3. Run one in a script

Off a TTY (CI, pipeline, sub-shell), the default policy is `no`.
foo refuses the action and exits with `UNAUTHORIZED`. Pass
`--confirm=yes` to opt in:

```sh
foo pattern delete reviewer --confirm=yes
# pattern "reviewer" deleted
```

### 4. Verify the policy with a dry refusal

To assert in CI that a destructive call is gated as expected, run
it without `--confirm` and check the exit code:

```sh
if foo pattern delete reviewer 2>/dev/null; then
  echo "should have been refused" >&2; exit 1
fi
```

## Common issues

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `UNAUTHORIZED` | Destructive call off a TTY without `--confirm=yes` | Add `--confirm=yes` |
| Script never returns | Destructive call on a TTY waiting for prompt | Add `--confirm=yes` or run with no TTY |
| `--force` / `--yes` flag not found | foo never shipped these | Use `--confirm=yes` |

## How it works

`--confirm` is a kit-shipped global flag with four values:

| Value | Behavior |
|-------|----------|
| `auto` | TTY → `prompt`; no TTY → `no` |
| `yes` | Proceed without prompting |
| `no` | Refuse with `UNAUTHORIZED` |
| `prompt` | Ask, even if no TTY (will read from stdin) |

A command must declare its side effect for kit to know whether to
apply the policy. foo's destructive leaves are annotated
`SideEffectDestructiveLocal`; reads and ordinary writes are not
gated.

The default is `auto`. The intent is: nothing scary happens
unattended, nothing slow happens when a human is sitting there.

`foo status` is the kit-shipped status command. It boots cleanly
even when the rest of foo is misconfigured. Use it to confirm
which kit version is running:

```sh
foo status
```

## Options

| Value | Use when |
|-------|----------|
| `--confirm=auto` (default) | You do not need to specify; kit picks based on TTY |
| `--confirm=yes` | Scripted/automated runs |
| `--confirm=no` | CI guard that should fail loudly if a script ever tries to mutate |
| `--confirm=prompt` | Force interactive confirmation regardless of TTY |

## Related docs

- [Reference: kit-conformance baseline](../reference/kit-conformance-baseline.md) — destructive annotations per leaf.
- [Reference: compatibility](../reference/compatibility.md) — kit version requirements.
- [Troubleshooting](../troubleshooting.md) — `UNAUTHORIZED` recovery flow.
