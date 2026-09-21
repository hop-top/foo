package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	llmerrors "hop.top/kit/go/ai/llm/errors"
)

// TestEnrichUnknownModel_OnlyWhenGuessed pins the discriminator: the
// hint fires when foo *guessed* the provider from an unrecognised model
// id, and stays silent otherwise. Without this, an explicit
// `-m gpt-nonexistent` would be told to check its RouteLLM setup.
func TestEnrichUnknownModel_OnlyWhenGuessed(t *testing.T) {
	modelErr := llmerrors.NewModel("private", "openai")

	cases := []struct {
		name     string
		client   *Client
		in       error
		wantHint bool
	}{
		{
			name:     "guessed id + model error gets the hint",
			client:   &Client{guessedModel: "private"},
			in:       modelErr,
			wantHint: true,
		},
		{
			name:     "recognised prefix keeps today's message",
			client:   &Client{},
			in:       llmerrors.NewModel("gpt-nonexistent", "openai"),
			wantHint: false,
		},
		{
			name:     "guessed id but a different error class is untouched",
			client:   &Client{guessedModel: "private"},
			in:       llmerrors.NewAuth("openai", errors.New("401")),
			wantHint: false,
		},
		{
			name:     "nil stays nil",
			client:   &Client{guessedModel: "private"},
			in:       nil,
			wantHint: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.client.enrichUnknownModel(tc.in)
			if tc.in == nil {
				if got != nil {
					t.Fatalf("enrichUnknownModel(nil) = %v, want nil", got)
				}
				return
			}
			hinted := strings.Contains(got.Error(), "matched no known model prefix")
			if hinted != tc.wantHint {
				t.Fatalf("hint present = %v, want %v; message: %s",
					hinted, tc.wantHint, got.Error())
			}
			// The original message must survive verbatim either way:
			// the hint appends, it never replaces.
			if !strings.Contains(got.Error(), tc.in.Error()) {
				t.Fatalf("original message lost; got %q, want it to contain %q",
					got.Error(), tc.in.Error())
			}
		})
	}
}

// TestEnrichUnknownModel_PreservesErrorContract guards the exit-code
// mapping. main.go derives the status from the error's AsCLIError
// identity, so the wrapped error must still match the original type via
// errors.As or a "model not available" would silently change exit code.
func TestEnrichUnknownModel_PreservesErrorContract(t *testing.T) {
	orig := llmerrors.NewModel("private", "openai")
	c := &Client{guessedModel: "private"}

	got := c.enrichUnknownModel(orig)

	var modelErr *llmerrors.ErrModel
	if !errors.As(got, &modelErr) {
		t.Fatalf("errors.As lost *ErrModel after enrichment: %T", got)
	}
	if modelErr.Model != "private" || modelErr.Provider != "openai" {
		t.Fatalf("wrapped ErrModel fields changed: %+v", modelErr)
	}
}

// TestEnrichUnknownModel_HintContent asserts the hint names every lever
// the task requires a reader to be able to act on. Asserting the exact
// sentence would pin prose; asserting the levers pins the contract.
func TestEnrichUnknownModel_HintContent(t *testing.T) {
	c := &Client{guessedModel: "private"}
	msg := c.enrichUnknownModel(llmerrors.NewModel("private", "openai")).Error()

	for _, want := range []string{
		"OpenAI-compatible",                   // the assumption foo made
		"RouteLLM tier",                       // the likely real intent
		"base_url",                            // how to reach another endpoint
		"LLM_BASE_URL",                        //
		"docs/how-to/use-a-local-endpoint.md", // where it is documented
		"foo model list",                      // how to discover real ids
		"private",                             // the id the user actually typed
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("hint missing %q; got: %s", want, msg)
		}
	}
}

// TestPrompt_EnrichesUnknownModel is the wiring test: a green unit test
// on enrichUnknownModel proves nothing if no request path calls it.
// Drives a real client built from a bare unrecognised id against a stub
// endpoint that 404s the model, and asserts the hint reaches the caller
// through Prompt.
func TestPrompt_EnrichesUnknownModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"model not found","type":"invalid_request_error"}}`))
	}))
	defer srv.Close()

	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("LLM_FALLBACK", "")
	t.Setenv("FOO_MODEL", "")

	client, err := NewClient(context.Background(), ClientOpts{
		Model: "private?base_url=" + srv.URL + "/v1",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client.guessedModel == "" {
		t.Fatal("guessedModel not recorded for a bare unrecognised id")
	}

	_, err = client.Prompt(context.Background(), "hi")
	if err == nil {
		t.Fatal("expected an error from the 404 stub, got nil")
	}
	if !strings.Contains(err.Error(), "matched no known model prefix") {
		t.Fatalf("Prompt did not enrich the failure; got: %v", err)
	}
}

// TestNewClient_ExplicitURI_NoGuess confirms the passthrough path never
// claims a guess: the user spelled the scheme out, so a failure there is
// not foo's assumption to explain.
func TestNewClient_ExplicitURI_NoGuess(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("FOO_MODEL", "")

	client, err := NewClient(context.Background(), ClientOpts{
		Model: "openai://private?base_url=http://127.0.0.1:1/v1",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client.guessedModel != "" {
		t.Fatalf("explicit URI recorded a guess: %q", client.guessedModel)
	}
}
