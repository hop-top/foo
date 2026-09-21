package llm

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// stubLookup resolves only the named secrets, so a test states which
// credentials exist instead of inheriting the operator's environment.
func stubLookup(present ...string) SecretLookup {
	have := make(map[string]bool, len(present))
	for _, k := range present {
		have[k] = true
	}
	return func(_ context.Context, key string) (string, bool, error) {
		if have[key] {
			return "value", true, nil
		}
		return "", false, nil
	}
}

// TestSecretName pins the env-name → secret-name mapping. aim publishes
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
	idx := NewAuthIndexFrom(context.Background(), map[string][]string{
		"groq":   {"GROQ_API_KEY"},
		"openai": {"OPENAI_API_KEY"},
	}, stubLookup("openai_api_key"))

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

// TestAuthIndex_AnyAlternativeSatisfies covers google, which accepts
// three interchangeable spellings of one key. Requiring all of them
// would report every google user as unconfigured.
func TestAuthIndex_AnyAlternativeSatisfies(t *testing.T) {
	env := map[string][]string{
		"google": {"GOOGLE_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY", "GEMINI_API_KEY"},
	}
	for _, key := range []string{"google_api_key", "google_generative_ai_api_key", "gemini_api_key"} {
		idx := NewAuthIndexFrom(context.Background(), env, stubLookup(key))
		if !idx.Satisfied("google") {
			t.Errorf("%s alone must satisfy google", key)
		}
		if got := idx.Lookup("google").SecretKey; got != key {
			t.Errorf("SecretKey = %q, want the alternative that resolved (%q)", got, key)
		}
	}
	none := NewAuthIndexFrom(context.Background(), env, stubLookup())
	if none.Satisfied("google") {
		t.Error("no google alternative configured; want not satisfied")
	}
	// A missing verdict still names something concrete to set.
	if got := none.Lookup("google").SecretKey; got != "google_api_key" {
		t.Errorf("unconfigured SecretKey = %q, want the first alternative", got)
	}
}

// TestAuthIndex_NoEnvMeansNoAuth covers the local-runtime case: a
// provider declaring no env var needs no credential and is always
// satisfied, with the "available" status `foo provider show` prints.
func TestAuthIndex_NoEnvMeansNoAuth(t *testing.T) {
	idx := NewAuthIndexFrom(context.Background(), map[string][]string{
		"ollama": nil,
	}, stubLookup())
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
	idx := NewAuthIndexFrom(context.Background(), map[string][]string{"openai": {"OPENAI_API_KEY"}}, stubLookup())
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
	lookup := func(_ context.Context, key string) (string, bool, error) {
		if key == "google_api_key" {
			return "", false, errors.New("keyring locked")
		}
		if key == "gemini_api_key" {
			return "value", true, nil
		}
		return "", false, nil
	}
	idx := NewAuthIndexFrom(context.Background(), map[string][]string{
		"google": {"GOOGLE_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY", "GEMINI_API_KEY"},
	}, lookup)
	if !idx.Satisfied("google") {
		t.Error("a later alternative resolved; the earlier error must not abort the scan")
	}
}

// TestAuthIndex_ConfiguredProviders backs the "nothing is reachable"
// guidance, which has to tell "no keys at all" from "keys, but not for
// what you filtered to".
func TestAuthIndex_ConfiguredProviders(t *testing.T) {
	idx := NewAuthIndexFrom(context.Background(), map[string][]string{
		"openai":  {"OPENAI_API_KEY"},
		"groq":    {"GROQ_API_KEY"},
		"mistral": {"MISTRAL_API_KEY"},
		"ollama":  nil,
	}, stubLookup("openai_api_key", "mistral_api_key"))

	want := []string{"mistral", "openai"}
	if got := idx.ConfiguredProviders(); !reflect.DeepEqual(got, want) {
		t.Errorf("ConfiguredProviders() = %v, want %v (sorted, keyed providers only)", got, want)
	}
}

