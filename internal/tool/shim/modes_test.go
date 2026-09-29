package shim

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hop.top/foo/internal/tool/gate"
	"hop.top/kit/go/core/scope"
)

// grepSpec exercises filter-before: foo walks, the authorizer grants
// files, grep gets an explicit list and never -r.
const grepSpec = `
spec: 1
name: grepx
description: Fixture grep with foo-side recursion.
command: {bin: [/usr/bin/grep]}
side_effect: read
ok_exit_codes: [0, 1]
params:
  - {name: pattern, type: string, description: s, max_len: 64, required: true, argv: ["-e", "{}"]}
  - name: path
    type: path
    description: p
    op: [read]
    repeated: true
    recursive_when: {recursive: true}
    recursion: filter_before
  - {name: recursive, type: bool, description: r}
argv: ["-H", "-n", "{pattern}", "--", "{path}"]
`

func grepTree(t *testing.T) string {
	dir := tempDir(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "TODO one\n")
	writeFile(t, filepath.Join(dir, ".env"), "TODO secret\n")
	writeFile(t, filepath.Join(dir, "sub", "b.txt"), "nothing\nTODO two\n")
	return dir
}

func TestFilterBefore_ExplicitFilesMergedChunks(t *testing.T) {
	dir := grepTree(t)
	for i := range 30 {
		writeFile(t, filepath.Join(dir, "many", "f"+strings.Repeat("x", i)), "TODO many\n")
	}
	l := mustSpec(t, grepSpec)
	auth := &fakeAuth{cwd: dir, deny: map[string]bool{".env": true}}
	e := &Engine{Authorizer: auth, Cwd: dir, ArgBudget: 1024}

	res := mustCall(t, e, l, `{"pattern":"TODO","path":["."],"recursive":true}`)

	if pa := auth.last.Paths[0]; pa.Recursion != gate.FilterBefore || pa.Op != scope.Read {
		t.Fatalf("request path = %+v; want filter-before read", pa)
	}
	out := stdout(res)
	if strings.Contains(out, "secret") || strings.Contains(out, ".env") {
		t.Fatalf("denied file reached grep: %q", out)
	}
	for _, want := range []string{filepath.Join(dir, "a.txt") + ":1:TODO one", filepath.Join(dir, "sub", "b.txt") + ":2:TODO two"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "TODO many"); n != 30 {
		t.Errorf("merged chunks hold %d of 30 matches", n)
	}
	if res.Chunks < 2 || res.Files != 32 || res.Filtered != 1 || res.ExitCode != 0 || !res.OK {
		t.Errorf("chunks %d files %d filtered %d exit %d ok %v", res.Chunks, res.Files, res.Filtered, res.ExitCode, res.OK)
	}
	for _, a := range res.Argv {
		if a == "-r" || a == "-R" {
			t.Fatalf("argv %q recurses in grep", res.Argv)
		}
	}

	// No match anywhere: grep's 1 is ok.
	res = mustCall(t, e, l, `{"pattern":"ABSENT","path":["."],"recursive":true}`)
	if res.ExitCode != 1 || !res.OK {
		t.Errorf("no match: exit %d ok %v; want 1, true", res.ExitCode, res.OK)
	}
	// Non-recursive: the root itself is passed, no walk.
	res = mustCall(t, e, l, `{"pattern":"TODO","path":["a.txt"]}`)
	if auth.last.Paths[0].Recursion != gate.NoRecursion || !strings.Contains(stdout(res), "TODO one") {
		t.Errorf("non-recursive call: recursion %v stdout %q", auth.last.Paths[0].Recursion, stdout(res))
	}
}

// findSpec exercises filter-after: find walks, foo drops every output
// entry the authorizer's Allow rejects.
const findSpec = `
spec: 1
name: findx
description: Fixture find with per-entry output filtering.
command: {bin: [/usr/bin/find]}
side_effect: read
output: paths0
expression_after_paths: true
params:
  - {name: path, type: path, description: p, op: [read], recursive: true, recursion: filter_after}
  - {name: name, type: string, description: n, max_len: 255, argv: ["-name", "{}"]}
argv: ["-P", "--", "{path}", "{name}", "-print0"]
`

