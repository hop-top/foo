package main

import (
	"context"
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
	"hop.top/kit/go/storage/secret"
	_ "hop.top/kit/go/storage/secret/env"
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

// schemeForModel maps a bare model id to its kit URI scheme and the env
// var holding that provider's key. An empty envVar means a local
// provider with no credential to precheck.
//
// This mirrors the host's own mapping. It is duplicated rather than
// imported because the host's copy lives in a foo internal package and
// this binary imports none — the same structural duplication
// resolveCachePath already carries.
func schemeForModel(model string) (scheme, envVar string) {
	switch {
	case strings.HasPrefix(model, "gpt-"), strings.HasPrefix(model, "o1"), strings.HasPrefix(model, "o3"):
		return "openai", "OPENAI_API_KEY"
	case strings.HasPrefix(model, "claude-"):
		return "anthropic", "ANTHROPIC_API_KEY"
	case strings.HasPrefix(model, "gemini-"):
		return "google", "GOOGLE_API_KEY"
	case strings.HasPrefix(model, "llama"), strings.HasPrefix(model, "mistral"), strings.HasPrefix(model, "deepseek-r1"):
		return "ollama", ""
	case strings.HasPrefix(model, "router-"):
		return "routellm", ""
	default:
		// Unknown prefix — assume an OpenAI-compatible endpoint
		// (openrouter, groq, together, a local vLLM).
		return "openai", "OPENAI_API_KEY"
	}
}

// modelURI turns a model selection into the provider URI kit's Resolve
// consumes.
//
// A value that already spells a scheme out ("openai://gpt-4o",
// "ollama://llama3") is handed through untouched: re-wrapping it yields
// "openai://openai://..." which sends the whole URI as the model name.
// The "://" test ignores anything after the first "?" because a bare
// model id may carry a base_url param whose value is itself a URL.
//
// For a bare id the scheme is derived, the API key is prechecked (a
// missing key must fail here with an actionable message, not as an
// opaque 401 from the provider) and base_url from llm.yaml /
// LLM_BASE_URL is folded in — a caller-supplied base_url on the model
// string outranks both and is left alone.
func modelURI(model string) (string, error) {
	head, _, _ := strings.Cut(model, "?")
	if strings.Contains(head, "://") {
		return model, nil
	}

	scheme, envVar := schemeForModel(model)
	bare := model
	if scheme == "routellm" {
		bare = strings.TrimPrefix(bare, "router-")
	}

	uri := scheme + "://" + bare
	if envVar != "" {
		key := lookupAPIKey(envVar)
		if key == "" {
			return "", unauthorizedErrorf(
				"missing %s for model %q (provider %s); export %s=... and retry, or pick another model with FOO_YOUTUBE_MODEL",
				envVar, model, scheme, envVar)
		}
		uri += querySep(uri) + "api_key=" + key
	}
	return applyConfiguredBaseURL(uri), nil
}

// querySep returns the separator that appends a param to s.
func querySep(s string) string {
	if strings.Contains(s, "?") {
		return "&"
	}
	return "?"
}

// applyConfiguredBaseURL folds the base_url resolved by kit's LoadConfig
// (llm.yaml `providers.<scheme>.base_url`, overridden by LLM_BASE_URL)
// onto the URI.
//
// kit's Resolve reads the URI and nothing else, so without this step
// neither documented lever reaches the provider and every request goes
// to the provider's public endpoint. An explicit base_url already on the
// URI came from the caller and outranks both file and env; a host-form
// URI already encodes its endpoint, so LoadConfig only echoes it back.
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

// lookupAPIKey resolves a provider key through kit's secret store rather
// than reading the environment directly, so a machine configured with a
// keychain or vault backend resolves the same way the host does. The
// default "env" backend preserves the plain behavior:
// `openai_api_key` → OPENAI_API_KEY. A direct os.Getenv is the
// last-resort fallback so a store-open failure never regresses a
// working env setup.
func lookupAPIKey(envVar string) string {
	key := strings.ToLower(envVar)
	if store, err := secret.Open(secret.Config{Backend: "env"}); err == nil {
		if got, getErr := store.Get(context.Background(), key); getErr == nil && len(got.Value) > 0 {
			return string(got.Value)
		}
	}
	return os.Getenv(envVar)
}

// answerFunc is the seam between the assembled prompt and the provider
// call. Production resolves a kit client and completes; tests swap it
// for a cassette- or httptest-backed client so the outbound request is
// observable. A stubbed ytRunner proves nothing about what foo-youtube
// sends to a model — this is the seam that can.
var answerFunc = answer

// answer resolves the provider for model and completes one turn.
func answer(ctx context.Context, model, prompt string) (string, error) {
	uri, err := modelURI(model)
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
