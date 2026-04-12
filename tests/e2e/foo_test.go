package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func runFoo(t *testing.T, tmpHome string, args ...string) (string, string, error) {
	// Build the binary if it doesn't exist (handled by Makefile usually, but good for go test ./...)
	binPath := filepath.Join("..", "..", "bin", "foo")
	if _, err := os.Stat(binPath); os.IsNotExist(err) {
		cmd := exec.Command("go", "build", "-o", binPath, "../../main.go")
		err := cmd.Run()
		require.NoError(t, err, "failed to build binary")
	}

	absBinPath, _ := filepath.Abs(binPath)
	cmd := exec.Command(absBinPath, args...)
	
	// Isolate the test environment
	cmd.Env = append(os.Environ(), 
		"HOME="+tmpHome,
		"XDG_CONFIG_HOME="+filepath.Join(tmpHome, ".config"),
		"XDG_STATE_HOME="+filepath.Join(tmpHome, ".local", "state"),
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func TestCLI_Basic(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("help", func(t *testing.T) {
		stdout, _, err := runFoo(t, tmpDir, "--help")
		require.NoError(t, err)
		require.Contains(t, stdout, "foo is an opinionated LLM CLI/REPL")
	})

	t.Run("version", func(t *testing.T) {
		stdout, _, err := runFoo(t, tmpDir, "--version")
		require.NoError(t, err)
		require.Contains(t, stdout, "foo version 0.1.0")
	})
}

func TestCLI_Pattern(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("add pattern", func(t *testing.T) {
		stdout, _, err := runFoo(t, tmpDir, "pattern", "add", "test-p", "You are a test assistant")
		require.NoError(t, err)
		require.Contains(t, stdout, "Pattern \"test-p\" added")
	})

	t.Run("import pattern", func(t *testing.T) {
		src := filepath.Join(tmpDir, "my-prompt.md")
		err := os.WriteFile(src, []byte("imported system prompt"), 0644)
		require.NoError(t, err)

		stdout, _, err := runFoo(t, tmpDir, "pattern", "import", src, "imported-p")
		require.NoError(t, err)
		require.Contains(t, stdout, "Pattern imported successfully")

		stdout, _, _ = runFoo(t, tmpDir, "pattern", "list")
		require.Contains(t, stdout, "- imported-p")
	})

	t.Run("list patterns", func(t *testing.T) {
		stdout, _, err := runFoo(t, tmpDir, "pattern", "list")
		require.NoError(t, err)
		require.Contains(t, stdout, "- test-p")
	})

	t.Run("remove pattern", func(t *testing.T) {
		stdout, _, err := runFoo(t, tmpDir, "pattern", "remove", "test-p")
		require.NoError(t, err)
		require.Contains(t, stdout, "Pattern \"test-p\" removed")
		
		stdout, _, _ = runFoo(t, tmpDir, "pattern", "list")
		require.NotContains(t, stdout, "- test-p")
	})
}

func TestCLI_Model(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("list models", func(t *testing.T) {
		stdout, _, err := runFoo(t, tmpDir, "model", "list")
		require.NoError(t, err)
		require.Contains(t, stdout, "Common models")
		require.Contains(t, stdout, "gpt-4o")
	})

	t.Run("set default model", func(t *testing.T) {
		stdout, _, err := runFoo(t, tmpDir, "model", "set-default", "gpt-4o")
		require.NoError(t, err)
		require.Contains(t, stdout, "Default model set to \"gpt-4o\"")
		
		// Verify it saved to config
		configPath := filepath.Join(tmpDir, ".config", "foo", "config.yaml")
		content, err := os.ReadFile(configPath)
		require.NoError(t, err)
		require.Contains(t, string(content), "model: gpt-4o")
	})

	t.Run("refresh models", func(t *testing.T) {
		stdout, _, err := runFoo(t, tmpDir, "model", "refresh")
		require.NoError(t, err)
		require.Contains(t, stdout, "Model list refreshed")
	})
}

func TestCLI_Provider(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("list providers", func(t *testing.T) {
		stdout, _, err := runFoo(t, tmpDir, "provider", "list")
		require.NoError(t, err)
		require.Contains(t, stdout, "Supported providers")
		require.Contains(t, stdout, "anthropic://")
	})

	t.Run("enable_disable provider", func(t *testing.T) {
		stdout, _, err := runFoo(t, tmpDir, "provider", "enable", "openai")
		require.NoError(t, err)
		require.Contains(t, stdout, "Provider \"openai\" enabled")

		stdout, _, err = runFoo(t, tmpDir, "provider", "disable", "openai")
		require.NoError(t, err)
		require.Contains(t, stdout, "Provider \"openai\" disabled")
	})

	t.Run("show provider", func(t *testing.T) {
		// Set an env var for the test
		os.Setenv("OPENAI_API_KEY", "sk-test123456789")
		defer os.Unsetenv("OPENAI_API_KEY")

		stdout, _, err := runFoo(t, tmpDir, "provider", "show", "openai")
		require.NoError(t, err)
		require.Contains(t, stdout, "Provider: openai")
		require.Contains(t, stdout, "configured (masked: sk-t...)")
	})
}

func TestCLI_Workspace(t *testing.T) {
	tmpDir := t.TempDir()
	
	// Running any command should initialize the workspace DB
	_, _, err := runFoo(t, tmpDir, "model", "list")
	require.NoError(t, err)
	
	dbPath := filepath.Join(tmpDir, ".config", "foo", "foo.db")
	_, err = os.Stat(dbPath)
	require.NoError(t, err, "workspace DB should be created")
}
