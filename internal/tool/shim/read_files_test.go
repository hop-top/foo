package shim

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hop.top/foo/internal/tool/gate"
)

func lines(s string) map[string]bool {
	out := map[string]bool{}
	for _, ln := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		out[ln] = true
	}
	return out
}

// Invalid arguments never reach the authorizer, let alone the command.
func wantInvalid(t *testing.T, name string, cases map[string]string) {
	t.Helper()
	l := builtinSpec(t, name)
	for args, param := range cases {
		auth := &readAuth{cwd: "/"}
		wantRefused(t, &Engine{Authorizer: auth, Cwd: "/"}, l, args, gate.KindInvalidArgs, param)
		if auth.calls != 0 {
			t.Errorf("%s %s: authorizer called for invalid args", name, args)
		}
	}
}

func TestLs(t *testing.T) {
	dir := readTree(t)
	e, _ := readEngine(dir)
	l := builtinSpec(t, "ls")

	res := mustCall(t, e, l, `{}`)
	wantArgv(t, res, l, []string{"-1"}, dir)
	got := lines(stdout(res))
	for _, want := range []string{"a.txt", "sub", "link", "secrets"} {
		if !got[want] {
			t.Errorf("ls . lacks %s: %q", want, stdout(res))
		}
	}
	if got[".env"] {
		t.Errorf("ls without all=true shows dotfiles: %q", stdout(res))
	}

	res = mustCall(t, e, l, `{"path":["sub","a.txt"],"long":true,"all":true,"sort":"size","reverse":true}`)
	wantArgv(t, res, l, []string{"-l", "-A", "-S", "-r"}, filepath.Join(dir, "sub"), filepath.Join(dir, "a.txt"))
	if !strings.Contains(stdout(res), "c.go") || !res.OK {
		t.Errorf("long listing: %q ok %v", stdout(res), res.OK)
	}

	res = mustCall(t, e, l, `{"path":["link"],"directory":true,"sort":"time"}`)
	wantArgv(t, res, l, []string{"-1", "-t", "-d"}, filepath.Join(dir, "a.txt"))

	// A name that looks like -R is a path after --: never recursion.
	writeFile(t, filepath.Join(dir, "-R", "only"), "x")
	res = mustCall(t, e, l, `{"path":["-R"]}`)
	wantArgv(t, res, l, []string{"-1"}, filepath.Join(dir, "-R"))
	if strings.TrimSpace(stdout(res)) != "only" {
		t.Errorf("ls -R-named dir printed %q; want its one entry", stdout(res))
	}
	wantRefused(t, e, l, `{"path":["--recursive"]}`, gate.KindNotFound, "path")

	wantRefused(t, e, l, `{"path":["sub","secrets"]}`, gate.KindDenied, "path")
	wantRefused(t, e, l, `{"path":[".env"]}`, gate.KindDenied, "path")
	wantInvalid(t, "ls", map[string]string{
		`{"recursive":true}`:   "recursive",
		`{"dereference":true}`: "dereference",
		`{"sort":"ctime"}`:     "sort",
		`{"long":"yes"}`:       "long",
		`{"path":[]}`:          "path",
	})
}

func TestCat(t *testing.T) {
	dir := readTree(t)
	e, _ := readEngine(dir)
	l := builtinSpec(t, "cat")

	res := mustCall(t, e, l, `{"path":["a.txt","sub/c.go"],"number":true}`)
	wantArgv(t, res, l, []string{"-n"}, filepath.Join(dir, "a.txt"), filepath.Join(dir, "sub", "c.go"))
	if out := stdout(res); !strings.Contains(out, "1\talpha") || !strings.Contains(out, "TODO nested") {
		t.Errorf("stdout = %q", out)
	}

	res = mustCall(t, e, l, `{"path":["link"]}`)
	wantArgv(t, res, l, nil, filepath.Join(dir, "a.txt"))

	// Flag-shaped names are files; "-" is never stdin.
	writeFile(t, filepath.Join(dir, "-n"), "flag-named\n")
	res = mustCall(t, e, l, `{"path":["-n"]}`)
	wantArgv(t, res, l, nil, filepath.Join(dir, "-n"))
	if stdout(res) != "flag-named\n" {
		t.Errorf("cat -n-named file printed %q", stdout(res))
	}
	wantRefused(t, e, l, `{"path":["-"]}`, gate.KindNotFound, "path")

	// One denied path refuses the whole call: a.txt is not printed.
	wantRefused(t, e, l, `{"path":["a.txt",".env"]}`, gate.KindDenied, "path")
	wantRefused(t, e, l, `{"path":["secrets/key.txt"]}`, gate.KindDenied, "path")
	wantRefused(t, e, l, `{"path":["sub"]}`, gate.KindInvalidArgs, "path")
	wantInvalid(t, "cat", map[string]string{
		`{"path":["a"],"show_all":true}`:                   "show_all",
		`{"path":[` + strings.Repeat(`"a",`, 16) + `"a"]}`: "path",
		`{}`: "path",
	})
}

