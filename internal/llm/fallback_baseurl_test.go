package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	kitllm "hop.top/kit/go/ai/llm"
)

// isolateBaseURLEnv clears every provider key, LLM_API_KEY, LLM_BASE_URL
// and LLM_FALLBACK — set-then-unset, so they are absent rather than
// empty and restored afterwards — and points XDG_CONFIG_HOME at an empty
// temp dir. A developer's real environment cannot reach these tests.
func isolateBaseURLEnv(t *testing.T) {
	t.Helper()
	clearProviderKeys(t)
	for _, v := range keyedSchemes {
		os.Unsetenv(v)
	}
	for _, v := range []string{"LLM_API_KEY", "LLM_BASE_URL", "LLM_FALLBACK"} {
		os.Unsetenv(v)
	}
}

func baseURLOf(t *testing.T, uri string) string {
	t.Helper()
	return parseOrFatal(t, uri).Params["base_url"]
}

const (
	fileOpenAIBase    = "http://127.0.0.1:9101/v1"
	fileAnthropicBase = "http://127.0.0.1:9102"
	envBase           = "http://127.0.0.1:9109/v1"
)

// TestFallbackURIs_FileBaseURL is the reported defect: a fallback on a
// scheme with providers.<scheme>.base_url in llm.yaml reached the
// provider's public endpoint, because the configured base_url was folded
// into the primary only. The fallback keeps its injected key.
func TestFallbackURIs_FileBaseURL(t *testing.T) {
	isolateBaseURLEnv(t)
	captureWarnings(t)
	t.Setenv("OPENAI_API_KEY", "fake-openai-key")
	writeLLMYAML(t, "providers:\n  openai:\n    base_url: "+fileOpenAIBase+"\nfallback:\n  - openai://fb-model\n")

	got := fallbackURIs("ollama://llama3.2")
	if len(got) != 1 {
		t.Fatalf("fallbackURIs = %q, want 1 entry", got)
	}
	if bu := baseURLOf(t, got[0]); bu != fileOpenAIBase {
		t.Errorf("fallback base_url = %q, want llm.yaml value %q (uri=%q)", bu, fileOpenAIBase, got[0])
	}
	if k := parseOrFatal(t, got[0]).Params["api_key"]; k != "fake-openai-key" {
		t.Errorf("fallback api_key = %q, want fake-openai-key", k)
	}
}

// TestFallbackURIs_EnvBaseURLSameScheme: LLM_BASE_URL reaches a fallback
// on the primary's scheme, and beats the file there, as it does for the
// primary.
func TestFallbackURIs_EnvBaseURLSameScheme(t *testing.T) {
	isolateBaseURLEnv(t)
	captureWarnings(t)
	t.Setenv("OPENAI_API_KEY", "fake-openai-key")
	t.Setenv("LLM_BASE_URL", envBase)
	t.Setenv("LLM_FALLBACK", "openai://fb-model")
	writeLLMYAML(t, "providers:\n  openai:\n    base_url: "+fileOpenAIBase+"\n")

	got := fallbackURIs("openai://primary-model")
	if len(got) != 1 {
		t.Fatalf("fallbackURIs = %q, want 1 entry", got)
	}
	if bu := baseURLOf(t, got[0]); bu != envBase {
		t.Errorf("same-scheme fallback base_url = %q, want LLM_BASE_URL %q", bu, envBase)
	}
}

// TestFallbackURIs_EnvBaseURLOtherSchemeIgnored: LLM_BASE_URL is one
// variable for every scheme. Lent to a fallback on another scheme it
// would send that provider's request — and its key — to the primary's
// server. Such a fallback takes its own llm.yaml base_url, or none.
func TestFallbackURIs_EnvBaseURLOtherSchemeIgnored(t *testing.T) {
	isolateBaseURLEnv(t)
	captureWarnings(t)
	t.Setenv("OPENAI_API_KEY", "fake-openai-key")
	t.Setenv("ANTHROPIC_API_KEY", "fake-ant-key")
	t.Setenv("GROQ_API_KEY", "fake-groq-key")
	t.Setenv("LLM_BASE_URL", envBase)
	t.Setenv("LLM_FALLBACK", "anthropic://claude-fb,groq://groq-fb")
	writeLLMYAML(t, "providers:\n  anthropic:\n    base_url: "+fileAnthropicBase+"\n")

	got := fallbackURIs("openai://primary-model")
	if len(got) != 2 {
		t.Fatalf("fallbackURIs = %q, want 2 entries", got)
	}
	if bu := baseURLOf(t, got[0]); bu != fileAnthropicBase {
		t.Errorf("anthropic fallback base_url = %q, want its llm.yaml value %q", bu, fileAnthropicBase)
	}
	if bu := baseURLOf(t, got[1]); bu != "" {
		t.Errorf("groq fallback base_url = %q, want none (provider default)", bu)
	}
}

