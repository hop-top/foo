package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	kitllm "hop.top/kit/go/ai/llm"
	_ "hop.top/kit/go/ai/llm/anthropic"
	_ "hop.top/kit/go/ai/llm/google"
	_ "hop.top/kit/go/ai/llm/ollama"
	_ "hop.top/kit/go/ai/llm/openai"
	_ "hop.top/kit/go/ai/llm/routellm"
	"hop.top/kit/go/console/output"
	"hop.top/kit/go/core/xdg"
)

// youtubeModelDefault is the model used when neither FOO_YOUTUBE_MODEL,
// FOO_MODEL nor the host config file names one. It matches the host's
// own built-in default so an unconfigured machine answers with the same
// model whichever entry point the user reaches for.
const youtubeModelDefault = "claude-3-5-sonnet-latest"

// exitUnauthorized is the §8.1 slot for a missing credential. A prompt
// run that cannot authenticate is not a usage error (the invocation was
// well-formed) and not a fetch error (nothing was fetched).
const exitUnauthorized = 4

func unauthorizedErrorf(format string, a ...any) *exitError {
	return &exitError{cli: &output.Error{
		Code:     output.CodeUnauthorized,
		Message:  fmt.Sprintf(format, a...),
		ExitCode: exitUnauthorized,
	}}
}

// resolvePrompt picks the question to ask about the video.
//
// Precedence mirrors the cache env namespace (resolveCachePath /
// resolveCacheTTL): the positional argument the user typed outranks
// everything, then this extension's FOO_YOUTUBE_PROMPT, then the
// host-level FOO_PROMPT it inherits.
//
// There is deliberately NO built-in default. A configured default is a
// question the user chose; a built-in one would be a question foo
// invented, and answering an unasked question is worse than answering
// none. An empty result means "no prompt" and the caller emits markdown
// — which is also what keeps `foo-youtube <url> | foo 'summarize'`
// working unchanged.
func resolvePrompt(arg string) string {
	if p := strings.TrimSpace(arg); p != "" {
		return p
	}
	for _, env := range []string{"FOO_YOUTUBE_PROMPT", "FOO_PROMPT"} {
		if p := strings.TrimSpace(os.Getenv(env)); p != "" {
			return p
		}
	}
	return ""
}

// resolveModel picks the model that answers the prompt, with the same
// extension-overrides-host namespace the cache settings use:
// FOO_YOUTUBE_MODEL → FOO_MODEL → the host config file's `model:` key →
// youtubeModelDefault.
//
// The config file is read rather than imported: this sidecar imports
// zero foo internal packages, so it parses the one key it needs out of
// ~/.config/foo/config.yaml itself. A missing or unreadable file is not
// an error — it just falls through to the built-in default.
func resolveModel() string {
	for _, env := range []string{"FOO_YOUTUBE_MODEL", "FOO_MODEL"} {
		if m := strings.TrimSpace(os.Getenv(env)); m != "" {
			return m
		}
	}
	if m := modelFromHostConfig(); m != "" {
		return m
	}
	return youtubeModelDefault
}

// modelFromHostConfig reads the `model:` key out of foo's user config
// file. It is a deliberately minimal line scan rather than a YAML
// decode: only one scalar top-level key is wanted, and a full decode
// would couple this sidecar to the host's config struct, which is
// exactly the coupling the zero-internal-imports invariant forbids.
// Anything it cannot understand yields "" and the caller falls through.
func modelFromHostConfig() string {
	dir, err := xdg.ConfigDir("foo")
	if err != nil {
		return ""
	}
	raw, err := os.ReadFile(dir + "/config.yaml")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		// Top-level keys only: an indented `model:` belongs to some
		// nested block (a pool entry, a provider) and is not the
		// host's default model.
		if line != strings.TrimLeft(line, " \t") {
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(key) != "model" {
			continue
		}
		value = strings.TrimSpace(value)
		if i := strings.Index(value, " #"); i >= 0 {
			value = strings.TrimSpace(value[:i])
		}
		return strings.Trim(value, `"'`)
	}
	return ""
}

// schemeForModel maps a bare model id to its kit URI scheme.
//
// Guessing a scheme from a bare id is this binary's policy, so it stays
// here; which key that scheme takes is kit's (see modelURI). It mirrors
// the host's own guess, duplicated rather than imported because the
// host's copy lives in a foo internal package and this binary imports
// none — the same structural duplication resolveCachePath already
// carries.
func schemeForModel(model string) string {
	switch {
	case strings.HasPrefix(model, "gpt-"), strings.HasPrefix(model, "o1"), strings.HasPrefix(model, "o3"):
		return "openai"
	case strings.HasPrefix(model, "claude-"):
		return "anthropic"
	case strings.HasPrefix(model, "gemini-"):
		return "google"
	case strings.HasPrefix(model, "llama"), strings.HasPrefix(model, "mistral"), strings.HasPrefix(model, "deepseek-r1"):
		return "ollama"
	case strings.HasPrefix(model, "router-"):
		return "routellm"
	default:
		// Unknown prefix — assume an OpenAI-compatible endpoint on the
		// openai scheme. Hosted gateways (openrouter, groq, ...) are
		// reached by naming their scheme in a URI, which then takes
		// that scheme's own key.
		return "openai"
	}
}

