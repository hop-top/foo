//go:build unix

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// shimCase is one tool call and what must come back.
type shimCase struct {
	name  string
	cwd   string
	tool  string
	args  map[string]any
	check func(t *testing.T, o outcome)
}

func runShimCases(t *testing.T, e *shimEnv, cases []shimCase) {
	t.Helper()
	for _, mode := range shimModes {
		for _, tc := range cases {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				tc.check(t, e.call(mode, tc.cwd, tc.tool, jsonArgs(t, tc.args)))
			})
		}
	}
}

// TestToolShims_Acceptance is the path-scope acceptance table: one
// read-only grant, strict mode, and each call through -T (in-process,
// stub model) and through a foo-tool-<name> link.
func TestToolShims_Acceptance(t *testing.T) {
	ensureBinary(t)
	e := newShimEnv(t)
	to := e.path("path", "to")
	mkfile(t, filepath.Join(to, "a"), "alpha needle\n")
	mkfile(t, filepath.Join(to, "x"), "keep me\n")
	mkfile(t, filepath.Join(to, "-R"), "a file named -R\n")
	mkfile(t, filepath.Join(to, ".env"), "needle secret\n")
	mkfile(t, filepath.Join(to, "secrets", "key.txt"), "needle KEY\n")
	mkfile(t, filepath.Join(to, "secrets", "nested", "deep.txt"), "needle DEEP\n")
	mkfile(t, filepath.Join(to, "private", "p.txt"), "hidden\n")
	mkfile(t, filepath.Join(to, ".ssh", "id"), "needle SSHPRIV\n")
	mkfile(t, filepath.Join(to, ".aws", "credentials"), "needle AWSCRED\n")
	mkfile(t, filepath.Join(to, ".aws", "config"), "needle AWSCFG\n")
	mkfile(t, filepath.Join(to, ".sshx", "f"), "needle lookalike\n")
	mkfile(t, filepath.Join(to, "big"), strings.Repeat("0123456789abcdef", 10<<16)) // 10 MiB
	mkfile(t, filepath.Join(to, "img.png"), "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	mkfile(t, e.path("-R"), "outside the grant\n")
	mkfile(t, e.path("outside", "deep", "f"), "outside\n")
	for _, d := range []string{"to", "tmp"} {
		require.NoError(t, os.MkdirAll(e.path(d), 0o755))
	}
	require.NoError(t, os.Symlink("/etc", filepath.Join(to, "etc-link")))
	require.NoError(t, os.Symlink(e.path("outside", "deep"), filepath.Join(to, "deep-link")))
	etc, err := filepath.EvalSymlinks("/etc")
	require.NoError(t, err)

	e.scope("mode: strict\nallow:\n  - path: \"" + to + "/**\"\n    ops: [read]\n" +
		"deny:\n  - \"" + to + "/private/**\"\n")
	e.policy(allowLocalChanges)

	runShimCases(t, e, []shimCase{
		// The plan's acceptance table.
		{"ls /path/to allowed", e.root, "ls", map[string]any{"path": []string{to}}, func(t *testing.T, o outcome) {
			res := wantRan(t, o)
			require.Contains(t, o.stdout(), "\na\n")
			require.Equal(t, []string{"--", to}, res.Argv[len(res.Argv)-2:])
		}},
		{"ls / denied", e.root, "ls", map[string]any{"path": []string{"/"}}, func(t *testing.T, o outcome) {
			wantRefused(t, o, "denied", "path", "/")
		}},
		{"ls /path/to/../.. denied", e.root, "ls", map[string]any{"path": []string{to + "/../.."}}, func(t *testing.T, o outcome) {
			wantRefused(t, o, "denied", "path", e.root)
		}},
		{"ls link to /etc denied", e.root, "ls", map[string]any{"path": []string{to + "/etc-link"}}, func(t *testing.T, o outcome) {
			wantRefused(t, o, "denied", "path", etc)
		}},
		{"ls -R is a path, not a flag", to, "ls", map[string]any{"path": []string{"-R"}}, func(t *testing.T, o outcome) {
			res := wantRan(t, o)
			require.Equal(t, []string{"--", to + "/-R"}, res.Argv[len(res.Argv)-2:])
			require.Contains(t, o.stdout(), "-R")
			require.NotContains(t, o.stdout(), "p.txt", "ls recursed")
		}},
		{"ls -R outside the grant denied", e.root, "ls", map[string]any{"path": []string{"-R"}}, func(t *testing.T, o outcome) {
			wantRefused(t, o, "denied", "path", e.path("-R"))
		}},
		{"rm under read-only denied", e.root, "rm", map[string]any{"path": []string{to + "/x"}}, func(t *testing.T, o outcome) {
			wantRefused(t, o, "denied", "path", "write")
			require.FileExists(t, filepath.Join(to, "x"))
		}},
		{"cp dst outside denied", e.root, "cp", map[string]any{"src": []string{to + "/a"}, "dst": e.path("tmp", "b")}, func(t *testing.T, o outcome) {
			wantRefused(t, o, "denied", "dst", "write")
			require.NoFileExists(t, e.path("tmp", "b"))
		}},

		// Additions: resolution.
		{"link/.. is resolved physically", e.root, "ls", map[string]any{"path": []string{to + "/deep-link/.."}}, func(t *testing.T, o outcome) {
			wantRefused(t, o, "denied", "path", e.path("outside"))
		}},
		{"relative path from a cwd inside", e.path("path"), "ls", map[string]any{"path": []string{"to"}}, func(t *testing.T, o outcome) {
			wantRan(t, o)
		}},
		{"relative path from a cwd outside", e.root, "ls", map[string]any{"path": []string{"to"}}, func(t *testing.T, o outcome) {
			wantRefused(t, o, "denied", "path", e.path("to"))
		}},

		// Additions: one denied path refuses the call; secrets.
		{"cat allowed plus /etc/passwd denied whole", e.root, "cat", map[string]any{"path": []string{to + "/a", "/etc/passwd"}}, func(t *testing.T, o outcome) {
			wantRefused(t, o, "denied", "path", "passwd")
			require.NotContains(t, o.Raw, "alpha")
		}},
		{"cat .env denied by the secret list", e.root, "cat", map[string]any{"path": []string{to + "/.env"}}, func(t *testing.T, o outcome) {
			wantRefused(t, o, "denied", "path", "deny rule")
		}},
		{"cat a file under secrets/ denied", e.root, "cat", map[string]any{"path": []string{to + "/secrets/key.txt"}}, func(t *testing.T, o outcome) {
			wantRefused(t, o, "denied", "path", "deny rule")
			require.NotContains(t, o.Raw, "KEY")
		}},
		{"cat a nested file under secrets/ denied", e.root, "cat", map[string]any{"path": []string{to + "/secrets/nested/deep.txt"}}, func(t *testing.T, o outcome) {
			wantRefused(t, o, "denied", "path", "deny rule")
			require.NotContains(t, o.Raw, "DEEP")
		}},
		{"mv source on a read-only grant denied", e.root, "mv", map[string]any{"src": []string{to + "/x"}, "dst": to + "/y"}, func(t *testing.T, o outcome) {
			wantRefused(t, o, "denied", "", "write")
			require.FileExists(t, filepath.Join(to, "x"))
		}},

		// Additions: recursion filters.
		{"grep recursive skips .env", e.root, "grep", map[string]any{"pattern": "needle", "path": []string{to}, "recursive": true}, func(t *testing.T, o outcome) {
			res := wantRan(t, o)
			require.Contains(t, o.stdout(), "alpha needle")
			require.NotContains(t, o.stdout(), "secret")
			require.GreaterOrEqual(t, res.Filtered, 1)
		}},
		{"find leaves out a denied subtree", e.root, "find", map[string]any{"path": []string{to}}, func(t *testing.T, o outcome) {
			res := wantRan(t, o)
			require.Contains(t, o.stdout(), to+"/a")
			require.NotContains(t, o.stdout(), to+"/private")
			require.GreaterOrEqual(t, res.Filtered, 1)
		}},

		{"find omits secrets/ and its contents", e.root, "find", map[string]any{"path": []string{to}}, func(t *testing.T, o outcome) {
			res := wantRan(t, o)
			require.Contains(t, o.stdout(), to+"/a")
			require.NotContains(t, o.stdout(), to+"/secrets")
			require.GreaterOrEqual(t, res.Filtered, 1)
		}},
		{"grep recursive skips secrets/", e.root, "grep", map[string]any{"pattern": "needle", "path": []string{to}, "recursive": true}, func(t *testing.T, o outcome) {
			wantRan(t, o)
			require.Contains(t, o.stdout(), "alpha needle")
			require.NotContains(t, o.stdout(), "KEY")
			require.NotContains(t, o.stdout(), "DEEP")
		}},

		// Additions: credential dirs inside the grant, not only under home.
		{"cat a project .ssh/id denied", e.root, "cat", map[string]any{"path": []string{to + "/.ssh/id"}}, func(t *testing.T, o outcome) {
			wantRefused(t, o, "denied", "path", "deny rule")
			require.NotContains(t, o.Raw, "SSHPRIV")
		}},
		{"cat a project .aws/credentials denied", e.root, "cat", map[string]any{"path": []string{to + "/.aws/credentials"}}, func(t *testing.T, o outcome) {
			wantRefused(t, o, "denied", "path", "deny rule")
			require.NotContains(t, o.Raw, "AWSCRED")
		}},
		{"cat a project .aws/config denied", e.root, "cat", map[string]any{"path": []string{to + "/.aws/config"}}, func(t *testing.T, o outcome) {
			wantRefused(t, o, "denied", "path", "deny rule")
			require.NotContains(t, o.Raw, "AWSCFG")
		}},
		{"find omits project .ssh/ and .aws/", e.root, "find", map[string]any{"path": []string{to}}, func(t *testing.T, o outcome) {
			wantRan(t, o)
			require.Contains(t, o.stdout(), to+"/.sshx/f")
			require.NotContains(t, o.stdout(), to+"/.ssh\n")
			require.NotContains(t, o.stdout(), to+"/.ssh/")
			require.NotContains(t, o.stdout(), to+"/.aws")
		}},
		{"grep recursive skips project .ssh/ and .aws/", e.root, "grep", map[string]any{"pattern": "needle", "path": []string{to}, "recursive": true}, func(t *testing.T, o outcome) {
			wantRan(t, o)
			require.Contains(t, o.stdout(), "needle lookalike")
			require.NotContains(t, o.stdout(), "SSHPRIV")
			require.NotContains(t, o.stdout(), "AWS")
		}},

		// Additions: argument smuggling.
		{"grep pattern -f/etc/passwd is a regex", e.root, "grep", map[string]any{"pattern": "-f/etc/passwd", "path": []string{to + "/a"}}, func(t *testing.T, o outcome) {
			res := wantRan(t, o)
			require.Equal(t, 1, res.ExitCode, "a literal pattern matches nothing")
			require.Contains(t, strings.Join(res.Argv, " "), "-e -f/etc/passwd --")
		}},
		{"find name=-delete is a glob", e.root, "find", map[string]any{"path": []string{to}, "name": "-delete"}, func(t *testing.T, o outcome) {
			wantRan(t, o)
			require.FileExists(t, filepath.Join(to, "a"))
		}},

		// Additions: output envelope.
		{"cat 10 MiB is truncated", e.root, "cat", map[string]any{"path": []string{to + "/big"}}, func(t *testing.T, o outcome) {
			res := wantRan(t, o)
			require.True(t, res.StdoutTruncated)
			require.EqualValues(t, 10<<20, res.StdoutBytes)
		}},
		{"cat binary is not sent", e.root, "cat", map[string]any{"path": []string{to + "/img.png"}}, func(t *testing.T, o outcome) {
			res := wantRan(t, o)
			require.True(t, res.StdoutBinary)
			require.Nil(t, res.Stdout)
		}},
	})
}

// TestToolShims_Writes covers the write tools on a writable grant: sed
// replacement semantics, dry_run on a read-only grant, the root guard
// and whole-tree refusals. Each case works in its own directory.
func TestToolShims_Writes(t *testing.T) {
	ensureBinary(t)
	e := newShimEnv(t)
	w, ro := e.path("w"), e.path("ro")
	require.NoError(t, os.MkdirAll(w, 0o755))
	require.NoError(t, os.MkdirAll(ro, 0o755))
	e.scope("mode: strict\nallow:\n  - \"" + w + "/**\"\n  - \"~/**\"\n  - path: \"" + ro + "/**\"\n    ops: [read]\n")
	e.policy(allowLocalChanges)

	for _, mode := range shimModes {
		dir := func(t *testing.T, base string) string {
			d := filepath.Join(base, mode, strings.ReplaceAll(filepath.Base(t.Name()), " ", "_"))
			require.NoError(t, os.MkdirAll(d, 0o755))
			return d
		}
		call := func(t *testing.T, tool string, args map[string]any) outcome {
			return e.call(mode, e.root, tool, jsonArgs(t, args))
		}
		t.Run(mode, func(t *testing.T) {
			t.Run("sed replace is literal", func(t *testing.T) {
				f := filepath.Join(dir(t, w), "s.txt")
				mkfile(t, f, "x y\n")
				wantRan(t, call(t, "sed", map[string]any{"path": []string{f}, "find": "x", "replace": `a&b\1`}))
				require.Equal(t, "a&b\\1 y\n", readFile(t, f))
			})
			t.Run("sed backrefs", func(t *testing.T) {
				f := filepath.Join(dir(t, w), "s.txt")
				mkfile(t, f, "x y\n")
				wantRan(t, call(t, "sed", map[string]any{"path": []string{f}, "find": "x", "replace": "a&b", "backrefs": true}))
				require.Equal(t, "axb y\n", readFile(t, f))
			})
			t.Run("sed newline in replace refused", func(t *testing.T) {
				f := filepath.Join(dir(t, w), "s.txt")
				mkfile(t, f, "x\n")
				wantRefused(t, call(t, "sed", map[string]any{"path": []string{f}, "find": "x", "replace": "y\nw /tmp/leak"}), "invalid_args", "replace", "newline")
				require.Equal(t, "x\n", readFile(t, f))
			})
			t.Run("sed dry_run on a read-only grant", func(t *testing.T) {
				f := filepath.Join(dir(t, ro), "r.txt")
				mkfile(t, f, "hello\n")
				o := call(t, "sed", map[string]any{"path": []string{f}, "find": "hello", "replace": "bye", "dry_run": true})
				wantRan(t, o)
				require.Equal(t, "bye\n", o.stdout())
				require.Equal(t, "hello\n", readFile(t, f))
				wantRefused(t, call(t, "sed", map[string]any{"path": []string{f}, "find": "hello", "replace": "bye"}), "denied", "path", "write")
				require.Equal(t, "hello\n", readFile(t, f))
			})
			t.Run("rm of home refused", func(t *testing.T) {
				for _, p := range []string{"~", e.home + "/"} {
					wantRefused(t, call(t, "rm", map[string]any{"path": []string{p}}), "invalid_args", "path", "home directory")
				}
				require.DirExists(t, e.home)
			})
			t.Run("rm recursive with a denied entry removes nothing", func(t *testing.T) {
				tree := filepath.Join(dir(t, w), "tree")
				mkfile(t, filepath.Join(tree, "a"), "a\n")
				mkfile(t, filepath.Join(tree, ".env"), "k=v\n")
				wantRefused(t, call(t, "rm", map[string]any{"path": []string{tree}, "recursive": true}), "denied", "path", ".env")
				require.FileExists(t, filepath.Join(tree, "a"))
			})
			t.Run("rm of a link keeps its target", func(t *testing.T) {
				d := dir(t, w)
				mkfile(t, filepath.Join(d, "target"), "t\n")
				require.NoError(t, os.Symlink(filepath.Join(d, "target"), filepath.Join(d, "lnk")))
				wantRan(t, call(t, "rm", map[string]any{"path": []string{filepath.Join(d, "lnk")}}))
				require.NoFileExists(t, filepath.Join(d, "lnk"))
				require.FileExists(t, filepath.Join(d, "target"))
			})
			t.Run("cp into a dir never clobbers", func(t *testing.T) {
				d := dir(t, w)
				mkfile(t, filepath.Join(d, "a"), "new\n")
				mkfile(t, filepath.Join(d, "dst", "a"), "old\n")
				wantRefused(t, call(t, "cp", map[string]any{"src": []string{filepath.Join(d, "a")}, "dst": filepath.Join(d, "dst")}), "invalid_args", "dst", "exists")
				require.Equal(t, "old\n", readFile(t, filepath.Join(d, "dst", "a")))
			})
			t.Run("mv from a read-only grant denied on src", func(t *testing.T) {
				src := filepath.Join(dir(t, ro), "m")
				mkfile(t, src, "m\n")
				wantRefused(t, call(t, "mv", map[string]any{"src": []string{src}, "dst": filepath.Join(dir(t, w), "m")}), "denied", "src", "write")
				require.FileExists(t, src)
			})
		})
	}
}

// TestToolShims_OutsideGrantAlike: outside the grant, a missing path,
// an existing file or directory and a link (into the grant included)
// all get the same refusal, so the model cannot probe what exists
// there or where a link points. Only the path it sent differs. strict
// denies; prompt with nobody to ask denies too.
func TestToolShims_OutsideGrantAlike(t *testing.T) {
	ensureBinary(t)
	e := newShimEnv(t)
	w := e.path("w")
	mkfile(t, filepath.Join(w, "f"), "f\n")
	out := e.path("outside")
	mkfile(t, filepath.Join(out, "f"), "secret\n")
	mkfile(t, filepath.Join(out, "d", "x"), "x\n")
	mkfile(t, e.path("elsewhere", "g"), "g\n")
	require.NoError(t, os.Symlink(e.path("elsewhere", "g"), filepath.Join(out, "l")))
	require.NoError(t, os.Symlink(e.path("elsewhere", "nothing"), filepath.Join(out, "dl")))
	// Aliases of places the root guard protects answer like the rest.
	require.NoError(t, os.Symlink(e.home, filepath.Join(out, "hl")))
	require.NoError(t, os.Symlink("/", filepath.Join(out, "rl")))
	require.NoError(t, os.Symlink("/usr", filepath.Join(out, "ul")))
	require.NoError(t, os.Symlink(topLinkTarget(), filepath.Join(out, "tl")))
	// Links into the grant: where they point is read outside it.
	require.NoError(t, os.MkdirAll(filepath.Join(w, "sub"), 0o755))
	require.NoError(t, os.Symlink(w, filepath.Join(out, "lw")))
	require.NoError(t, os.Symlink(filepath.Join(w, "f"), filepath.Join(out, "lwf")))
	require.NoError(t, os.Symlink(filepath.Join(w, "sub"), filepath.Join(out, "lws")))
	probes := []string{"f", "d", "missing", "l", "dl", "hl", "rl", "ul", "tl", "missing-dir/x", "f/x",
		"lw", "lwf", "lws", "lw/f", "lw/x", "d/../../w/f", "missing-dir/../../w/f"}

	for _, scopeMode := range []string{"strict", "prompt"} {
		e.scope("mode: " + scopeMode + "\nallow:\n  - \"" + w + "/**\"\n")
		for _, mode := range shimModes {
			for _, tool := range []string{"cat", "ls", "rm", "cp"} {
				t.Run(scopeMode+"/"+mode+"/"+tool, func(t *testing.T) {
					answers := map[string][]string{}
					for _, rel := range probes {
						p := out + "/" + rel // not filepath.Join: keep ".."
						args := map[string]any{"path": []string{p}}
						if tool == "cp" {
							args = map[string]any{"src": []string{filepath.Join(w, "f")}, "dst": p}
						}
						o := e.call(mode, e.root, tool, jsonArgs(t, args))
						require.NotNilf(t, o.Err, "%s %s ran: %s", tool, rel, o.Raw)
						require.Equalf(t, "denied", o.Err.Kind, "%s %s: %s", tool, rel, o.Raw)
						// Shapes: the lexical path climbs back into the grant or not.
						shape := "outside"
						if strings.Contains(rel, "..") {
							shape = "climb"
						}
						raw := strings.ReplaceAll(o.Raw, p, "<P>")
						raw = strings.ReplaceAll(raw, filepath.Clean(p), "<P>")
						raw = strings.ReplaceAll(raw, filepath.Dir(filepath.Clean(p)), "<P/..>")
						answers[shape] = append(answers[shape], rel+"\t"+raw)
					}
					for shape, list := range answers {
						want := strings.SplitN(list[0], "\t", 2)[1]
						for _, a := range list[1:] {
							require.Equalf(t, want, strings.SplitN(a, "\t", 2)[1], "%s answers differ:\n%s", shape, strings.Join(list, "\n"))
						}
					}
				})
			}
		}
	}
}

// topLinkTarget is what a top-level link points to when the target is
// not itself directly under / (macOS /tmp -> /private/tmp); "/" when
// no such link exists.
func topLinkTarget() string {
	for _, top := range []string{"/tmp", "/var", "/etc", "/bin", "/lib", "/sbin"} {
		fi, err := os.Lstat(top)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			continue
		}
		if target, err := filepath.EvalSymlinks(top); err == nil && filepath.Dir(target) != "/" {
			return target
		}
	}
	return "/"
}

