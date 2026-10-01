package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fooBin is the binary under test: built once per package run into a
// scratch dir, so a stale build cannot mask a change in command
// vocabulary and the source tree is never written to.
var (
	fooBin    string
	buildOnce sync.Once
	buildErr  error
)

func TestMain(m *testing.M) { os.Exit(runTests(m)) }

// noUpdateNotifier opts a foo run out of the passive update check,
// which would otherwise dial api.github.com on every invocation.
const noUpdateNotifier = "FOO_NO_UPDATE_NOTIFIER"

func runTests(m *testing.M) int {
	// Every foo the suite starts inherits this: no run asks GitHub for
	// the latest release. A harness that builds its own environment
	// carries it too (see noUpdateNotifier).
	if err := os.Setenv(noUpdateNotifier, "1"); err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		return 1
	}
	dir, err := os.MkdirTemp("", "foo-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: scratch dir:", err)
		return 1
	}
	defer os.RemoveAll(dir)
	// Canonical path: shims compare their own location against it.
	if dir, err = filepath.EvalSymlinks(dir); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: scratch dir:", err)
		return 1
	}
	fooBin = filepath.Join(dir, "foo")
	return m.Run()
}

// aimCatalogFixture is a small hand-written models.dev catalog: openai
// and anthropic, plus fixturecloud, a provider only this file knows, so
// a test can tell the fixture answered rather than a live catalog.
const aimCatalogFixture = "testdata/aim/models-dev.json"

// seedAimCatalog installs aimCatalogFixture as a fresh aim catalog
// cache under cacheHome (an XDG_CACHE_HOME), unless one is there. A
// fresh cache is served as is: foo never fetches models.dev, so the
// suite needs no network and every run sees the same catalog.
func seedAimCatalog(t *testing.T, cacheHome string) {
	t.Helper()
	dir := filepath.Join(cacheHome, "hop", "aim")
	payload := filepath.Join(dir, "models-dev.json")
	if _, err := os.Stat(payload); err == nil {
		return
	}
	data, err := os.ReadFile(aimCatalogFixture)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(payload, data, 0o600))
	meta := fmt.Sprintf(`{"last_fetch":%q,"ttl_seconds":%d}`, time.Now().UTC().Format(time.RFC3339Nano), int64(30*24*time.Hour/time.Second))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o600))
}