// TestFallbackURIs_ExplicitBaseURLKept: a ?base_url= written on the entry
// outranks file and env, and is not duplicated.
func TestFallbackURIs_ExplicitBaseURLKept(t *testing.T) {
	isolateBaseURLEnv(t)
	captureWarnings(t)
	t.Setenv("OPENAI_API_KEY", "fake-openai-key")
	t.Setenv("LLM_BASE_URL", envBase)
	const explicit = "http://127.0.0.1:9103/v1"
	t.Setenv("LLM_FALLBACK", "openai://fb-model?base_url="+explicit)
	writeLLMYAML(t, "providers:\n  openai:\n    base_url: "+fileOpenAIBase+"\n")

	got := fallbackURIs("openai://primary-model")
	if len(got) != 1 {
		t.Fatalf("fallbackURIs = %q, want 1 entry", got)
	}
	if n := strings.Count(got[0], "base_url="); n != 1 {
		t.Errorf("base_url appears %d times in %q, want 1", n, got[0])
	}
	if bu := baseURLOf(t, got[0]); bu != explicit {
		t.Errorf("fallback base_url = %q, want explicit %q", bu, explicit)
	}
}

// TestFallbackURIs_LocalSchemeDefaultKept: a local runtime fallback with
// nothing configured for its scheme keeps its adapter default, even when
// LLM_BASE_URL points the primary elsewhere.
func TestFallbackURIs_LocalSchemeDefaultKept(t *testing.T) {
	isolateBaseURLEnv(t)
	captureWarnings(t)
	t.Setenv("OPENAI_API_KEY", "fake-openai-key")
	t.Setenv("LLM_BASE_URL", envBase)
	t.Setenv("LLM_FALLBACK", "ollama://llama3.2,lmstudio://local-model")

	got := fallbackURIs("openai://primary-model")
	want := []string{"ollama://llama3.2", "lmstudio://local-model"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("local fallbacks rewritten:\n got  %q\n want %q", got, want)
	}
}

// TestResolvedURI_URIFormGetsConfiguredBaseURL: `-m openai://model` was
// never given providers.openai.base_url or LLM_BASE_URL — only a bare id
// was — so the same model spelled as a URI reached api.openai.com.
func TestResolvedURI_URIFormGetsConfiguredBaseURL(t *testing.T) {
	t.Run("file", func(t *testing.T) {
		isolateBaseURLEnv(t)
		t.Setenv("OPENAI_API_KEY", "fake-openai-key")
		writeLLMYAML(t, "providers:\n  openai:\n    base_url: "+fileOpenAIBase+"\n")

		got, err := resolvedURIForModel("openai://gpt-4o")
		if err != nil {
			t.Fatalf("resolvedURIForModel: %v", err)
		}
		if bu := baseURLOf(t, got); bu != fileOpenAIBase {
			t.Errorf("base_url = %q, want llm.yaml value %q (uri=%q)", bu, fileOpenAIBase, got)
		}
	})
	t.Run("env", func(t *testing.T) {
		isolateBaseURLEnv(t)
		t.Setenv("OPENAI_API_KEY", "fake-openai-key")
		t.Setenv("LLM_BASE_URL", envBase)
		writeLLMYAML(t, "providers:\n  openai:\n    base_url: "+fileOpenAIBase+"\n")

		got, err := resolvedURIForModel("openai://gpt-4o")
		if err != nil {
			t.Fatalf("resolvedURIForModel: %v", err)
		}
		if bu := baseURLOf(t, got); bu != envBase {
			t.Errorf("base_url = %q, want LLM_BASE_URL %q (uri=%q)", bu, envBase, got)
		}
	})
	t.Run("explicit", func(t *testing.T) {
		isolateBaseURLEnv(t)
		t.Setenv("OPENAI_API_KEY", "fake-openai-key")
		t.Setenv("LLM_BASE_URL", envBase)
		const explicit = "http://127.0.0.1:9104/v1"
		got, err := resolvedURIForModel("openai://gpt-4o?base_url=" + explicit)
		if err != nil {
			t.Fatalf("resolvedURIForModel: %v", err)
		}
		if bu := baseURLOf(t, got); bu != explicit || strings.Count(got, "base_url=") != 1 {
			t.Errorf("uri = %q, want the explicit base_url %q once", got, explicit)
		}
	})
}

