package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// runFoo executes the foo binary against an isolated $HOME, returning
// stdout, stderr, and the exec error. The binary is rebuilt every test
// run so stale `bin/foo` cannot mask a change in command vocabulary.
func runFoo(t *testing.T, tmpHome string, args ...string) (string, string, error) {
	t.Helper()
	binPath := filepath.Join("..", "..", "bin", "foo")
	absBinPath, err := filepath.Abs(binPath)
	require.NoError(t, err)
	cmd := exec.Command(absBinPath, args...)

	cmd.Env = append(os.Environ(),
		"HOME="+tmpHome,
		"XDG_CONFIG_HOME="+filepath.Join(tmpHome, ".config"),
		"XDG_STATE_HOME="+filepath.Join(tmpHome, ".local", "state"),
		"XDG_DATA_HOME="+filepath.Join(tmpHome, ".local", "share"),
		// Force the no-color path so assertions are not foiled by
		// terminfo escape sequences.
		"NO_COLOR=1",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	return stdout.String(), stderr.String(), err
}

// ensureBinary builds foo into bin/foo. Tests share one binary.
func ensureBinary(t *testing.T) {
	t.Helper()
	binPath := filepath.Join("..", "..", "bin", "foo")
	mainPath := filepath.Join("..", "..", ".")
	cmd := exec.Command("go", "build", "-o", binPath, mainPath)
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "build foo: %s", string(out))
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
		require.Contains(t, stdout, "foo v0.1.0")
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
		name           string
		seed           [][]string
		deleteArgs     []string
		successOutput  string
		listArgs       []string
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