// TestToolShims_RootGuardPhases: a value naming $HOME as written is
// refused before the scope is consulted; a granted link to $HOME is
// refused after it, before rm runs; an ungranted one is denied by the
// scope. $HOME (a throwaway dir here) and the links stay.
func TestToolShims_RootGuardPhases(t *testing.T) {
	ensureBinary(t)
	e := newShimEnv(t)
	w := e.path("w")
	mkfile(t, filepath.Join(e.home, "keep"), "keep\n")
	require.NoError(t, os.MkdirAll(w, 0o755))
	require.NoError(t, os.Symlink(e.home, filepath.Join(w, "hl")))
	require.NoError(t, os.Symlink(e.home, e.path("hl")))
	e.scope("mode: strict\nallow:\n  - \"" + w + "/**\"\n  - \"" + e.home + "/**\"\n")
	e.policy(allowLocalChanges)
	rm := func(p string) map[string]any { return map[string]any{"path": []string{p}} }
	runShimCases(t, e, []shimCase{
		{"rm ~ refused as written", w, "rm", rm("~"), func(t *testing.T, o outcome) {
			wantRefused(t, o, "invalid_args", "path", "is the home directory")
		}},
		{"rm $HOME refused as written", w, "rm", rm(e.home), func(t *testing.T, o outcome) {
			wantRefused(t, o, "invalid_args", "path", "is the home directory")
		}},
		{"rm granted link to $HOME refused", w, "rm", rm("hl"), func(t *testing.T, o outcome) {
			wantRefused(t, o, "invalid_args", "path", "is the home directory")
		}},
		{"rm ungranted link to $HOME denied", w, "rm", rm(e.path("hl")), func(t *testing.T, o outcome) {
			wantRefused(t, o, "denied", "path", e.root)
			require.NotContains(t, o.Raw, "home directory")
		}},
	})
	require.Equal(t, "keep\n", readFile(t, filepath.Join(e.home, "keep")))
	for _, l := range []string{filepath.Join(w, "hl"), e.path("hl")} {
		fi, err := os.Lstat(l)
		require.NoError(t, err)
		require.NotZero(t, fi.Mode()&os.ModeSymlink, l)
	}
}