func TestFilterAfter_DropsDeniedEntries(t *testing.T) {
	dir := grepTree(t)
	writeFile(t, filepath.Join(dir, "secret", "k.txt"), "k")
	l := mustSpec(t, findSpec)
	auth := &fakeAuth{cwd: dir, deny: map[string]bool{".env": true, "secret": true}}
	e := &Engine{Authorizer: auth, Cwd: dir}

	res := mustCall(t, e, l, `{"path":".","name":"*"}`)

	if auth.last.Paths[0].Recursion != gate.FilterAfter {
		t.Fatalf("recursion = %v; want filter-after", auth.last.Paths[0].Recursion)
	}
	lines := strings.Split(strings.TrimSpace(stdout(res)), "\n")
	got := map[string]bool{}
	for _, ln := range lines {
		got[ln] = true
	}
	for _, want := range []string{dir, filepath.Join(dir, "a.txt"), filepath.Join(dir, "sub", "b.txt")} {
		if !got[want] {
			t.Errorf("output lacks %s: %q", want, lines)
		}
	}
	for ln := range got {
		if strings.Contains(ln, "secret") || strings.Contains(ln, ".env") {
			t.Errorf("denied entry %s in output", ln)
		}
	}
	if res.Filtered != 3 { // .env, secret, secret/k.txt
		t.Errorf("filtered = %d; want 3", res.Filtered)
	}
	if strings.Contains(stdout(res), "\x00") || res.StdoutBinary {
		t.Error("paths0 output must reach the model as lines, not NUL-separated")
	}
	if res.StdoutBytes != int64(len(stdout(res))) {
		t.Errorf("stdout_bytes %d counts pre-filter output; want post-filter %d", res.StdoutBytes, len(stdout(res)))
	}
}

// cpSpec exercises dirent targets, into_dir, all-or-nothing recursion
// and side_effect_if escalation — all visible in the gate request.
const cpSpec = `
spec: 1
name: cpx
description: Fixture cp.
command: {bin: [%BIN%]}
side_effect: write
side_effect_if:
  - when: {overwrite: true}
    side_effect: destructive
params:
  - {name: src, type: path, description: s, op: [read], repeated: true, must_exist: true, recursive_when: {recursive: true}, recursion: all_or_nothing}
  - {name: dst, type: path, description: d, op: [write], target: dirent, into_dir: true, recursive_when: {recursive: true}, recursion: all_or_nothing}
  - {name: recursive, type: bool, description: r, argv: {true: ["-R", "-P"]}}
  - {name: overwrite, type: bool, description: o, argv: {false: ["-n"]}}
argv: ["{recursive}", "{overwrite}", "--", "{src}", "{dst}"]
`

func TestRequest_DirentIntoDirRecursionEscalation(t *testing.T) {
	dir := tempDir(t)
	bin := writeScript(t, dir, "cpbin", `for a in "$@"; do printf '[%s]' "$a"; done`)
	l := mustSpec(t, strings.ReplaceAll(cpSpec, "%BIN%", bin))
	auth := &fakeAuth{cwd: dir}
	e := &Engine{Authorizer: auth, Cwd: dir}

	res := mustCall(t, e, l, `{"src":["a"],"dst":"b"}`)
	req := auth.last
	if req.SideEffect != "write" {
		t.Errorf("side effect = %q; want write", req.SideEffect)
	}
	src, dst := req.Paths[0], req.Paths[1]
	if src.Target != gate.Follow || src.Op != scope.Read || !src.MustExist || src.Recursion != gate.NoRecursion {
		t.Errorf("src = %+v", src)
	}
	if dst.Target != gate.Dirent || !dst.IntoDir || dst.Op != scope.Write {
		t.Errorf("dst = %+v", dst)
	}
	if got := stdout(res); got != "[-n][--]["+filepath.Join(dir, "a")+"]["+filepath.Join(dir, "b")+"]" {
		t.Errorf("argv = %s", got)
	}

	mustCall(t, e, l, `{"src":["a"],"dst":"b","recursive":true,"overwrite":true}`)
	if req := auth.last; req.SideEffect != "destructive" || req.Paths[0].Recursion != gate.AllOrNothing ||
		req.Paths[1].Recursion != gate.AllOrNothing || !req.Paths[1].IntoDir {
		t.Errorf("escalated request: side effect %q, src %+v, dst %+v", req.SideEffect, req.Paths[0], req.Paths[1])
	}
	if prompt := auth.last.Argv(map[string][]string{"src": {"/x/a"}, "dst": {"/y/b"}}); strings.Join(prompt, " ") != bin+" -R -P -- /x/a /y/b" {
		t.Errorf("prompt argv = %q", prompt)
	}
}

// sedSpec exercises the generated script: the model passes find and
// replace strings, foo renders the s command.
const sedSpec = `
spec: 1
name: sedx
description: Fixture substitution.
command:
  bin: [/usr/bin/sed]
  variants:
    gnu: {prefix: ["--sandbox"]}
    bsd:
      params:
        dry_run: {argv: {false: ["-i", ""]}}
side_effect: destructive
side_effect_if: [{when: {dry_run: true}, side_effect: read}]
params:
  - {name: path, type: path, description: p, op: [read, write], kind: file}
  - {name: find, type: string, description: f, max_len: 1024, required: true}
  - {name: replace, type: string, description: r, max_len: 1024, required: true}
  - {name: global, type: bool, description: g}
  - {name: ignore_case, type: bool, description: i}
  - {name: occurrence, type: int, description: o, min: 1, max: 512}
  - {name: dry_run, type: bool, description: d, argv: {false: ["-i"]}}
script: {kind: sed_substitute, find: find, replace: replace, flags: {global: g, ignore_case: I}, occurrence: occurrence}
argv: ["{dry_run}", "-e", "{script}", "--", "{path}"]
`

