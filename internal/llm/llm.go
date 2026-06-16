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
	_ "hop.top/kit/go/ai/llm/google"
	_ "hop.top/kit/go/ai/llm/ollama"
	_ "hop.top/kit/go/ai/llm/openai"
	_ "hop.top/kit/go/ai/llm/routellm"
	"hop.top/kit/go/storage/secret"
	_ "hop.top/kit/go/storage/secret/env"
)

type Client struct {
	client *kitllm.Client
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
// and each entry is added via WithFallback. Errors from LoadConfig are
// tolerated — missing/invalid config must not block a working call.
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
			return buildClient(scheme, picked.ID, envVar)
		}
	}

	// Paths 1 and 2: explicit model. Detect scheme from prefix.
	scheme, envVar := schemeForModel(model)
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
	return buildClient(scheme, model, envVar)
}

// buildClient is the common URI-build + fallback-wiring step shared by
// every NewClient code path. Centralizes the API-key precheck so any
// future scheme picked up by the pool picker honors the same error
// shape.
func buildClient(scheme, model, envVar string) (*Client, error) {
	var uri string
	if envVar != "" {
		key := lookupAPIKey(envVar)
		if key == "" {
			return nil, fmt.Errorf("missing %s for model %q (provider %s); export %s=... and retry, or switch models with `foo model default <model>`", envVar, model, scheme, envVar)
		}
		uri = fmt.Sprintf("%s://%s?api_key=%s", scheme, model, key)
	} else {
		uri = fmt.Sprintf("%s://%s", scheme, model)
	}

	p, err := kitllm.Resolve(uri)
	if err != nil {
		return nil, err
	}

	// Fallback wiring (per LoadConfig + LLM_FALLBACK env). LoadConfig
	// errors are tolerated so a missing config file never blocks a
	// successful single-provider call; per-URI Resolve errors skip
	// just that one entry so one bad fallback can't disable the rest.
	var clientOpts []kitllm.Option
	if cfg, cfgErr := kitllm.LoadConfig(uri); cfgErr == nil {
		for _, fbURI := range cfg.Fallbacks {
			fb, fbErr := kitllm.Resolve(fbURI)
			if fbErr != nil {
				continue
			}
			clientOpts = append(clientOpts, kitllm.WithFallback(fb))
		}
	}

	return &Client{
		client: kitllm.NewClient(p, clientOpts...),
	}, nil
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
func schemeForModel(model string) (scheme, envVar string) {
	switch {
	case strings.HasPrefix(model, "gpt-") || strings.HasPrefix(model, "o1") || strings.HasPrefix(model, "o3"):
		return "openai", "OPENAI_API_KEY"
	case strings.HasPrefix(model, "claude-"):
		return "anthropic", "ANTHROPIC_API_KEY"
	case strings.HasPrefix(model, "gemini-"):
		return "google", "GOOGLE_API_KEY"
	case strings.HasPrefix(model, "llama") || strings.HasPrefix(model, "mistral") || strings.HasPrefix(model, "deepseek-r1"):
		return "ollama", ""
	case strings.HasPrefix(model, "router-"):
		return "routellm", ""
	default:
		// Unknown prefix — assume openai-compatible (openrouter, groq, etc.).
		return "openai", "OPENAI_API_KEY"
	}
}

// envVarForScheme returns the env var holding the API key for a scheme.
// Used on the picker path, where the scheme comes from the registry
// rather than a model-id prefix guess.
func envVarForScheme(scheme string) string {
	switch scheme {
	case "openai":
		return "OPENAI_API_KEY"
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "google":
		return "GOOGLE_API_KEY"
	case "ollama", "routellm":
		return ""
	default:
		// Unknown scheme registered in the pool — assume openai-
		// compatible. Most aggregators (openrouter, groq, together)
		// fall through here.
		return "OPENAI_API_KEY"
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

func (c *Client) Prompt(ctx context.Context, prompt string) (string, error) {
	resp, err := c.client.Complete(ctx, kitllm.Request{
		Messages: []kitllm.Message{
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		return "", err
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
	return c.client.CallWithTools(ctx, kitllm.Request{
		Messages: messages,
	}, tools)
}

// PromptStream streams LLM response tokens to w. Falls back to
// non-streaming Prompt if the provider doesn't support streaming.
func (c *Client) PromptStream(ctx context.Context, w io.Writer, prompt string) error {
	req := kitllm.Request{
		Messages: []kitllm.Message{
			{Role: "user", Content: prompt},
		},
	}

	iter, err := c.client.Stream(ctx, req)
	if err != nil {
		// Fallback: provider may not support streaming.
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
			return err
		}
		if _, writeErr := fmt.Fprint(w, tok.Content); writeErr != nil {
			return writeErr
		}
		if tok.Done {
			return nil
		}
	}
}
