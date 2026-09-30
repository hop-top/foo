package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"

	"hop.top/aim"
	kitllm "hop.top/kit/go/ai/llm"
	_ "hop.top/kit/go/ai/llm/anthropic"
	llmerrors "hop.top/kit/go/ai/llm/errors"
	_ "hop.top/kit/go/ai/llm/google"
	_ "hop.top/kit/go/ai/llm/ollama"
	_ "hop.top/kit/go/ai/llm/openai"
	_ "hop.top/kit/go/ai/llm/routellm"
	"hop.top/kit/go/console/output"
	"hop.top/kit/go/storage/secret"
	_ "hop.top/kit/go/storage/secret/env"
)

type Client struct {
	client *kitllm.Client

	// maxTokens caps completion length on every request this client
	// issues. Zero means unset: the field is omitted and the provider
	// default applies, matching kit's Request.MaxTokens semantics.
	// Some OpenAI-compatible servers reject requests that omit it.
	maxTokens int

	// guessedModel holds the bare model id whose provider foo *guessed*
	// — the id matched no known prefix, so schemeForModel fell to its
	// openai-compatible default arm. Empty on every other path
	// (recognised prefix, explicit URI, pool pick), which is what makes
	// the guess distinguishable from a deliberate openai request when a
	// request later fails. Consumed only by enrichUnknownModel.
	guessedModel string
}

// ClientOpts threads invocation-time choices through NewClient. Model is
// foo's selected model string (after --model / FOO_MODEL / config layer
// resolution); empty means "let the pool picker decide". Profile and
// Budget are consumed only on the picker path; explicit Model overrides
// both.
//
// Registry is injected to keep NewClient testable: tests pass an
// in-memory fixture registry, production passes nil and gets the
// process-wide aim default.
type ClientOpts struct {
	Model    string
	Profile  kitllm.RequestProfile
	Budget   kitllm.BudgetTier
	Registry *aim.Registry

	// MaxTokens caps completion length. Zero means unset (provider
	// default). Threaded onto every request the client issues.
	MaxTokens int
}

// defaultRegistry is the process-wide aim registry foo lends to the
// picker when ClientOpts.Registry is nil. Constructed lazily so unit
// tests that never pick from a real registry don't pay the network
// cost. Concurrent first-touch is safe via defaultRegistryOnce.
var (
	defaultRegistry     *aim.Registry
	defaultRegistryOnce sync.Once
)

func ensureRegistry() *aim.Registry {
	defaultRegistryOnce.Do(func() {
		defaultRegistry = aim.NewRegistry()
	})
	return defaultRegistry
}

