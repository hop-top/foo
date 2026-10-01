# Upgrade foo

Apply the latest released version of foo to the binary you are
running.

## Use this when

- You see a notice that a newer foo version is available.
- You want to pull a recent fix without re-running `go install`.

## Before you begin

You need:

- foo installed somewhere writable by your user (typically
  `$GOPATH/bin/foo` or `$HOME/go/bin/foo`).
- Network access to the GitHub releases API.

## Outcome

After this guide you will have:

- The latest released foo binary installed in place.
- A verifiable version string from `foo --version`.

## Quick path

```sh
foo upgrade
foo --version
```

## Steps

### 1. Check the running version

```sh
foo --version
# foo 0.1.0
```

### 2. Run the upgrade

```sh
foo upgrade
```

Expected: a message indicating whether an upgrade was applied or
the binary is already at the latest version. The command consults
the kit-shared upgrade checker, downloads the matching release
artifact, and replaces the running binary atomically.

### 3. Verify the new version

```sh
foo --version
```

The version string should reflect the release you just installed.

## Common issues

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| Network error | Releases API unreachable | Check connectivity; try again |
| Permission denied | Binary path not writable | Re-install via `go install hop.top/foo@latest` |
| "Already at latest" | Nothing to do | Confirm with `foo --version` |

## How it works

`foo upgrade` runs the kit-shared `upgrade.RunCLI` flow against
the `hop-top/foo` GitHub releases. Idempotent: re-running when
already current is a no-op. State (last-checked timestamp,
cached release metadata) lives under
`$XDG_STATE_HOME/foo/`.

Outside `foo upgrade`, a foo invocation also checks for a newer
release before the command runs (via `upgrade.NotifyIfAvailable`)
and prints a one-line notice to stderr when one exists. A successful
check is cached for 4 hours; a failed one is not, so the check gives
up after 2 seconds rather than delay the command.

The check runs only for someone who can see the notice. It is
skipped, with no network request at all, when:

| Condition | Why |
|-----------|-----|
| stderr is not a terminal | Piped or captured output belongs to a script |
| `--offline` | No request leaves the machine |
| `--quiet` | Non-essential output is suppressed |
| `FOO_NO_UPDATE_NOTIFIER` set (any value) | Explicit opt-out |
| `CI` set (any value) | CI runners set it; nobody reads the notice |
| the command is `foo upgrade` | It checks for itself |

`foo upgrade` ignores `FOO_NO_UPDATE_NOTIFIER` and `CI`: asking for
an upgrade always checks.

## Options

`foo upgrade` takes no flags today. The behavior is controlled
entirely by the kit-shipped upgrade subsystem.

## Related docs

- [Troubleshooting](../troubleshooting.md) — recovery if upgrade leaves the binary in a bad state.
- [Reference: commands](../reference/commands.md#top-level) — top-level verb surface.
