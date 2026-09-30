package commands

import (
	"bytes"
	"encoding/json"
	"testing"

	gokeyring "github.com/zalando/go-keyring"
)

// Every test here runs against go-keyring's in-memory mock (TestMain);
// none reaches the OS keychain. They go through go-keyring directly,
// not kit's keyring package: importing that here would register the
// backend in the test binary and hide a build that fails to link it.

// keyringSet stores value under key in the keyring service the way a
// user would, outside foo.
func keyringSet(t *testing.T, service, key, value string) {
	t.Helper()
	if err := gokeyring.Set(service, key, value); err != nil {
		t.Fatalf("keyring set %s/%s: %v", service, key, err)
	}
	t.Cleanup(func() { _ = gokeyring.Delete(service, key) })
}

// providerShowArgs is providerShowJSON with extra global flags.
func providerShowArgs(t *testing.T, scheme string, globals ...string) map[string]any {
	t.Helper()
	r := New("test")
	var out bytes.Buffer
	r.Cmd.SetOut(&out)
	r.Cmd.SetErr(&bytes.Buffer{})
	r.Cmd.SetArgs(append(globals, "provider", "show", scheme, "--format", "json"))
	if err := r.Cmd.Execute(); err != nil {
		t.Fatalf("provider show %s: %v", scheme, err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", out.String(), err)
	}
	return got
}

// TestSecretStore_KeyringBackendOpens: `secrets.backend: keyring` is a
// backend this build links, so the store opens instead of warning
// secrets.store.unavailable and dropping to env vars.
func TestSecretStore_KeyringBackendOpens(t *testing.T) {
	storeEnv(t, "  backend: keyring\n  service: foo-test\n")
	_ = providerShowArgs(t, "openrouter")
	if _, err := cfg.SecretStore(); err != nil {
		t.Fatalf("keyring backend did not open: %v", err)
	}
}

// TestRun_ReadsKeyringStore: a key kept in the keyring under its
// documented lowercase name reaches both surfaces — provider show
// reports it as the store key, and the run's precheck accepts it.
func TestRun_ReadsKeyringStore(t *testing.T) {
	storeEnv(t, "  backend: keyring\n  service: foo-test\n")
	keyringSet(t, "foo-test", "openrouter_api_key", "sk-or-fake-keyring")

	got := providerShowArgs(t, "openrouter")
	if got["status"] != "configured" {
		t.Fatalf("provider show status = %v, want configured", got["status"])
	}
	if got["secret_key"] != "openrouter_api_key" {
		t.Errorf("provider show secret_key = %v, want openrouter_api_key", got["secret_key"])
	}
	if err := runOffline(t); isMissingKey(err) {
		t.Fatalf("provider show says configured, but the run refused the keyring key: %v", err)
	}
}

// TestProfile_ScopesKeyringService: --profile names the keyring
// service, so a key stored for one profile is invisible to another and
// to the config's own service.
func TestProfile_ScopesKeyringService(t *testing.T) {
	storeEnv(t, "  backend: keyring\n  service: foo-test\n")
	keyringSet(t, "work", "openrouter_api_key", "sk-or-fake-work")

	if got := providerShowArgs(t, "openrouter", "--profile", "work"); got["status"] != "configured" {
		t.Errorf("--profile work: status = %v, want configured", got["status"])
	}
	if got := providerShowArgs(t, "openrouter", "--profile", "home"); got["status"] != "missing" {
		t.Errorf("--profile home: status = %v, want missing", got["status"])
	}
	if got := providerShowArgs(t, "openrouter"); got["status"] != "missing" {
		t.Errorf("no --profile: status = %v, want missing", got["status"])
	}
}
