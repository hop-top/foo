package shim

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

const okSpec = `
spec: 1
name: okx
description: Minimal valid fixture.
command: {bin: [/bin/cat]}
side_effect: read
params:
  - {name: path, type: path, description: p, op: [read]}
  - {name: pattern, type: string, description: s, max_len: 10, argv: ["-e", "{}"]}
argv: ["{pattern}", "--", "{path}"]
`

func TestLint_RejectsUnsafeSpecs(t *testing.T) {
	if _, err := Parse([]byte(okSpec)); err != nil {
		t.Fatalf("baseline fixture rejected: %v", err)
	}
	for _, tc := range []struct{ name, from, to, want string }{
		{"version", "spec: 1", "spec: 2", "version 2"},
		{"unknown key", "side_effect: read", "side_effect: read\nshell: true", "shell"},
		{"unknown param key", "op: [read]}", "op: [read], raw: true}", "raw"},
		{"bad name", "name: okx", "name: Ok-X", "name"},
		{"relative bin", "[/bin/cat]", "[cat]", "clean absolute"},
		{"launcher bin", "[/bin/cat]", "[/bin/sh]", "launcher"},
		{"env launcher", "[/bin/cat]", "[/usr/bin/env]", "launcher"},
		{"argv type", "type: string", "type: argv", "type \"argv\""},
		{"path before --", `argv: ["{pattern}", "--", "{path}"]`, `argv: ["{path}", "{pattern}", "--"]`, "must follow a literal --"},
		{"no --", `argv: ["{pattern}", "--", "{path}"]`, `argv: ["{pattern}", "{path}"]`, "must follow a literal --"},
		{"string after --", `argv: ["{pattern}", "--", "{path}"]`, `argv: ["--", "{path}", "{pattern}"]`, "after --"},
		{"bare string", `["-e", "{}"]`, `["{}"]`, "literal option"},
		{"string not after option", `["-e", "{}"]`, `["x", "{}"]`, "literal option"},
		{"fragment names another param", `["-e", "{}"]`, `["-e", "{path}"]`, "braces only as {}"},
		{"unknown placeholder", `"--", "{path}"]`, `"--", "{path}", "{nope}"]`, "names no param"},
		{"missing description", "description: p,", "", "description is required"},
		{"string without max_len", "max_len: 10, ", "", "max_len"},
		{"too many items", "op: [read]}", "op: [read], repeated: true, max_items: 300}", "max_items"},
		{"path argv", "op: [read]}", `op: [read], argv: ["-f"]}`, "path params take no argv"},
		{"timeout too long", "side_effect: read", "side_effect: read\ntimeout: 5m", "timeout"},
		{"side effect", "side_effect: read", "side_effect: sometimes", "side_effect"},
		{"recursion unset", "op: [read]}", "op: [read], recursive: true}", "recursion"},
		{"unused path", `"--", "{path}"]`, `"--"]`, "never placed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := strings.Replace(okSpec, tc.from, tc.to, 1)
			if src == okSpec {
				t.Fatalf("fixture edit %q did not apply", tc.from)
			}
			_, err := Parse([]byte(src))
			var le *LintError
			if !errors.As(err, &le) {
				t.Fatalf("accepted; want lint error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q; want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestBuiltinSpecs_AllLoad(t *testing.T) {
	cat := Load(LoadOptions{})
	if len(cat.Invalid) > 0 {
		t.Fatalf("invalid builtin specs: %v", cat.Invalid[0])
	}
	var names []string
	for _, l := range cat.Specs {
		names = append(names, l.Spec.Name)
		if l.Source.Kind != SourceBuiltin || l.SourceLabel() != "builtin" || l.Overrides {
			t.Errorf("%s source = %+v", l.Spec.Name, l.Source)
		}
	}
	// Only the commands the track ships (§9); the read set all present.
	shipped := []string{"cat", "cp", "find", "grep", "head", "ls", "mkdir", "mv", "rm", "sed", "stat", "tail", "wc"}
	for _, n := range names {
		if !slices.Contains(shipped, n) {
			t.Errorf("unexpected builtin spec %q", n)
		}
	}
	for _, n := range []string{"cat", "find", "grep", "head", "ls", "stat", "tail", "wc"} {
		if !slices.Contains(names, n) {
			t.Errorf("builtin specs %v lack %s", names, n)
		}
	}
}

func userSpec(name, bin, extra string) string {
	return `spec: 1
name: ` + name + `
description: User spec ` + name + `.
command: {bin: [` + bin + `]}
side_effect: read
params:
  - {name: path, type: path, description: p, op: [read]}
` + extra + `argv: ["--", "{path}"]
`
}

func TestLoad_Precedence(t *testing.T) {
	root := tempDir(t)
	sys, user := filepath.Join(root, "sys"), filepath.Join(root, "user")
	builtin := fstest.MapFS{
		"specs/wc.yaml":  {Data: []byte(userSpec("wc", "/bin/cat", ""))},
		"specs/cat.yaml": {Data: []byte(userSpec("cat", "/bin/cat", ""))},
		"specs/ls.yaml":  {Data: []byte(userSpec("ls", "/bin/cat", ""))},
	}
	writeFile(t, filepath.Join(sys, "wc.yaml"), userSpec("wc", "/bin/cat", ""))
	writeFile(t, filepath.Join(user, "wc.yaml"), userSpec("wc", "/bin/cat", ""))
	writeFile(t, filepath.Join(sys, "cat.yaml"), userSpec("cat", "/bin/cat", ""))
	// A broken user override must not fall back to the builtin.
	writeFile(t, filepath.Join(user, "ls.yaml"), "spec: 1\nname: ls\n")
	// A new command: allowed, with a warning for its string param.
	jq := strings.Replace(userSpec("jqx", "/bin/cat", `  - {name: filter, type: string, description: f, max_len: 99, argv: ["--arg={}"]}
`), `argv: ["--"`, `argv: ["{filter}", "--"`, 1)
	writeFile(t, filepath.Join(user, "jqx.yaml"), jq)
	writeFile(t, filepath.Join(user, "nobin.yaml"), userSpec("nobin", "/nonexistent/bin", ""))
	writeFile(t, filepath.Join(user, "misnamed.yaml"), userSpec("other", "/bin/cat", ""))
	writeFile(t, filepath.Join(user, "notes.txt"), "ignored")

	cat := Load(LoadOptions{Builtin: builtin, SystemDir: sys, UserDir: user})

	wc, _ := cat.Lookup("wc")
	if wc == nil || wc.Source.Kind != SourceUser || !wc.Overrides ||
		wc.SourceLabel() != "user:"+filepath.Join(user, "wc.yaml")+" (overrides builtin)" {
		t.Errorf("wc = %+v", wc)
	}
	c, _ := cat.Lookup("cat")
	if c == nil || c.Source.Kind != SourceSystem || !c.Overrides {
		t.Errorf("cat = %+v", c)
	}
	if l, inv := cat.Lookup("ls"); l != nil || inv == nil || inv.Source.Kind != SourceUser {
		t.Errorf("broken ls override: loaded %v invalid %v; want invalid, no builtin fallback", l, inv)
	}
	j, _ := cat.Lookup("jqx")
	if j == nil || j.Overrides || len(j.Warnings) != 1 || !strings.Contains(j.Warnings[0], "not path-gated") {
		t.Errorf("jqx = %+v", j)
	}
	if _, inv := cat.Lookup("nobin"); inv == nil || !strings.Contains(inv.Error(), "no executable") {
		t.Errorf("nobin invalid = %v", inv)
	}
	if _, inv := cat.Lookup("misnamed"); inv == nil || !strings.Contains(inv.Error(), "does not match file name") {
		t.Errorf("misnamed invalid = %v", inv)
	}
	if l, inv := cat.Lookup("notes"); l != nil || inv != nil {
		t.Error("non-yaml file loaded")
	}
}

func TestLoad_MissingDirsAreEmpty(t *testing.T) {
	cat := Load(LoadOptions{UserDir: "/nonexistent/u", SystemDir: "/nonexistent/s"})
	if l, _ := cat.Lookup("wc"); l == nil {
		t.Fatal("builtin wc missing when spec dirs are absent")
	}
	if _, err := os.Stat("/nonexistent/u"); err == nil {
		t.Fatal("Load created a spec dir")
	}
}
