# Kit strict-validation baseline

Snapshot of `Root.Validate()` failures emitted by `foo` against
`hop.top/kit v0.4.0-alpha.6`. Captured by running the freshly-built
`foo --help` (strict gate runs at `Execute`, so validation fires
before any subcommand body). This file is the acceptance checklist
for the conformance sweep; every bucket below must be empty before
the strict gate is re-armed.

## How to reproduce

```
go build -o /tmp/foo .
/tmp/foo --help
```

Any non-zero exit prefixed with `cli validation failed:` is one or
more of the buckets below.

## Buckets

### 1. Missing `kit/side-effect` (26 leaves)

`foo embed add`, `foo embed collection delete`, `foo embed collection list`,
`foo embed file`, `foo embed search`, `foo fragment create`,
`foo fragment delete`, `foo fragment list`, `foo fragment show`,
`foo model current`, `foo model default`, `foo pattern create`,
`foo pattern delete`, `foo pattern import`, `foo pattern list`,
`foo pattern show`, `foo provider list`, `foo provider show`,
`foo schema compile`, `foo schema create`, `foo schema delete`,
`foo schema list`, `foo schema show`, `foo shell`,
`foo strategy list`, `foo upgrade`.

Annotation key: `kit/side-effect`. Helper: `kitcli.SetSideEffect`.
Closed set per `kit/go/console/cli/sideeffect.go`:
`read | write | write-local | write-shared | destructive |
destructive-local | destructive-shared | interactive`.

### 2. Missing `kit/idempotent` (5 leaves)

`foo embed file`, `foo pattern import`, `foo schema compile`,
`foo shell`, `foo upgrade`.

Helper: `kitcli.SetIdempotency`. Closed set: `yes | no | conditional`.
Kit auto-stamps defaults for verbs `list`, `show`, `get`, `find`,
`search`, `info`, `current`, `path(s)`, `doctor`, `delete`, `edit`,
`update`, `use`, `default`, `sync`, `reprocess` (all `yes`),
`create` and `add` (both `no`). Non-default verbs must annotate
explicitly.

### 3. Missing `Long` (25 leaves)

`foo embed add`, `foo embed collection delete`, `foo embed collection list`,
`foo embed file`, `foo embed search`, `foo fragment delete`,
`foo fragment list`, `foo fragment show`, `foo model current`,
`foo model default`, `foo pattern create`, `foo pattern delete`,
`foo pattern import`, `foo pattern list`, `foo pattern show`,
`foo provider list`, `foo provider show`, `foo schema compile`,
`foo schema create`, `foo schema delete`, `foo schema list`,
`foo schema show`, `foo shell`, `foo strategy list`, `foo upgrade`.

Set `cobra.Command.Long` on every leaf — kit reads it for the help
surface and for spec projection. Short is not a substitute.

### 4. Missing reserved `status` subcommand (1)

`foo` root must mount the kit-shipped `status` command. Canonical
wire-up: pass `kitcli.WithStatus(kitcli.StatusConfig{})` as an option
to `kitcli.New`. Kit's `status` is self-annotated so it satisfies
the validator on its own; the root just has to register it.

### 5. Missing `kit/top-level-verb` (2 depth-1 leaves)

`foo shell`, `foo upgrade`.

These are depth-1 runnable leaves. Mark each with
`kitcli.SetTopLevelVerb(cmd)` so the validator knows the
`<tool> <verb>` shape is intentional and not an accident.

### 6. Missing `kit/hierarchical` (3 intermediates feeding depth-3 leaves)

`foo embed collection`, `foo embed collection delete`,
`foo embed collection list`.

The leaves at `foo embed collection {delete,list}` sit at depth 3.
Kit requires every intermediate node from root to the leaf to carry
`kitcli.SetHierarchical(cmd)` — i.e. both `embed` and `embed
collection` need it.

### 7. Passthrough (none currently)

No `cobra.ArbitraryArgs` leaves emit a passthrough warning today.
Re-check after T-0037 in case a leaf flips its `Args` mode.

### 8. Local-global flag collisions (none currently)

The validator did not flag any local flags shadowing kit's global
set (`--format`, `--cols`, `--config`, `--confirm`, `--chdir`,
`--profile`, `--verbose`, `--quiet`, `--api-version`, `--no-prompt`,
`--progress-format`, `--template`). Re-check after T-0037.

## Exit gate

T-0039 re-arms the strict gate and asserts `root.Validate()` returns
nil. All buckets above must be closed before that test passes.
