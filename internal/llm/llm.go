package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	"hop.top/kit/go/storage/secret"
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

	// Secrets is foo's configured secret store (config `secrets:`).
	// Every key lookup this client makes — the primary's precheck and
	// each fallback entry — hands it to kit, which asks it for the
	// provider's key names (under SecretName) before the environment.
	// Nil means no store: llm.yaml and env vars only.
	Secrets secret.Store
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

// sharedRegistry hands kit foo's registry.
func sharedRegistry(context.Context) (*aim.Registry, error) {
	return ensureRegistry(), nil
}

// Kit reads provider facts (key variables, aliases, protocol routes)
// from its default registry's on-disk catalog cache; it never fetches
// it. Handing kit foo's registry means a run, `foo model list` and the
// pool picker all read one catalog.
func init() {
	kitllm.SetDefaultRegistry(sharedRegistry)
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
// and each entry is added via WithFallback once its scheme's key and
// configured endpoint are applied (fallbackURIs). Errors from LoadConfig are tolerated —
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
			// picked.Provider is the catalog provider id, used as the
			// URI scheme as is (kit resolves "fireworks-ai" to its
			// adapter); picked.ID is the model name kit's adapter
			// expects. The prefix-based scheme guess (schemeForModel)
			// is bypassed: the registry already names the provider.
			return buildClient(ctx, opts.Secrets, picked.Provider, picked.ID, opts.MaxTokens)
		}
	}

	// Paths 1 and 2: explicit model.
	return newClientFromModel(ctx, opts.Secrets, model, opts.MaxTokens)
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
// the scheme's key and configured endpoint when the URI names none (see
// applyKey, applyConfiguredBaseURL).
func newClientFromModel(ctx context.Context, store secret.Store, model string, maxTokens int) (*Client, error) {
	uri, guessed, err := resolveURIForModel(ctx, store, model)
	if err != nil {
		return nil, err
	}
	client, err := buildClientFromURI(ctx, store, uri, maxTokens)
	if err != nil {
		return nil, err
	}
	if guessed {
		client.guessedModel = model
	}
	return client, nil
}

// buildClient is the common URI-build + fallback-wiring step for a
// pool pick. Key and endpoint are applied as for any other path, so
// every scheme the picker can return honors the same precheck.
func buildClient(ctx context.Context, store secret.Store, scheme, model string, maxTokens int) (*Client, error) {
	uri, err := applyKey(ctx, store, scheme+"://"+model)
	if err != nil {
		return nil, err
	}
	return buildClientFromURI(ctx, store, applyConfiguredBaseURL(uri, scheme), maxTokens)
}

