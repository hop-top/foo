// Live-endpoint model discovery — the second [CatalogSource].
//
// The aim catalog (models.dev) is a census of models that exist in
// public. It structurally cannot know about a model you serve yourself:
// its ~222 providers hold no entry for a self-hosted or private
// endpoint, so `foo model list` against a local llama.cpp, vLLM, Ollama
// or colibri instance shows nothing you can actually call. The only
// source that knows your inventory is the server.
//
// Every OpenAI-compatible server answers `GET /v1/models` with the same
// envelope, so one code path covers all of them — Ollama serves both its
// native /api/tags and the OpenAI shim with identical ids, which is why
// no per-runtime special casing appears here.
//
// What comes back is thin: an id, a creation stamp, an owner. There is
// no cost, context window, reasoning flag or open-weights data, and this
// file never invents any — a zero field means "the endpoint did not say"
// and callers must not read it as "zero". That asymmetry is why
// [SourceEndpoint] travels on every row.

package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	kitllm "hop.top/kit/go/ai/llm"
)

// SourceEndpoint marks rows discovered from a live OpenAI-compatible
// `/v1/models` response.
//
// It is a distinct [ModelSource] from [SourceCatalog] because the two
// carry different guarantees: a catalog row is rich but hypothetical (it
// describes a model that exists somewhere), while an endpoint row is
// sparse but real (the server answering right now serves this id).
// Conflating them would let a caller read an endpoint row's zero Context
// as a genuine zero.
const SourceEndpoint ModelSource = "endpoint"

// endpointListPath is appended to the configured base URL. Base URLs are
// documented to already carry the `/v1` prefix the server mounts (see
// docs/how-to/use-a-local-endpoint.md), matching how the chat path
// appends only `/chat/completions`.
const endpointListPath = "/models"

// DefaultEndpointTimeout bounds a `/v1/models` probe.
//
// Measurement says a wrong endpoint fails fast on its own — 25ms for a
// closed local port (connection refused), 115ms for a routable host with
// nothing listening — so this is not what keeps the command responsive
// in the common failure. It exists for the case that does hang: a host
// that accepts the TCP connection and then never answers, such as a
// half-open ssh tunnel, where there is no refusal to trip on and the
// dial would otherwise block until the OS gives up.
const DefaultEndpointTimeout = 10 * time.Second

// endpointCatalog adapts a live `/v1/models` endpoint to CatalogSource.
type endpointCatalog struct {
	baseURL string
	client  *http.Client
}

// NewEndpointCatalog returns a CatalogSource that lists models from an
// OpenAI-compatible server at baseURL.
//
// A nil client means a fresh one bounded by [DefaultEndpointTimeout];
// tests inject their own to reach an httptest server or an xrr cassette.
// The client is a parameter rather than a package var so two endpoints
// can be listed in one process without racing on shared state.
func NewEndpointCatalog(baseURL string, client *http.Client) CatalogSource {
	if client == nil {
		client = &http.Client{Timeout: DefaultEndpointTimeout}
	}
	return endpointCatalog{baseURL: baseURL, client: client}
}

// endpointModelsResponse is the OpenAI `/v1/models` envelope. Only the
// fields foo can project onto a ModelEntry are decoded; unknown fields
// are ignored so a server that adds its own extras still parses.
type endpointModelsResponse struct {
	Data []endpointModel `json:"data"`
}

// endpointModel is one entry of the `data` array.
type endpointModel struct {
	ID      string `json:"id"`
	OwnedBy string `json:"owned_by"`
}