// NewClient resolves a kit LLM client. Three code paths:
//
//  1. opts.Model is non-empty and starts with "router-": passthrough
//     to kit's routellm adapter (the model name itself encodes the
//     router decision). Picker is bypassed.
//  2. opts.Model is non-empty (any other shape): explicit pin. Scheme
//     is detected from the model prefix; key precheck runs against
//     that scheme. Picker is bypassed.
//  3. opts.Model is empty: pool path. LoadPool reads the user's
//     llm.yaml pool block, PickProviderInPool selects a model under
//     opts.Budget filtered by opts.Profile, scheme is derived from
//     the pick's Provider, and the URI is assembled from the picked
//     model. Empty pool → fall back to behavior #2 with the package
//     default model and a one-line slog warning.
//
// Fallback wiring runs on every path: kit's LoadConfig reads
// `~/.config/hop/llm.yaml` `fallback:` plus the LLM_FALLBACK env var
// and each entry is added via WithFallback once its scheme's key is
// injected (fallbackURIs). Errors from LoadConfig are tolerated —
// missing/invalid config must not block a working call.
func NewClient(ctx context.Context, opts ClientOpts) (*Client, error) {
	model := opts.Model

	// Path 3: empty model → consult the pool picker. On any soft
	// failure (no pool block, picker failure) we degrade to the
	// explicit path with the foo-default model.
	if model == "" {
		pool, _ := kitllm.LoadPool()
		if len(pool) == 0 {
			// No pool block authored. Surface one slog warning so
			// operators discover the surface; do not error — the seed
			// path writes a default config on first run.
			slog.Warn(
				"llm.pool.empty: no pool block in ~/.config/hop/llm.yaml; falling back to single default model",
				slog.String("hint", "edit ~/.config/hop/llm.yaml or run foo to seed a default"),
			)
		} else {
			// Help operators understand why the picker has fewer
			// candidates than the file looks to declare. Kit's
			// PickProviderInPool silently elides disabled entries
			// (Stage="pool_disabled" in the trace); we surface the
			// count once here so `--picker-debug` is not the only way
			// to discover it.
			enabled := 0
			for _, e := range pool {
				if e.Enabled {
					enabled++
				}
			}
			if enabled < len(pool) {
				slog.Debug(
					"llm.pool.entries: disabled entries elided from picker",
					slog.Int("total", len(pool)),
					slog.Int("enabled", enabled),
					slog.Int("disabled", len(pool)-enabled),
				)
			}
			reg := opts.Registry
			if reg == nil {
				reg = ensureRegistry()
			}
			picked, pickErr := kitllm.PickProviderInPool(ctx, reg, opts.Profile, opts.Budget, pool)
			if pickErr != nil {
				// Picker exhausted the pool. Surface a structured
				// error so operators distinguish "no pool" from
				// "pool matched nothing".
				return nil, fmt.Errorf("pool picker found no qualifying model under budget %q: %w", opts.Budget.String(), pickErr)
			}
			// picked.Provider is the URI scheme; picked.ID is the
			// model name kit's adapter expects. Bypass the
			// prefix-based scheme guess (schemeForModel) by going
			// through envVarForScheme which trusts the registry value.
			scheme := picked.Provider
			envVar := envVarForScheme(scheme)
			return buildClient(scheme, picked.ID, envVar, opts.MaxTokens)
		}
	}

	// Paths 1 and 2: explicit model.
	return newClientFromModel(model, opts.MaxTokens)
}

// modelIsURI reports whether a --model value is already a
// scheme-qualified URI ("openai://gpt-4o") rather than a bare model id
// ("gpt-4o").
//
// The test is "://" and not ":" on purpose: routellm pins carry a
// threshold separator ("router-mf:0.5") that is not a scheme delimiter.
//
// Only the portion before the first "?" is examined. A bare model id may
// carry a query string whose value is itself a URL
// ("qwen3.6-colibri?base_url=http://host/v1"); the "://" in that value
// belongs to the param, not to the model.
func modelIsURI(model string) bool {
	head, _, _ := strings.Cut(model, "?")
	return strings.Contains(head, "://")
}

// newClientFromModel handles the explicit-model paths (1 and 2).
//
// A URI-shaped value is never re-wrapped the way the bare-id path is:
// that yields "openai://openai://<model>?api_key=...", which sends the
// whole URI as the model name and drops the api_key — the provider then
// 404s and kit maps that to the misleading "model not available". Kit's
// Resolve already reads api_key and base_url out of the URI's query
// params, so the caller keeps full control of both; foo only appends
// the scheme's key when the URI names none (see injectURIKey).
func newClientFromModel(model string, maxTokens int) (*Client, error) {
	uri, guessed, err := resolveURIForModel(model)
	if err != nil {
		return nil, err
	}
	client, err := buildClientFromURI(uri, maxTokens)
	if err != nil {
		return nil, err
	}
	if guessed {
		client.guessedModel = model
	}
	return client, nil
}

// buildClient is the common URI-build + fallback-wiring step shared by
// every NewClient code path. Centralizes the API-key precheck so any
// future scheme picked up by the pool picker honors the same error
// shape.
func buildClient(scheme, model, envVar string, maxTokens int) (*Client, error) {
	uri, err := buildURI(scheme, model, envVar)
	if err != nil {
		return nil, err
	}
	return buildClientFromURI(uri, maxTokens)
}