// querySep returns the separator that appends a param to s: "?" when s
// has no query string yet, "&" when it does.
func querySep(s string) string {
	if strings.Contains(s, "?") {
		return "&"
	}
	return "?"
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
//
// Either way the key is kit's (applyKey): a URI-form value keeps a
// caller-supplied ?api_key=, and a bare id is first given the scheme
// schemeForModel maps it to.
func resolveURIForModel(ctx context.Context, store secret.Store, model string) (uri string, guessed bool, err error) {
	if modelIsURI(model) {
		uri, err := applyKey(ctx, store, model)
		if err != nil {
			return "", false, err
		}
		return applyConfiguredBaseURL(uri, schemeOf(uri)), false, nil
	}
	scheme, guessed := schemeForModel(model)
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
	built, err := applyKey(ctx, store, scheme+"://"+model)
	if err != nil {
		return "", false, err
	}
	return applyConfiguredBaseURL(built, scheme), guessed, nil
}

// resolvedURIForModel is the URI-only view of resolveURIForModel, kept
// for call sites (tests, provenance) that assert on the URI and have no
// use for the guess flag. It resolves with no secret store configured:
// keys come from llm.yaml and env vars only.
func resolvedURIForModel(model string) (string, error) {
	uri, _, err := resolveURIForModel(context.Background(), nil, model)
	return uri, err
}

// buildClientFromURI resolves a fully-formed provider URI and wires the
// fallback chain. Shared by the bare-id path (which assembles the URI)
// and the URI passthrough path (which received one from --model).
func buildClientFromURI(ctx context.Context, store secret.Store, uri string, maxTokens int) (*Client, error) {
	p, err := kitllm.Resolve(uri)
	if err != nil {
		return nil, err
	}

	// Per-URI Resolve errors skip just that one entry so one bad
	// fallback can't disable the rest.
	var clientOpts []kitllm.Option
	for _, fbURI := range fallbackURIs(ctx, store, uri) {
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
// Kit's Resolve takes a key and an endpoint from the URI and nowhere
// else, so an entry passed through bare reached its provider's public
// endpoint unauthenticated. Each entry gets its key from kit the same
// way a URI-form --model does and the same endpoint resolution as the
// primary (applyConfiguredBaseURL, with LLM_BASE_URL scoped to the
// primary's scheme).
//
// An entry whose key cannot be found is dropped, not fatal: the primary
// may be healthy, and failing the run over a backup that is never
// needed would be worse than running without it. The drop is announced
// once per process on stderr, naming the variable to set. An entry kit
// cannot key for another reason (a key a URI cannot carry) is dropped
// the same way.
//
// store is the primary's: a fallback's key comes from the same
// configured secret store as the primary's.
func fallbackURIs(ctx context.Context, store secret.Store, uri string) []string {
	cfg, err := kitllm.LoadConfig(uri)
	if err != nil {
		return nil
	}
	primaryScheme := schemeOf(uri)
	out := make([]string, 0, len(cfg.Fallbacks))
	for _, fb := range cfg.Fallbacks {
		keyed, err := kitllm.ApplyAPIKey(ctx, &namedStore{inner: store}, fb)
		if err != nil {
			warnDroppedFallback(fb, err)
			continue
		}
		out = append(out, applyConfiguredBaseURL(keyed, primaryScheme))
	}
	return out
}

// droppedFallbacks remembers which fallback drops were already
// announced, so a process that builds more than one client warns once.
var (
	droppedFallbacksMu sync.Mutex
	droppedFallbacks   = map[string]struct{}{}
)

// warnDroppedFallback announces a dropped fallback entry once. A
// missing key names the variable to set; any other kit error is
// reported as is (kit's errors carry names, never key values). The
// entry is identified by scheme and model only: its query may carry a
// key.
func warnDroppedFallback(fb string, err error) {
	id := "(unparsable entry)"
	if parsed, perr := kitllm.ParseURI(fb); perr == nil {
		id = parsed.Scheme + "://" + parsed.Model
	}
	droppedFallbacksMu.Lock()
	_, seen := droppedFallbacks[id]
	droppedFallbacks[id] = struct{}{}
	droppedFallbacksMu.Unlock()
	if seen {
		return
	}
	var missing *kitllm.MissingKeyError
	if !errors.As(err, &missing) {
		slog.Warn(
			"llm.fallback.dropped: fallback cannot be used; skipping it",
			slog.String("fallback", id),
			slog.String("error", err.Error()),
		)
		return
	}
	envVar := firstEnvVar(missing)
	slog.Warn(
		"llm.fallback.dropped: fallback has no API key; skipping it",
		slog.String("fallback", id),
		slog.String("missing", envVar),
		slog.String("hint", "export "+envVar+"=... to enable it, or remove it from LLM_FALLBACK / llm.yaml fallback:"),
	)
}

// schemeForModel maps a bare model id to its kit URI scheme. The
// router- prefix returns "routellm" so the caller knows to strip the
// marker before building the URI. The scheme's key is kit's business
// (applyKey).
//
// guessed reports that no prefix matched and the openai-compatible
// default arm answered. The scheme is the same either way; the flag
// only records *why*, so a later "model not available" can say foo
// assumed the provider instead of implying the user picked it.
func schemeForModel(model string) (scheme string, guessed bool) {
	switch {
	case strings.HasPrefix(model, "gpt-") || strings.HasPrefix(model, "o1") || strings.HasPrefix(model, "o3"):
		return "openai", false
	case strings.HasPrefix(model, "claude-"):
		return "anthropic", false
	case strings.HasPrefix(model, "gemini-"):
		return "google", false
	case strings.HasPrefix(model, "llama") || strings.HasPrefix(model, "mistral") || strings.HasPrefix(model, "deepseek-r1"):
		return "ollama", false
	case strings.HasPrefix(model, "router-"):
		return "routellm", false
	default:
		// Unknown prefix — assume an OpenAI-compatible endpoint on the
		// openai scheme, so OPENAI_API_KEY is the key it takes. Hosted
		// gateways (openrouter, groq, ...) are reached by naming their
		// scheme in a URI, which uses that scheme's own key.
		return "openai", true
	}
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
