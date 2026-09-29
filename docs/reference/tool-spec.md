# Tool spec format

Every key of the YAML spec that defines an OS tool: foo's thirteen
built-ins and your own. Task walkthrough:
[Add or change an OS tool](../how-to/write-tool-specs.md).

## Locations and precedence

| Source | Path | `foo tool list` SOURCE |
|--------|------|------------------------|
| User | `<foo config dir>/tools/<name>.yaml` (macOS `~/Library/Application Support/foo/tools`, Linux `~/.config/foo/tools`, or `$XDG_CONFIG_HOME/foo/tools`) | `user:<path>` |
| System | `/etc/xdg/foo/tools/<name>.yaml` | `system:<path>` |
| Built-in | compiled into foo | `builtin` |

For each name the first row that has a file wins, valid or not: a
spec that fails to load is skipped with a warning and its name is
unavailable (`-T` exits 3 with the reason); foo never falls back to a
lower row. A user or system spec with a built-in's name shows
`(overrides builtin)`. Files starting with `.` or not ending in `.yaml`
are ignored.

Unknown keys are errors everywhere.

## Top-level keys

| Key | Required | Default | Meaning |
|-----|----------|---------|---------|
| `spec` | yes | | Schema version. Only `1` |
| `name` | yes | | Tool name for `-T` and the model. `^[a-z][a-z0-9_]{0,31}$`; must equal the file name |
| `description` | yes | | What the model reads. foo appends a sentence on path conventions when the tool has path params |
| `command` | yes | | [Command](#command) |
| `side_effect` | yes | | `read`, `write` or `destructive`. Picks the approval rule; see [side effects](#side-effects) |
| `side_effect_if` | no | | Escalations: `[{when: {param: value, …}, side_effect: …}]`; the first entry whose `when` all match wins |
| `network` | no | `none` | `none`, `local-only` or `egress`. Reported to hosts in `--ext-info`; foo's own policy lookup treats every spec call as `none` |
| `timeout` | no | `30s` | Go duration, at most `120s`. The command's process group is killed when it runs out; the call returns `timeout` |
| `ok_exit_codes` | no | `[0]` | Exit codes reported as `ok: true` (grep: `[0, 1]`) |
| `output` | no | `text` | `text`, or `paths0`: the command prints NUL-separated paths, foo drops each one outside the scope and returns the rest one per line. Needs a `filter_after` path param |
| `params` | no | | [Params](#params) |
| `exclusive` | no | | Groups of two or more param names; a call may set at most one per group |
| `argv` | yes | | [Argument template](#argv-template) |
| `expression_after_paths` | no | `false` | Allow tokens after the path placeholders (find's tests) |
| `script` | no | | [Generated script](#script) |

## Command

| Key | Meaning |
|-----|---------|
| `bin` | Absolute, clean paths. The first that is an executable regular file is pinned when the spec loads; `$PATH` is never searched. None found → spec not loaded. Launchers are rejected: `sh`, `bash`, `zsh`, `env`, `xargs`, `sudo`, `nohup`, `timeout`, `busybox`, `perl`, `python`, `node` and similar |
| `variants` | Overrides per binary flavor, keyed `gnu`, `bsd` or `busybox` |

The flavor comes from `<bin> --version`: `(GNU …)` is `gnu`,
`BusyBox` is `busybox`, anything else (including an error) is `bsd`.
It is cached per binary path, size and mtime. A variant may set:

| Key | Meaning |
|-----|---------|
| `prefix` | Literal tokens inserted right after `bin` (GNU sed: `[--sandbox]`) |
| `params.<name>.argv` | Replaces a bool's `{true, false}` map or an int/string fragment |
| `params.<name>.values` | Replaces some enum values' tokens |
| `ok_exit_codes` | Replaces `ok_exit_codes` |
| `unsupported` | Params removed from the model's schema on this flavor; not a required param or a path param without a default |

## Params

Every param has:

| Key | Meaning |
|-----|---------|
| `name` | `^[a-z][a-z0-9_]{0,31}$`; `script` is reserved |
| `type` | `path`, `bool`, `enum`, `int` or `string`; nothing else exists |
| `description` | Required. The model sees it, plus the default (`Default false.`) |
| `required` | The model must set it. Not with `default`. A `path` param without a `default` is required anyway |
| `default` | Value used when the model leaves it out |

### `path`

The value is resolved from the run's directory (`~` = home; no glob,
no `$VAR`), symlinks and `..` physically, checked against the scope
for `op`, and passed to the command as a canonical absolute path. It
takes no `argv`; place it with `{name}` after `--`.

| Key | Default | Meaning |
|-----|---------|---------|
| `op` | | Required. `[read]`, `[write]`, `[exec]` or a combination; every op is checked |
| `op_when` | | `{when: {…}, op: […]}`: use a narrower op when `when` matches. Dropping `write` needs a `read` side effect under the same condition (sed `dry_run`) |
| `repeated` | `false` | The model passes a JSON array of paths |
| `max_items` | `64` | With `repeated`: 1..256 |
| `must_exist` | `false` | Missing path → `not_found` |
| `kind` | `any` | `file` or `dir`: on a non-recursive call, an existing path of the other kind is refused |
| `target` | `follow` | `follow`: act on what a final symlink points to. `dirent`: act on the entry itself (the link, never its target); the parent directory is checked for `write` too, and a symlink's target for `op` |
| `into_dir` | `false` | An existing directory value receives each source (the path params declared before it) as `dir/<base>`, checked there. Several sources need an existing directory |
| `recursive` / `recursive_when` | | The path is a tree: always (`recursive: true`) or when `{param: value}` matches. Exclusive |
| `recursion` | | Required when the param may recurse. `filter_before`: foo walks the tree (no symlinks) and passes only permitted regular files; needs `op: [read]` and `target: follow`; one per spec. `filter_after`: the command walks, foo filters its `paths0` output. `all_or_nothing`: foo checks every entry first and refuses the call if any is out of scope |
| `parents` / `parents_when` | | Every missing ancestor is checked for `write` too (mkdir -p). Exclusive |
| `clobber_when` | | `{param: value}` that permits an existing destination; otherwise the call is refused with `exists`. An existing directory is always refused. Needs `write` |
| `protect_roots` | `false` | Refuse `/`, the home directory and entries directly under `/` (or what they link to), whatever the scope. Needs `write`. On an `into_dir` param it applies where the entry lands |

### `bool`

| Key | Meaning |
|-----|---------|
| `argv` | `{true: [tokens], false: [tokens]}`; a missing key adds nothing. Literal tokens only |

Unset and no default means `false`.

### `enum`

| Key | Meaning |
|-----|---------|
| `values` | `{value: [tokens]}`. Values match `^[A-Za-z0-9_.-]{1,32}$`; tokens are literal. No `argv` key |

### `int`

| Key | Meaning |
|-----|---------|
| `min`, `max` | Required bounds |
| `argv` | Fragment with one `{}`, e.g. `["-n", "{}"]` |

### `string`

| Key | Default | Meaning |
|-----|---------|---------|
| `max_len` | | Required, 1..4096 characters |
| `pattern` | | Regular expression the whole value must match |
| `newline` | `false` | Allow `\n` and `\r`. NUL is always rejected |
| `argv` | | Fragment with one `{}` |

An `int` or `string` fragment starts with a literal option and puts
the value in its own token after it (`["-e", "{}"]`) or joins it to
the first token (`["--regexp={}"]`). The value can never be a bare
argument, so it cannot become a path or a flag. A `string` is not
path-checked: in a spec for a command foo does not ship, each one
draws a warning (`strings are not path-gated`); make sure the command
cannot read a file named by it.

## `argv` template

`argv` lists literal tokens and `{param}` placeholders, rendered after
`bin` and any variant `prefix`. An unset `enum`, `int` or `string`
param renders nothing; an unset `bool` renders its `false` tokens.

- A placeholder is a whole token and appears once; braces anywhere
  else are an error.
- Every `path` placeholder follows a literal `--`.
- `int` and `string` placeholders come before `--`, unless
  `expression_after_paths` is set and they follow the paths.
- After the first path, only paths, unless `expression_after_paths`;
  never a token between two paths.
- Every `path`, `enum`, `int`, `string` param, and every `bool` with
  `argv`, must be placed.

## `script`

Generates one argument from typed params, so the model never supplies
a script. Only `kind: sed_substitute` exists; it renders
`s<\x01>find<\x01>replace<\x01>flags` into the `{script}` placeholder,
which must follow a literal option (`-e`) before `--`.

| Key | Meaning |
|-----|---------|
| `find`, `replace` | `string` params. `\x01`, newlines and NUL are rejected |
| `flags` | `{bool param: g \| I}` |
| `occurrence` | `int` param rendered as the numeric flag; bounds within 1..512 |
| `backrefs` | `bool` param. Off: `replace` is inserted literally (foo escapes `&` and `\`). On: sed syntax (`&`, `\1`–`\9`); a trailing lone `\` is refused |

Params that feed the script cannot appear in `argv`.

## Side effects

| `side_effect` | Default action |
|---------------|----------------|
| `read` | Runs |
| `write` | Asks first (foo's rule over kit's default) |
| `destructive` | Asks first |

Change these in `tool-policy.yaml`
([how-to](../how-to/use-os-tools.md#7-let-writes-run-without-asking-optional)).
With no terminal, a call that would ask is refused (`declined`).

## At run time

| Aspect | Behavior |
|--------|----------|
| Working directory | Where foo was started; base of relative paths |
| Environment | `PATH=/usr/bin:/bin` plus `HOME`, `LANG`, `LC_*`, `TZ`, `TMPDIR`; everything else dropped |
| stdin | `/dev/null` |
| stdout | First 64 KiB kept; `stdout_bytes` counts all. A NUL in the first 8 KiB or invalid UTF-8 → `stdout: null`, `stdout_binary: true` |
| stderr | First 8 KiB kept |
| Exit code | Returned as a result, never an error; `ok` from `ok_exit_codes` |

## `--ext-info`

Through a `foo-tool-<name>` link, `--ext-info` prints the model-facing
`parameters` schema and a `foo_tool` object with `spec`,
`side_effect`, `side_effect_if`, `network`, `digest` and, per path
param, its `op`, `target` and the other path keys above. `foo_tool` is
foo's description of its own tools; foo does not read it from
third-party plugins.

## Related docs

- [Add or change an OS tool](../how-to/write-tool-specs.md)
- [Reference: `tool`](commands.md#tool) — built-in tools and their
  parameters.
