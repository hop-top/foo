package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeLLMYAML writes llm.yaml under the XDG_CONFIG_HOME withAuth set,
// replacing its empty-pool stub.
func writeLLMYAML(t *testing.T, body string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "hop")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "llm.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// providerShowJSON drives `foo provider show <scheme> --format json` and
// returns the raw output and its decoded fields.
func providerShowJSON(t *testing.T, scheme string) (string, map[string]any) {
	t.Helper()
	r := New("test")
	var out bytes.Buffer
	r.Cmd.SetOut(&out)
	r.Cmd.SetErr(&bytes.Buffer{})
	r.Cmd.SetArgs([]string{"provider", "show", scheme, "--format", "json"})
	if err := r.Cmd.Execute(); err != nil {
		t.Fatalf("provider show %s: %v", scheme, err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", out.String(), err)
	}
	return out.String(), got
}

// TestProviderShow_HonoursEveryPrecheckSource: a key a run would use —
// from llm.yaml or LLM_API_KEY — is "configured", with key_source naming
// where it came from and secret_key still naming the scheme's own key.
// The key value never appears.
func TestProviderShow_HonoursEveryPrecheckSource(t *testing.T) {
	for _, tc := range []struct {
		name, scheme, status, source, secret string
		setup                                func(t *testing.T)
		value                                string
	}{
		{
			name: "llm.yaml", scheme: "openai", status: "configured", source: "llm.yaml", secret: "openai_api_key",
			setup: func(t *testing.T) { writeLLMYAML(t, "providers:\n  openai:\n    api_key: fake-yaml-key\npool: []\n") },
			value: "fake-yaml-key",
		},
		{
			name: "LLM_API_KEY", scheme: "groq", status: "configured", source: "LLM_API_KEY", secret: "groq_api_key",
			setup: func(t *testing.T) { t.Setenv("LLM_API_KEY", "fake-llm-key") },
			value: "fake-llm-key",
		},
		{
			name: "file key is its scheme's only", scheme: "groq", status: "missing", source: "", secret: "groq_api_key",
			setup: func(t *testing.T) { writeLLMYAML(t, "providers:\n  openai:\n    api_key: fake-yaml-key\n") },
		},
		{
			name: "no key", scheme: "openai", status: "missing", source: "", secret: "openai_api_key",
			setup: func(*testing.T) {},
		},
		{
			name: "local scheme the catalog lists a key for", scheme: "lmstudio", status: "available", source: "", secret: "",
			setup: func(*testing.T) {},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string][]string{"lmstudio": {"LMSTUDIO_API_KEY"}}
			for k, v := range reachabilityEnv {
				env[k] = v
			}
			withAuth(t, env)
			tc.setup(t)

			raw, got := providerShowJSON(t, tc.scheme)
			if got["status"] != tc.status {
				t.Errorf("status = %v, want %s", got["status"], tc.status)
			}
			if s, _ := got["key_source"].(string); s != tc.source {
				t.Errorf("key_source = %q, want %q", s, tc.source)
			}
			if s, _ := got["secret_key"].(string); s != tc.secret {
				t.Errorf("secret_key = %q, want %q", s, tc.secret)
			}
			if tc.value != "" && strings.Contains(raw, tc.value) {
				t.Errorf("provider show printed the key value: %s", raw)
			}
		})
	}
}

// TestProviderShow_TableUnchanged: key_source is a structured-output
// field; the table keeps its SCHEME / AUTH / STATUS columns.
func TestProviderShow_TableUnchanged(t *testing.T) {
	withAuth(t, reachabilityEnv)
	t.Setenv("LLM_API_KEY", "fake-llm-key")

	r := New("test")
	var out bytes.Buffer
	r.Cmd.SetOut(&out)
	r.Cmd.SetErr(&bytes.Buffer{})
	r.Cmd.SetArgs([]string{"provider", "show", "openai", "--format", "table"})
	if err := r.Cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	header := strings.Fields(strings.SplitN(out.String(), "\n", 2)[0])
	if strings.Join(header, " ") != "SCHEME AUTH STATUS" {
		t.Errorf("table header = %q, want SCHEME AUTH STATUS", header)
	}
	if !strings.Contains(out.String(), "configured") || strings.Contains(out.String(), "fake-llm-key") {
		t.Errorf("table = %q, want configured and no key value", out.String())
	}
}

// TestModelList_KeepsRowsAKeyFromAnySourceReaches: the listing hides a
// provider's rows only when a run would refuse it for want of a key.
func TestModelList_KeepsRowsAKeyFromAnySourceReaches(t *testing.T) {
	for _, tc := range []struct {
		name          string
		setup         func(t *testing.T)
		shown, hidden []string
	}{
		{
			name:   "LLM_API_KEY",
			setup:  func(t *testing.T) { t.Setenv("LLM_API_KEY", "fake-llm-key") },
			shown:  []string{"gpt-x", "gemini-x"},
			hidden: []string{"llama-x"}, // no adapter: a key does not help
		},
		{
			name:   "llm.yaml openai key",
			setup:  func(t *testing.T) { writeLLMYAML(t, "providers:\n  openai:\n    api_key: fake-yaml-key\n") },
			shown:  []string{"gpt-x"},
			hidden: []string{"gemini-x", "llama-x"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withCatalog(t, stubCatalog{entries: reachabilityEntries()})
			withAuth(t, reachabilityEnv)
			tc.setup(t)

			stdout, stderr, err := runList(t)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			for _, id := range tc.shown {
				if !strings.Contains(stdout, id) {
					t.Errorf("%s: a run can reach %q, so the listing must show it: %q", tc.name, id, stdout)
				}
			}
			for _, id := range tc.hidden {
				if strings.Contains(stdout, id) {
					t.Errorf("%s: %q must stay hidden: %q", tc.name, id, stdout)
				}
			}
			if strings.Contains(stderr, "no provider API key is configured") {
				t.Errorf("%s: footer claims no key is configured: %q", tc.name, stderr)
			}
		})
	}
}
