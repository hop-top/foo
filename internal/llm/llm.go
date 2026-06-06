package llm

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"hop.top/kit/go/ai/llm"
	_ "hop.top/kit/go/ai/llm/anthropic"
	_ "hop.top/kit/go/ai/llm/google"
	_ "hop.top/kit/go/ai/llm/ollama"
	_ "hop.top/kit/go/ai/llm/openai"
	_ "hop.top/kit/go/ai/llm/routellm"
)

type Client struct {
	client *llm.Client
}

func NewClient(ctx context.Context, model string) (*Client, error) {
	// Select provider scheme + the env var that holds its key. Empty
	// envVar means the scheme doesn't need a key (e.g., ollama is local).
	var scheme, envVar string
	switch {
	case strings.HasPrefix(model, "gpt-") || strings.HasPrefix(model, "o1-") || strings.HasPrefix(model, "o3-"):
		scheme, envVar = "openai", "OPENAI_API_KEY"
	case strings.HasPrefix(model, "claude-"):
		scheme, envVar = "anthropic", "ANTHROPIC_API_KEY"
	case strings.HasPrefix(model, "gemini-"):
		scheme, envVar = "google", "GOOGLE_API_KEY"
	case strings.HasPrefix(model, "llama") || strings.HasPrefix(model, "mistral") || strings.HasPrefix(model, "deepseek-r1"):
		scheme, envVar = "ollama", ""
	case strings.HasPrefix(model, "router-"):
		// Passthrough to kit's routellm adapter. The suffix after
		// "router-" is forwarded verbatim as the URI model field, so
		// `router-mf:0.7` becomes `routellm://mf:0.7` — exactly the
		// `<router_name>:<threshold>` shape kit's adapter parses.
		// All other routellm config (base URL, strong/weak model,
		// router list) lives in kit's standard locations:
		// `~/.config/hop/llm.yaml`'s `providers.routellm.routellm`
		// extras block or the ROUTELLM_* env vars. foo does not
		// surface a parallel flag set; see docs/how-to/route-across-models.md.
		model = strings.TrimPrefix(model, "router-")
		scheme, envVar = "routellm", ""
	default:
		// Unknown prefix — assume openai-compatible (openrouter, groq, etc.).
		scheme, envVar = "openai", "OPENAI_API_KEY"
	}

	var uri string
	if envVar != "" {
		key := os.Getenv(envVar)
		if key == "" {
			return nil, fmt.Errorf("missing %s for model %q (provider %s); export %s=... and retry, or switch models with `foo model default <model>`", envVar, model, scheme, envVar)
		}
		uri = fmt.Sprintf("%s://%s?api_key=%s", scheme, model, key)
	} else {
		uri = fmt.Sprintf("%s://%s", scheme, model)
	}

	p, err := llm.Resolve(uri)
	if err != nil {
		return nil, err
	}

	// Resolve fallback URIs from kit's config layer
	// (~/.config/hop/llm.yaml `fallback:` and the LLM_FALLBACK env
	// var). LoadConfig errors are tolerated: missing/invalid kit
	// config must not block a successful single-provider call.
	// Fallback URIs that fail to resolve are skipped individually so
	// one bad entry cannot disable the rest of the chain.
	var opts []llm.Option
	if cfg, cfgErr := llm.LoadConfig(uri); cfgErr == nil {
		for _, fbURI := range cfg.Fallbacks {
			fb, fbErr := llm.Resolve(fbURI)
			if fbErr != nil {
				continue
			}
			opts = append(opts, llm.WithFallback(fb))
		}
	}

	return &Client{
		client: llm.NewClient(p, opts...),
	}, nil
}

func (c *Client) Prompt(ctx context.Context, prompt string) (string, error) {
	resp, err := c.client.Complete(ctx, llm.Request{
		Messages: []llm.Message{
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
	messages []llm.Message,
	tools []llm.ToolDef,
) (llm.ToolResponse, error) {
	return c.client.CallWithTools(ctx, llm.Request{
		Messages: messages,
	}, tools)
}

// PromptStream streams LLM response tokens to w. Falls back to
// non-streaming Prompt if the provider doesn't support streaming.
func (c *Client) PromptStream(ctx context.Context, w io.Writer, prompt string) error {
	req := llm.Request{
		Messages: []llm.Message{
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
