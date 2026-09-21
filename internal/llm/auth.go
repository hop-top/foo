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
// aim already carries the authoritative data. Every provider in the
// models.dev census declares the env var names it accepts
// (aim.Provider.Env), including the multi-alternative cases — google
// accepts GOOGLE_API_KEY, GOOGLE_GENERATIVE_AI_API_KEY or
// GEMINI_API_KEY, any one of which is enough. So the requirement is read
// from the catalog rather than restated here, and a provider foo has
// never heard of is described correctly the day models.dev adds it.
//
// The one thing aim cannot answer is whether the key is *present*, since
// foo's secret store may be a keyring rather than the environment. That
// half goes through a SecretLookup the caller supplies, so a user with a
// keyring backend is never told their configured key is missing.

package llm

import (
	"context"
	"sort"
	"strings"

	"hop.top/aim"
)

// SecretLookup reports whether a named secret resolves in foo's
// configured secret store, and its value if so.
//
// The signature is config.Config.LookupSecret's, deliberately: the
// production caller passes that method value directly, and this package
// does not import internal/config (which would invert the dependency —
// config is the lower layer). Tests pass a map-backed stub.
type SecretLookup func(ctx context.Context, key string) (string, bool, error)

// SecretName maps an aim env var name onto the key foo's secret store
// knows it by.
//
// aim publishes upstream env var names in upstream form: OPENAI_API_KEY.
// foo's secret store is keyed in lowercase — `openai_api_key` — which is
// what root.go's provider switch has always used and what
// `foo secret set` writes. The env backend closes the loop by
// uppercasing on read (prefix + strings.ToUpper(key)), so the lowercase
// name round-trips to the same OPENAI_API_KEY an operator exported,
// while a keyring backend sees the lowercase name the rest of foo uses.
// Lowercasing is therefore the whole mapping, and doing it in one named
// function is what keeps the two directions from drifting.
func SecretName(envVar string) string {
	return strings.ToLower(strings.TrimSpace(envVar))
}