// buildURI assembles the provider URI for a bare model id, running the
// API-key precheck for keyed schemes.
//
// A bare model id may already carry query params
// ("qwen3.6-colibri?base_url=..."), so the api_key separator is "&" in
// that case. Always emitting "?" produced a second question mark, which
// kit's parser folds into the preceding value.
func buildURI(scheme, model, envVar string) (string, error) {
	if envVar == "" {
		return fmt.Sprintf("%s://%s", scheme, model), nil
	}
	key := schemeKey(envVar)
	if key == "" {
		return "", missingKeyError(envVar, model, scheme)
	}
	return fmt.Sprintf("%s://%s%sapi_key=%s", scheme, model, querySep(model), key), nil
}

// injectURIKey appends the scheme's API key to a URI-form --model value
// that names none.
//
// Kit's Resolve takes the key from the URI's api_key param and nowhere
// else, so a URI passed through bare reached the provider with no
// Authorization header ("openrouter://..." → 401 "Missing
// Authentication header"). The key comes from the same lookup and the
// same precheck as a bare id's, so the two spellings of one model
// behave alike.
//
// Left untouched: a URI that already carries api_key (the caller's
// choice outranks the environment), a local scheme (no credential), and
// a scheme foo has no entry for or a URI kit cannot parse — kit reports
// those itself, more accurately than a missing-key error would.
func injectURIKey(uri string) (string, error) {
	keyed, missing := keyURI(uri)
	if missing != nil {
		return "", missingKeyError(missing.envVar, missing.model, missing.scheme)
	}
	return keyed, nil
}

// missingKey describes a keyed URI whose API key could not be found.
type missingKey struct {
	envVar, model, scheme string
}

// keyURI is injectURIKey's resolution without the error policy: it
// returns uri with the scheme's key appended, or the missing key's
// description. The primary model turns a miss into a precheck failure;
// a fallback entry turns it into a dropped entry (fallbackURIs).
func keyURI(uri string) (string, *missingKey) {
	parsed, err := kitllm.ParseURI(uri)
	if err != nil {
		return uri, nil
	}
	if _, explicit := parsed.Params["api_key"]; explicit {
		return uri, nil
	}
	envVar := envVarForScheme(parsed.Scheme)
	if envVar == "" {
		return uri, nil
	}
	key := schemeKey(envVar)
	if key == "" {
		return "", &missingKey{envVar: envVar, model: parsed.Model, scheme: parsed.Scheme}
	}
	return uri + querySep(uri) + "api_key=" + key, nil
}

// missingKeyError is the precheck failure every path shares, so a bare
// id, a URI and a pool pick all name the variable to set the same way.
func missingKeyError(envVar, model, scheme string) error {
	return output.UnauthorizedError(fmt.Sprintf("missing %s for model %q (provider %s); export %s=... and retry, or switch models with `foo model default <model>`", envVar, model, scheme, envVar))
}

// querySep returns the separator that appends a param to s: "?" when s
// has no query string yet, "&" when it does.
func querySep(s string) string {
	if strings.Contains(s, "?") {
		return "&"
	}
	return "?"
}

// applyConfiguredBaseURL folds the base_url resolved by kit's LoadConfig
// (llm.yaml `providers.<scheme>.base_url`, overridden by LLM_BASE_URL)
// into the URI as a param.
//
// foo builds a URI by hand and hands it to kitllm.Resolve, which reads
// the URI alone — so neither documented lever reached the provider and
// requests went to the provider's public endpoint regardless. LoadConfig
// is the function that applies both layers, so it resolves them here.
//
// A base_url already present on the URI is left alone: it came from the
// caller's --model value and outranks both file and env.
func applyConfiguredBaseURL(uri string) string {
	parsed, err := kitllm.ParseURI(uri)
	if err != nil {
		return uri
	}
	if _, explicit := parsed.Params["base_url"]; explicit {
		return uri
	}
	cfg, err := kitllm.LoadConfig(uri)
	if err != nil || cfg.Provider.BaseURL == "" {
		return uri
	}
	// Host-form URIs ("scheme://host:port/model") already encode an
	// endpoint; LoadConfig echoes it back as BaseURL, so appending it
	// as a param would be redundant.
	if parsed.Host != "" {
		return uri
	}
	return uri + querySep(uri) + "base_url=" + cfg.Provider.BaseURL
}