// modelURI turns a model selection into the provider URI kit's Resolve
// consumes, API key included.
//
// A value that already spells a scheme out ("openrouter://openai/gpt-4.1-nano",
// "ollama://llama3") keeps it: re-wrapping it yields
// "openai://openai://..." which sends the whole URI as the model name.
// The "://" test ignores anything after the first "?" because a bare
// model id may carry a base_url param whose value is itself a URL. For
// a bare id the scheme is guessed (schemeForModel). Either form then
// takes the configured endpoint (applyConfiguredBaseURL).
//
// The key, for either form, is kit's llm.ApplyAPIKey: an api_key
// already on the URI, llm.yaml providers.<scheme>.api_key /
// api_key_env, the scheme's own variables (OPENROUTER_API_KEY, ...,
// aliases and aim catalog providers included), then LLM_API_KEY for a
// provider that requires one. A local runtime (ollama, routellm) needs
// none. A required key found nowhere fails here with an actionable
// message and exit 4 rather than as an opaque 401 from the provider;
// any other error passes through unchanged.
//
// The returned URI holds the key: never log or print it.
func modelURI(ctx context.Context, model string) (string, error) {
	uri := model
	if head, _, _ := strings.Cut(model, "?"); !strings.Contains(head, "://") {
		scheme := schemeForModel(model)
		bare := model
		if scheme == "routellm" {
			bare = strings.TrimPrefix(bare, "router-")
		}
		uri = scheme + "://" + bare
	}
	uri = applyConfiguredBaseURL(uri)

	keyed, err := kitllm.ApplyAPIKey(ctx, nil, uri)
	var missing *kitllm.MissingKeyError
	if errors.As(err, &missing) && len(missing.EnvVars) > 0 {
		envVar := missing.EnvVars[0]
		return "", unauthorizedErrorf(
			"missing %s for model %q (provider %s); export %s=... and retry, or pick another model with FOO_YOUTUBE_MODEL",
			envVar, model, missing.Scheme, envVar)
	}
	if err != nil {
		return "", err
	}
	return keyed, nil
}

// querySep returns the separator that appends a param to s.
func querySep(s string) string {
	if strings.Contains(s, "?") {
		return "&"
	}
	return "?"
}

// applyConfiguredBaseURL folds the configured endpoint onto uri as a
// base_url param, for a bare id and a URI-form model alike.
//
// kit's Resolve reads the URI and nothing else, so without this step
// neither documented lever reaches the provider and every request goes
// to the provider's public endpoint. Precedence, highest first — the
// host's own, for its primary model:
//
//  1. ?base_url= already on uri: the caller's own choice, never replaced
//  2. LLM_BASE_URL
//  3. llm.yaml base_url, from the block kit reads for the scheme: its
//     own, else an alias's (gemini reads providers.google)
//  4. nothing: the adapter's default
//
// The host scopes LLM_BASE_URL to its primary model's scheme so a
// fallback on another scheme never borrows it, and reads the file tier
// alone (kit's ProviderSettingsFor) for those. This binary runs one
// model and no fallbacks, so the model is always the primary and kit's
// LoadConfig, which layers LLM_BASE_URL over the same block, answers
// tiers 2-3 as they stand. A host-form URI ("scheme://host:port/model")
// already names its endpoint and is left alone.
func applyConfiguredBaseURL(uri string) string {
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
	cfg, err := kitllm.LoadConfig(uri)
	if err != nil || cfg.Provider.BaseURL == "" {
		return uri
	}
	return uri + querySep(uri) + "base_url=" + cfg.Provider.BaseURL
}

// answerFunc is the seam between the assembled prompt and the provider
// call. Production resolves a kit client and completes; tests swap it
// for a cassette- or httptest-backed client so the outbound request is
// observable. A stubbed ytRunner proves nothing about what foo-youtube
// sends to a model — this is the seam that can.
var answerFunc = answer

// answer resolves the provider for model and completes one turn.
func answer(ctx context.Context, model, prompt string) (string, error) {
	uri, err := modelURI(ctx, model)
	if err != nil {
		return "", err
	}
	provider, err := kitllm.Resolve(uri)
	if err != nil {
		return "", err
	}
	return complete(ctx, kitllm.NewClient(provider), prompt)
}

// complete issues the single completion turn. Split from answer so the
// cassette test drives the exact request-building code production uses
// while supplying its own provider.
func complete(ctx context.Context, client *kitllm.Client, prompt string) (string, error) {
	resp, err := client.Complete(ctx, kitllm.Request{
		Messages: []kitllm.Message{{Role: "user", Content: prompt}},
	})
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

// buildPrompt frames the user's question against the extracted video.
//
// The whole rendered markdown goes in — metadata header included, since
// title, channel and upload date are frequently what the question is
// actually about ("when did they say this?", "who is this?"). Nothing is
// truncated: a transcript trimmed to fit changes the answer without
// saying so, so an oversized video surfaces the provider's own
// context-window error instead.
func buildPrompt(question, document string) string {
	var b strings.Builder
	b.WriteString("Answer the question about the YouTube video below, using only the video's own content.\n\n")
	b.WriteString("<video>\n")
	b.WriteString(strings.TrimRight(document, "\n"))
	b.WriteString("\n</video>\n\n")
	b.WriteString("Question: ")
	b.WriteString(question)
	b.WriteString("\n")
	return b.String()
}
