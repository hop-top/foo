package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

const (
	defaultModel     = "text-embedding-3-small"
	defaultDimension = 1536
	openaiURL        = "https://api.openai.com/v1/embeddings"
)

// OpenAIEmbedder calls the OpenAI embeddings API directly via HTTP.
type OpenAIEmbedder struct {
	apiKey string
	model  string
	dim    int
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

// NewOpenAIEmbedder creates a new OpenAI-backed Embedder.
// Uses OPENAI_API_KEY from the environment.
func NewOpenAIEmbedder(opts ...OpenAIOption) (*OpenAIEmbedder, error) {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY not set")
	}

	e := &OpenAIEmbedder{
		apiKey: key,
		model:  defaultModel,
		dim:    defaultDimension,
		client: http.DefaultClient,
	}
	for _, o := range opts {
		o(e)
	}
	return e, nil
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openaiURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai request: %w", err)
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
