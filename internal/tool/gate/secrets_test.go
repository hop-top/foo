package gate_test

import (
	"path/filepath"
	"strings"
	"testing"

	"hop.top/foo/internal/tool/gate"
	"hop.top/kit/go/core/scope"
)

var allOps = []scope.Op{scope.Read, scope.Write, scope.Exec}

// secretsTree: p/ granted every op, with secret-named directories and
// files inside, their lookalikes, and ~/.ssh.
func secretsTree(t *testing.T) *fsEnv {
	t.Helper()
	e := newFS(t)
	e.file(t, "p/ok.txt", "ok")
	e.file(t, "p/mysecrets.txt", "not a secret by name")
	e.file(t, "p/notsecrets/n.txt", "n")
	e.file(t, "p/secrets/key.txt", "KEY")
	e.file(t, "p/secrets/nested/deep.txt", "DEEP")
	e.file(t, "p/secrets.yaml", "s")
	e.file(t, "p/credentials/c", "C")
	e.file(t, "p/.env/x", "venv-like dir named .env")
	e.file(t, "p/.env.d/x", "X=1")
	e.file(t, "p/tls.pem/cert", "c")
	e.file(t, "q/secrets", "a FILE named secrets")
	e.file(t, "home/.ssh/id_x", "k")
	e.scopeYAML(t, `allow:
  - "{root}/p/**"
  - "{root}/q/**"
  - "~/**"
`)
	return e
}

// concrete turns a secret pattern into a path it names: "**/" leading
// patterns land under p/, "~/" under the test home, "**" and "*" become
// literal names. ok is false for patterns this host cannot resolve
// (Windows env macros).
func concrete(e *fsEnv, p scope.Pattern) (string, bool) {
	s := string(p)
	switch {
	case strings.Contains(s, "%"):
		return "", false
	case strings.HasPrefix(s, "~/"):
		s = e.home + s[1:]
	case strings.HasPrefix(s, "**/"):
		s = e.p("p") + s[2:]
	}
	s = strings.ReplaceAll(s, "**", "d")
	s = strings.ReplaceAll(s, "*", "x")
	return filepath.Clean(s), true
}

// Every pattern of kit's secret list denies the entry it names and
// everything under it, for every op: a directory named like a secret
// hides its contents, not only its own entry.
func TestSecretDenyList_EveryPatternCoversDescendants(t *testing.T) {
	e := secretsTree(t)
	sc, err := gate.LoadScope("foo")
	if err != nil {
		t.Fatal(err)
	}
	for _, pat := range scope.SecretPaths() {
		entry, ok := concrete(e, pat)
		if !ok {
			continue
		}
		t.Run(string(pat), func(t *testing.T) {
			for _, p := range []string{entry, entry + "/f", entry + "/sub/deep/f"} {
				for _, op := range allOps {
					if dec, err := sc.Policy.Check(scope.Path(p), op); dec != scope.Denied {
						t.Errorf("%v %s = %v (%v); want Denied", op, p, dec, err)
					}
				}
			}
		})
	}
}

// The gate, end to end: secret entries, their contents and nested
// contents are refused; lookalike siblings are not.
func TestSecretDenyList_Gate(t *testing.T) {
	e := secretsTree(t)
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))
	for _, tc := range []struct {
		rel    string
		denied bool
	}{
		{"p/secrets", true},                 // the dir itself
		{"p/secrets/key.txt", true},         // a file inside
		{"p/secrets/nested/deep.txt", true}, // nested
		{"p/secrets/nested", true},          // nested dir
		{"p/secrets.yaml", true},            // secrets* by prefix
		{"q/secrets", true},                 // a file named secrets
		{"p/credentials/c", true},           // credentials* dir
		{"p/.env/x", true},                  // .env as a dir
		{"p/.env.d/x", true},                // .env.* as a dir
		{"p/tls.pem/cert", true},            // *.pem as a dir
		{"home/.ssh", true},                 // ~/.ssh itself
		{"home/.ssh/id_x", true},            // ~/.ssh/**
		{"p/ok.txt", false},                 // sibling
		{"p/mysecrets.txt", false},          // secrets* is a name prefix, not a substring
		{"p/notsecrets/n.txt", false},       // likewise for a dir
		{"p/notsecrets", false},             // and the dir itself
	} {
		t.Run(tc.rel, func(t *testing.T) {
			req := readCall("cat", e.p(tc.rel))
			if tc.denied {
				mustRefuse(t, g, req, gate.KindDenied)
			} else {
				mustAllow(t, g, req)
			}
		})
	}
}

// Creating a file inside a secrets dir is a write under it.
func TestSecretDenyList_WriteIntoSecretDir(t *testing.T) {
	e := secretsTree(t)
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))
	req := gate.Request{Tool: "touch", SideEffect: "write", Paths: []gate.PathArg{
		{Param: "path", Values: []string{e.p("p/secrets/new.txt")}, Op: scope.Write},
	}}
	mustRefuse(t, g, req, gate.KindDenied)
}