// TestFileBaseURL_MatchesKit pins the llm.yaml read foo does itself (the
// file tier without LLM_BASE_URL, which LoadConfig cannot give) to kit's
// own reading of the same file, including a file kit rejects outright.
func TestFileBaseURL_MatchesKit(t *testing.T) {
	bodies := map[string]string{
		"set":       "providers:\n  anthropic:\n    base_url: " + fileAnthropicBase + "\n",
		"absent":    "providers:\n  openai:\n    base_url: " + fileOpenAIBase + "\n",
		"empty":     "",
		"bad-yaml":  "providers: [\n",
		"bad-shape": "providers:\n  anthropic:\n    base_url: " + fileAnthropicBase + "\npool: not-a-list\n",
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			isolateBaseURLEnv(t)
			writeLLMYAML(t, body)
			cfg, err := kitllm.LoadConfig("anthropic://m")
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if got, want := fileBaseURL("anthropic"), cfg.Provider.BaseURL; got != want {
				t.Errorf("fileBaseURL = %q, kit LoadConfig = %q", got, want)
			}
		})
	}
}

// recordingEndpoint is an OpenAI-compatible server that records the
// model of every chat request. Models named "primary-*" fail with a
// non-retried 500, which kit treats as fallbackable.
type recordingEndpoint struct {
	mu     sync.Mutex
	models []string
	srv    *httptest.Server
}

func newRecordingEndpoint(t *testing.T) *recordingEndpoint {
	t.Helper()
	r := &recordingEndpoint{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var in struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &in)
		r.mu.Lock()
		r.models = append(r.models, in.Model)
		r.mu.Unlock()
		if strings.HasPrefix(in.Model, "primary-") {
			w.Header().Set("x-should-retry", "false")
			http.Error(w, `{"error":{"message":"boom"}}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","object":"chat.completion","created":0,"model":"`+in.Model+
			`","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

// loopbackOnly makes http.DefaultClient — the one the openai adapter
// uses — refuse any non-loopback host for the rest of the test, and
// returns the refused hosts. A regression then fails locally instead of
// reaching a public provider with a fake key.
func loopbackOnly(t *testing.T) func() []string {
	t.Helper()
	var (
		mu      sync.Mutex
		refused []string
	)
	prev := http.DefaultClient.Transport
	next := prev
	if next == nil {
		next = http.DefaultTransport
	}
	http.DefaultClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if h := r.URL.Hostname(); h != "127.0.0.1" && h != "localhost" && h != "::1" {
			mu.Lock()
			refused = append(refused, r.URL.Host)
			mu.Unlock()
			return nil, errors.New("test refused non-loopback host " + r.URL.Host)
		}
		return next.RoundTrip(r)
	})
	t.Cleanup(func() { http.DefaultClient.Transport = prev })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), refused...)
	}
}

func (r *recordingEndpoint) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.models...)
}

// TestNewClient_FallbackReachesConfiguredEndpoint drives the wire: the
// primary fails on the server, and the fallback — configured only
// through llm.yaml base_url — must land on the same server rather than
// api.openai.com.
func TestNewClient_FallbackReachesConfiguredEndpoint(t *testing.T) {
	isolateBaseURLEnv(t)
	captureWarnings(t)
	ep := newRecordingEndpoint(t)
	refused := loopbackOnly(t)
	t.Setenv("OPENAI_API_KEY", "fake-openai-key")
	writeLLMYAML(t, "providers:\n  openai:\n    base_url: "+ep.srv.URL+"/v1\nfallback:\n  - openai://fb-model\n")

	client, err := NewClient(context.Background(), ClientOpts{Model: "primary-model"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	out, err := client.Prompt(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Prompt: %v (endpoint saw %q)", err, ep.seen())
	}
	if out != "ok" {
		t.Errorf("Prompt = %q, want ok", out)
	}
	if got := strings.Join(ep.seen(), ","); got != "primary-model,fb-model" {
		t.Errorf("endpoint saw %q, want primary-model,fb-model", got)
	}
	if hosts := refused(); len(hosts) != 0 {
		t.Errorf("requests left for public hosts %q", hosts)
	}
}

// TestBuildClient_PoolPickReachesConfiguredEndpoint: a pool pick builds
// its URI through buildClient, which also skipped the configured
// base_url.
func TestBuildClient_PoolPickReachesConfiguredEndpoint(t *testing.T) {
	isolateBaseURLEnv(t)
	captureWarnings(t)
	ep := newRecordingEndpoint(t)
	refused := loopbackOnly(t)
	t.Setenv("OPENAI_API_KEY", "fake-openai-key")
	writeLLMYAML(t, "providers:\n  openai:\n    base_url: "+ep.srv.URL+"/v1\n")

	client, err := buildClient("openai", "picked-model", "OPENAI_API_KEY", 0)
	if err != nil {
		t.Fatalf("buildClient: %v", err)
	}
	if _, err := client.Prompt(context.Background(), "hi"); err != nil {
		t.Fatalf("Prompt: %v (endpoint saw %q)", err, ep.seen())
	}
	if got := strings.Join(ep.seen(), ","); got != "picked-model" {
		t.Errorf("endpoint saw %q, want picked-model", got)
	}
	if hosts := refused(); len(hosts) != 0 {
		t.Errorf("requests left for public hosts %q", hosts)
	}
}