// runFoo executes the foo binary against an isolated $HOME, returning
// stdout, stderr, and the exec error.
func runFoo(t *testing.T, tmpHome string, args ...string) (string, string, error) {
	t.Helper()
	seedAimCatalog(t, filepath.Join(tmpHome, ".cache"))
	cmd := exec.Command(fooBin, args...)

	cmd.Env = append(os.Environ(),
		"HOME="+tmpHome,
		"XDG_CONFIG_HOME="+filepath.Join(tmpHome, ".config"),
		"XDG_STATE_HOME="+filepath.Join(tmpHome, ".local", "state"),
		"XDG_DATA_HOME="+filepath.Join(tmpHome, ".local", "share"),
		// Without it, an inherited XDG_CACHE_HOME puts the model
		// catalog and endpoint caches in the developer's real cache,
		// shared with every concurrent run.
		"XDG_CACHE_HOME="+filepath.Join(tmpHome, ".cache"),
		// The binary links the keyring secrets backend; an inherited
		// FOO_SECRETS_BACKEND=keyring would reach the real keychain.
		"FOO_SECRETS_BACKEND=env",
		// Force the no-color path so assertions are not foiled by
		// terminfo escape sequences.
		"NO_COLOR=1",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// ensureBinary builds foo into fooBin on first use. Tests share one
// binary. VCS stamping is off: no test reads it (the version comes from
// ldflags), and it fails wherever the go tool misreads the checkout,
// e.g. in a linked worktree under a dir holding an unrelated .git.
//
// The xrr tag compiles in the cassette seam (xrr_seam.go), so a test
// can replay model calls recorded from a real provider. It stays inert
// unless the test sets XRR_MODE; release builds never carry it.
func ensureBinary(t *testing.T) {
	t.Helper()
	buildOnce.Do(func() {
		cmd := exec.Command("go", "build", "-buildvcs=false", "-tags", "xrr", "-o", fooBin, ".")
		cmd.Dir = filepath.Join("..", "..")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("build foo: %w\n%s", err, out)
		}
	})
	require.NoError(t, buildErr)
}

func TestCLI_Basic(t *testing.T) {
	ensureBinary(t)
	tmpDir := t.TempDir()

	t.Run("help", func(t *testing.T) {
		stdout, _, err := runFoo(t, tmpDir, "--help")
		require.NoError(t, err)
		// Current foo Short text. If this string drifts the test
		// must drift with it.
		require.Contains(t, stdout, "LLM workflows from the terminal")
	})

	t.Run("version", func(t *testing.T) {
		stdout, _, err := runFoo(t, tmpDir, "--version")
		require.NoError(t, err)
		// kit renders version as `<name> v<version>` on a single line.
		// Default build (no ldflags) reports "dev"; release builds set
		// the real semver via -X main.version=...
		require.Contains(t, stdout, "foo v")
	})

	t.Run("status", func(t *testing.T) {
		// Mounted by kit's WithStatus option; never exercised in
		// the legacy suite. Smokes that the kit runtime boots and
		// the default status providers render.
		stdout, _, err := runFoo(t, tmpDir, "status", "--format=json")
		require.NoError(t, err)
		require.Contains(t, stdout, "\"sections\"")
	})
}

func TestCLI_Pattern(t *testing.T) {
	ensureBinary(t)
	tmpDir := t.TempDir()

	t.Run("create", func(t *testing.T) {
		stdout, _, err := runFoo(t, tmpDir, "pattern", "create", "test-p", "You are a test assistant")
		require.NoError(t, err)
		require.Contains(t, stdout, `pattern "test-p" saved`)
	})

	t.Run("import", func(t *testing.T) {
		src := filepath.Join(tmpDir, "my-prompt.md")
		err := os.WriteFile(src, []byte("imported system prompt"), 0o644)
		require.NoError(t, err)

		stdout, _, err := runFoo(t, tmpDir, "pattern", "import", src, "imported-p")
		require.NoError(t, err)
		require.Contains(t, stdout, "pattern imported")
	})

	t.Run("list", func(t *testing.T) {
		stdout, _, err := runFoo(t, tmpDir, "pattern", "list")
		require.NoError(t, err)
		require.Contains(t, stdout, "test-p")
		require.Contains(t, stdout, "imported-p")
	})

	t.Run("show", func(t *testing.T) {
		stdout, _, err := runFoo(t, tmpDir, "pattern", "show", "test-p")
		require.NoError(t, err)
		require.Contains(t, stdout, "test-p")
	})
}

// TestCLI_Destructive_ConfirmPolicy locks in kit's --confirm policy
// against foo's destructive leaves. Non-TTY default is "no"; a
// destructive command must refuse without --confirm=yes and proceed
// when it is supplied. Read commands ignore --confirm entirely.
func TestCLI_Destructive_ConfirmPolicy(t *testing.T) {
	ensureBinary(t)

	cases := []struct {
		name          string
		seed          [][]string
		deleteArgs    []string
		successOutput string
		listArgs      []string
	}{
		{
			name:          "pattern",
			seed:          [][]string{{"pattern", "create", "doomed", ""}},
			deleteArgs:    []string{"pattern", "delete", "doomed"},
			successOutput: `pattern "doomed" deleted`,
			listArgs:      []string{"pattern", "list"},
		},
		{
			name:          "fragment",
			seed:          [][]string{{"fragment", "create", "doomed", "scrap text"}},
			deleteArgs:    []string{"fragment", "delete", "doomed"},
			successOutput: `fragment "doomed" deleted`,
			listArgs:      []string{"fragment", "list"},
		},
		{
			name:          "schema",
			seed:          [][]string{{"schema", "create", "doomed", "name, age int"}},
			deleteArgs:    []string{"schema", "delete", "doomed"},
			successOutput: `schema "doomed" deleted`,
			listArgs:      []string{"schema", "list"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			if tc.name == "fragment" {
				srcPath := filepath.Join(tmpDir, "frag-src.txt")
				require.NoError(t, os.WriteFile(srcPath, []byte("scrap text"), 0o644))
				tc.seed = [][]string{{"fragment", "create", "doomed", srcPath}}
			}

			for _, seed := range tc.seed {
				_, _, err := runFoo(t, tmpDir, seed...)
				require.NoError(t, err, "seed: %v", seed)
			}

			t.Run("refused without --confirm", func(t *testing.T) {
				_, stderr, err := runFoo(t, tmpDir, tc.deleteArgs...)
				require.Error(t, err, "non-TTY delete without --confirm must exit non-zero")
				require.Contains(t, stderr, "UNAUTHORIZED",
					"kit confirm policy should report UNAUTHORIZED on non-TTY default")
			})

			t.Run("proceeds with --confirm=yes", func(t *testing.T) {
				args := append(append([]string{}, tc.deleteArgs...), "--confirm=yes")
				stdout, _, err := runFoo(t, tmpDir, args...)
				require.NoError(t, err)
				require.Contains(t, stdout, tc.successOutput)
			})

			t.Run("read commands ignore --confirm", func(t *testing.T) {
				args := append(append([]string{}, tc.listArgs...), "--confirm=no")
				_, _, err := runFoo(t, tmpDir, args...)
				require.NoError(t, err)
			})
		})
	}
}

func TestCLI_Provider(t *testing.T) {
	ensureBinary(t)
	tmpDir := t.TempDir()

	t.Run("list", func(t *testing.T) {
		stdout, _, err := runFoo(t, tmpDir, "provider", "list")
		require.NoError(t, err)
		// At least the always-present anthropic + openai schemes.
		require.Contains(t, stdout, "anthropic")
		require.Contains(t, stdout, "openai")
	})

	t.Run("show with secret configured", func(t *testing.T) {
		t.Setenv("OPENAI_API_KEY", "sk-test123456789")
		stdout, _, err := runFoo(t, tmpDir, "provider", "show", "openai")
		require.NoError(t, err)
		require.Contains(t, stdout, "openai")
		require.Contains(t, stdout, "configured")
	})

	// show answers from the key, not unconditionally, and for a
	// provider known only from the catalog fixture: no live catalog
	// was needed to know fixturecloud wants FIXTURECLOUD_API_KEY.
	show := func(t *testing.T, scheme string) map[string]string {
		t.Helper()
		stdout, stderr, err := runFoo(t, tmpDir, "provider", "show", scheme, "--format=json")
		require.NoError(t, err, stderr)
		var got map[string]string
		require.NoError(t, json.Unmarshal([]byte(stdout), &got), stdout)
		return got
	}
	for _, tc := range []struct{ scheme, env, secret string }{
		{"openai", "OPENAI_API_KEY", "openai_api_key"},
		{"fixturecompat", "FIXTURECOMPAT_API_KEY", "fixturecompat_api_key"},
		{"fixturecloud", "FIXTURECLOUD_API_KEY", "fixturecloud_api_key"},
	} {
		t.Run("show "+tc.scheme+" without key", func(t *testing.T) {
			for _, name := range []string{tc.env, "LLM_API_KEY"} {
				t.Setenv(name, "") // restores the caller's value after
				require.NoError(t, os.Unsetenv(name))
			}
			got := show(t, tc.scheme)
			require.Equal(t, "missing", got["status"], got)
			require.Equal(t, "api_key", got["auth_type"], got)
			require.Equal(t, tc.secret, got["secret_key"], got)
		})
		// Exported but empty is no key: the env secret backend
		// answers it with an empty secret, which must not count.
		t.Run("show "+tc.scheme+" with empty key", func(t *testing.T) {
			t.Setenv(tc.env, "")
			t.Setenv("LLM_API_KEY", "")
			got := show(t, tc.scheme)
			require.Equal(t, "missing", got["status"], got)
		})
		t.Run("show "+tc.scheme+" with key", func(t *testing.T) {
			t.Setenv(tc.env, "sk-test123456789")
			got := show(t, tc.scheme)
			require.Equal(t, "configured", got["status"], got)
		})
	}

	// A run with an empty key fails the precheck (exit 5) before any
	// request, for a scheme with its own adapter and for a catalog
	// provider reached through its protocol alike.
	for _, tc := range []struct{ model, env string }{
		{"openai://gpt-4o", "OPENAI_API_KEY"},
		{"fixturecompat://compat-1", "FIXTURECOMPAT_API_KEY"},
	} {
		t.Run("run "+tc.model+" with empty key", func(t *testing.T) {
			t.Setenv(tc.env, "")
			t.Setenv("LLM_API_KEY", "")
			// Nothing may leave the machine if the precheck lets it by.
			t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")
			_, stderr, err := runFoo(t, tmpDir, "-m", tc.model, "--no-stream", "hi")
			var exitErr *exec.ExitError
			require.ErrorAs(t, err, &exitErr, stderr)
			require.Equal(t, 5, exitErr.ExitCode(), "stderr: %s", stderr)
			require.Contains(t, stderr, "missing "+tc.env)
		})
	}
}

func TestCLI_Model(t *testing.T) {
	ensureBinary(t)
	tmpDir := t.TempDir()

	t.Run("current empty by default", func(t *testing.T) {
		_, _, err := runFoo(t, tmpDir, "model", "current")
		require.NoError(t, err)
	})

	t.Run("default sets model", func(t *testing.T) {
		stdout, _, err := runFoo(t, tmpDir, "model", "default", "gpt-4o")
		require.NoError(t, err)
		require.Contains(t, stdout, `default model set to "gpt-4o"`)
	})
}

// TestCLI_BudgetValidation locks in two things: invalid --budget values
// error with a did-you-mean hint when the picker is engaged, AND --budget
// is silently ignored when -m is set (since -m bypasses the picker).
func TestCLI_BudgetValidation(t *testing.T) {
	ensureBinary(t)
	tmpDir := t.TempDir()

	t.Run("invalid budget errors with did-you-mean", func(t *testing.T) {
		// --dry-run skips the LLM call but still runs PersistentPreRunE,
		// which is where ResolveBudget fires.
		_, stderr, err := runFoo(t, tmpDir, "--dry-run", "--budget", "chep", "hi")
		require.Error(t, err, "invalid --budget must reject before any LLM call")
		require.Contains(t, stderr, "did you mean",
			"error should suggest the closest valid tier")
	})

	t.Run("invalid budget ignored when --model is set", func(t *testing.T) {
		// With -m set, budget validation should be skipped because the
		// picker is bypassed and the value would be inert. --dry-run
		// guarantees no LLM call regardless.
		_, _, err := runFoo(t, tmpDir, "--dry-run", "-m", "gpt-4o", "--budget", "bogus", "hi")
		require.NoError(t, err,
			"--budget should be silently ignored when -m is set")
	})
}

// TestCLI_Tool covers the -T discovery surface on the built binary,
// where main.go maps typed errors to exit codes: `go test` against the
// command tree alone cannot see the process status.
func TestCLI_Tool(t *testing.T) {
	ensureBinary(t)
	tmpDir := t.TempDir()

	t.Run("list", func(t *testing.T) {
		stdout, _, err := runFoo(t, tmpDir, "--offline", "tool", "list", "--format=json")
		require.NoError(t, err)
		require.Contains(t, stdout, `"name": "foo_time"`)
		require.Contains(t, stdout, `"source": "builtin"`)
	})

	for _, args := range [][]string{
		{"--offline", "--dry-run", "-T", "nope", "hi"},
		{"--offline", "--dry-run", "hi", "-T", "nope"},
	} {
		t.Run("unknown -T exits not-found: "+strings.Join(args, " "), func(t *testing.T) {
			_, stderr, err := runFoo(t, tmpDir, args...)
			var exitErr *exec.ExitError
			require.ErrorAs(t, err, &exitErr)
			require.Equal(t, 3, exitErr.ExitCode(), "stderr: %s", stderr)
			require.Contains(t, stderr, `"nope"`)
			require.Contains(t, stderr, "foo tool list")
		})
	}
}
