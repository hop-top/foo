// Provider credential detection — "can this machine actually call that
// provider right now".
//
// This file exists because foo had two incompatible answers to that
// question and both surfaces that asked it got a wrong one.
//
// `foo provider show` used a four-case switch over scheme names
// (anthropic, openai, google/gemini, ollama) whose default arm reported
// "available", i.e. "needs no auth". That default is the common case:
// groq, mistral, xai, deepseek, fireworks and two hundred more all
// require an API key and were all told they needed none. A filter built
// on that switch would keep exactly the providers it is wrong about.
//
// The answer is now the run's own: for any provider a kit adapter
// serves — registered scheme, catalog alias ("fireworks-ai") or catalog
// protocol route — the index resolves the key as a run does
// (kitllm.ResolveAPIKey, against the same secret store) and reports
// where kit found it. Key variables, their order, llm.yaml's api_key /
// api_key_env (the scheme's block or an alias's), LLM_API_KEY, blank
// values and local runtimes are all kit's; the listing and `foo
// provider show` therefore cannot call "missing" a key a run would use,
// nor "configured" one it would refuse.
//
// A catalog provider no adapter serves cannot be run at all. kit still
// takes what its own llm.yaml block names, so the index reports that;
// otherwise it reads the variables aim lists, by kit's rules (store,
// then environment; a blank value is no key), so `provider show` still
// names what that provider would want.

package llm

import (
	"context"
	"os"
	"sort"

	"hop.top/aim"
	"hop.top/kit/go/storage/secret"
)

// ProviderAuth describes one provider's credential requirement and
// whether this machine satisfies it.
type ProviderAuth struct {
	// Provider is the provider id ("openai", "groq").
	Provider string
	// EnvVars are the env var names the provider's key is read from,
	// highest precedence first: kit's for a provider an adapter serves,
	// otherwise the alternatives aim lists. Empty when the provider
	// takes no key of its own.
	EnvVars []string
	// SecretKey is the secret-store name of the provider's own key:
	// the name that resolved when one did, otherwise the first one, so
	// it always names something concrete to set. Empty when no
	// credential is required.
	SecretKey string
	// Required reports whether any credential is needed. False for a
	// local runtime such as ollama.
	Required bool
	// Configured reports whether a required credential was found. It
	// is false when Required is false: "no credential was found"
	// is not a claim worth making about a provider that wants none.
	Configured bool
	// Source names where the key was found; empty unless Configured.
	Source KeySource
}

// Satisfied reports whether foo can authenticate to the provider: either
// no credential is needed, or one was found.
//
// This is the single predicate both `foo model list` and `foo provider
// show` branch on, which is the point of the type. Two surfaces deriving
// "can I call this" from the same fields by hand is how they came to
// disagree in the first place.
func (a ProviderAuth) Satisfied() bool { return !a.Required || a.Configured }

// Status renders the verdict in the vocabulary `foo provider show` has
// always printed, so the shared source of truth did not cost that
// command its output contract.
//
//   - "available" — no credential required (a local runtime).
//   - "configured" — a required credential is present.
//   - "missing" — a required credential is absent.
func (a ProviderAuth) Status() string {
	switch {
	case !a.Required:
		return "available"
	case a.Configured:
		return "configured"
	default:
		return "missing"
	}
}

// AuthType names the kind of credential, for the AUTH column of
// `foo provider show`. The vocabulary predates this file and is kept:
// "api_key" for a provider that wants one, "local" for one that does
// not.
//
// The old switch had a third value, "unknown", for every scheme it did
// not enumerate — which was almost all of them. Nothing is unknown any
// more: a provider absent from the catalog has no declared requirement,
// which is the same evidence "local" rests on, and inventing a third
// verdict for it would only re-open the gap this file closes. Callers
// that need the distinction read EnvVars, which is empty in both cases
// and non-empty in neither's.
func (a ProviderAuth) AuthType() string {
	if a.Required {
		return "api_key"
	}
	return "local"
}

// AuthIndex answers the credential question for any provider id or kit
// scheme.
//
// It is built once per command run rather than queried per row: a
// listing touches thousands of rows across hundreds of providers, and a
// keyring-backed secret store charges real latency per lookup. The index
// resolves each distinct name exactly once.
type AuthIndex struct {
	// byName holds every catalog provider id, each resolved under its
	// own name as a run naming it as its scheme would: fireworks-ai
	// reads providers.fireworks-ai in llm.yaml, then (kit's alias rule)
	// providers.fireworks.
	byName map[string]ProviderAuth
	// catalog marks the catalog provider ids, the names listing rows
	// carry.
	catalog map[string]bool
	// store answers names the catalog does not list (Lookup).
	store secret.Store
}

// NewAuthIndex resolves every catalog provider's credential state.
//
// reg nil means foo's shared aim registry — the same one the listing
// reads, so the providers described here are exactly the providers the
// rows came from. store is foo's configured secret store, the one a
// run's precheck reads (ClientOpts.Secrets); nil means none, and keys
// then come from llm.yaml and the environment, as for a run.
//
// A registry read failure is returned rather than swallowed. Guessing
// that nothing is configured would hide every model in the default view
// and blame the user's missing keys for foo's failed catalog read.
func NewAuthIndex(ctx context.Context, reg *aim.Registry, store secret.Store) (*AuthIndex, error) {
	if reg == nil {
		reg = ensureRegistry()
	}
	providers, err := reg.Providers(ctx)
	if err != nil {
		return nil, err
	}
	envByProvider := make(map[string][]string, len(providers))
	for _, p := range providers {
		envByProvider[p.ID] = p.Env
	}
	return NewAuthIndexFrom(ctx, envByProvider, store), nil
}