// resolveURIForModel returns the provider URI a given --model value
// resolves to, without constructing a client. It mirrors
// newClientFromModel's branching exactly so tests can assert on the URI
// that reaches kit — the corruption this guards against produces a
// malformed URI that Resolve accepts without error, so the URI itself is
// the only observable short of the wire.
//
// guessed forwards schemeForModel's report that the scheme was assumed
// rather than matched. A URI-shaped value is never a guess: the caller
// spelled the scheme out.
func resolveURIForModel(model string) (uri string, guessed bool, err error) {
	if modelIsURI(model) {
		uri, err := injectURIKey(model)
		return uri, false, err
	}
	scheme, envVar, guessed := schemeForModel(model)
	if scheme == "routellm" {
		// router- prefix: strip the marker so the URI ends up as
		// routellm://<router>:<threshold>. The pool picker is
		// bypassed because routellm IS a routing mechanism, just
		// answering a different question: pool routing scores
		// (price, capability, context window) **pre-flight** using
		// static metadata; routellm scores **per-request** on
		// prompt content via a RouteLLM server. The two compose —
		// a routellm pin still inherits the kit fallback chain.
		// Full comparison table:
		// docs/how-to/route-across-models.md#pool-routing-vs-router-x
		model = strings.TrimPrefix(model, "router-")
	}
	built, err := buildURI(scheme, model, envVar)
	if err != nil {
		return "", false, err
	}
	return applyConfiguredBaseURL(built), guessed, nil
}

// resolvedURIForModel is the URI-only view of resolveURIForModel, kept
// for call sites (tests, provenance) that assert on the URI and have no
// use for the guess flag.
func resolvedURIForModel(model string) (string, error) {
	uri, _, err := resolveURIForModel(model)
	return uri, err
}

// buildClientFromURI resolves a fully-formed provider URI and wires the
// fallback chain. Shared by the bare-id path (which assembles the URI)
// and the URI passthrough path (which received one from --model).
func buildClientFromURI(uri string, maxTokens int) (*Client, error) {
	p, err := kitllm.Resolve(uri)
	if err != nil {
		return nil, err
	}

	// Per-URI Resolve errors skip just that one entry so one bad
	// fallback can't disable the rest.
	var clientOpts []kitllm.Option
	for _, fbURI := range fallbackURIs(uri) {
		fb, fbErr := kitllm.Resolve(fbURI)
		if fbErr != nil {
			continue
		}
		clientOpts = append(clientOpts, kitllm.WithFallback(fb))
	}

	return &Client{
		client:    kitllm.NewClient(p, clientOpts...),
		maxTokens: maxTokens,
	}, nil
}

// fallbackURIs returns the fallback chain for a client whose primary is
// uri, each entry carrying its scheme's API key. kit's LoadConfig reads
// llm.yaml `fallback:` and LLM_FALLBACK (env wins); its errors are
// tolerated so a missing config file never blocks a working
// single-provider call.
//
// Kit's Resolve takes a key from the URI and nowhere else, so an entry
// passed through bare reached its provider unauthenticated. Each entry
// gets the same key resolution as a URI-form --model (keyURI).
//
// An entry whose key cannot be found is dropped, not fatal: the primary
// may be healthy, and failing the run over a backup that is never
// needed would be worse than running without it. The drop is announced
// once per process on stderr, naming the variable to set.
func fallbackURIs(uri string) []string {
	cfg, err := kitllm.LoadConfig(uri)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(cfg.Fallbacks))
	for _, fb := range cfg.Fallbacks {
		keyed, missing := keyURI(fb)
		if missing != nil {
			warnDroppedFallback(missing)
			continue
		}
		out = append(out, keyed)
	}
	return out
}

