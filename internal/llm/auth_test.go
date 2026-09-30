package llm

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// TestSecretName pins the env-name → secret-name mapping. Kit asks for
// upstream env var names (OPENAI_API_KEY); foo's store is keyed
// lowercase and the env backend uppercases on read, so lowercasing is
// the whole translation. Getting it backwards reports every configured
// key as missing.
func TestSecretName(t *testing.T) {
	for in, want := range map[string]string{
		"OPENAI_API_KEY":               "openai_api_key",
		"GOOGLE_GENERATIVE_AI_API_KEY": "google_generative_ai_api_key",
		"  XAI_API_KEY  ":              "xai_api_key",
	} {
		if got := SecretName(in); got != want {
			t.Errorf("SecretName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestAuthIndex_KeyedProviderNeedsItsKey is the defect that motivated
// this file: a provider that requires an API key and has none must not
// be reported as usable. The old four-case switch answered "available"
// for groq, which is exactly backwards.
func TestAuthIndex_KeyedProviderNeedsItsKey(t *testing.T) {
	unsetKeyEnv(t)
	idx := NewAuthIndexFrom(context.Background(), map[string][]string{
		"groq":   {"GROQ_API_KEY"},
		"openai": {"OPENAI_API_KEY"},
	}, stubStore("openai_api_key"))

	if idx.Satisfied("groq") {
		t.Error("groq requires GROQ_API_KEY and none is configured; want not satisfied")
	}
	if !idx.Satisfied("openai") {
		t.Error("openai_api_key is configured; want satisfied")
	}
	if got := idx.Lookup("groq").Status(); got != "missing" {
		t.Errorf("groq status = %q, want %q", got, "missing")
	}
	if got := idx.Lookup("openai").Status(); got != "configured" {
		t.Errorf("openai status = %q, want %q", got, "configured")
	}
}

// TestAuthIndex_AnyAlternativeSatisfies covers a catalog provider no
// adapter serves that accepts several interchangeable spellings of one
// key. Requiring all of them would report every user of it as
// unconfigured. (A provider an adapter serves is answered by kit's key
// plan instead — see TestAuthIndex_GoogleTakesEitherKey.)
func TestAuthIndex_AnyAlternativeSatisfies(t *testing.T) {
	unsetKeyEnv(t)
	env := map[string][]string{
		"acme": {"ACME_API_KEY", "ACME_TOKEN", "ACME_KEY"},
	}
	for _, key := range []string{"acme_api_key", "acme_token", "acme_key"} {
		idx := NewAuthIndexFrom(context.Background(), env, stubStore(key))
		if !idx.Satisfied("acme") {
			t.Errorf("%s alone must satisfy acme", key)
		}
		if got := idx.Lookup("acme").SecretKey; got != key {
			t.Errorf("SecretKey = %q, want the alternative that resolved (%q)", got, key)
		}
	}
	none := NewAuthIndexFrom(context.Background(), env, stubStore())
	if none.Satisfied("acme") {
		t.Error("no acme alternative configured; want not satisfied")
	}
	// A missing verdict still names something concrete to set.
	if got := none.Lookup("acme").SecretKey; got != "acme_api_key" {
		t.Errorf("unconfigured SecretKey = %q, want the first alternative", got)
	}
}

// TestAuthIndex_NoEnvMeansNoAuth covers the local-runtime case: a
// provider declaring no env var needs no credential and is always
// satisfied, with the "available" status `foo provider show` prints.
func TestAuthIndex_NoEnvMeansNoAuth(t *testing.T) {
	unsetKeyEnv(t)
	idx := NewAuthIndexFrom(context.Background(), map[string][]string{
		"ollama": nil,
	}, stubStore())
	got := idx.Lookup("ollama")
	if !got.Satisfied() {
		t.Error("a provider requiring no credential must be satisfied")
	}
	if got.Required {
		t.Error("Required must be false with no env vars declared")
	}
	if got.Status() != "available" || got.AuthType() != "local" {
		t.Errorf("status/auth = %q/%q, want available/local", got.Status(), got.AuthType())
	}
}

// TestAuthIndex_UnknownProviderNeedsNoAuth pins the reading for a
// provider absent from the catalog — a self-hosted runtime with no
// models.dev entry. No declared requirement means no requirement.
func TestAuthIndex_UnknownProviderNeedsNoAuth(t *testing.T) {
	unsetKeyEnv(t)
	idx := NewAuthIndexFrom(context.Background(), map[string][]string{"openai": {"OPENAI_API_KEY"}}, stubStore())
	if !idx.Satisfied("some-local-runtime") {
		t.Error("a provider with no declared requirement must be satisfied")
	}
}

// TestAuthIndex_NilIndexSatisfies keeps a nil receiver usable: the
// pre-credential behaviour, not a listing collapsed to nothing.
func TestAuthIndex_NilIndexSatisfies(t *testing.T) {
	var idx *AuthIndex
	if !idx.Satisfied("groq") {
		t.Error("nil index must not hide rows")
	}
	if idx.ConfiguredProviders() != nil {
		t.Error("nil index has no configured providers")
	}
}

// TestAuthIndex_LookupErrorIsNotFatal checks one unreadable backend
// entry does not make the remaining alternatives unaskable.
func TestAuthIndex_LookupErrorIsNotFatal(t *testing.T) {
	unsetKeyEnv(t)
	store := newFakeStore("acme_key", "value")
	store.errs["acme_api_key"] = errors.New("keyring locked")
	idx := NewAuthIndexFrom(context.Background(), map[string][]string{
		"acme": {"ACME_API_KEY", "ACME_TOKEN", "ACME_KEY"},
	}, store)
	if !idx.Satisfied("acme") {
		t.Error("a later alternative resolved; the earlier error must not abort the scan")
	}
}

// TestAuthIndex_ConfiguredProviders backs the "nothing is reachable"
// guidance, which has to tell "no keys at all" from "keys, but not for
// what you filtered to".
func TestAuthIndex_ConfiguredProviders(t *testing.T) {
	unsetKeyEnv(t)
	idx := NewAuthIndexFrom(context.Background(), map[string][]string{
		"openai":  {"OPENAI_API_KEY"},
		"groq":    {"GROQ_API_KEY"},
		"mistral": {"MISTRAL_API_KEY"},
		"ollama":  nil,
	}, stubStore("openai_api_key", "mistral_api_key"))

	want := []string{"mistral", "openai"}
	if got := idx.ConfiguredProviders(); !reflect.DeepEqual(got, want) {
		t.Errorf("ConfiguredProviders() = %v, want %v (sorted, keyed providers only)", got, want)
	}
}

// TestFilterReachable_HidesKeylessAndUnroutable is the listing-level
// contract: a row survives only when foo has both an adapter and a
// credential for its provider.
func TestFilterReachable_HidesKeylessAndUnroutable(t *testing.T) {
	unsetKeyEnv(t)
	idx := NewAuthIndexFrom(context.Background(), map[string][]string{
		"openai": {"OPENAI_API_KEY"},
		"groq":   {"GROQ_API_KEY"},
		"google": {"GOOGLE_API_KEY"},
	}, stubStore("openai_api_key", "groq_api_key"))

	entries := []ModelEntry{
		{Provider: "openai", ID: "keyed-and-routable", Routable: true},
		{Provider: "google", ID: "routable-no-key", Routable: true},
		{Provider: "groq", ID: "keyed-no-adapter"},
		{Provider: "ollama", ID: "local-runtime", Routable: true},
	}
	kept, hidden := FilterReachable(entries, idx)
	if hidden != 2 {
		t.Errorf("hidden = %d, want 2", hidden)
	}
	got := make([]string, 0, len(kept))
	for _, e := range kept {
		got = append(got, e.ID)
	}
	want := []string{"keyed-and-routable", "local-runtime"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("kept = %v, want %v", got, want)
	}
}

// TestFilterReachable_NilIndexKeepsEverythingRoutable keeps the
// credential-unknown path from hiding rows on the adapter axis alone.
func TestFilterReachable_NilIndexKeepsEverythingRoutable(t *testing.T) {
	entries := []ModelEntry{
		{Provider: "openai", ID: "a", Routable: true},
		{Provider: "exotic", ID: "b"},
	}
	kept, hidden := FilterReachable(entries, nil)
	if len(kept) != 1 || kept[0].ID != "a" || hidden != 1 {
		t.Errorf("kept = %v, hidden = %d; want only the routable row", kept, hidden)
	}
}

// TestAuthIndex_LookupResolvesAliases: a kit scheme and the catalog id
// kit serves it under both resolve to the provider's key, each under the
// name the caller passed.
func TestAuthIndex_LookupResolvesAliases(t *testing.T) {
	unsetKeyEnv(t)
	idx := NewAuthIndexFrom(context.Background(), map[string][]string{
		"google":       {"GOOGLE_API_KEY"},
		"fireworks-ai": {"FIREWORKS_API_KEY"},
		"togetherai":   {"TOGETHER_API_KEY"},
	}, stubStore("google_api_key"))

	for scheme, want := range map[string]string{
		"gemini":       "configured",
		"google":       "configured",
		"fireworks":    "missing",
		"fireworks-ai": "missing",
		"together":     "missing",
		"togetherai":   "missing",
	} {
		got := idx.Lookup(scheme)
		if got.Status() != want {
			t.Errorf("Lookup(%q).Status() = %q, want %q", scheme, got.Status(), want)
		}
		if got.Provider != scheme {
			t.Errorf("Lookup(%q).Provider = %q, want the name as passed", scheme, got.Provider)
		}
	}

	// A local router takes no key of its own.
	if got := idx.Lookup("routellm"); got.Status() != "available" {
		t.Errorf("routellm status = %q, want available", got.Status())
	}
}
