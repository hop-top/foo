package commands

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hop.top/foo/internal/llm"
	"hop.top/kit/go/console/output"
)

// storeEnv isolates a run from the operator's machine: throwaway XDG
// and HOME, every key variable cleared, an empty pool so the first-run
// seed stays quiet. secrets is the body of foo's config.yaml `secrets:`
// block; empty writes no config file (foo's defaults).
func storeEnv(t *testing.T, secrets string) {
	t.Helper()
	for _, v := range append(append([]string{}, keyEnvVars...),
		"LLM_BASE_URL", "LLM_FALLBACK", "LLM_PROVIDER",
		"FOO_SECRETS_BACKEND", "FOO_SECRETS_PREFIX", "FOO_SECRETS_SERVICE", "FOO_MODEL") {
		t.Setenv(v, "")
		_ = os.Unsetenv(v)
	}
	root := t.TempDir()
	for _, v := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		dir := filepath.Join(root, strings.ToLower(v))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv(v, dir)
	}
	xdg := os.Getenv("XDG_CONFIG_HOME")
	for path, body := range map[string]string{
		filepath.Join(xdg, "hop", "llm.yaml"): "pool: []\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if secrets != "" {
		path := filepath.Join(xdg, "foo", "config.yaml")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("secrets:\n"+secrets), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// provider show reads the index through this var; build it from the
	// same store helper production uses, minus the models.dev fetch.
	prev := providerAuthIndex
	providerAuthIndex = func(ctx context.Context) (*llm.AuthIndex, error) {
		return llm.NewAuthIndexFrom(ctx, map[string][]string{"openrouter": {"OPENROUTER_API_KEY"}}, secretStore()), nil
	}
	t.Cleanup(func() { providerAuthIndex = prev })
}

// runOffline drives one prompt through the real root. The model's
// base_url is a closed loopback port and --offline refuses anything
// else, so no request can leave the machine.
func runOffline(t *testing.T) error {
	t.Helper()
	r := New("test")
	r.Cmd.SetOut(&bytes.Buffer{})
	r.Cmd.SetErr(&bytes.Buffer{})
	r.Cmd.SetIn(strings.NewReader(""))
	r.Cmd.SetArgs([]string{"--offline", "-m", "openrouter://openai/gpt-4.1-nano?base_url=http://127.0.0.1:9/v1", "hi"})
	return r.Cmd.Execute()
}

// isMissingKey reports whether err is the run's key-precheck refusal.
func isMissingKey(err error) bool {
	var ce *output.Error
	return err != nil && errors.As(err, &ce) && ce.Code == output.CodeUnauthorized &&
		strings.Contains(err.Error(), "missing OPENROUTER_API_KEY")
}

// TestRun_ReadsConfiguredSecretStore is the reported split: with a
// configured store holding the key, `provider show` said "configured"
// while the run's precheck — which only ever opened the default env
// backend — refused it. Both now read the store foo is configured with.
// A prefixed env backend stands in for a keychain or vault: it is a
// configured store the default lookup cannot see.
func TestRun_ReadsConfiguredSecretStore(t *testing.T) {
	storeEnv(t, "  backend: env\n  prefix: FOO_\n")
	t.Setenv("FOO_OPENROUTER_API_KEY", "sk-or-fake-stored")

	_, got := providerShowJSON(t, "openrouter")
	if got["status"] != "configured" {
		t.Fatalf("provider show status = %v, want configured", got["status"])
	}
	if err := runOffline(t); isMissingKey(err) {
		t.Fatalf("provider show says configured, but the run refused the stored key: %v", err)
	}
}

// TestRun_DefaultStoreIsPlainEnv: with no secrets block the store is
// the unprefixed env backend, so a prefixed variable is nobody's key and
// the plain variable still is — the pre-store behaviour, unchanged.
func TestRun_DefaultStoreIsPlainEnv(t *testing.T) {
	storeEnv(t, "")
	t.Setenv("FOO_OPENROUTER_API_KEY", "sk-or-fake-stored")

	_, got := providerShowJSON(t, "openrouter")
	if got["status"] != "missing" {
		t.Errorf("provider show status = %v, want missing", got["status"])
	}
	if err := runOffline(t); !isMissingKey(err) {
		t.Errorf("run err = %v, want the missing-key precheck", err)
	}

	t.Setenv("OPENROUTER_API_KEY", "sk-or-fake-plain")
	_, got = providerShowJSON(t, "openrouter")
	if got["status"] != "configured" {
		t.Errorf("provider show status = %v, want configured", got["status"])
	}
	if err := runOffline(t); isMissingKey(err) {
		t.Errorf("plain env key refused: %v", err)
	}
}

// TestSecretStore_UnopenableBackendFallsBackToEnv: a backend foo cannot
// open leaves both surfaces on the env var rather than failing the run.
func TestSecretStore_UnopenableBackendFallsBackToEnv(t *testing.T) {
	storeEnv(t, "  backend: no-such-backend\n")
	t.Setenv("OPENROUTER_API_KEY", "sk-or-fake-plain")

	_, got := providerShowJSON(t, "openrouter")
	if got["status"] != "configured" {
		t.Errorf("provider show status = %v, want configured", got["status"])
	}
	if err := runOffline(t); isMissingKey(err) {
		t.Errorf("env key refused under an unopenable backend: %v", err)
	}
}
