// Provider keys. Kit resolves them (kitllm.ApplyAPIKey): which
// variables a scheme reads, in what order, from llm.yaml, the secret
// store, the environment and LLM_API_KEY. foo keeps only its own policy
// around that call: the secret-store names it documents, the exit code
// and wording of a missing key, and what a fallback without a key does.

package llm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	kitllm "hop.top/kit/go/ai/llm"
	"hop.top/kit/go/console/output"
	"hop.top/kit/go/storage/secret"
)

// applyKey returns uri with its provider's key set as the api_key
// param, which is where kit's Resolve reads it from. uri must name its
// scheme. A URI already carrying api_key, a local runtime without its
// own key, and a scheme no adapter serves come back unchanged (kit
// reports the last one itself, at Resolve).
//
// A required key found nowhere is foo's precheck failure
// (missingKeyError); any other kit error is returned as is.
func applyKey(ctx context.Context, store secret.Store, uri string) (string, error) {
	keyed, err := kitllm.ApplyAPIKey(ctx, &namedStore{inner: store}, uri)
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
	keyed, err := kitllm.ApplyAPIKey(ctx, &namedStore{inner: store}, uri)
	if err != nil {
		return "", err
	}
	parsed, err := kitllm.ParseURI(keyed)
	if err != nil {
		// ParseURI quotes its input, which now carries the key.
		return "", errors.New("llm: provider URI must be scheme://model")
	}
	return parsed.Params["api_key"], nil
}

// missingKeyError is the precheck failure every path shares, so a bare
// id, a URI and a pool pick all name the variable to set the same way:
// the first one kit consulted, the highest-precedence name.
func missingKeyError(m *kitllm.MissingKeyError) error {
	envVar := firstEnvVar(m)
	return output.UnauthorizedError(fmt.Sprintf("missing %s for model %q (provider %s); export %s=... and retry, or switch models with `foo model default <model>`", envVar, m.Model, m.Scheme, envVar))
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

// SecretName maps an env var name onto the key foo's secret store
// knows it by.
//
// Kit asks a store for a provider's key under the variable's own name
// (OPENAI_API_KEY). foo's store is keyed in lowercase — `openai_api_key`
// — which is what `foo provider show` reports and what users have
// stored. namedStore applies this mapping to every lookup kit makes, so
// the documented names keep working on every backend; the env backend
// uppercases on read, so either spelling reaches the same variable
// there.
func SecretName(envVar string) string {
	return strings.ToLower(strings.TrimSpace(envVar))
}

// namedStore is the secret.Store foo hands kit: inner (foo's configured
// store, or nil for none) asked under SecretName.
//
// A backend error reads as "not here": kit then tries the next name and
// the environment, so an unreachable keyring never breaks a working
// env-based setup (kit itself would fail the run on it).
//
// It also records what kit asked, which is how the credential index
// names the source of a key without resolving it a second way: asked
// counts lookups (none means kit took the key from llm.yaml before
// consulting any name), hit is the name that answered.
type namedStore struct {
	inner secret.Store
	asked int
	hit   string
}

func (s *namedStore) Get(ctx context.Context, key string) (*secret.Secret, error) {
	s.asked++
	if s.inner == nil {
		return nil, secret.ErrNotFound
	}
	name := SecretName(key)
	got, err := s.inner.Get(ctx, name)
	if err != nil || got == nil || len(got.Value) == 0 {
		return nil, secret.ErrNotFound
	}
	s.hit = name
	return got, nil
}

func (s *namedStore) List(ctx context.Context, prefix string) ([]string, error) {
	if s.inner == nil {
		return nil, nil
	}
	return s.inner.List(ctx, SecretName(prefix))
}

func (s *namedStore) Exists(ctx context.Context, key string) (bool, error) {
	if s.inner == nil {
		return false, nil
	}
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
	// KeySourceLLMConfig: providers.<scheme>.api_key in llm.yaml.
	KeySourceLLMConfig KeySource = "llm.yaml"
)

// keyStatus is what a run on scheme would do for its key, without the
// key: the same kitllm.ApplyAPIKey call, observed.
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

// resolveKeyStatus runs kit's key resolution for scheme against store
// and reports the outcome. The source comes from what kit asked of the
// store: no lookup at all means llm.yaml's api_key answered; a store
// hit names the key; otherwise the value came from the environment,
// under one of the provider's names or else LLM_API_KEY.
func resolveKeyStatus(ctx context.Context, store secret.Store, scheme string) keyStatus {
	key, routed := kitllm.ProviderKeyFor(scheme)
	st := keyStatus{routed: routed, key: key}
	if !routed {
		return st
	}
	if len(key.EnvVars) > 0 {
		st.secretKey = SecretName(key.EnvVars[0])
	}
	probe := &namedStore{inner: store}
	keyed, err := kitllm.ApplyAPIKey(ctx, probe, scheme+"://probe")
	if err != nil {
		return st
	}
	parsed, err := kitllm.ParseURI(keyed)
	if err != nil {
		return st
	}
	if _, ok := parsed.Params["api_key"]; !ok {
		return st
	}
	st.found = true
	switch {
	case probe.asked == 0:
		st.source = KeySourceLLMConfig
	case probe.hit != "":
		st.source, st.secretKey = KeySourceSecret, probe.hit
	default:
		// kit read the environment: the first of the provider's names
		// that is set, else LLM_API_KEY.
		st.source = KeySourceLLMAPIKey
		for _, name := range key.EnvVars {
			if os.Getenv(name) != "" {
				st.source, st.secretKey = KeySourceSecret, SecretName(name)
				break
			}
		}
	}
	return st
}