// TestToolShims_NoScope: without scope.yaml every call is denied, and
// the message names the file to create and `foo scope`.
func TestToolShims_NoScope(t *testing.T) {
	ensureBinary(t)
	e := newShimEnv(t)
	mkfile(t, e.path("p", "a"), "a\n")
	want := filepath.Join(e.config, "foo", "scope.yaml")
	runShimCases(t, e, []shimCase{
		{"ls denied", e.root, "ls", map[string]any{"path": []string{e.path("p")}}, func(t *testing.T, o outcome) {
			wantRefused(t, o, "denied", "", want)
			require.Contains(t, o.Err.Message, "foo scope show")
		}},
	})
}

// TestToolShims_NeedsApprovalWithoutTerminal: what would prompt is
// refused when nobody can be asked (link mode always; -T with no tty).
func TestToolShims_NeedsApprovalWithoutTerminal(t *testing.T) {
	ensureBinary(t)
	e := newShimEnv(t)
	w := e.path("w")
	mkfile(t, filepath.Join(w, "f"), "f\n")
	mkfile(t, e.path("elsewhere", "g"), "g\n")
	e.scope("mode: prompt\nallow:\n  - \"" + w + "/**\"\n")
	runShimCases(t, e, []shimCase{
		{"destructive policy prompt declined", e.root, "rm", map[string]any{"path": []string{filepath.Join(w, "f")}}, func(t *testing.T, o outcome) {
			wantRefused(t, o, "declined", "", "cannot be asked")
			require.FileExists(t, filepath.Join(w, "f"))
		}},
		{"prompt-mode path denied", e.root, "cat", map[string]any{"path": []string{e.path("elsewhere", "g")}}, func(t *testing.T, o outcome) {
			wantRefused(t, o, "denied", "path", "approval cannot be asked")
		}},
	})
}

