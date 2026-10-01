package main

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"hop.top/foo/internal/testutil"
	kitllm "hop.top/kit/go/ai/llm"
)

// llmEnvNames is every variable kit's key and endpoint resolution reads
// for the schemes this binary links, plus the universal ones. A
// developer's own keys or endpoints must never steer a test, and no test
// may reach a real provider with them.
var llmEnvNames = []string{
	"LLM_API_KEY", "LLM_BASE_URL", "LLM_PROVIDER", "LLM_FALLBACK",
	"OPENAI_API_KEY", "OPENAI_BASE_URL",
	"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL",
	"GOOGLE_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY", "GEMINI_API_KEY",
	"OPENROUTER_API_KEY", "GROQ_API_KEY", "XAI_API_KEY",
	"TOGETHER_API_KEY", "FIREWORKS_API_KEY", "DEEPSEEK_API_KEY",
	"MISTRAL_API_KEY", "LMSTUDIO_API_KEY",
	"OLLAMA_API_KEY", "OLLAMA_HOST", "ROUTELLM_API_KEY", "TRITON_API_KEY",
}

// TestMain points HOME and every XDG base at a throwaway directory for
// the whole package and drops the provider variables above, so no test
// reads the developer's llm.yaml, aim catalog cache or keys, and kit's
// process-wide catalog memo can only ever hold what a test seeded.
func TestMain(m *testing.M) {
	os.Exit(runIsolated(m))
}

// testCallerEnv is the environment the test binary started with, minus
// the provider variables, for subprocesses that need the caller's
// toolchain setup (module cache, GOWORK) rather than the throwaway HOME.
var testCallerEnv []string

func runIsolated(m *testing.M) int {
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(llmEnvNames, name) {
			testCallerEnv = append(testCallerEnv, kv)
		}
	}
	// The go dirs are pinned first, so the fake yt-dlp builds against
	// the caller's module and build caches, not an empty one.
	cleanup, err := testutil.IsolateUserDirs()
	if err != nil {
		fmt.Fprintln(os.Stderr, "foo-youtube tests: isolate user dirs:", err)
		return 1
	}
	defer cleanup()
	for _, name := range llmEnvNames {
		os.Unsetenv(name)
	}
	// A request that misses its local stub (a base_url that failed to
	// apply) must fail on a closed port, never reach a public provider.
	// Loopback stubs bypass the proxy; net/http reads these once, so
	// they are set before any test runs.
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		os.Setenv(name, "http://127.0.0.1:9")
	}
	for _, name := range []string{"NO_PROXY", "no_proxy", "ALL_PROXY", "all_proxy"} {
		os.Unsetenv(name)
	}
	return m.Run()
}

// isolateLLMEnv gives one test its own HOME and XDG bases, and unsets
// every provider variable (t.Setenv first, so each is restored after
// the test). kit's default aim registry and catalog memo are dropped on
// both sides of the test so a catalog seen by one test never answers for
// another.
func isolateLLMEnv(t *testing.T) (xdgConfig string) {
	t.Helper()
	home := t.TempDir()
	xdgConfig = home + "/.config"
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)
	t.Setenv("XDG_CACHE_HOME", home+"/.cache")
	t.Setenv("XDG_DATA_HOME", home+"/.local/share")
	t.Setenv("XDG_STATE_HOME", home+"/.local/state")
	for _, name := range llmEnvNames {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
	kitllm.ResetDefaultRegistry()
	t.Cleanup(kitllm.ResetDefaultRegistry)
	return xdgConfig
}

// assertNoSecret fails when s carries any of the key values.
func assertNoSecret(t *testing.T, s string, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if strings.Contains(s, k) {
			t.Errorf("output carries a key value: %q", s)
		}
	}
}