// droppedFallbacks remembers which fallback drops were already
// announced, so a process that builds more than one client warns once.
var (
	droppedFallbacksMu sync.Mutex
	droppedFallbacks   = map[string]struct{}{}
)

func warnDroppedFallback(m *missingKey) {
	id := m.scheme + "://" + m.model
	droppedFallbacksMu.Lock()
	_, seen := droppedFallbacks[id]
	droppedFallbacks[id] = struct{}{}
	droppedFallbacksMu.Unlock()
	if seen {
		return
	}
	slog.Warn(
		"llm.fallback.dropped: fallback has no API key; skipping it",
		slog.String("fallback", id),
		slog.String("missing", m.envVar),
		slog.String("hint", "export "+m.envVar+"=... to enable it, or remove it from LLM_FALLBACK / llm.yaml fallback:"),
	)
}

// schemeKey resolves the API key for a keyed scheme whose URI names no
// ?api_key= (an explicit param outranks everything and is never
// replaced). Precedence, highest first:
//
//  1. the scheme's own key: secret store, then its env var (lookupAPIKey)
//  2. LLM_API_KEY, kit's universal key
//
// This is kit's own order for the two: SecretFor tries the provider's
// key before LLM_API_KEY, and the google adapter reads GEMINI_API_KEY
// before LLM_API_KEY. LLM_API_KEY is scheme-agnostic by design, so it
// is sent to whichever keyed provider lacks its own key — fallbacks
// included, as kit's LoadConfig applies it to every URI. That is the
// user's explicit choice; per-scheme variables avoid it.
//
// Kit's Resolve reads the key from the URI alone, so the key returned
// here is exactly the one kit sends: the precheck and the request
// cannot disagree.
func schemeKey(envVar string) string {
	if key := lookupAPIKey(envVar); key != "" {
		return key
	}
	return os.Getenv(kitllm.FallbackEnvKey)
}

// lookupAPIKey resolves a provider API key through the kit secret store
// rather than reading the process environment directly. The store is
// opened with the default "env" backend, so the historical behavior is
// preserved: secret key `openai_api_key` maps to env var
// `OPENAI_API_KEY` (the env backend uppercases and swaps `/`→`_`).
// Configuring a different backend (keychain, vault) in foo's config
// transparently redirects the lookup without touching this call site.
//
// envVar is the canonical env-var spelling (e.g. "OPENAI_API_KEY"); it
// is lowercased to form the backend-neutral secret key. A direct
// os.Getenv read is the last-resort fallback so a store-open failure
// never regresses a working env-based setup.
func lookupAPIKey(envVar string) string {
	key := strings.ToLower(envVar)
	store, err := secret.Open(secret.Config{Backend: "env"})
	if err == nil {
		if got, getErr := store.Get(context.Background(), key); getErr == nil {
			return string(got.Value)
		}
	}
	return os.Getenv(envVar)
}

