package config

import "testing"

// TestSecretStore_OpensDocumentedBackends: every backend foo documents
// for `secrets.backend` is linked into this build and opens from the
// fields foo's config carries. Construction only: opening a keyring
// store records its service name and touches no keychain.
func TestSecretStore_OpensDocumentedBackends(t *testing.T) {
	for _, tc := range []Secrets{
		{Backend: "env"},
		{Backend: "env", Prefix: "FOO_"},
		{Backend: "keyring"},
		{Backend: "keyring", Service: "foo"},
	} {
		c := Default()
		c.Secrets = tc
		store, err := c.SecretStore()
		if err != nil {
			t.Errorf("%+v: %v", tc, err)
			continue
		}
		if store == nil {
			t.Errorf("%+v: nil store with nil error", tc)
		}
	}
}

// TestDefault_SecretsIsEnv: no `secrets:` block keeps the unprefixed env
// backend.
func TestDefault_SecretsIsEnv(t *testing.T) {
	if got := Default().Secrets; got != (Secrets{Backend: "env"}) {
		t.Errorf("default secrets = %+v, want env backend, no prefix or service", got)
	}
}

// TestSecretStore_UnknownBackend: a name kit has no backend for is an
// error the caller reports, not a silent env store.
func TestSecretStore_UnknownBackend(t *testing.T) {
	c := Default()
	c.Secrets.Backend = "no-such-backend"
	if _, err := c.SecretStore(); err == nil {
		t.Error("unknown backend opened without error")
	}
}