// TestToolShims_UnknownTool: an unknown -T name, or a link named for no
// spec, exits 3 before any model call.
func TestToolShims_UnknownTool(t *testing.T) {
	ensureBinary(t)
	e := newShimEnv(t)
	t.Run(modeInProcess, func(t *testing.T) {
		stub := newStubModel(t, "ls", "{}")
		code, _, stderr := e.run(e.root, []string{"OPENAI_API_KEY=sk-test", "LLM_BASE_URL=" + stub.URL + "/v1"},
			"--offline", "-m", "gpt-4o", "-T", "nope", "go")
		require.Equal(t, 3, code, stderr)
		for _, want := range []string{`"nope"`, "ls", "rm", "sed", "foo tool list"} {
			require.Contains(t, stderr, want)
		}
		require.Empty(t, stub.toolMessage())
	})
	t.Run(modeLink, func(t *testing.T) {
		link := filepath.Join(e.bin, "foo-tool-nope")
		require.NoError(t, os.Symlink(e.foo, link))
		code, _, stderr := e.exec(e.root, nil, `{"arguments":{}}`, link)
		require.Equal(t, 3, code, stderr)
		require.Contains(t, stderr, `no tool spec named "nope"`)
	})
}

// TestToolShims_ToolList: the built-ins are listed with their side
// effect and path ops, and a third-party foo-tool-ls earlier on PATH
// is shadowed, never run.
func TestToolShims_ToolList(t *testing.T) {
	ensureBinary(t)
	e := newShimEnv(t)
	rival := e.path("rival")
	mkfile(t, filepath.Join(rival, "foo-tool-ls"), "#!/bin/sh\necho '{\"name\":\"ls\",\"version\":\"1\",\"description\":\"rival\"}'\n")
	require.NoError(t, os.Chmod(filepath.Join(rival, "foo-tool-ls"), 0o755))
	e.setPath(rival, e.bin)

	code, stdout, stderr := e.run(e.root, nil, "--offline", "tool", "list", "--format=json")
	require.Equal(t, 0, code, stderr)
	var rows []struct {
		Name, Source, Status, Paths string
		SideEffect                  string `json:"side_effect"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &rows))
	got := map[string]string{}
	for _, r := range rows {
		got[r.Name+" "+r.Status] = r.Source + " " + r.SideEffect + " " + r.Paths
	}
	require.Equal(t, "builtin read path:r", got["ls active"])
	require.Equal(t, "builtin write src:r dst:w", got["cp active"])
	require.Equal(t, "builtin destructive path:w", got["rm active"])
	require.Equal(t, filepath.Join(rival, "foo-tool-ls")+" unknown ungated", got["ls shadowed"])
}
