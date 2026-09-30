package llm

import (
	"os"

	"gopkg.in/yaml.v3"
	kitllm "hop.top/kit/go/ai/llm"
)

// baseURLEnv is kit's universal endpoint override, read by LoadConfig.
const baseURLEnv = "LLM_BASE_URL"

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
//  3. llm.yaml providers.<scheme>.base_url
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

// configuredBaseURL returns tiers 2-3 of applyConfiguredBaseURL for a
// URI with no base_url param and no host.
//
// kit's LoadConfig is the implementation of record for both tiers, and
// is what `foo model list` reads (ResolveConfiguredEndpoint), so it
// answers whenever its answer is the right one: always when LLM_BASE_URL
// applies, and when LLM_BASE_URL is unset (LoadConfig then returns the
// file value). Only an other-scheme URI under a set LLM_BASE_URL needs
// the file tier alone, which LoadConfig cannot give — it layers the env
// over the file unconditionally.
func configuredBaseURL(uri, scheme string, envApplies bool) string {
	if !envApplies && os.Getenv(baseURLEnv) != "" {
		return fileBaseURL(scheme)
	}
	cfg, err := kitllm.LoadConfig(uri)
	if err != nil {
		return ""
	}
	return cfg.Provider.BaseURL
}

// llmFile mirrors the shape kit's LoadConfig decodes llm.yaml into, so
// a file kit discards as a whole (bad YAML, a mistyped block) is
// discarded here too and the two reads never disagree.
type llmFile struct {
	Default   string `yaml:"default"`
	Providers map[string]struct {
		APIKey  string         `yaml:"api_key"`
		BaseURL string         `yaml:"base_url"`
		Model   string         `yaml:"model"`
		Extra   map[string]any `yaml:",inline"`
	} `yaml:"providers"`
	Fallback []string `yaml:"fallback"`
	Pool     []struct {
		Alias   string  `yaml:"alias,omitempty"`
		Scheme  string  `yaml:"scheme"`
		Model   string  `yaml:"model"`
		Enabled *bool   `yaml:"enabled,omitempty"`
		Weight  float64 `yaml:"weight,omitempty"`
	} `yaml:"pool,omitempty"`
}

// fileBaseURL returns llm.yaml providers.<scheme>.base_url, ignoring
// LLM_BASE_URL. Like kit, a missing, unreadable or undecodable file
// means "nothing configured".
func fileBaseURL(scheme string) string {
	path, err := SeedPath()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var f llmFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return ""
	}
	return f.Providers[scheme].BaseURL
}

// schemeOf returns uri's scheme, or "" when kit cannot parse it.
func schemeOf(uri string) string {
	parsed, err := kitllm.ParseURI(uri)
	if err != nil {
		return ""
	}
	return parsed.Scheme
}
