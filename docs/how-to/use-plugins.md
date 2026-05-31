# Use foo plugins

Install a `foo-*` plugin binary, verify foo discovers it, and pipe
its output into a foo prompt.

## Use this when

- You want `foo youtube ...`, `foo scrape ...`, or any other
  `foo-<name>` plugin to work end-to-end.
- The pipeline `foo <plugin> ... | foo -p <pattern>` returns
  "command not found" or an unexpected error.

## Before you begin

You need:

- Go 1.26+ on PATH (to `go install` the official plugins).
- A directory on `$PATH` where `go install` writes binaries
  (`go env GOBIN` or `$(go env GOPATH)/bin`).
- Foo built or installed; run `foo --version` to confirm.

## Outcome

After this guide you will:

- Have at least one `foo-*` plugin on `$PATH`.
- See it appear under PLUGINS in `foo --help`.
- Successfully run `foo <plugin> ... | foo -p <pattern>`.

## Quick path

```sh
go install hop.top/foo/cmd/foo-youtube@latest
foo --help | grep -A2 PLUGINS
foo youtube "https://youtu.be/<id>" | foo -p summarize
```

## Steps

### 1. Install the plugin binary

The two plugins shipped in this repo:

```sh
go install hop.top/foo/cmd/foo-youtube@latest
go install hop.top/foo/cmd/foo-scrape@latest
```

`foo-youtube` requires `yt-dlp` on PATH; `foo-scrape` has no
external dependencies.

Third-party plugins follow the same `foo-<name>` naming. Any
binary on `$PATH` whose name starts with `foo-` (and which
implements `--ext-info`) will be discovered.

### 2. Verify discovery

```sh
foo --help
```

Look for a `PLUGINS` group at the bottom:

```text
PLUGINS:
  scrape    Url to markdown conversion with readability
  youtube   Youtube transcript and metadata extraction
```

Descriptions come from each plugin's `--ext-info` response. If a
plugin you installed is missing from this list:

- Confirm `which foo-<name>` returns a path.
- Confirm the binary is executable (`chmod +x`).
- Run `foo-<name> --ext-info` directly; it must emit valid JSON.

### 3. Run the plugin standalone

Before piping, smoke the plugin alone:

```sh
foo youtube --help
foo scrape --help
```

The dispatcher executes the underlying binary with argv
passthrough, so flag handling is the plugin's own (not cobra's).

### 4. Pipe plugin output into foo

Plugins write markdown to stdout, which foo treats as the user
prompt when piped:

```sh
foo youtube "https://youtu.be/<id>" | foo -p summarize
foo scrape "https://example.com/article" | foo -p extract_wisdom
```

The receiving `foo` must (a) have an `ANTHROPIC_API_KEY` or
`OPENAI_API_KEY` set, and (b) find the named pattern. See
[Manage patterns](manage-patterns.md) for the pattern lookup
rules — pattern scope is the most common pipeline failure cause.

## Common issues

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `foo: '<plugin>' is not a foo command` | Binary missing from `$PATH` | `go install hop.top/foo/cmd/foo-<name>@latest` and confirm with `which foo-<name>` |
| Plugin runs but description is generic | `--ext-info` returns non-JSON or errors | Run `foo-<name> --ext-info` directly; fix the plugin |
| `no prompt provided (stdin was empty)` from downstream `foo` | Upstream plugin failed and produced no output | Run the plugin alone to see its stderr |
| `pattern "<name>" not found` from downstream `foo` | Pattern saved project-local in a different cwd | [Import the pattern globally](manage-patterns.md#scope-and-cwd) |

## How it works

Foo calls `dispatch.Register` from `hop.top/kit/go/ai/ext/dispatch`
at startup. The registrar scans `$PATH` for executables named
`foo-*`, registers each as a hidden cobra subcommand with
`DisableFlagParsing: true`, and forwards argv straight to the
binary via `exec.Command`.

Plugin binaries are interrogated for `--ext-info` once at help
render time to populate the description shown in `foo --help`.
Foo also stamps the discovered subcommands with kit annotations
(side-effect: interactive, idempotency: no, passthrough) so the
strict-validation gate stays armed even with arbitrary plugins
present.

## Options

| Plugin | Repo path | Runtime requirement |
|--------|-----------|---------------------|
| `foo-youtube` | `cmd/foo-youtube` | `yt-dlp` on PATH |
| `foo-scrape` | `cmd/foo-scrape` | none |

## Related docs

- [Manage patterns](manage-patterns.md) — pattern scope, the
  second-most-common pipeline failure.
- [Concepts](../concepts.md#plugins-via-path-discovery) — the
  underlying PATH-discovery model.
- [Troubleshooting](../troubleshooting.md) — symptom index for
  pipeline failures.
