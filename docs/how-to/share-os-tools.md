# Share foo's OS tools with other hosts

Let another program that speaks foo's tool-plugin protocol run foo's
OS tools (`ls`, `grep`, `rm`, …) under the same path scope you set up
for `-T`.

## Use this when

- Another agent host runs `foo-tool-<name>` plugins and you want it to
  have `foo-tool-ls`, `foo-tool-grep` and the rest.
- You want those calls held to your `scope.yaml`, not to whatever the
  host allows.

You do **not** need this for `foo -T ls`: foo runs its own tools
in-process.

## Before you begin

You need:

- A working `scope.yaml`
  ([Let the model use OS commands safely](use-os-tools.md#2-write-scopeyaml)).
  Without one, every call through a link is denied.
- A host that execs `foo-tool-<name>` binaries found on `$PATH` and
  writes one JSON request on stdin
  ([call protocol](write-plugins.md#call-protocol)).

## Outcome

After this guide you will have a `foo-tool-<name>` link for each of
foo's spec tools on the host's `$PATH`, gated by your scope.

## Quick path

```sh
foo tool install --dir ~/.local/bin      # a directory already on $PATH
echo '{"name":"ls","arguments":{"path":["src"]}}' | foo-tool-ls
```

## Steps

### 1. Create the links

```
$ foo tool install
[foo] note: /Users/me/.local/bin/foo is not on $PATH; add it for other hosts to find the links
NAME   LINK                                      ACTION   REASON
cat    /Users/me/.local/bin/foo/foo-tool-cat     created
cp     /Users/me/.local/bin/foo/foo-tool-cp      created
…
```

Each link is a symlink named `foo-tool-<name>` pointing at the foo
binary. The default directory, `~/.local/bin/foo`, is off `$PATH` on
purpose: add it to the host's `PATH`, or pass `--dir` with a directory
that is already on it. Running `install` again reports `unchanged`;
links for specs you removed are deleted; anything at a link path that
is not a link to foo is left alone and reported `skipped`.

### 2. Call a tool the way a host does

```
$ cd ~/proj
$ echo '{"name":"ls","arguments":{"path":["src"]}}' | foo-tool-ls
{"result":{"exit_code":0,"ok":true,"stdout":"main.go\n",…,"argv":["/bin/ls","-1","--","/Users/me/proj/src"],…}}
```

Relative paths start from the directory the host runs the link in.
A refusal is a normal response (exit 0): `error` is a string, as the
plugin protocol defines it, and `error_detail` carries the structured
form (`kind`, `message`, `param`, `path`, `op`).

### 3. Know what a link never does: ask

Run through a link, foo checks arguments, paths and the side-effect
policy exactly as it does for `-T`, but it never asks a question. Any
call that would prompt (a `write` or `destructive` side effect, or a
path `mode: prompt` would ask about) is refused:

```
$ echo '{"name":"rm","arguments":{"path":["build/app.log"]}}' | foo-tool-rm
{"error":"declined: approval required but cannot be asked: no terminal to ask for approval","error_detail":{"kind":"declined","message":"approval required but cannot be asked: no terminal to ask for approval"}}
```

To let a host change files, auto-allow that side effect in
`tool-policy.yaml`
([step 7](use-os-tools.md#7-let-writes-run-without-asking-optional)).
It applies to `-T` runs too.

### 4. Remove the links

```sh
foo tool uninstall                  # or: foo tool uninstall --dir ~/.local/bin
```

Only links that point at foo are removed.

## Common issues

| Symptom | Cause | Fix |
|---------|-------|-----|
| Host cannot find `foo-tool-ls` | Link directory not on the host's `$PATH` | Add it, or reinstall with `--dir` |
| `skipped: exists and is not a link to foo` | Another file owns that name | Remove or rename it; foo never overwrites it |
| Every call `declined` | The call needs approval and links never ask | Auto-allow in `tool-policy.yaml`, or use `foo -T` on a terminal |
| Links break after moving foo | Links point at the foo path used at install time | Run `foo tool install` again |
| `foo-tool-x: no tool spec named "x"` (exit 3) | A link whose name matches no spec | `foo tool list`; `foo tool uninstall` cleans stale links |

## How it works

foo checks the name it was started as. `foo-tool-ls --ext-info` prints
the tool's discovery metadata: `parameters` (the JSON Schema the host
passes to its model) and `foo_tool`, foo's own annotations (side
effect, and for each path argument its ops and how it is resolved).
Discovery reads no policy file. A call reads your `scope.yaml` and
`tool-policy.yaml` from foo's config directory, like `-T` does. A
manifest in foo's state directory records the links foo created, so
`install` and `uninstall` touch nothing else.

`foo tool list` skips links to foo on `$PATH`, so installing them
never adds a second `ls`.

## Related docs

- [Let the model use OS commands safely](use-os-tools.md)
- [Write plugins: call protocol](write-plugins.md#call-protocol)
- [Reference: `tool`](../reference/commands.md#tool)
