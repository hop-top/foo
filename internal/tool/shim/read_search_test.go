package shim

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"hop.top/foo/internal/tool/gate"
)

func TestGrep_FileArgv(t *testing.T) {
	dir := readTree(t)
	e, auth := readEngine(dir)
	l := builtinSpec(t, "grep")
	c := filepath.Join(dir, "sub", "c.go")

	res := mustCall(t, e, l, `{"pattern":"TODO","path":["sub/c.go"]}`)
	wantArgv(t, res, l, []string{"-H", "-n", "-e", "TODO"}, c)
	if stdout(res) != c+":2:// TODO nested\n" || res.ExitCode != 0 || !res.OK {
		t.Errorf("stdout %q exit %d", stdout(res), res.ExitCode)
	}
	if auth.last.Paths[0].Recursion != gate.NoRecursion {
		t.Errorf("non-recursive call walked: %+v", auth.last.Paths[0])
	}

	res = mustCall(t, e, l, `{"pattern":"ALPHA|beta","path":["link"],"ignore_case":true,"extended":true,"word":true,"invert":true,"line_number":false,"max_count":2,"context":1}`)
	wantArgv(t, res, l, []string{"-H", "-i", "-E", "-w", "-v", "-m", "2", "-C", "1", "-e", "ALPHA|beta"}, filepath.Join(dir, "a.txt"))
	res = mustCall(t, e, l, `{"pattern":"a.","path":["a.txt"],"fixed":true,"count":true}`)
	wantArgv(t, res, l, []string{"-H", "-F", "-c", "-n", "-e", "a."}, filepath.Join(dir, "a.txt"))
	if stdout(res) != filepath.Join(dir, "a.txt")+":0\n" || res.ExitCode != 1 || !res.OK {
		t.Errorf("fixed count: %q exit %d ok %v; want 0 matches, exit 1 ok", stdout(res), res.ExitCode, res.OK)
	}
	res = mustCall(t, e, l, `{"pattern":"TODO","path":["a.txt","sub/c.go"],"files_only":true}`)
	if stdout(res) != c+"\n" {
		t.Errorf("files_only = %q", stdout(res))
	}

	// A directory without recursive=true is refused, not searched.
	wantRefused(t, e, l, `{"pattern":"x","path":["sub"]}`, gate.KindInvalidArgs, "path")
	wantRefused(t, e, l, `{"pattern":"TODO","path":["a.txt",".env"]}`, gate.KindDenied, "path")
}

// A pattern is always the argument of -e: a regex, never a flag or a
// file to read patterns from.
func TestGrep_PatternIsNeverAFlag(t *testing.T) {
	dir := readTree(t)
	e, _ := readEngine(dir)
	l := builtinSpec(t, "grep")
	writeFile(t, filepath.Join(dir, "pats"), "alpha\n")
	for _, pat := range []string{"-f" + filepath.Join(dir, "pats"), "-r", "--", "-e", "--file=/etc/passwd"} {
		res := mustCall(t, e, l, `{"pattern":"`+pat+`","path":["a.txt"]}`)
		wantArgv(t, res, l, []string{"-H", "-n", "-e", pat}, filepath.Join(dir, "a.txt"))
		if res.ExitCode != 1 || stdout(res) != "" {
			t.Errorf("pattern %q: exit %d stdout %q; want a literal no-match", pat, res.ExitCode, stdout(res))
		}
	}
	// A path shaped like a flag is a file after --.
	writeFile(t, filepath.Join(dir, "-r"), "alpha\n")
	res := mustCall(t, e, l, `{"pattern":"alpha","path":["-r"]}`)
	wantArgv(t, res, l, []string{"-H", "-n", "-e", "alpha"}, filepath.Join(dir, "-r"))
	if stdout(res) != filepath.Join(dir, "-r")+":1:alpha\n" {
		t.Errorf("stdout %q", stdout(res))
	}
}

