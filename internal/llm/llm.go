package llm

import (
	"context"
	"fmt"
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