func TestSedScript_GeneratedNeverModelSupplied(t *testing.T) {
	dir := tempDir(t)
	f := writeFile(t, filepath.Join(dir, "f.txt"), "a-A-a\n")
	l := mustSpec(t, sedSpec)
	auth := &fakeAuth{cwd: dir}
	e := &Engine{Authorizer: auth, Cwd: dir}

	res := mustCall(t, e, l, `{"path":"f.txt","find":"a","replace":"b","global":true,"ignore_case":true,"dry_run":true}`)
	if i := indexOf(res.Argv, "s\x01a\x01b\x01gI"); i < 1 || res.Argv[i-1] != "-e" {
		t.Fatalf("argv %q lacks `-e s^Aa^Ab^AgI`", res.Argv)
	}
	if got := stdout(res); got != "b-b-b\n" || auth.last.SideEffect != "read" {
		t.Errorf("dry run: stdout %q side effect %q", got, auth.last.SideEffect)
	}
	if data, _ := os.ReadFile(f); string(data) != "a-A-a\n" {
		t.Errorf("dry run edited the file: %q", data)
	}

	mustCall(t, e, l, `{"path":"f.txt","find":"a","replace":"c","occurrence":2}`)
	if data, _ := os.ReadFile(f); string(data) != "a-A-c\n" || auth.last.SideEffect != "destructive" {
		t.Errorf("in-place edit: file %q side effect %q", data, auth.last.SideEffect)
	}

	// A script without a backrefs param is always literal.
	mustCall(t, e, l, `{"path":"f.txt","find":"c","replace":"[&\\1]"}`)
	if data, _ := os.ReadFile(f); string(data) != "a-A-[&\\1]\n" {
		t.Errorf("literal replace: file %q", data)
	}

	for _, bad := range []string{
		`{"path":"f.txt","find":"a","replace":"b\nw /tmp/leak"}`,
		`{"path":"f.txt","find":"a\u0001","replace":"b"}`,
		`{"path":"f.txt","find":"a\\","replace":"b"}`,
		`{"path":"f.txt","find":"a","replace":"b","script":"w /tmp/leak"}`,
	} {
		_, err := call(t, e, l, bad)
		wantKind(t, err, gate.KindInvalidArgs)
	}
}

func TestVariants_FlavorFromPinnedBinary(t *testing.T) {
	dir := tempDir(t)
	gnu := writeScript(t, dir, "gnubin", `if [ "$1" = --version ]; then echo "wc (GNU coreutils) 9.1"; exit 0; fi
for a in "$@"; do printf '[%s]' "$a"; done`)
	bsd := writeScript(t, dir, "bsdbin", `if [ "$1" = --version ]; then echo "grep (BSD grep, GNU compatible) 2.6.0-FreeBSD"; exit 0; fi
for a in "$@"; do printf '[%s]' "$a"; done`)
	const spec = `
spec: 1
name: varx
description: Fixture with flavor variants.
command:
  bin: [%BIN%]
  variants:
    gnu:
      prefix: ["--gnu"]
      params: {long: {argv: {true: ["--long"]}}}
    bsd:
      unsupported: [fancy]
      ok_exit_codes: [0, 3]
side_effect: read
params:
  - {name: path, type: path, description: p, op: [read]}
  - {name: long, type: bool, description: l, argv: {true: ["-l"]}}
  - {name: fancy, type: bool, description: f, argv: {true: ["-F"]}}
argv: ["{long}", "{fancy}", "--", "{path}"]
`
	cache := filepath.Join(dir, "flavors.json")
	e := &Engine{Authorizer: &fakeAuth{cwd: dir}, Cwd: dir, Flavors: NewFlavors(cache)}

	lg := mustSpec(t, strings.ReplaceAll(spec, "%BIN%", gnu))
	res := mustCall(t, e, lg, `{"path":"x","long":true,"fancy":true}`)
	if got := stdout(res); got != "[--gnu][--long][-F][--]["+filepath.Join(dir, "x")+"]" {
		t.Errorf("gnu argv = %s", got)
	}

	lb := mustSpec(t, strings.ReplaceAll(spec, "%BIN%", bsd))
	res = mustCall(t, e, lb, `{"path":"x","long":true}`)
	if got := stdout(res); got != "[-l][--]["+filepath.Join(dir, "x")+"]" {
		t.Errorf("bsd argv = %s (BSD grep's 'GNU compatible' must not read as gnu)", got)
	}
	_, err := call(t, e, lb, `{"path":"x","fancy":true}`)
	wantKind(t, err, gate.KindInvalidArgs)
	if strings.Contains(string(NewTool(e, lb).Parameters()), "fancy") {
		t.Error("unsupported param still in the bsd schema")
	}
	if data, err := os.ReadFile(cache); err != nil || !strings.Contains(string(data), "gnu") {
		t.Errorf("flavor cache %q not persisted: %v", data, err)
	}
}

