# Let the model use OS commands safely

Give the model `ls`, `cat`, `grep`, `rm`, `sed` and the other built-in
OS tools, and decide per path and per operation what it may touch.

## Use this when

- You want the model to read or change files while it answers
  (`foo -T grep,cat "where do we parse the config?"`).
- A tool call came back `denied` or `declined` and you want to know
  why.
- You want writes to run without a confirmation, or every call to ask
  first.

## Before you begin

You need:

- foo on your `$PATH` and a model that supports tool calls.
- A directory you are willing to let the model read (and, for write
  tools, change).

foo never runs these tools through a shell. It checks every path
argument against your **scope** (`scope.yaml`) before the command
starts, and with no `scope.yaml` it refuses every call.

## Outcome

After this guide you will have:

- A `scope.yaml` granting read on a project and write on one folder.
- Verified the grant with `foo scope check` before the model sees it.
- Run the model with OS tools and seen what it gets back when a path
  is refused.

## Quick path

```sh
# 1. Grant read on ~/proj (XDG_CONFIG_HOME unset? see step 2 for the default)
mkdir -p "$XDG_CONFIG_HOME/foo"
cat > "$XDG_CONFIG_HOME/foo/scope.yaml" <<'EOF'
allow:
  - path: "~/proj/**"
    ops: [read]
EOF

# 2. Check it
foo scope check ~/proj/src/main.go

# 3. Use it
cd ~/proj && foo -T ls,cat,grep "Where are the TODOs?"
```

## Steps

### 1. Find the tools

```sh
foo tool list
```

Excerpt (DESCRIPTION trimmed):

```
NAME         SOURCE   DESCRIPTION  PARAMS  SIDE-EFFECT  PATHS         STATUS
cat          builtin  Print the…   true    read         path:r        active
cp           builtin  Copy file…   true    write        src:r dst:w   active
rm           builtin  Remove fi…   true    destructive  path:w        active
sed          builtin  Replace t…   true    destructive  path:rw       active
```

