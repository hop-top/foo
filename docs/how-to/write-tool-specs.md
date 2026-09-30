# Add or change an OS tool

Describe a command in a YAML spec and foo offers it to the model with
the same path checks as its built-in tools. The same file format lets
you replace a built-in, for example a stricter `head`.

## Use this when

- You want the model to run a command foo does not ship (`file`,
  `cut`, …) with its path arguments checked against your scope.
- You want to change a built-in tool: fewer flags, lower limits.

For a tool that is a program of its own (calls an API, keeps state),
write a [plugin](write-plugins.md#llm-tool-plugin--foo-tool-name)
instead: foo cannot check the paths a plugin opens.

## Before you begin

You need a `scope.yaml`
([Let the model use OS commands safely](use-os-tools.md)) and the
absolute path of the command's binary.

## Outcome

A `<name>.yaml` in foo's `tools` directory that `foo tool list` shows
and `-T <name>` enables.

## Quick path

```sh
mkdir -p "$XDG_CONFIG_HOME/foo/tools"
$EDITOR "$XDG_CONFIG_HOME/foo/tools/file.yaml"
foo tool list | grep '^file '
```

## Steps

### 1. Write the spec

Save as `$XDG_CONFIG_HOME/foo/tools/file.yaml`, next to `scope.yaml`
(`XDG_CONFIG_HOME` defaults to `~/.config` on Linux and
`~/Library/Application Support` on macOS). The file name must be the
tool name:

```yaml
spec: 1
name: file
description: Identify the type of each file from its content.
command:
  bin: [/usr/bin/file]
side_effect: read
params:
  - name: path
    type: path
    description: Files to identify.
    op: [read]
    repeated: true
    max_items: 16
    must_exist: true
  - name: mime
    type: bool
    description: Print a MIME type instead of a description.
    default: false
    argv: {true: ["--mime-type"]}
argv: ["-b", "{mime}", "--", "{path}"]
```

The rules that matter most:

- `bin` is absolute and pinned when foo loads the spec; `$PATH` is
  never searched. Shells, `env`, `xargs`, `sudo` and other launchers
  are rejected.
- Every argument the model can set is a typed `param`. A `path`
  param is resolved and checked for its `op` before the call; its
  placeholder must come after a literal `--` in `argv`.
- `side_effect` (`read`, `write`, `destructive`) decides whether the
  call asks first.

Every key: [Reference: tool spec format](../reference/tool-spec.md).

### 2. Check it loaded

Excerpt (some columns elided):

```
$ foo tool list
NAME  SOURCE                                         …  SIDE-EFFECT  PATHS   STATUS
file  user:$XDG_CONFIG_HOME/foo/tools/file.yaml      …  read         path:r  active
```

A spec that fails to load is left out, and `foo tool list` says why
on stderr:

```
[foo] warning: skipping tool spec shell (user:…/tools/shell.yaml): invalid tool spec: command.bin "/bin/sh" is a program launcher; argv: path {path} must follow a literal --
```

`-T` with that name fails with the same reason and exit 3; foo never
falls back to a built-in of the same name.

### 3. Mind the `string` warning

`string` params are allowed but not path-checked. For a command foo
does not ship, foo warns once per string param:

```
[foo] warning: tool spec cut (user:…/tools/cut.yaml): param "fields": strings are not path-gated; make sure cut cannot read a path from it
```

Before you keep one, make sure the command cannot treat that value as
a file name (`grep -f`, `file -m`, `jq`'s `include` all can). Narrow it
with `pattern:` and `max_len:`.

### 4. Override a built-in (optional)

Save a spec with a built-in's name, e.g. `tools/head.yaml`, and it
replaces foo's `head` everywhere, including `foo tool install` links
(excerpt):

```
$ foo tool list
NAME  SOURCE                                                             …  STATUS
head  user:$XDG_CONFIG_HOME/foo/tools/head.yaml (overrides builtin)      …  active
```

Start from the built-in's behavior in
[Read tools](../reference/commands.md#read-tools) /
[Write tools](../reference/commands.md#write-tools).

## Common issues

| Symptom | Cause | Fix |
|---------|-------|-----|
| Tool missing from `foo tool list` | Spec failed to load | Read the `skipping tool spec` warning on stderr |
| `name "x" does not match file name "y.yaml"` | `name:` differs from the file name | Rename one |
| `no executable among command.bin` | None of the `bin` paths exists here | List every location (`[/usr/bin/x, /bin/x]`) |
| `path {p} must follow a literal --` | Path placeholder before `--` | Put `"--"` before path placeholders in `argv` |
| `<type> params need an argv fragment` | `int`/`string` param without `argv: ["-x", "{}"]` | Add the option it fills |
| Listed as `shadowed` | A built-in Go tool (`foo_time`, `foo_version`) owns the name | Pick another name |

## How it works

foo reads specs from three places; for each name the highest one wins:

1. `$XDG_CONFIG_HOME/foo/tools/<name>.yaml` (user),
2. `/etc/xdg/foo/tools/<name>.yaml` (system),
3. foo's embedded specs (built-in).

Specs outrank `foo-tool-<name>` plugins on `$PATH`: a plugin with a
spec's name is listed as `shadowed` and never runs. foo's Go built-ins
(`foo_time`, `foo_version`) outrank specs.

## Related docs

- [Reference: tool spec format](../reference/tool-spec.md)
- [Let the model use OS commands safely](use-os-tools.md)
- [Share foo's OS tools with other hosts](share-os-tools.md)
