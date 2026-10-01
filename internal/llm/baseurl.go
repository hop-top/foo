package llm

import (
	kitllm "hop.top/kit/go/ai/llm"
)

// applyConfiguredBaseURL folds the configured endpoint into uri as a
// base_url param. Every URI foo sends goes through it — a bare id, a
// URI-form --model, a pool pick and each fallback — so one model
// reaches one endpoint however it was named.
//
// foo builds URIs by hand and hands them to kitllm.Resolve, which reads
// the URI alone; without this fold, neither llm.yaml nor LLM_BASE_URL
// reached the provider. Precedence, highest first:
//
//  1. ?base_url= already on uri: the caller's own choice, never replaced
//  2. LLM_BASE_URL, only when uri's scheme is primaryScheme
//  3. llm.yaml base_url, from the block kit reads for the scheme: its
//     own, else an alias's (gemini reads providers.google,
//     fireworks-ai providers.fireworks)
//  4. nothing: the adapter's default (local runtimes included)
//
// primaryScheme is the scheme of the run's primary model; a primary
// passes its own. LLM_BASE_URL names one server for any scheme. Lent to
// a fallback on another scheme it would send that provider's request,
// and its key, to the primary's server — so such a fallback gets its
// own scheme's file value or nothing.
//
// A host-form URI ("scheme://host:port/model") already names its
// endpoint and is left alone.
func applyConfiguredBaseURL(uri, primaryScheme string) string {
	parsed, err := kitllm.ParseURI(uri)
	if err != nil {
		return uri
	}
	if _, explicit := parsed.Params["base_url"]; explicit {
		return uri
	}
	if parsed.Host != "" {
		return uri
	}
	base := configuredBaseURL(uri, parsed.Scheme, parsed.Scheme == primaryScheme)
	if base == "" {
		return uri
	}
	return uri + querySep(uri) + "base_url=" + base
}

// EndpointBaseURL returns the base URL a call whose only model is uri
// reaches, or "" for the adapter's default. It is applyConfiguredBaseURL
// with uri as its own primary, read back: the same ladder a run's
// primary model takes (?base_url=, LLM_BASE_URL, the llm.yaml block's
// base_url), and for a host-form URI the host, as kit's Resolve reads
// it.
//
// It serves callers that speak to the provider directly rather than
// through a kit client — the embedder — so they reach the server a run
// on the same URI reaches. The value is the API root (the OpenAI
// adapter's "…/v1"); the caller appends its own path.
func EndpointBaseURL(uri string) string {
	parsed, err := kitllm.ParseURI(applyConfiguredBaseURL(uri, schemeOf(uri)))
	if err != nil {
		return ""
	}
	if base, ok := parsed.Params["base_url"]; ok {
		return base
	}
	if parsed.Host != "" {
		return "http://" + parsed.Host
	}
	return ""
}

// configuredBaseURL returns tiers 2-3 of applyConfiguredBaseURL for a
// URI with no base_url param and no host.
//
// Where LLM_BASE_URL applies, kit's LoadConfig answers: it layers the
// variable over the file, and is what `foo model list` reads
// (ResolveConfiguredEndpoint). Elsewhere the file tier stands alone,
// which kit's ProviderSettingsFor gives: the block LoadConfig reads for
// the scheme, an alias's included, without LLM_BASE_URL over it.
func configuredBaseURL(uri, scheme string, envApplies bool) string {
	if !envApplies {
		settings, _ := kitllm.ProviderSettingsFor(scheme)
		return settings.BaseURL
	}
	cfg, err := kitllm.LoadConfig(uri)
	if err != nil {
		return ""
	}
	return cfg.Provider.BaseURL
}

// schemeOf returns uri's scheme, or "" when kit cannot parse it.
func schemeOf(uri string) string {
	parsed, err := kitllm.ParseURI(uri)
	if err != nil {
		return ""
	}
	return parsed.Scheme
}
