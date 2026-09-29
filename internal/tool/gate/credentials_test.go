package gate_test

import (
	"slices"
	"testing"

	"hop.top/foo/internal/tool/gate"
	"hop.top/kit/go/core/scope"
)

// credDirs are the directories kit denies under home only; foo denies
// them at any depth.
var credDirs = []string{".ssh", ".aws", ".azure", ".gnupg", ".kube", ".pki", ".config/gcloud"}

// credTree: p/ granted every op, holding each credential dir at the
// top and nested deep, credential files, and lookalike names.
func credTree(t *testing.T) *fsEnv {
	t.Helper()
	e := newFS(t)
	for _, d := range credDirs {
		e.file(t, "p/"+d+"/f", "SECRET")
		e.file(t, "p/"+d+"/nested/deep/f", "SECRET")
		e.file(t, "p/sub/deeper/"+d+"/f", "SECRET")
	}
	for _, f := range []string{
		"p/.netrc", "p/sub/.pgpass", "p/.pypirc", "p/.my.cnf",
		"p/ok.txt", "p/.sshx/f", "p/my.ssh/f", "p/ssh/f", "p/.ssh.d/f",
		"p/.aws-sam/f", "p/.kubex/f", "p/gcloud/f", "p/config/gcloud/f",
		"p/.config/other/f", "p/.netrc.example", "p/.npmrc",
	} {
		e.file(t, f, "x")
	}
	e.scopeYAML(t, "allow:\n  - \"{root}/p/**\"\n")
	return e
}

// A credential dir inside a granted tree is denied like the one under
// home: the dir itself, a file inside, nested contents, and the same dir
// deeper down, for every op.
func TestCredentialDirsAnywhere_Denied(t *testing.T) {
	e := credTree(t)
	sc, err := gate.LoadScope("foo")
	if err != nil {
		t.Fatal(err)
	}
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))
	for _, d := range credDirs {
		for _, rel := range []string{
			"p/" + d,                     // the dir itself
			"p/" + d + "/f",              // a file inside
			"p/" + d + "/nested/deep/f",  // nested
			"p/" + d + "/nested",         // a nested dir
			"p/sub/deeper/" + d,          // the dir deeper down
			"p/sub/deeper/" + d + "/f",   // and a file in it
			"p/" + d + "/not-yet-a-file", // a write target inside
		} {
			t.Run(rel, func(t *testing.T) {
				for _, op := range allOps {
					if dec, err := sc.Policy.Check(scope.Path(e.p(rel)), op); dec != scope.Denied {
						t.Errorf("%v %s = %v (%v); want Denied", op, rel, dec, err)
					}
				}
				if rel != "p/"+d+"/not-yet-a-file" {
					mustRefuse(t, g, readCall("cat", e.p(rel)), gate.KindDenied)
				}
			})
		}
	}
}

// Home-anchored credential files that hold secrets wherever they sit
// are denied at any depth too.
func TestCredentialFilesAnywhere_Denied(t *testing.T) {
	e := credTree(t)
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))
	for _, rel := range []string{"p/.netrc", "p/sub/.pgpass", "p/.pypirc", "p/.my.cnf"} {
		t.Run(rel, func(t *testing.T) {
			mustRefuse(t, g, readCall("cat", e.p(rel)), gate.KindDenied)
		})
	}
}

// Siblings and names that only look like a credential dir or file stay
// readable: the anywhere forms match whole path components, and a bare
// "gcloud" is not ".config/gcloud". A project .npmrc is left readable
// on purpose (registry config, rarely a literal token).
func TestCredentialsAnywhere_LookalikesAllowed(t *testing.T) {
	e := credTree(t)
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))
	for _, rel := range []string{
		"p/ok.txt", "p/.sshx/f", "p/my.ssh/f", "p/ssh/f", "p/.ssh.d/f",
		"p/.aws-sam/f", "p/.kubex/f", "p/gcloud/f", "p/config/gcloud/f",
		"p/.config/other/f", "p/.config", "p/.netrc.example", "p/.npmrc",
	} {
		t.Run(rel, func(t *testing.T) {
			mustAllow(t, g, readCall("cat", e.p(rel)))
		})
	}
}