// schemeForModel maps a model id to its kit URI scheme and the env var
// that holds the provider's key. Empty envVar = local provider (no
// precheck). The router- prefix returns "routellm" so the caller knows
// to strip the marker before building the URI.
//
// guessed reports that no prefix matched and the openai-compatible
// default arm answered. The scheme is the same either way; the flag
// only records *why*, so a later "model not available" can say foo
// assumed the provider instead of implying the user picked it.
func schemeForModel(model string) (scheme, envVar string, guessed bool) {
	switch {
	case strings.HasPrefix(model, "gpt-") || strings.HasPrefix(model, "o1") || strings.HasPrefix(model, "o3"):
		return "openai", "OPENAI_API_KEY", false
	case strings.HasPrefix(model, "claude-"):
		return "anthropic", "ANTHROPIC_API_KEY", false
	case strings.HasPrefix(model, "gemini-"):
		return "google", "GOOGLE_API_KEY", false
	case strings.HasPrefix(model, "llama") || strings.HasPrefix(model, "mistral") || strings.HasPrefix(model, "deepseek-r1"):
		return "ollama", "", false
	case strings.HasPrefix(model, "router-"):
		return "routellm", "", false
	default:
		// Unknown prefix — assume an OpenAI-compatible endpoint on the
		// openai scheme, so OPENAI_API_KEY is the key it takes. Hosted
		// gateways (openrouter, groq, ...) are reached by naming their
		// scheme in a URI, which uses that scheme's own key.
		return "openai", "OPENAI_API_KEY", true
	}
}

// schemeKeyEnv maps every kit scheme foo links an adapter for to the env
// var holding that provider's API key. "" marks a local runtime that
// takes no credential. Names are each provider's documented variable
// (the same ones the aim catalog lists). lookupAPIKey lowercases them
// into the secret-store key (openrouter_api_key), so a configured
// secret backend and a plain `export OPENROUTER_API_KEY=` both resolve.
//
// The OpenAI-compatible gateways get their own variables. They used to
// fall through to OPENAI_API_KEY, which made an OpenRouter user store an
// OpenRouter key where a real OpenAI key belongs — and sent a real
// OpenAI key to OpenRouter when both were meant to coexist.
//
// google and gemini are one adapter under two names; foo has always
// read GOOGLE_API_KEY for it. lmstudio is a local server and, like
// ollama, is not prechecked.
//
// [TestEnvVarForScheme_CoversEveryKitScheme] fails when kit registers a
// scheme missing here, so a new adapter gets a deliberate entry rather
// than a silent default.
var schemeKeyEnv = map[string]string{
	"openai":     "OPENAI_API_KEY",
	"anthropic":  "ANTHROPIC_API_KEY",
	"google":     "GOOGLE_API_KEY",
	"gemini":     "GOOGLE_API_KEY",
	"openrouter": "OPENROUTER_API_KEY",
	"groq":       "GROQ_API_KEY",
	"xai":        "XAI_API_KEY",
	"together":   "TOGETHER_API_KEY",
	"fireworks":  "FIREWORKS_API_KEY",
	"deepseek":   "DEEPSEEK_API_KEY",
	"mistral":    "MISTRAL_API_KEY",
	"lmstudio":   "",
	"ollama":     "",
	"routellm":   "",
}

// envVarForScheme returns the env var holding the API key for a scheme
// the caller named — a pool pick, or a URI-form --model. "" means no
// precheck.
//
// A scheme absent from schemeKeyEnv gets "", not OPENAI_API_KEY: foo
// cannot know that provider's credential, and lending it the OpenAI key
// would send that key to a host that is not OpenAI. Such a scheme is one
// kit does not register either, so Resolve rejects it with a
// provider-not-found error that names the real problem.
//
// Only the *guessed* bare-id arm of schemeForModel uses OPENAI_API_KEY
// for an unrecognised id, and that is correct there: the request goes
// to the openai scheme.
func envVarForScheme(scheme string) string {
	return schemeKeyEnv[scheme]
}

// PickFromPool exposes the pool picker behind foo's package boundary
// for tests that want to assert on the picked model without standing
// up an entire NewClient (which also resolves providers and wires
// fallbacks). Returns the picked (scheme, model) on success.
func PickFromPool(ctx context.Context, reg *aim.Registry, profile kitllm.RequestProfile, budget kitllm.BudgetTier, pool []kitllm.PoolEntry) (scheme, model string, err error) {
	picked, err := kitllm.PickProviderInPool(ctx, reg, profile, budget, pool)
	if err != nil {
		return "", "", err
	}
	return picked.Provider, picked.ID, nil
}

