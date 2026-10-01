// Provider keys. Kit resolves them (kitllm.ApplyAPIKey for a run,
// kitllm.ResolveAPIKey to say where a key came from): which variables a
// scheme reads, in what order, from llm.yaml, the secret store, the
// environment and LLM_API_KEY, what counts as blank, and what a store
// backend failure does. foo keeps only its own policy around those
// calls: the secret-store names it documents, the exit code and wording
// of a missing key, and what a fallback without a key does.

package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	kitllm "hop.top/kit/go/ai/llm"
	"hop.top/kit/go/console/output"
	"hop.top/kit/go/storage/secret"
)

// applyKey returns uri with its provider's key set as the api_key
// param, which is where kit's Resolve reads it from. uri must name its
// scheme. A URI already carrying a non-blank api_key, a local runtime
// without its own key, and a scheme no adapter serves whose llm.yaml
// block names no key come back unchanged (kit reports the last one
// itself, at Resolve). A blank api_key is dropped and the key resolved.
//
// A required key found nowhere is foo's precheck failure
// (missingKeyError); any other kit error is returned as is. A store
// backend failure does not stop the search: kit logs it when another
// source has the key, and missingKeyError names it when none does.
func applyKey(ctx context.Context, store secret.Store, uri string) (string, error) {
	keyed, err := kitllm.ApplyAPIKey(ctx, storeFor(store), uri)
	var missing *kitllm.MissingKeyError
	if errors.As(err, &missing) {
		return "", missingKeyError(missing)
	}
	return keyed, err
}

// APIKey returns the key a request to uri would carry, resolved the
// way a run resolves it: the same kitllm.ApplyAPIKey call over foo's
// configured store (nil for none) under its documented names. It is
// for callers that talk to a provider without a kit client, such as
// the embedder.
//
// A required key found nowhere is kit's *kitllm.MissingKeyError
// (errors.Is kitllm.ErrMissingKey), left for the caller to word. A
// scheme that needs no key returns "".
func APIKey(ctx context.Context, store secret.Store, uri string) (string, error) {
	keyed, err := kitllm.ApplyAPIKey(ctx, storeFor(store), uri)
	if err != nil {
		return "", err
	}
	parsed, err := kitllm.ParseURI(keyed)
	if err != nil {
		return "", err // kit masks the key in the URI it quotes
	}
	return parsed.Params["api_key"], nil
}

// missingKeyError is the precheck failure every path shares, so a bare
// id, a URI and a pool pick all name the variable to set the same way:
// the first one kit consulted, the highest-precedence name. A secret
// store that failed on the way is named too: the key may sit in it.
func missingKeyError(m *kitllm.MissingKeyError) error {
	envVar := firstEnvVar(m)
	msg := fmt.Sprintf("missing %s for model %q (provider %s); export %s=... and retry, or switch models with `foo model default <model>`", envVar, m.Model, m.Scheme, envVar)
	if m.StoreErr != nil {
		msg += " (" + m.StoreErr.Error() + ")"
	}
	return output.UnauthorizedError(msg)
}

// firstEnvVar is the variable a missing-key message tells the user to
// set. kit always lists at least LLM_API_KEY; the guard is for a zero
// error only.
func firstEnvVar(m *kitllm.MissingKeyError) string {
	if len(m.EnvVars) == 0 {
		return kitllm.FallbackEnvKey
	}
	return m.EnvVars[0]
}

// blankKey reports whether v holds no key: it is empty or only
// whitespace. kit applies the same rule at every key source, so foo's
// own lookup for a provider kit cannot reach (lookupCatalogKey)
// applies it too.
func blankKey(v string) bool { return strings.TrimSpace(v) == "" }

// SecretName maps an env var name onto the key foo's secret store
// knows it by.
//
// Kit asks a store for a provider's key under the variable's own name
// (OPENAI_API_KEY), and names it so in a KeySource. foo's store is
// keyed in lowercase — `openai_api_key` — which is what `foo provider
// show` reports and what users have stored. namedStore applies this
// mapping to every lookup kit makes, so the documented names keep
// working on every backend; the env backend uppercases on read, so
// either spelling reaches the same variable there.
func SecretName(envVar string) string {
	return strings.ToLower(strings.TrimSpace(envVar))
}

// storeFor returns the secret.Store foo hands kit: store asked under
// SecretName, or nil (kit's "no store") when there is none.
func storeFor(store secret.Store) secret.Store {
	if store == nil {
		return nil
	}
	return namedStore{inner: store}
}

// namedStore asks inner for every key under its SecretName. It changes
// names only: blank values and backend failures are kit's to judge.
type namedStore struct {
	inner secret.Store
}

func (s namedStore) Get(ctx context.Context, key string) (*secret.Secret, error) {
	return s.inner.Get(ctx, SecretName(key))
}

func (s namedStore) List(ctx context.Context, prefix string) ([]string, error) {
	return s.inner.List(ctx, SecretName(prefix))
}

func (s namedStore) Exists(ctx context.Context, key string) (bool, error) {
	return s.inner.Exists(ctx, SecretName(key))
}

// KeySource names where a provider key was found. Never the key.
type KeySource string

const (
	// KeySourceSecret: one of the provider's own key names, from the
	// secret store or the env var of that name.
	KeySourceSecret KeySource = "secret_key"
	// KeySourceLLMAPIKey: kit's universal LLM_API_KEY.
	KeySourceLLMAPIKey KeySource = "LLM_API_KEY"
	// KeySourceLLMConfig: api_key in the llm.yaml block kit reads for
	// the scheme (its own, or an alias's).
	KeySourceLLMConfig KeySource = "llm.yaml"
)

// keyStatus is what a run on scheme would do for its key, without the
// key: kit's resolution, reported in foo's vocabulary.
type keyStatus struct {
	// routed is false when no kit adapter serves the scheme.
	routed bool
	// key is kit's plan for the scheme: names, highest first, and
	// whether the key is optional.
	key kitllm.ProviderKey
	// found reports that kit resolved a key; source and secretKey say
	// where.
	found     bool
	source    KeySource
	secretKey string
}

// resolveKeyStatus resolves scheme's key as a run would
// (kitllm.ResolveAPIKey, against store under foo's names) and maps
// kit's KeySource onto foo's: llm.yaml's api_key is KeySourceLLMConfig,
// a provider key name from the store or the environment is
// KeySourceSecret under its SecretName, LLM_API_KEY is
// KeySourceLLMAPIKey. secretKey names the key that resolved, else the
// first of the scheme's names, so it always names something to set.
func resolveKeyStatus(ctx context.Context, store secret.Store, scheme string) keyStatus {
	res, err := kitllm.ResolveAPIKey(ctx, storeFor(store), scheme+"://probe")
	var missing *kitllm.MissingKeyError
	if err != nil && !errors.As(err, &missing) {
		return keyStatus{}
	}
	st := keyStatus{routed: res.Known, key: res.Key, found: res.Found()}
	if len(res.Key.EnvVars) > 0 {
		st.secretKey = SecretName(res.Key.EnvVars[0])
	}
	switch res.Source.Kind {
	case kitllm.KeySourceConfig:
		st.source = KeySourceLLMConfig
	case kitllm.KeySourceStore, kitllm.KeySourceEnv:
		st.source, st.secretKey = KeySourceSecret, SecretName(res.Source.Name)
	case kitllm.KeySourceFallback:
		st.source = KeySourceLLMAPIKey
	}
	return st
}