// Recursion is foo's walk: grep gets the allowed regular files, never a
// directory and never -r; .env and secrets/ are withheld and counted.
func TestGrep_RecursiveFilterBefore(t *testing.T) {
	dir := readTree(t)
	e, auth := readEngine(dir)
	l := builtinSpec(t, "grep")

	res := mustCall(t, e, l, `{"pattern":"TODO","path":["."],"recursive":true}`)

	if pa := auth.last.Paths[0]; pa.Recursion != gate.FilterBefore {
		t.Fatalf("request recursion %v; want filter-before", pa.Recursion)
	}
	dd := slices.Index(res.Argv, "--")
	files := res.Argv[dd+1:]
	slices.Sort(files)
	want := []string{filepath.Join(dir, "a.txt"), filepath.Join(dir, "sub", "c.go")}
	if !slices.Equal(files, want) {
		t.Errorf("grep got files %q; want only %q (no dirs, links, .env, secrets)", files, want)
	}
	if !slices.Equal(res.Argv[:dd], []string{l.Bin, "-H", "-n", "-e", "TODO"}) {
		t.Errorf("argv options %q", res.Argv[:dd])
	}
	out := stdout(res)
	if !strings.Contains(out, filepath.Join(dir, "sub", "c.go")+":2:// TODO nested") {
		t.Errorf("stdout lacks the nested match: %q", out)
	}
	if strings.Contains(out, "hunter2") || strings.Contains(out, "private key") {
		t.Fatalf("denied content reached the output: %q", out)
	}
	if res.Filtered != 2 || res.Files != 2 || !res.OK || res.ExitCode != 0 {
		t.Errorf("filtered %d files %d exit %d; want 2, 2, 0", res.Filtered, res.Files, res.ExitCode)
	}

	// Everything under the root withheld: nothing runs, no match, ok.
	writeFile(t, filepath.Join(dir, "cfg", ".env"), "TODO\n")
	res = mustCall(t, e, l, `{"pattern":"TODO","path":["cfg"],"recursive":true}`)
	if res.Files != 0 || res.Filtered != 1 || res.ExitCode != 1 || !res.OK || stdout(res) != "" {
		t.Errorf("all withheld: files %d filtered %d exit %d stdout %q", res.Files, res.Filtered, res.ExitCode, stdout(res))
	}
	wantRefused(t, e, l, `{"pattern":"TODO","path":["secrets"],"recursive":true}`, gate.KindDenied, "path")
}

func TestGrep_Invalid(t *testing.T) {
	wantInvalid(t, "grep", map[string]string{
		`{"path":["a"]}`:                                            "pattern",
		`{"pattern":"a\nb","path":["a"]}`:                           "pattern",
		`{"pattern":"` + strings.Repeat("x", 1025) + `"}`:           "pattern",
		`{"pattern":"a","path":["a"],"file":"/etc/passwd"}`:         "file",
		`{"pattern":"a","path":["a"],"include":"*.go"}`:             "include",
		`{"pattern":"a","path":["a"],"dereference":true}`:           "dereference",
		`{"pattern":"a","path":["a"],"fixed":true,"extended":true}`: "extended",
		`{"pattern":"a","path":["a"],"context":21}`:                 "context",
		`{"pattern":"a","path":["a"],"max_count":0}`:                "max_count",
	})
}