Thirteen OS tools ship with foo. Read: `ls`, `cat`, `head`, `tail`,
`wc`, `find`, `grep`, `stat`. Write: `cp`, `mv`, `mkdir`, `rm`, `sed`.
PATHS lists each path argument with the operations foo checks on it
(`r` read, `w` write). A `foo-tool-<name>` plugin on `$PATH` is
checked the same way when it declares its paths under `foo_tool`
([Gate your plugin](write-plugins.md#gate-your-plugin-declare-foo_tool));
one that declares none shows `ungated`: foo cannot check paths a
plugin does not declare, so only `--tools-approve` stands between the
model and it. Enable tools by name with `-T` (repeat it, or
comma-separate). Per-tool arguments:
[Read tools](../reference/commands.md#read-tools) and
[Write tools](../reference/commands.md#write-tools).

### 2. Write `scope.yaml`

`scope.yaml` lives in foo's config directory,
`$XDG_CONFIG_HOME/foo/scope.yaml`. When `XDG_CONFIG_HOME` is unset it
defaults to `~/.config` on Linux and `~/Library/Application Support`
on macOS.

A system-wide `/etc/xdg/foo/scope.yaml` is read first; your file adds
its rules and, when it sets one, its mode wins. Not sure which path applies? Run
`foo scope show`: with no file it names the path it looked for.

```yaml
mode: strict
allow:
  - path: "~/proj/**"
    ops: [read]
  - path: "~/proj/build/**"
    ops: [read, write]
```

- Each rule is a glob (`**` crosses directories, and `dir/**` also
  covers `dir` itself). `~` is your home directory.
- `ops` limits a rule to `read`, `write` and/or `exec`. A bare
  string (`- "~/proj/**"`) covers **every** op, write included.
- `deny:` takes the same forms and always beats `allow:`.
- foo adds a deny list for secrets on every op: `.env`, `*.pem`,
  `*.key`, `id_rsa*`, `credentials*`, `secrets*`, `~/.ssh/**`,
  `~/.aws/**`, browser cookie stores and more. A name on the list
  covers everything under it too: `secrets/key.txt` is denied like
  `secrets.yaml`, and `find` or `grep -r` never show what is inside.
  Names match from their start, so `mysecrets.txt` is not on the
  list. `foo scope show` prints every pattern.
- Credential stores are denied wherever they sit, not only in your
  home: a `.ssh/`, `.aws/`, `.azure/`, `.gnupg/`, `.kube/`, `.pki/`
  or `.config/gcloud/` directory at any depth of a granted tree (on
  macOS also `Library/Keychains/` and `Library/Cookies/`), and any
  `.netrc`, `.pgpass`, `.pypirc` or `.my.cnf` file. Whole names only:
  `.sshx/`, `my.ssh/` and a bare `gcloud/` stay readable. A project
  `.npmrc` stays readable too (registry config); `~/.npmrc` does not.
  Add a `deny:` rule for anything else your tree keeps secret.

What each tool needs: reading tools need `read`; `mkdir`, `rm` and a
`cp`/`mv` destination need `write` on the path **and on its parent
directory** (removing or creating an entry changes the directory);
`mv` sources need `read` and `write`; `sed` needs `read` and `write`,
or only `read` with `dry_run=true`.

### 3. Check the grant

`foo scope check` runs a path through the same checks as a tool call
that reads it and prints the verdict, with the path it resolves to:

```
$ cd ~/proj
$ foo scope check src/main.go
PATH                        OP    DECISION  REASON
/Users/me/proj/src/main.go  read  allowed
$ foo scope check src/main.go --op write
PATH                        OP     DECISION  REASON
/Users/me/proj/src/main.go  write  denied    no scope allow rule covers write here
GENERIC: path denied: a tool call would be refused
```

`foo scope test` checks several paths at once:

```
$ foo scope test build/app.log .env / --op write
PATH                          OP     DECISION  REASON
/Users/me/proj/build/app.log  write  allowed
/Users/me/proj/.env           write  denied    matches a scope deny rule for write
/                             write  denied    no scope allow rule covers write here
GENERIC: 2 of 3 paths denied: a tool call would be refused
```

Exit codes: 0 allowed, 1 denied, 8 would prompt, 9 would warn, 2 for
a `scope.yaml` that does not load. Script on them; see
[`scope`](../reference/commands.md#scope).

### 4. Run the model with tools

Run foo from the directory relative paths should start from: the
model's `path=src` means `<where you ran foo>/src`, and the command
runs there too. `--tools-debug` prints each call and its result on
stderr:

```
$ cd ~/proj
$ foo -T grep,ls --tools-debug "Where are the TODOs?"
[tool] call: grep (id=call_0) args={"pattern": "TODO", "path": ["."], "recursive": true}
[tool] result: grep: {"exit_code":0,"ok":true,"stdout":"/Users/me/proj/src/main.go:3:// TODO: handle errors\n",…,"argv":["/usr/bin/grep","-H","-n","-e","TODO","--","/Users/me/proj/README.md","/Users/me/proj/build/app.log","/Users/me/proj/src/main.go"],"files":3,…,"filtered":1,…}
```

`grep` searched the three permitted files; `.env` was left out and
counted in `filtered`.

### 5. Read what the model gets back

A command that ran returns its output, exit code and the canonical
paths it ran on, whatever the exit code. A call foo refused never
started, and the model gets the reason:

```
$ foo -T ls --tools-debug "What is in /?"
[tool] call: ls (id=call_0) args={"path": ["/"]}
[tool] error: ls: denied: path /: no scope allow rule covers read here
```

The model receives:

```json
{"error":{"kind":"denied","message":"no scope allow rule covers read here","param":"path","path":"/","op":"read"}}
```

| `kind` | Meaning |
|--------|---------|
| `denied` | The scope refused a path (or there is no `scope.yaml`) |
| `declined` | Approval was needed and you said no, or nobody could be asked |
| `policy` | `tool-policy.yaml` denies this side effect on the tool's network |
| `invalid_args` | Bad arguments, an existing destination without `overwrite=true`, or a protected path (below) |
| `not_found` | A path that must exist does not (only for a path the scope grants) |
| `timeout` | The command ran past its limit (30s; 60s for `find`, `grep`, `cp`, `mv`, `rm`); output discarded |

One denied path refuses the whole call: `cat a b` with `b` denied
prints nothing.

Outside the grant the model learns nothing about your files: a path
the scope does not grant gets the same refusal whether it exists or
not, and whatever it is (file, directory, link, or a path through
one, even a link that points into the grant). `not_found`, "is a directory" and similar errors come only for
paths the scope grants, or after you approve the path in
`mode: prompt`.

### 6. Approve changes

`write` and `destructive` calls ask before they run; reads do not.
The question shows the exact command foo will run:

```
[tool] rm wants to run: /bin/rm -- /Users/me/proj/build/app.log
  policy: destructive side effect (irreversible local mutation; surface the diff to the user)
Allow this call? [y/N] y
```

Answers come from your terminal (`/dev/tty`), never from stdin, so
`cat notes.md | foo -T sed ...` still asks you. With no terminal (CI,
cron, a detached session) nothing is asked and the call comes back
`declined`:

```
[tool] error: rm: declined: approval required but cannot be asked: no terminal to read the answer from (open /dev/tty: device not configured)
```

`--tools-approve` asks before **every** call, reads included. It never
overrides a denial. A call you decline comes back `declined` from
every tool, plugins and built-ins included.

### 7. Let writes run without asking (optional)

To auto-allow writes that stay inside your grant, create
`$XDG_CONFIG_HOME/foo/tool-policy.yaml`, next to `scope.yaml`:

```yaml
schema_version: "1.0"
rules:
  - side_effect: write
    network: none
    action: auto-allow
    reason: "writes stay inside the scope grant"
```

Now `mkdir`, `cp` and `mv` run without a question; `rm`, `sed` and
`overwrite=true` (side effect `destructive`) still ask. Add a
`destructive` rule to change those too, or `action: deny` to forbid a
class outright (the call returns `policy`).

A rule matches a tool by its side effect and the `network` its spec
declares. The built-in tools declare `network: none`. A
[tool spec](../reference/tool-spec.md#side-effects) of yours that
declares `local-only` or `egress` gets that network's rule instead:
by default a write there asks, an `egress` read asks, and an `egress`
destructive call is refused. To let one of your egress tools read
without asking:

```yaml
  - side_effect: read
    network: egress
    action: auto-allow
    reason: "my mirror tool only fetches from a trusted host"
```

Name the network, not `any`: an exact `(side_effect, network)` rule
beats `any`, and kit and foo already have an exact rule for every
`read`, `write` and `destructive` cell, so a `network: any` rule never
takes effect for these tools.

### 8. Pick a mode

`mode:` in `scope.yaml` decides what happens to a path a deny rule
matches, or that no allow rule covers:

| Mode | Such a path |
|------|-------------|
| `strict` (default) | Denied |
| `prompt` | foo asks once per call, naming each path, op and reason; denied with no terminal |
| `warn` | Runs, and foo logs one warning per call |

```
[tool] ls wants to run: /bin/ls -1 -- /usr
  scope: read /usr (no scope rule covers this path)
Allow this call? [y/N]
```

A recursive `grep` or `find` never asks per file: in `strict` and
`prompt` modes it skips such entries and counts them in `filtered`.

## Common issues

| Symptom | Cause | Fix |
|---------|-------|-----|
| `USAGE: load tool scope policy: …` or `… side-effect policy: …`, exit 2 | `scope.yaml` or `tool-policy.yaml` under `$XDG_CONFIG_HOME/foo` does not load; the message names the file and the problem. Runs that select no OS tool are unaffected; links (`foo-tool-*`) print the same on stderr and exit 2 | Fix the file; `foo scope show` reports the same error |
| Every call `denied` with `no scope policy: … scope.yaml does not exist` | No `scope.yaml` where foo looks | Create the file the message names; `foo scope show` prints the path |
| `denied` on a path you allowed | A symlink on the way resolves outside the rule, a `..` climbs through a directory outside it, or a relative path was taken from another directory | `foo scope check <path>` from the same directory shows the resolved path; grant that, or name the path without `..` |
| `denied` on a path through a link into your grant | The link sits in a directory the scope does not grant, and no rule names the path through it: `~/code` links to `/data/code`, the rule says `/data/code/**`, the call sends `~/code/x` | Write the rule the way paths are sent (`~/code/**`), or send the target path. `foo scope check ~/code/x` shows the refusal and the target |
| `declined … cannot be asked: no terminal` | A write or destructive call, or `mode: prompt`, with no terminal | Run on a terminal, or auto-allow with `tool-policy.yaml` |
| `rm`/`mkdir` denied inside a write grant | The entry's parent directory is not writable in the scope | Grant `dir/**` rather than `dir/sub/**`, or accept that the grant root itself cannot be removed |
| Output shows `/private/tmp/…` for `/tmp/…` (macOS) | foo reports canonical paths; `/tmp` links to `/private/tmp` | Nothing to fix; rules written as `/tmp/**` still match, for paths sent as `/tmp/…` too. Refusals name such paths as sent |
| `invalid_args: is the home directory …` | `rm`, `mv` and `cp` never act on `/`, `~` or an entry directly under `/`, nor on a granted link to one (possibly after you approved the call) | Name a path below them |
| `timeout` | The command outran its limit (a FIFO, a huge tree) | Narrow the path; `find maxdepth=`, `head` instead of `cat` |

More: [Troubleshooting](../troubleshooting.md#tool-calls-denied-or-declined).

## How it works

For every call, before anything runs, foo:

1. Validates the arguments against the tool's spec: types, bounds,
   no unknown keys. There is no free-form argument: flags come only
   from typed parameters.
2. Resolves each path from the directory you ran foo in (`~` is
   home; no globbing, no `$VAR`), following symlinks and `..`
   physically, so `link/..` means the parent of the link's target.
3. Checks every path against the scope for the op the tool performs,
   then the side effect against the policy table, and asks once if
   either wants a confirmation. Only then does it report what is
   wrong with a path (missing, wrong kind, unresolvable). A `..` that
   climbs out of a directory the scope does not grant is refused, and
   a refusal names a path outside the grant as it was sent, not what
   it resolves to. So is a path that follows a symlink sitting in a
   directory the scope does not grant, wherever it points, unless a
   rule as you wrote it names the path through that link (`/tmp/**`
   on macOS, `~/code/**` where `~/code` is a link). A link from
   outside into the grant is no way in, as one from inside to outside
   is no way out; otherwise what the link points to would be the
   answer. Only links in the path as sent are held to this: a link
   the model reaches *through* another link (a granted `p/x` pointing
   at `/out/y`, which points back into the grant) is followed like
   any other, so the answer can still show whether `/out/y` leads into
   the grant. The model cannot create links, so this needs one already
   on disk; keep links that leave the grant out of granted trees if
   that matters.
4. Runs the pinned binary (`/bin/ls`, never a `$PATH` lookup) with
   the **checked** canonical paths, each after a literal `--`, so a
   path named `-R` is a file, never a flag.

`rm`, `mv` and `cp` also refuse `/`, the home directory and every entry
directly under `/`, whatever the scope. A path that names one as written
(`~`, `/`, `/tmp/..`, `/usr`) is refused before step 3, with no question
asked. A path that reaches one only through the disk (a link to `~`,
`/private/tmp` on macOS, where an `mv` or `cp` destination lands) is
refused after step 3 and before step 4: you may be asked first and
refused after. A path outside the scope gets the scope's `denied` either
way, so a refusal never tells what an ungranted link points to.
Recursive `rm`, `cp` and `mv` check every entry of the tree and refuse
the whole call if one is out of scope.

## Related docs

- [Reference: `tool`](../reference/commands.md#tool) and
  [`scope`](../reference/commands.md#scope) — every flag and column.
- [Share foo's OS tools with other hosts](share-os-tools.md) —
  `foo tool install`.
- [Add or change an OS tool](write-tool-specs.md) — your own specs.
- [Concepts: tools](../concepts.md#tools-agentic-dispatch) — how
  tools fit in.
