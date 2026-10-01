package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"hop.top/foo/internal/llm"
	kitllm "hop.top/kit/go/ai/llm"
	"hop.top/kit/go/console/output"
	"hop.top/kit/go/storage/secret"
)

const (
	defaultModel     = "text-embedding-3-small"
	defaultDimension = 1536
	// openaiAPIRoot is the OpenAI adapter's default base URL: where
	// the embeddings path is joined when no endpoint is configured.
	openaiAPIRoot  = "https://api.openai.com/v1"
	embeddingsPath = "/embeddings"
)

// OpenAIEmbedder calls an OpenAI-compatible embeddings API directly
// via HTTP.
type OpenAIEmbedder struct {
	apiKey string
	model  string
	dim    int
	store  secret.Store
	// url is the full embeddings endpoint, resolved at construction;
	// tests point it at a local recorder.
	url    string
	client *http.Client
}

// OpenAIOption configures the OpenAI embedder.
type OpenAIOption func(*OpenAIEmbedder)

// WithModel overrides the default embedding model.
func WithModel(model string) OpenAIOption {
	return func(e *OpenAIEmbedder) { e.model = model }
}

// WithDimension overrides the default vector dimension.
func WithDimension(dim int) OpenAIOption {
	return func(e *OpenAIEmbedder) { e.dim = dim }
}

// WithSecretStore sets the secret store the API key is read from:
// foo's configured store, the one the run path reads. Nil, the
// default, reads the environment only.
func WithSecretStore(store secret.Store) OpenAIOption {
	return func(e *OpenAIEmbedder) { e.store = store }
}

// NewOpenAIEmbedder creates a new OpenAI-backed Embedder.
// The OpenAI API key is resolved as a run resolves it (llm.APIKey):
// the store given by WithSecretStore under its documented name
// `openai_api_key`, then OPENAI_API_KEY, then LLM_API_KEY, with
// llm.yaml's providers.openai.api_key ahead of all three.
//
// The endpoint is resolved as a run on the same openai URI resolves it
// (llm.EndpointBaseURL): LLM_BASE_URL, then llm.yaml
// providers.openai.base_url, then OpenAI's public API. The embeddings
// path is joined under that API root, so a base of
// http://host:8000/v1 is posted to at http://host:8000/v1/embeddings.
func NewOpenAIEmbedder(opts ...OpenAIOption) (*OpenAIEmbedder, error) {
	e := &OpenAIEmbedder{
		model:  defaultModel,
		dim:    defaultDimension,
		client: http.DefaultClient,
	}
	for _, o := range opts {
		o(e)
	}

	uri := "openai://" + e.model
	e.url = embeddingsURL(llm.EndpointBaseURL(uri))

	key, err := llm.APIKey(context.Background(), e.store, uri)
	switch {
	case errors.Is(err, kitllm.ErrMissingKey):
		return nil, output.UnauthorizedError("OPENAI_API_KEY not set")
	case err != nil:
		return nil, err
	}
	e.apiKey = key
	return e, nil
}

// embeddingsURL joins the embeddings path to an API root, tolerating a
// trailing slash. An empty base is OpenAI's own.
func embeddingsURL(base string) string {
	if base == "" {
		base = openaiAPIRoot
	}
	return strings.TrimRight(base, "/") + embeddingsPath
}

func (e *OpenAIEmbedder) Dimension() int { return e.dim }

type embeddingReq struct {
	Input []string `json:"input"`
	Model string   `json:"model"`
}

type embeddingResp struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Embed generates embeddings for the given texts in a single batch call.
func (e *OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(embeddingReq{Input: texts, Model: e.model})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, llm.OfflineRefusal(fmt.Errorf("openai request: %w", err))
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai %d: %s", resp.StatusCode, raw)
	}

	var result embeddingResp
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	if result.Error != nil {
		return nil, fmt.Errorf("openai error: %s", result.Error.Message)
	}

	// Order by index.
	vecs := make([][]float32, len(result.Data))
	for _, d := range result.Data {
		vecs[d.Index] = d.Embedding
	}
	return vecs, nil
}