// ListModels fetches and projects the endpoint's inventory.
//
// Every failure path names the URL that was tried. A down ssh tunnel is
// the common case and it must never surface as an empty list: "this
// server has no models" and "this server did not answer" are opposite
// facts, and the user acts on them differently.
func (c endpointCatalog) ListModels(ctx context.Context) ([]ModelEntry, error) {
	url := endpointModelsURL(c.baseURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("foo: build request for %s: %w", url, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		// The transport error alone reads as a bare dial failure with
		// no hint of which endpoint foo was even talking to, so the
		// URL is folded in ahead of it.
		return nil, fmt.Errorf("foo: list models from %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("foo: list models from %s: endpoint returned %s",
			url, http.StatusText(resp.StatusCode))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("foo: read model list from %s: %w", url, err)
	}

	var decoded endpointModelsResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		// A server that answers 200 with HTML (a captive portal, a
		// proxy error page, a base_url pointing at a web UI) lands
		// here; quoting the URL is what makes that diagnosable.
		return nil, fmt.Errorf("foo: parse model list from %s: %w", url, err)
	}

	out := make([]ModelEntry, 0, len(decoded.Data))
	for _, m := range decoded.Data {
		if m.ID == "" {
			continue
		}
		out = append(out, entryFromEndpoint(m, c.baseURL))
	}

	// CatalogSource promises a deterministic order and the server's
	// own ordering is not guaranteed stable across calls (Ollama sorts
	// by mtime), so ids are sorted here. Ranking still runs downstream.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// entryFromEndpoint projects one `/v1/models` entry onto a foo row.
//
// Fields the envelope cannot supply — context window, cost, tool-call
// and reasoning support — are deliberately left zero rather than
// guessed. Reachable is true by construction: the id came from a server
// that just answered, which is a stronger reachability proof than the
// catalog's "foo links an adapter for this provider" test.
func entryFromEndpoint(m endpointModel, baseURL string) ModelEntry {
	return ModelEntry{
		Source:    SourceEndpoint,
		Provider:  endpointProvider(m, baseURL),
		ID:        m.ID,
		Reachable: true,
	}
}

// endpointProvider labels the row's origin.
//
// `owned_by` is the only provenance the envelope carries, but servers
// disagree on what it means: Ollama reports "library" for everything,
// colibri reports its own name. A generic value says nothing a reader
// can use, so it falls back to the endpoint's host — which is the fact
// that actually distinguishes two local servers in one listing.
func endpointProvider(m endpointModel, baseURL string) string {
	switch m.OwnedBy {
	case "", "library", "system", "organization_owner":
		if host := endpointHost(baseURL); host != "" {
			return host
		}
		return "endpoint"
	default:
		return m.OwnedBy
	}
}

// endpointHost extracts host:port from a base URL without url.Parse
// ceremony, tolerating a missing scheme.
func endpointHost(baseURL string) string {
	s := strings.TrimSpace(baseURL)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return s
}

// endpointModelsURL joins a base URL to the models path, tolerating a
// trailing slash and a base that already names the collection.
func endpointModelsURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(base, endpointListPath) {
		return base
	}
	return base + endpointListPath
}

// ResolveConfiguredEndpoint returns the endpoint foo is configured to
// talk to, or "" when none is set.
//
// Precedence is not re-derived here. kit's LoadConfig is the single
// implementation of the documented ladder — llm.yaml
// `providers.<scheme>.base_url`, overridden by LLM_BASE_URL — and is the
// same function [applyConfiguredBaseURL] calls on the completion path,
// so `foo model list` and `foo "hello"` can never disagree about which
// server they mean. The third documented lever, `?base_url=` on the
// model, is per-invocation and has no model to hang off here; `--endpoint`
// is its equivalent on this surface and outranks both, which the caller
// applies by not calling this at all.
//
// A scheme-only URI is passed because LoadConfig needs a scheme to find
// the provider block but no model id to resolve base_url — verified
// against kit: "openai://" resolves both layers, while "" errors with
// "no URI provided and no default configured".
//
// Errors are swallowed to "": a missing or malformed llm.yaml means "no
// endpoint configured", which is a fallback to the catalog, not a
// failure of `foo model list`.
func ResolveConfiguredEndpoint() string {
	cfg, err := kitllm.LoadConfig(endpointProbeURI)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cfg.Provider.BaseURL)
}

// endpointProbeURI is the scheme-only URI handed to LoadConfig to read
// the configured base_url. "openai" is the scheme foo treats as the
// OpenAI-compatible default, and is the scheme the local-endpoint how-to
// documents configuring.
const endpointProbeURI = "openai://"

// ErrEndpointFlagUnsupported reports a flag that cannot be honoured
// against a live endpoint.
var ErrEndpointFlagUnsupported = errors.New("flag not supported with --endpoint")

// CatalogOnlyFlagError names a flag that only the catalog can satisfy.
//
// `/v1/models` returns ids and nothing else, so a filter over cost,
// context window, reasoning or open-weights has no data to act on. The
// two silent alternatives are both lies — applying the filter to zero
// values drops everything, ignoring it returns rows the user asked to
// exclude — so the combination is refused by name instead.
type CatalogOnlyFlagError struct {
	// Flag is the offending flag as the user typed it, without the
	// leading dashes.
	Flag string
}

func (e *CatalogOnlyFlagError) Error() string {
	return fmt.Sprintf("foo: --%s needs model metadata the catalog has and a live endpoint does not; "+
		"drop --%s to filter the catalog, or drop --endpoint to list from the endpoint",
		e.Flag, e.Flag)
}

func (e *CatalogOnlyFlagError) Unwrap() error { return ErrEndpointFlagUnsupported }