func TestFind_FilterAfter(t *testing.T) {
	dir := readTree(t)
	e, auth := readEngine(dir)
	l := builtinSpec(t, "find")
	// Links: one to a denied file, one to a directory outside the tree.
	outside := tempDir(t)
	writeFile(t, filepath.Join(outside, "leak.txt"), "x")
	if err := os.Symlink(filepath.Join(dir, "secrets", "key.txt"), filepath.Join(dir, "sk")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "out")); err != nil {
		t.Fatal(err)
	}

	res := mustCall(t, e, l, `{}`)
	if auth.last.Paths[0].Recursion != gate.FilterAfter {
		t.Fatalf("recursion %v; want filter-after", auth.last.Paths[0].Recursion)
	}
	if !slices.Equal(res.Argv, []string{l.Bin, "-P", "--", dir, "-print0"}) {
		t.Fatalf("argv %q", res.Argv)
	}
	got := lines(stdout(res))
	for _, p := range []string{"", "a.txt", "sub", "sub/c.go", "link", "out"} {
		if !got[filepath.Join(dir, p)] {
			t.Errorf("output lacks %s: %q", p, stdout(res))
		}
	}
	for ln := range got {
		if strings.Contains(ln, ".env") || strings.Contains(ln, "secrets") || strings.HasSuffix(ln, "/sk") || strings.Contains(ln, "leak") {
			t.Errorf("output shows %s", ln)
		}
	}
	if res.Filtered != 4 { // .env, secrets, secrets/key.txt, sk
		t.Errorf("filtered %d; want 4", res.Filtered)
	}

	res = mustCall(t, e, l, `{"path":["."],"maxdepth":3,"mindepth":1,"type":"f","name":"*.go","iname":"C*","mtime_days":-1}`)
	if !slices.Equal(res.Argv, []string{l.Bin, "-P", "--", dir, "-maxdepth", "3", "-mindepth", "1", "-type", "f", "-name", "*.go", "-iname", "C*", "-mtime", "-1", "-print0"}) {
		t.Fatalf("argv %q", res.Argv)
	}
	if stdout(res) != filepath.Join(dir, "sub", "c.go")+"\n" {
		t.Errorf("stdout %q", stdout(res))
	}
	res = mustCall(t, e, l, `{"path":["sub","link"],"type":"l"}`)
	if !slices.Equal(res.Argv, []string{l.Bin, "-P", "--", filepath.Join(dir, "sub"), filepath.Join(dir, "a.txt"), "-type", "l", "-print0"}) {
		t.Fatalf("argv %q", res.Argv)
	}

	wantRefused(t, e, l, `{"path":["secrets"]}`, gate.KindDenied, "path")
}

// Predicate values are always option arguments: -delete, -exec and
// friends given as a name are glob patterns, never actions.
func TestFind_PredicateInjection(t *testing.T) {
	dir := readTree(t)
	e, _ := readEngine(dir)
	l := builtinSpec(t, "find")
	for _, name := range []string{"-delete", "-exec", "(", "!", "-fprint", "--"} {
		res := mustCall(t, e, l, `{"name":"`+name+`"}`)
		if i := slices.Index(res.Argv, "-name"); i < 0 || res.Argv[i+1] != name || res.Argv[len(res.Argv)-1] != "-print0" {
			t.Fatalf("argv %q: %q not the -name argument", res.Argv, name)
		}
		if stdout(res) != "" {
			t.Errorf("name %q matched %q", name, stdout(res))
		}
		if _, err := os.Stat(filepath.Join(dir, "a.txt")); err != nil {
			t.Fatalf("name %q: a.txt gone: %v", name, err)
		}
	}
	// A root named like a predicate is a path after --.
	writeFile(t, filepath.Join(dir, "-delete", "x"), "x")
	res := mustCall(t, e, l, `{"path":["-delete"]}`)
	if !slices.Equal(res.Argv, []string{l.Bin, "-P", "--", filepath.Join(dir, "-delete"), "-print0"}) {
		t.Fatalf("argv %q", res.Argv)
	}
	if _, err := os.Stat(filepath.Join(dir, "-delete", "x")); err != nil {
		t.Fatalf("-delete root was deleted: %v", err)
	}

	wantInvalid(t, "find", map[string]string{
		`{"exec":"rm"}`:       "exec",
		`{"follow":true}`:     "follow",
		`{"newer":"a"}`:       "newer",
		`{"maxdepth":65}`:     "maxdepth",
		`{"mindepth":-1}`:     "mindepth",
		`{"type":"s"}`:        "type",
		`{"mtime_days":3651}`: "mtime_days",
		`{"name":"a\nb"}`:     "name",
		`{"name":"` + strings.Repeat("x", 256) + `"}`: "name",
	})
}
