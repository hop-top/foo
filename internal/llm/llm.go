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
	var uri string
	switch {
	case strings.HasPrefix(model, "gpt-") || strings.HasPrefix(model, "o1-") || strings.HasPrefix(model, "o3-"):
		uri = fmt.Sprintf("openai://%s?api_key=%s", model, os.Getenv("OPENAI_API_KEY"))
	case strings.HasPrefix(model, "claude-"):
		uri = fmt.Sprintf("anthropic://%s?api_key=%s", model, os.Getenv("ANTHROPIC_API_KEY"))
	case strings.HasPrefix(model, "gemini-"):
		uri = fmt.Sprintf("google://%s?api_key=%s", model, os.Getenv("GOOGLE_API_KEY"))
	case strings.HasPrefix(model, "llama") || strings.HasPrefix(model, "mistral") || strings.HasPrefix(model, "deepseek-r1"):
		uri = fmt.Sprintf("ollama://%s", model)
	case strings.HasPrefix(model, "router-"):
		uri = fmt.Sprintf("routellm://%s", strings.TrimPrefix(model, "router-"))
	default:
		// Fallback to openai scheme if unknown, maybe it's a compat provider
		uri = fmt.Sprintf("openai://%s?api_key=%s", model, os.Getenv("OPENAI_API_KEY"))
	}

	p, err := llm.Resolve(uri)
	if err != nil {
		return nil, err
	}

	return &Client{
		client: llm.NewClient(p),
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