// Walks: grep (filter before) withholds every file under a secrets
// dir, find (filter after) hides each entry under it, and rm -r / cp -R
// (all or nothing) refuse a tree holding one.
func TestSecretDenyList_Walks(t *testing.T) {
	e := secretsTree(t)
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))

	grant := mustAllow(t, g, grepCall(e.p("p")))
	for _, f := range grant.Files["path"] {
		if strings.Contains(f, "/secrets/") || strings.Contains(f, "/credentials/") || strings.Contains(f, "/.env") {
			t.Errorf("grep walk kept %s", f)
		}
	}

	find := gate.Request{Tool: "find", SideEffect: "read", Paths: []gate.PathArg{
		{Param: "path", Values: []string{e.p("p")}, Op: scope.Read, Recursion: gate.FilterAfter},
	}}
	grant = mustAllow(t, g, find)
	for rel, want := range map[string]bool{
		"p/ok.txt":                  true,
		"p/mysecrets.txt":           true,
		"p/notsecrets/n.txt":        true,
		"p/secrets":                 false,
		"p/secrets/key.txt":         false,
		"p/secrets/nested":          false,
		"p/secrets/nested/deep.txt": false,
		"p/credentials/c":           false,
		"p/.env/x":                  false,
		"p/.env.d/x":                false,
		"p/tls.pem/cert":            false,
	} {
		if got := grant.Allow(e.p(rel)); got != want {
			t.Errorf("find Allow(%s) = %v; want %v", rel, got, want)
		}
	}

	e.file(t, "p/proj/a.txt", "a")
	e.file(t, "p/proj/secrets/key.txt", "KEY")
	ge := mustRefuse(t, g, rmRecursive(e.p("p/proj")), gate.KindDenied)
	if !strings.Contains(ge.Message, e.p("p/proj/secrets")) {
		t.Errorf("rm -r message %q should name the secrets entry", ge.Message)
	}

	e.mkdir(t, "p/w")
	cp := gate.Request{Tool: "cp", SideEffect: "write", Paths: []gate.PathArg{
		{Param: "src", Values: []string{e.p("p/secrets")}, Op: scope.Read, MustExist: true, Recursion: gate.AllOrNothing},
		{Param: "dst", Values: []string{e.p("p/w/copy")}, Op: scope.Write, Target: gate.Dirent, Recursion: gate.AllOrNothing},
	}}
	mustRefuse(t, g, cp, gate.KindDenied)
}

// Each pattern is followed by its descendant form; patterns already
// recursive are kept as they are, and a kit list that already carries
// descendant forms (or repeats a pattern) comes out the same.
func TestWithDescendants(t *testing.T) {
	for _, tc := range []struct {
		name     string
		in, want []scope.Pattern
	}{
		{"adds descendants",
			[]scope.Pattern{"**/secrets*", "~/.netrc", "~/.ssh/**"},
			[]scope.Pattern{"**/secrets*", "**/secrets*/**", "~/.netrc", "~/.netrc/**", "~/.ssh/**"}},
		{"kit already returns descendant forms",
			[]scope.Pattern{"**/secrets*", "**/secrets*/**", "~/.ssh/**"},
			[]scope.Pattern{"**/secrets*", "**/secrets*/**", "~/.ssh/**"}},
		{"kit returns only descendant forms",
			[]scope.Pattern{"**/secrets*/**"},
			[]scope.Pattern{"**/secrets*/**"}},
		{"repeats dropped",
			[]scope.Pattern{"**/.env", "**/.env", "**/.env/**"},
			[]scope.Pattern{"**/.env", "**/.env/**"}},
		{"empty", nil, []scope.Pattern{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := gate.WithDescendants(tc.in)
			if strings.Join(toStrings(got), " ") != strings.Join(toStrings(tc.want), " ") {
				t.Fatalf("got %q; want %q", got, tc.want)
			}
		})
	}
}

// LoadScope's secret deny rule is SecretPatterns, which is what
// `foo scope show` lists: every kit pattern plus a descendant form.
func TestLoadScope_SecretRuleIsSecretPatterns(t *testing.T) {
	secretsTree(t)
	sc, err := gate.LoadScope("foo")
	if err != nil {
		t.Fatal(err)
	}
	want := gate.SecretPatterns()
	var found bool
	for _, r := range sc.Policy.Rules() {
		if !r.Allow && strings.Join(toStrings(r.Patterns), " ") == strings.Join(toStrings(want), " ") {
			found = r.Ops == scope.Read|scope.Write|scope.Exec
		}
	}
	if !found {
		t.Fatalf("no read|write|exec deny rule with SecretPatterns in %+v", sc.Policy.Rules())
	}
	has := map[scope.Pattern]bool{}
	for _, p := range want {
		has[p] = true
	}
	for _, p := range scope.SecretPaths() {
		d := p
		if !strings.HasSuffix(string(p), "/**") {
			d = p + "/**"
		}
		if !has[p] || !has[d] {
			t.Errorf("SecretPatterns lacks %s or %s", p, d)
		}
	}
}

func toStrings(ps []scope.Pattern) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = string(p)
	}
	return out
}