func TestHead(t *testing.T) {
	dir := readTree(t)
	e, _ := readEngine(dir)
	l := builtinSpec(t, "head")
	a := filepath.Join(dir, "a.txt")

	res := mustCall(t, e, l, `{"path":["a.txt"],"lines":2}`)
	wantArgv(t, res, l, []string{"-n", "2"}, a)
	if stdout(res) != "alpha\nbeta\n" {
		t.Errorf("head -n 2 = %q", stdout(res))
	}
	res = mustCall(t, e, l, `{"path":["a.txt"],"bytes":3}`)
	wantArgv(t, res, l, []string{"-c", "3"}, a)
	if stdout(res) != "alp" {
		t.Errorf("head -c 3 = %q", stdout(res))
	}
	res = mustCall(t, e, l, `{"path":["a.txt","sub/c.go"],"lines":1}`)
	if out := stdout(res); !strings.Contains(out, "==> "+a+" <==") || !strings.Contains(out, "package c") {
		t.Errorf("multi-file head = %q", out)
	}

	// A path shaped like an option is a file after --.
	writeFile(t, filepath.Join(dir, "-c"), "one\n")
	res = mustCall(t, e, l, `{"path":["-c"]}`)
	wantArgv(t, res, l, nil, filepath.Join(dir, "-c"))
	if stdout(res) != "one\n" {
		t.Errorf("head of -c-named file = %q", stdout(res))
	}

	wantRefused(t, e, l, `{"path":[".env"],"lines":1}`, gate.KindDenied, "path")
	wantInvalid(t, "head", map[string]string{
		`{"path":["a"],"lines":1,"bytes":1}`: "bytes",
		`{"path":["a"],"lines":0}`:           "lines",
		`{"path":["a"],"lines":-5}`:          "lines",
		`{"path":["a"],"lines":100001}`:      "lines",
		`{"path":["a"],"lines":"5"}`:         "lines",
		`{"path":["a"],"bytes":1048577}`:     "bytes",
		`{"path":["a"],"quiet":true}`:        "quiet",
	})
}

func TestTail(t *testing.T) {
	dir := readTree(t)
	e, _ := readEngine(dir)
	l := builtinSpec(t, "tail")
	a := filepath.Join(dir, "a.txt")

	res := mustCall(t, e, l, `{"path":["a.txt"],"lines":1}`)
	wantArgv(t, res, l, []string{"-n", "1"}, a)
	if stdout(res) != "delta\n" {
		t.Errorf("tail -n 1 = %q", stdout(res))
	}
	res = mustCall(t, e, l, `{"path":["link"],"bytes":6}`)
	wantArgv(t, res, l, []string{"-c", "6"}, a)
	if stdout(res) != "delta\n" {
		t.Errorf("tail -c 6 = %q", stdout(res))
	}
	res = mustCall(t, e, l, `{"path":["a.txt"]}`)
	wantArgv(t, res, l, nil, a)
	if !strings.HasPrefix(stdout(res), "alpha\n") {
		t.Errorf("tail default = %q", stdout(res))
	}

	writeFile(t, filepath.Join(dir, "-f"), "not following\n")
	res = mustCall(t, e, l, `{"path":["-f"]}`)
	wantArgv(t, res, l, nil, filepath.Join(dir, "-f"))
	if stdout(res) != "not following\n" {
		t.Errorf("tail of -f-named file = %q", stdout(res))
	}

	wantRefused(t, e, l, `{"path":["a.txt","secrets/key.txt"]}`, gate.KindDenied, "path")
	wantInvalid(t, "tail", map[string]string{
		`{"path":["a"],"follow":true}`:       "follow",
		`{"path":["a"],"lines":1,"bytes":1}`: "bytes",
		`{"path":["a"],"lines":0}`:           "lines",
		`{"path":["a"],"bytes":0}`:           "bytes",
		`{"path":["a"],"lines":100001}`:      "lines",
	})
}

func TestStat(t *testing.T) {
	dir := readTree(t)
	e, _ := readEngine(dir)
	l := builtinSpec(t, "stat")
	a, sub := filepath.Join(dir, "a.txt"), filepath.Join(dir, "sub")

	res := mustCall(t, e, l, `{"path":["link","sub"]}`)
	var opts []string
	switch fl := flavorOf(l); fl {
	case FlavorBSD:
		opts = l.Spec.Command.Variants[FlavorBSD].Prefix
		if len(opts) == 0 || opts[0] != "-f" {
			t.Fatalf("bsd prefix %q; want -f FORMAT", opts)
		}
	default:
		opts = l.Spec.Command.Variants[fl].Prefix
		if len(opts) == 0 || opts[0] != "-c" {
			t.Fatalf("%s prefix %q; want -c FORMAT", fl, opts)
		}
	}
	wantArgv(t, res, l, opts, a, sub)
	out := lines(stdout(res))
	if len(out) != 2 {
		t.Fatalf("stat printed %q; want one line per path", stdout(res))
	}
	for ln := range out {
		switch {
		case strings.HasSuffix(ln, " path="+a):
			if !strings.Contains(ln, "size=23 ") || !strings.Contains(strings.ToLower(ln), "type=regular file ") ||
				!strings.Contains(ln, "perms=-rw") {
				t.Errorf("file line %q", ln)
			}
		case strings.HasSuffix(ln, " path="+sub):
			if !strings.Contains(strings.ToLower(ln), "type=directory ") || !strings.Contains(ln, "perms=d") {
				t.Errorf("dir line %q", ln)
			}
		default:
			t.Errorf("unexpected line %q", ln)
		}
	}

	if err := os.Mkdir(filepath.Join(dir, "-L"), 0o755); err != nil {
		t.Fatal(err)
	}
	res = mustCall(t, e, l, `{"path":["-L"]}`)
	wantArgv(t, res, l, opts, filepath.Join(dir, "-L"))

	wantRefused(t, e, l, `{"path":["secrets"]}`, gate.KindDenied, "path")
	wantInvalid(t, "stat", map[string]string{
		`{"path":["a"],"format":"%n"}`: "format",
		`{"path":["a"],"follow":true}`: "follow",
		`{"path":["a\u0000"]}`:         "path",
	})
}