func TestClassifyVersion(t *testing.T) {
	for out, want := range map[string]string{
		"wc (GNU coreutils) 9.1":                       FlavorGNU,
		"grep (GNU grep) 3.11":                         FlavorGNU,
		"grep (BSD grep, GNU compatible) 2.6.0":        FlavorBSD,
		"/usr/bin/wc: illegal option -- -":             FlavorBSD,
		"BusyBox v1.36.1 (2024) multi-call binary.":    FlavorBusyBox,
		"sed: unrecognized option: version\nBusyBox v": FlavorBusyBox,
	} {
		if got := ClassifyVersion(out); got != want {
			t.Errorf("ClassifyVersion(%q) = %s; want %s", out, got, want)
		}
	}
}

// TestIntoDir_ArgvAndNoClobber runs real cp: a destination directory
// receives each source at the path the authorizer mapped and checked,
// and an existing entry there is refused unless overwrite=true.
func TestIntoDir_ArgvAndNoClobber(t *testing.T) {
	dir := tempDir(t)
	writeFile(t, filepath.Join(dir, "a"), "A")
	writeFile(t, filepath.Join(dir, "b"), "B")
	out := filepath.Join(dir, "out")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := strings.Replace(strings.ReplaceAll(cpSpec, "%BIN%", "/bin/cp"),
		"into_dir: true,", "into_dir: true, clobber_when: {overwrite: true},", 1)
	if !strings.Contains(spec, "clobber_when") {
		t.Fatal("fixture edit did not apply")
	}
	l := mustSpec(t, spec)
	auth := &fakeAuth{cwd: dir}
	e := &Engine{Authorizer: auth, Cwd: dir}

	res := mustCall(t, e, l, `{"src":["a","b"],"dst":"out"}`)
	if got := res.Argv[len(res.Argv)-1]; got != out {
		t.Fatalf("argv %q: target %q; want the directory %s", res.Argv, got, out)
	}
	if res.ExitCode != 0 {
		t.Fatalf("cp failed: %s", res.Stderr)
	}
	for name, want := range map[string]string{"a": "A", "b": "B"} {
		if data, _ := os.ReadFile(filepath.Join(out, name)); string(data) != want {
			t.Errorf("out/%s = %q", name, data)
		}
	}
	if len(res.Paths) != 4 || res.Paths[3].Resolved != filepath.Join(out, "b") {
		t.Errorf("paths = %+v; want one dst row per mapped source", res.Paths)
	}

	// Existing out/a: refused without overwrite, nothing copied.
	writeFile(t, filepath.Join(dir, "a"), "A2")
	_, err := call(t, e, l, `{"src":["a"],"dst":"out"}`)
	if ge := wantKind(t, err, gate.KindInvalidArgs); ge.Path != filepath.Join(out, "a") || ge.Param != "dst" {
		t.Errorf("clobber refusal = %+v", ge)
	}
	if data, _ := os.ReadFile(filepath.Join(out, "a")); string(data) != "A" {
		t.Errorf("refused call still copied: out/a = %q", data)
	}
	res = mustCall(t, e, l, `{"src":["a"],"dst":"out","overwrite":true}`)
	if data, _ := os.ReadFile(filepath.Join(out, "a")); res.ExitCode != 0 || string(data) != "A2" {
		t.Errorf("overwrite: exit %d out/a = %q", res.ExitCode, data)
	}

	// An existing directory at the mapped place: refused even with
	// overwrite (cp would nest inside it, unchecked).
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(out, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = call(t, e, l, `{"src":["d"],"dst":"out","recursive":true,"overwrite":true}`)
	wantKind(t, err, gate.KindInvalidArgs)

	// A new name is passed as-is.
	res = mustCall(t, e, l, `{"src":["b"],"dst":"out/renamed"}`)
	if got := res.Argv[len(res.Argv)-1]; got != filepath.Join(out, "renamed") {
		t.Errorf("rename target = %q", got)
	}
	// Several sources onto a non-directory: refused by the gate.
	_, err = call(t, e, l, `{"src":["a","b"],"dst":"nowhere"}`)
	wantKind(t, err, gate.KindInvalidArgs)
}
