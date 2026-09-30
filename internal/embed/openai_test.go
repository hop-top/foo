package embed

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"hop.top/kit/go/console/output"
	"hop.top/kit/go/storage/secret"
	_ "hop.top/kit/go/storage/secret/env"
)

// isolateKeys clears every variable a key lookup could read (t.Setenv
// first, so the operator's values come back afterwards) and points
// HOME and XDG_CONFIG_HOME at empty dirs, so no real llm.yaml answers.
func isolateKeys(t *testing.T) {
	t.Helper()
	for _, v := range []string{"OPENAI_API_KEY", "LLM_API_KEY", "FOO_OPENAI_API_KEY"} {
		t.Setenv(v, "")
		_ = os.Unsetenv(v)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

// fakeStore is a map-backed secret.Store keyed the way foo documents
// its store names (lowercase): a keychain or vault stand-in.
type fakeStore map[string]string

func (s fakeStore) Get(_ context.Context, key string) (*secret.Secret, error) {
	v, ok := s[key]
	if !ok {
		return nil, secret.ErrNotFound
	}
	return &secret.Secret{Key: key, Value: []byte(v)}, nil
}

func (s fakeStore) List(context.Context, string) ([]string, error) { return nil, nil }

func (s fakeStore) Exists(ctx context.Context, key string) (bool, error) {
	_, err := s.Get(ctx, key)
	return err == nil, nil
}

// sentAuth embeds one text against a local recorder and returns the
// Authorization header the request carried.
func sentAuth(t *testing.T, e *OpenAIEmbedder) string {
	t.Helper()
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{0.1, 0.2}, "index": 0}},
		})
	}))
	t.Cleanup(srv.Close)
	e.url = srv.URL
	if _, err := e.Embed(context.Background(), []string{"hello"}); err != nil {
		t.Fatalf("embed: %v", err)
	}
	return got
}

// TestOpenAIEmbedder_ReadsConfiguredStore is the defect: embedding
// always opened the default env backend, so a key held only in foo's
// configured store (a prefixed env backend, a keychain) was never sent.
func TestOpenAIEmbedder_ReadsConfiguredStore(t *testing.T) {
	isolateKeys(t)
	t.Setenv("FOO_OPENAI_API_KEY", "sk-prefixed-fake")
	prefixed, err := secret.Open(secret.Config{Backend: "env", Prefix: "FOO_"})
	if err != nil {
		t.Fatal(err)
	}

	for name, store := range map[string]secret.Store{
		"prefixed env backend":  prefixed,
		"documented store name": fakeStore{"openai_api_key": "sk-prefixed-fake"},
	} {
		t.Run(name, func(t *testing.T) {
			e, err := NewOpenAIEmbedder(WithSecretStore(store))
			if err != nil {
				t.Fatalf("key held in the configured store refused: %v", err)
			}
			if got := sentAuth(t, e); got != "Bearer sk-prefixed-fake" {
				t.Errorf("Authorization = %q, want the store's key", got)
			}
		})
	}
}

// TestOpenAIEmbedder_StoreBeatsEnv: the store is consulted before the
// environment, as on the run path.
func TestOpenAIEmbedder_StoreBeatsEnv(t *testing.T) {
	isolateKeys(t)
	t.Setenv("OPENAI_API_KEY", "sk-env-fake")
	e, err := NewOpenAIEmbedder(WithSecretStore(fakeStore{"openai_api_key": "sk-store-fake"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := sentAuth(t, e); got != "Bearer sk-store-fake" {
		t.Errorf("Authorization = %q, want the store's key", got)
	}
}

// TestOpenAIEmbedder_PlainEnvUnchanged: no store, or the default
// unprefixed env store, still reads OPENAI_API_KEY.
func TestOpenAIEmbedder_PlainEnvUnchanged(t *testing.T) {
	isolateKeys(t)
	t.Setenv("OPENAI_API_KEY", "sk-env-fake")
	plain, err := secret.Open(secret.Config{Backend: "env"})
	if err != nil {
		t.Fatal(err)
	}
	for name, opts := range map[string][]OpenAIOption{
		"no store":          nil,
		"default env store": {WithSecretStore(plain)},
	} {
		t.Run(name, func(t *testing.T) {
			e, err := NewOpenAIEmbedder(opts...)
			if err != nil {
				t.Fatal(err)
			}
			if got := sentAuth(t, e); got != "Bearer sk-env-fake" {
				t.Errorf("Authorization = %q, want OPENAI_API_KEY", got)
			}
		})
	}
}

// TestOpenAIEmbedder_MissingKey: the refusal keeps its code and wording,
// and a prefixed variable is nobody's key without a store that says so.
func TestOpenAIEmbedder_MissingKey(t *testing.T) {
	isolateKeys(t)
	t.Setenv("FOO_OPENAI_API_KEY", "sk-prefixed-fake")
	plain, err := secret.Open(secret.Config{Backend: "env"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewOpenAIEmbedder(WithSecretStore(plain))
	var ce *output.Error
	if !errors.As(err, &ce) || ce.Code != output.CodeUnauthorized {
		t.Fatalf("err = %v, want an unauthorized refusal", err)
	}
	if !strings.Contains(err.Error(), "OPENAI_API_KEY not set") {
		t.Errorf("err = %q, want the existing wording", err)
	}
	if strings.Contains(err.Error(), "sk-prefixed-fake") {
		t.Errorf("err leaks a key: %q", err)
	}
}
