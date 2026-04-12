package llm

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"hop.top/kit/llm"
	_ "hop.top/kit/llm/anthropic"
	_ "hop.top/kit/llm/openai"
)

type Client struct {
	client *llm.Client
}

func NewClient(ctx context.Context, model string) (*Client, error) {
	var uri string
	if strings.HasPrefix(model, "gpt-") || strings.HasPrefix(model, "o1-") {
		uri = fmt.Sprintf("openai://%s?api_key=%s", model, os.Getenv("OPENAI_API_KEY"))
	} else if strings.HasPrefix(model, "claude-") {
		uri = fmt.Sprintf("anthropic://%s?api_key=%s", model, os.Getenv("ANTHROPIC_API_KEY"))
	} else {
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
