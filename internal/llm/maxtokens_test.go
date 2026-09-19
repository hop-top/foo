package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// captureBody stands in for an OpenAI-compatible endpoint and records
// the decoded request body of the first completion call.
func captureBody(t *testing.T, got *map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Errorf("decode body %q: %v", body, err)
		}
		*got = decoded
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestPrompt_MaxTokensReachesWire is the regression guard for the
// missing cap: foo never set Request.MaxTokens, so servers that require
// an explicit limit rejected every request. Asserts on the serialized
// body because that is the only place the omission was observable.
func TestPrompt_MaxTokensReachesWire(t *testing.T) {
	t.Setenv("FOO_MODEL", "")
	t.Setenv("LLM_FALLBACK", "")

	var got map[string]any
	srv := captureBody(t, &got)

	client, err := NewClient(context.Background(), ClientOpts{
		Model:     "openai://test-model?base_url=" + srv.URL + "&api_key=k",
		MaxTokens: 512,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.Prompt(context.Background(), "hi"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	raw, ok := got["max_tokens"]
	if !ok {
		t.Fatalf("max_tokens absent from request body: %v", got)
	}
	if n, isNum := raw.(float64); !isNum || int(n) != 512 {
		t.Fatalf("max_tokens = %v, want 512", raw)
	}
}

// TestPrompt_MaxTokensUnsetOmitsField pins the opt-in half of the
// contract: zero must leave the field off the wire so providers apply
// their own default. A hard-coded fallback here would silently truncate
// every response for users who never asked for a cap.
func TestPrompt_MaxTokensUnsetOmitsField(t *testing.T) {
	t.Setenv("FOO_MODEL", "")
	t.Setenv("LLM_FALLBACK", "")

	var got map[string]any
	srv := captureBody(t, &got)

	client, err := NewClient(context.Background(), ClientOpts{
		Model: "openai://test-model?base_url=" + srv.URL + "&api_key=k",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.Prompt(context.Background(), "hi"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	if raw, ok := got["max_tokens"]; ok && raw != nil {
		t.Fatalf("max_tokens = %v, want field omitted when unset", raw)
	}
}

// TestPromptStream_MaxTokensReachesWire covers the streaming path,
// which builds its own Request and so could regress independently of
// Prompt. The fake server replies with a non-streaming body; the client
// falls back to Prompt, which still carries the cap.
func TestPromptStream_MaxTokensReachesWire(t *testing.T) {
	t.Setenv("FOO_MODEL", "")
	t.Setenv("LLM_FALLBACK", "")

	var got map[string]any
	srv := captureBody(t, &got)

	client, err := NewClient(context.Background(), ClientOpts{
		Model:     "openai://test-model?base_url=" + srv.URL + "&api_key=k",
		MaxTokens: 256,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.PromptStream(context.Background(), io.Discard, "hi"); err != nil {
		t.Fatalf("PromptStream: %v", err)
	}

	raw, ok := got["max_tokens"]
	if !ok {
		t.Fatalf("max_tokens absent from streaming request body: %v", got)
	}
	if n, isNum := raw.(float64); !isNum || int(n) != 256 {
		t.Fatalf("max_tokens = %v, want 256", raw)
	}
}