// ErrNoPoolMatch is foo's sentinel for "the picker rejected every pool
// entry". Wraps kit's NoMatchError so errors.Is / errors.As keep
// working across the boundary.
var ErrNoPoolMatch = errors.New("foo: no pool entry matches request")

// enrichUnknownModel appends an actionable hint when a request fails
// with kit's "model not available" *and* the provider foo asked was a
// guess — i.e. the model id matched no known prefix and schemeForModel
// fell to its openai-compatible default arm.
//
// Without the hint the failure names openai, which is the one thing the
// user never said: a RouteLLM tier ("private", "coding") reads as
// "OpenAI is broken" rather than "the request never reached your
// router". The hint states the assumption foo made and the two ways to
// correct it.
//
// Deliberately narrow. An explicit `-m gpt-nonexistent`, a full
// `openai://...` URI and a pool pick all leave guessedModel empty, so
// they keep today's message; a wrong guess about a real openai id is
// the user's own guess, not foo's. Any other error class is returned
// untouched, and the original error is wrapped with %w so the error
// type and its exit-code mapping are preserved.
func (c *Client) enrichUnknownModel(err error) error {
	if err == nil || c.guessedModel == "" {
		return err
	}
	var modelErr *llmerrors.ErrModel
	if !errors.As(err, &modelErr) {
		return err
	}
	return fmt.Errorf(
		"%w; %q matched no known model prefix, so foo assumed an OpenAI-compatible provider and never asked anything else. "+
			"If %[2]q is a RouteLLM tier, name the scheme: -m 'routellm://%[2]s'. "+
			"If it lives on another endpoint, point foo at it with ?base_url=, LLM_BASE_URL, or providers.<scheme>.base_url (docs/how-to/use-a-local-endpoint.md). "+
			"`foo model list` shows the ids foo can reach",
		err, c.guessedModel)
}

func (c *Client) Prompt(ctx context.Context, prompt string) (string, error) {
	resp, err := c.client.Complete(ctx, kitllm.Request{
		Messages: []kitllm.Message{
			{Role: "user", Content: prompt},
		},
		MaxTokens: c.maxTokens,
	})
	if err != nil {
		return "", offlineRefusal(c.enrichUnknownModel(err))
	}
	return resp.Content, nil
}

// CallWithTools sends messages with tool definitions to the LLM and
// returns the response which may contain tool calls. Requires the
// underlying provider to implement kit/llm.ToolCaller.
func (c *Client) CallWithTools(
	ctx context.Context,
	messages []kitllm.Message,
	tools []kitllm.ToolDef,
) (kitllm.ToolResponse, error) {
	resp, err := c.client.CallWithTools(ctx, kitllm.Request{
		Messages:  messages,
		MaxTokens: c.maxTokens,
	}, tools)
	return resp, offlineRefusal(c.enrichUnknownModel(err))
}

// PromptStream streams LLM response tokens to w. Falls back to
// non-streaming Prompt if the provider doesn't support streaming.
func (c *Client) PromptStream(ctx context.Context, w io.Writer, prompt string) error {
	req := kitllm.Request{
		Messages: []kitllm.Message{
			{Role: "user", Content: prompt},
		},
		MaxTokens: c.maxTokens,
	}

	iter, err := c.client.Stream(ctx, req)
	if err != nil {
		// Fallback: provider may not support streaming. Prompt already
		// enriches, so no second pass here.
		resp, promptErr := c.Prompt(ctx, prompt)
		if promptErr != nil {
			return promptErr
		}
		_, writeErr := fmt.Fprint(w, resp)
		return writeErr
	}
	defer iter.Close()

	for {
		tok, err := iter.Next()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return offlineRefusal(c.enrichUnknownModel(err))
		}
		if _, writeErr := fmt.Fprint(w, tok.Content); writeErr != nil {
			return writeErr
		}
		if tok.Done {
			return nil
		}
	}
}
