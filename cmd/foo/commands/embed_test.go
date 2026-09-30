package commands

import (
	"errors"
	"testing"

	"hop.top/foo/internal/config"
	"hop.top/kit/go/console/output"
)

// withSecrets sets foo's loaded config to the defaults plus s, as the
// root's pre-run would after reading a `secrets:` block.
func withSecrets(t *testing.T, s config.Secrets) {
	t.Helper()
	prev := cfg
	t.Cleanup(func() { cfg = prev })
	cfg = config.Default()
	if s.Backend != "" {
		cfg.Secrets = s
	}
}

// TestEmbed_ReadsConfiguredSecretStore: `foo embed` opened the default
// env backend itself, so a key the run path reads from the configured
// store was refused here. The embedder is built on secretStore().
func TestEmbed_ReadsConfiguredSecretStore(t *testing.T) {
	storeEnv(t, "")
	withSecrets(t, config.Secrets{Backend: "env", Prefix: "FOO_"})
	t.Setenv("FOO_OPENAI_API_KEY", "sk-oa-fake-stored")

	if _, err := newEmbedder(); err != nil {
		t.Fatalf("key held in the configured store refused: %v", err)
	}
}

// TestEmbed_DefaultStoreIsPlainEnv: with no secrets block a prefixed
// variable is nobody's key and the refusal keeps its exit code.
func TestEmbed_DefaultStoreIsPlainEnv(t *testing.T) {
	storeEnv(t, "")
	withSecrets(t, config.Secrets{})
	t.Setenv("FOO_OPENAI_API_KEY", "sk-oa-fake-stored")

	_, err := newEmbedder()
	var ce *output.Error
	if !errors.As(err, &ce) || ce.Code != output.CodeUnauthorized {
		t.Fatalf("err = %v, want the unauthorized refusal", err)
	}

	t.Setenv("OPENAI_API_KEY", "sk-oa-fake-plain")
	if _, err := newEmbedder(); err != nil {
		t.Errorf("plain env key refused: %v", err)
	}
}