// NewAuthIndexFrom builds an index from an explicit provider→env-vars
// map, bypassing the aim registry. It is the seam tests use to pin the
// catalog without a models.dev fetch, and the constructor a future
// non-aim provider source would call. The env vars are consulted only
// for a provider no kit adapter serves; kit's key plan answers for the
// rest.
func NewAuthIndexFrom(ctx context.Context, envByProvider map[string][]string, store secret.Store) *AuthIndex {
	idx := &AuthIndex{
		byName:  make(map[string]ProviderAuth, len(envByProvider)),
		catalog: make(map[string]bool, len(envByProvider)),
		store:   store,
	}
	for id, env := range envByProvider {
		idx.catalog[id] = true
		idx.byName[id] = resolveProviderAuth(ctx, id, env, store)
	}
	return idx
}

// resolveProviderAuth decides name's state. For a name a kit adapter
// serves, that is the outcome of the key resolution a run on name would
// make (resolveKeyStatus); only the source is kept, never the key.
// Otherwise catalogEnv, aim's list, answers (resolveCatalogAuth), after
// the key kit takes from name's own llm.yaml block.
func resolveProviderAuth(ctx context.Context, name string, catalogEnv []string, store secret.Store) ProviderAuth {
	st := resolveKeyStatus(ctx, store, name)
	if !st.routed {
		return resolveCatalogAuth(ctx, name, catalogEnv, store, st)
	}
	a := ProviderAuth{
		Provider: name,
		EnvVars:  st.key.EnvVars,
		Required: !st.key.Optional && len(st.key.EnvVars) > 0,
	}
	if !a.Required {
		return a
	}
	a.SecretKey = st.secretKey
	if st.found {
		a.Configured = true
		a.Source = st.source
	}
	return a
}

// resolveCatalogAuth decides the state of a catalog provider no kit
// adapter serves. It is satisfied when *any one* of its env vars
// holds a key: aim lists alternatives, not a conjunction — three
// spellings of one key, and requiring all three would report every
// user of it as unconfigured.
//
// The names are looked up as kit looks up a routed provider's: the
// store first, then the environment, and a blank value (empty or only
// whitespace) is no key. A blank variable reads "missing" on this
// branch as it does on kit's. LLM_API_KEY and other providers' llm.yaml
// blocks are not consulted: kit lends neither to a provider it cannot
// reach.
//
// own is kit's resolution for the scheme, which reads its own llm.yaml
// block only (api_key, or the variable api_key_env names): a key found
// there is the provider's own, and answers first.
func resolveCatalogAuth(ctx context.Context, provider string, envVars []string, store secret.Store, own keyStatus) ProviderAuth {
	a := ProviderAuth{Provider: provider, EnvVars: envVars, Required: len(envVars) > 0}
	if own.found {
		a.Required, a.Configured, a.Source = true, true, own.source
		a.SecretKey = own.secretKey
		if a.SecretKey == "" && len(envVars) > 0 {
			a.SecretKey = SecretName(envVars[0])
		}
		return a
	}
	if !a.Required {
		return a
	}
	// Name the first alternative up front, so a "missing" verdict can
	// tell the user which variable to set even though none resolved.
	a.SecretKey = SecretName(envVars[0])
	if name, ok := lookupCatalogKey(ctx, store, envVars); ok {
		a.SecretKey = name
		a.Configured = true
		a.Source = KeySourceSecret
	}
	return a
}

// lookupCatalogKey reports the secret name of the first of envVars
// holding a key: in store (nil for none) under foo's SecretName, then
// in the environment. A blank value is no key, kit's rule (blankKey). A
// store lookup error counts as "not this one", as it does in kit: one
// unreadable backend entry must not make the other alternatives
// unaskable, and the listing has to render either way.
func lookupCatalogKey(ctx context.Context, store secret.Store, envVars []string) (string, bool) {
	if store != nil {
		for _, envVar := range envVars {
			name := SecretName(envVar)
			if got, err := store.Get(ctx, name); err == nil && got != nil && !blankKey(string(got.Value)) {
				return name, true
			}
		}
	}
	for _, envVar := range envVars {
		if !blankKey(os.Getenv(envVar)) {
			return SecretName(envVar), true
		}
	}
	return "", false
}

// Lookup returns the credential state of a catalog provider id or a kit
// scheme, as a run naming it as its scheme would find it. A name the
// catalog does not list (a kit scheme such as gemini, whatever
// `foo provider show` was given) is resolved on the spot.
//
// A provider neither kit nor the catalog knows — a local ollama or
// llama.cpp that publishes no models.dev entry — reports no requirement,
// and therefore Satisfied. That is the deliberate reading: foo has no
// evidence a key is needed, and hiding a locally served model behind a
// credential nobody asked for is the worse error of the two.
func (a *AuthIndex) Lookup(provider string) ProviderAuth {
	if a == nil {
		return ProviderAuth{Provider: provider}
	}
	if got, ok := a.byName[provider]; ok {
		return got
	}
	return resolveProviderAuth(context.Background(), provider, nil, a.store)
}

// Satisfied is the row-level predicate: can foo authenticate to this
// provider right now.
func (a *AuthIndex) Satisfied(provider string) bool {
	return a.Lookup(provider).Satisfied()
}

// ConfiguredProviders lists, sorted, the catalog providers whose
// credential requirement is met, from whichever source. It backs the
// "nothing is reachable" guidance, which needs to distinguish "no keys
// at all" from "keys, but none for the providers you filtered to".
func (a *AuthIndex) ConfiguredProviders() []string {
	if a == nil {
		return nil
	}
	var out []string
	for id := range a.catalog {
		if auth := a.byName[id]; auth.Required && auth.Configured {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}