// TestFilterReachable_HidesKeylessAndUnroutable is the listing-level
// contract: a row survives only when foo has both an adapter and a
// credential for its provider.
func TestFilterReachable_HidesKeylessAndUnroutable(t *testing.T) {
	idx := NewAuthIndexFrom(context.Background(), map[string][]string{
		"openai": {"OPENAI_API_KEY"},
		"groq":   {"GROQ_API_KEY"},
		"google": {"GOOGLE_API_KEY"},
	}, stubLookup("openai_api_key", "groq_api_key"))

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

// TestAuthIndex_SchemeSpellingsExistInCatalog is the completeness check
// the stubbed alias test cannot make: it reads the real catalog and
// reports every kit scheme that resolves to no provider record.
//
// A scheme in that state is reported "available" — needs no credential
// — which is how `foo provider show fireworks` came to bless a provider
// that will 401. Two of these are legitimate (a local runtime publishes
// no models.dev entry), so this cannot simply fail on a non-empty set;
// it fails when a scheme that *does* have a catalog twin under another
// spelling is missing its alias, which is detectable by looking for a
// provider id the scheme is a prefix of.
//
// Skipped when the catalog has never been fetched: a cold CI box has no
// business failing a test about credential spelling.
func TestAuthIndex_SchemeSpellingsExistInCatalog(t *testing.T) {
	ctx := context.Background()
	reg := ensureRegistry()
	if !ReadCatalogProvenance(reg).Fetched {
		t.Skip("catalog never fetched; nothing to check spellings against")
	}
	providers, err := reg.Providers(ctx)
	if err != nil {
		t.Skipf("catalog unreadable: %v", err)
	}
	known := make(map[string]bool, len(providers))
	for _, p := range providers {
		known[p.ID] = true
	}

	for scheme := range routableProviders() {
		if known[scheme] || schemeProviderAliases[scheme] != "" {
			continue
		}
		// No exact record and no alias. If some catalog id merely
		// spells the same provider differently, an alias is missing and
		// foo will claim the provider needs no key.
		for id := range known {
			if id != scheme && strings.HasPrefix(id, scheme) {
				t.Errorf("kit scheme %q has no catalog record but %q looks like the same provider; "+
					"without an entry in schemeProviderAliases, `foo provider show %s` reports "+
					"it needs no credential", scheme, id, scheme)
				break
			}
		}
	}

	// The other direction: an alias must name a provider the catalog
	// actually has, or it silently does nothing.
	for scheme, provider := range schemeProviderAliases {
		if !known[provider] {
			t.Errorf("alias %q -> %q names no catalog provider; the mapping is dead", scheme, provider)
		}
	}
}

// TestAuthIndex_LookupSchemeResolvesAliases pins the translation itself,
// against a fixture rather than the live catalog.
func TestAuthIndex_LookupSchemeResolvesAliases(t *testing.T) {
	idx := NewAuthIndexFrom(context.Background(), map[string][]string{
		"google":       {"GOOGLE_API_KEY"},
		"fireworks-ai": {"FIREWORKS_API_KEY"},
		"togetherai":   {"TOGETHER_API_KEY"},
	}, stubLookup("google_api_key"))

	for scheme, want := range map[string]string{
		"gemini":    "configured",
		"google":    "configured",
		"fireworks": "missing",
		"together":  "missing",
	} {
		got := idx.LookupScheme(scheme)
		if got.Status() != want {
			t.Errorf("LookupScheme(%q).Status() = %q, want %q", scheme, got.Status(), want)
		}
		// Reported under the name the caller passed, not the alias.
		if got.Provider != scheme {
			t.Errorf("LookupScheme(%q).Provider = %q, want the scheme as passed", scheme, got.Provider)
		}
	}

	// A scheme with no alias and no record stays "needs nothing".
	if got := idx.LookupScheme("routellm"); got.Status() != "available" {
		t.Errorf("routellm status = %q, want available", got.Status())
	}
}