// ProviderAuth describes one provider's credential requirement and
// whether this machine satisfies it.
type ProviderAuth struct {
	// Provider is the provider id ("openai", "groq").
	Provider string
	// EnvVars are the upstream env var names the provider accepts, in
	// the order aim lists them. Empty means the provider needs no
	// credential at all.
	EnvVars []string
	// SecretKey is the secret-store name that satisfied the
	// requirement, or — when nothing satisfied it — the first
	// alternative, so an error message can name something concrete.
	// Empty when no credential is required.
	SecretKey string
	// Required reports whether any credential is needed. False for a
	// local runtime such as ollama.
	Required bool
	// Configured reports whether a required credential was found. It
	// is false when Required is false: "no credential was found"
	// is not a claim worth making about a provider that wants none.
	Configured bool
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

// AuthIndex answers the credential question for any provider id.
//
// It is built once per command run rather than queried per row: a
// listing touches thousands of rows across hundreds of providers, and a
// keyring-backed secret store charges real latency per lookup. The index
// resolves each distinct provider exactly once.
type AuthIndex struct {
	byProvider map[string]ProviderAuth
}

// NewAuthIndex resolves every catalog provider's credential state.
//
// reg nil means foo's shared aim registry — the same one the listing
// reads, so the providers described here are exactly the providers the
// rows came from. lookup nil means "no secret store": every required
// credential reports missing, which is the correct reading of "foo
// cannot consult a store" and keeps the index usable in tests.
//
// A registry read failure is returned rather than swallowed. Guessing
// that nothing is configured would hide every model in the default view
// and blame the user's missing keys for foo's failed catalog read.
func NewAuthIndex(ctx context.Context, reg *aim.Registry, lookup SecretLookup) (*AuthIndex, error) {
	if reg == nil {
		reg = ensureRegistry()
	}
	providers, err := reg.Providers(ctx)
	if err != nil {
		return nil, err
	}
	idx := &AuthIndex{byProvider: make(map[string]ProviderAuth, len(providers))}
	for _, p := range providers {
		idx.byProvider[p.ID] = resolveAuth(ctx, p.ID, p.Env, lookup)
	}
	return idx, nil
}

// NewAuthIndexFrom builds an index from an explicit provider→env-vars
// map, bypassing aim. It is the seam tests use to pin the predicate
// without a models.dev fetch, and the constructor a future non-aim
// provider source would call.
func NewAuthIndexFrom(ctx context.Context, envByProvider map[string][]string, lookup SecretLookup) *AuthIndex {
	idx := &AuthIndex{byProvider: make(map[string]ProviderAuth, len(envByProvider))}
	for id, env := range envByProvider {
		idx.byProvider[id] = resolveAuth(ctx, id, env, lookup)
	}
	return idx
}

// resolveAuth decides one provider's state. A provider is satisfied when
// *any one* of its env vars resolves: aim lists alternatives, not a
// conjunction — google's three names are three spellings of one key, and
// requiring all three would report every google user as unconfigured.
func resolveAuth(ctx context.Context, provider string, envVars []string, lookup SecretLookup) ProviderAuth {
	a := ProviderAuth{Provider: provider, EnvVars: envVars, Required: len(envVars) > 0}
	if !a.Required {
		return a
	}
	// Name the first alternative up front, so a "missing" verdict can
	// tell the user which variable to set even though none resolved.
	a.SecretKey = SecretName(envVars[0])
	if lookup == nil {
		return a
	}
	for _, envVar := range envVars {
		key := SecretName(envVar)
		// A lookup error is treated as "not this one" rather than
		// aborted on: one unreadable backend entry must not make the
		// other two alternatives unaskable, and the listing has to
		// render either way.
		if _, ok, err := lookup(ctx, key); err == nil && ok {
			a.SecretKey = key
			a.Configured = true
			return a
		}
	}
	return a
}

// Lookup returns the provider's credential state.
//
// A provider the index has never heard of — one absent from the catalog,
// such as a local ollama or llama.cpp that publishes no models.dev
// entry — reports no requirement, and therefore Satisfied. That is the
// deliberate reading: foo has no evidence a key is needed, and hiding a
// locally served model behind a credential nobody asked for is the worse
// error of the two.
func (a *AuthIndex) Lookup(provider string) ProviderAuth {
	if a == nil {
		return ProviderAuth{Provider: provider}
	}
	if got, ok := a.byProvider[provider]; ok {
		return got
	}
	return ProviderAuth{Provider: provider}
}

// Satisfied is the row-level predicate: can foo authenticate to this
// provider right now.
func (a *AuthIndex) Satisfied(provider string) bool {
	return a.Lookup(provider).Satisfied()
}

// schemeProviderAliases maps a kit scheme name onto the catalog provider
// id that carries its credential requirement, for the schemes where the
// two spell the same provider differently.
//
// Without an entry a scheme finds no catalog record, which reads as "no
// declared requirement" and reports "available" — the exact wrong answer
// the hand-maintained switch used to give, re-introduced for a handful
// of providers instead of two hundred. Each mapping below is a scheme
// foo links an adapter for whose catalog id is spelled otherwise: kit
// says "gemini", "fireworks", "together"; models.dev says "google",
// "fireworks-ai", "togetherai".
//
// Schemes genuinely absent from the catalog need no entry and must not
// get one. "routellm" is a local router with no upstream provider
// record, and "available" is the truthful answer for it.
// [TestAuthIndex_SchemeSpellingsExistInCatalog] is what keeps the two
// cases apart as kit links new adapters.
var schemeProviderAliases = map[string]string{
	"gemini":    "google",
	"fireworks": "fireworks-ai",
	"together":  "togetherai",
}

// LookupScheme answers the credential question for a kit *scheme* name
// rather than a catalog provider id, resolving the spellings where the
// two disagree.
//
// It is a separate method from [Lookup] because the two callers hold
// different things. A listing holds catalog rows, whose Provider is
// already the catalog's own id, so translating would be a no-op at best
// and a corruption at worst. `foo provider show` holds whatever the user
// typed, which is a kit scheme.
//
// The result is reported under the name the caller passed. The alias is
// an implementation detail of where the requirement was found, and
// echoing "google" back at someone who asked about "gemini" would look
// like a typo in foo rather than an answer.
func (a *AuthIndex) LookupScheme(scheme string) ProviderAuth {
	provider := scheme
	if alias, ok := schemeProviderAliases[scheme]; ok {
		provider = alias
	}
	got := a.Lookup(provider)
	got.Provider = scheme
	return got
}

// ConfiguredProviders lists, sorted, the providers whose credential
// requirement is met by a stored secret. It backs the "nothing is
// reachable" guidance, which needs to distinguish "no keys at all" from
// "keys, but none for the providers you filtered to".
func (a *AuthIndex) ConfiguredProviders() []string {
	if a == nil {
		return nil
	}
	var out []string
	for id, auth := range a.byProvider {
		if auth.Required && auth.Configured {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}