// find over the tree leaves out every credential dir and its contents,
// and keeps the lookalikes.
func TestCredentialDirsAnywhere_FindOmits(t *testing.T) {
	e := credTree(t)
	g, _ := newGate(t, e.root, gate.WithConfirmer(yes()))
	find := gate.Request{Tool: "find", SideEffect: "read", Paths: []gate.PathArg{
		{Param: "path", Values: []string{e.p("p")}, Op: scope.Read, Recursion: gate.FilterAfter},
	}}
	grant := mustAllow(t, g, find)
	for _, d := range credDirs {
		for _, rel := range []string{"p/" + d, "p/" + d + "/f", "p/sub/deeper/" + d + "/f"} {
			if grant.Allow(e.p(rel)) {
				t.Errorf("find kept %s", rel)
			}
		}
	}
	for _, rel := range []string{"p/.sshx/f", "p/gcloud/f", "p/.config/other/f"} {
		if !grant.Allow(e.p(rel)) {
			t.Errorf("find dropped %s", rel)
		}
	}
}

// The anywhere form of a home dir keeps its whole home-relative path;
// patterns with globs, non-dir patterns, non-home patterns and bare
// generic names yield nothing.
func TestAnywhereDirs(t *testing.T) {
	got := gate.AnywhereDirs([]scope.Pattern{
		"~/.ssh/**",
		"~/.config/gcloud/**",
		"~/Library/Keychains/**",
		"~/Documents/**",                // one generic segment: too broad
		"~/.netrc",                      // a file
		"~/.mozilla/firefox/**/key*.db", // globs
		"~/.x[y]/**",                    // globs
		"**/secrets*",
		"/etc/ssh/**", // not under home
		"~/**",
		"~/.ssh/**", // repeat
	})
	want := []scope.Pattern{"**/.ssh", "**/.config/gcloud", "**/Library/Keychains"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q; want %q", got, want)
	}
}

// SecretPatterns carries the anywhere forms, entry and descendant, for
// every credential dir and file foo denies at any depth, and none for
// the names it leaves alone.
func TestSecretPatterns_AnywhereForms(t *testing.T) {
	got := gate.SecretPatterns()
	for _, want := range []scope.Pattern{
		"**/.ssh", "**/.ssh/**", "**/.aws", "**/.aws/**", "**/.azure/**",
		"**/.gnupg/**", "**/.kube/**", "**/.pki/**",
		"**/.config/gcloud", "**/.config/gcloud/**",
		"**/.netrc", "**/.pgpass", "**/.pypirc", "**/.my.cnf",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("SecretPatterns lacks %s", want)
		}
	}
	for _, not := range []scope.Pattern{"**/gcloud", "**/gcloud/**", "**/.config/**", "**/.npmrc"} {
		if slices.Contains(got, not) {
			t.Errorf("SecretPatterns has over-broad %s", not)
		}
	}
}

// A kit that already lists the anywhere forms yields no duplicates and
// the same set.
func TestSecretPatternsFrom_KitWithAnywhereForms(t *testing.T) {
	base := []scope.Pattern{"**/.env", "~/.ssh/**", "~/.netrc"}
	plain := gate.SecretPatternsFrom(base)
	withForms := gate.SecretPatternsFrom(append(slices.Clone(base), "**/.ssh/**", "**/.ssh", "**/.netrc", "**/.netrc/**"))
	seen := map[scope.Pattern]bool{}
	for _, p := range withForms {
		if seen[p] {
			t.Errorf("duplicate %s in %q", p, withForms)
		}
		seen[p] = true
	}
	a, b := slices.Clone(plain), slices.Clone(withForms)
	slices.Sort(a)
	slices.Sort(b)
	if !slices.Equal(a, b) {
		t.Fatalf("with kit anywhere forms %q; without %q", b, a)
	}
}
