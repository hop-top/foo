package llm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"testing"

	"hop.top/aim"
	kitllm "hop.top/kit/go/ai/llm"
)

// Key resolution is kit's (ApplyAPIKey). These tests pin the seams foo
// keeps around it: the run and kit agree on every scheme, catalog
// provider ids reach their adapter with their key, and the listing
// answers "has a key" the way the run does.

// catalogAliasCases are catalog provider ids spelled otherwise than the
// kit scheme serving them, with the variable their key is read from.
var catalogAliasCases = map[string]string{
	"fireworks-ai": "FIREWORKS_API_KEY",
	"togetherai":   "TOGETHER_API_KEY",
}

// TestResolveURI_CatalogIDTakesItsKey: a catalog provider id used as a
// scheme ("fireworks-ai://...", what `foo model list` and a pool pick
// name) gets the provider's key, and fails the precheck without one.
func TestResolveURI_CatalogIDTakesItsKey(t *testing.T) {
	for id, envVar := range catalogAliasCases {
		t.Run(id, func(t *testing.T) {
			clearProviderKeys(t)
			t.Setenv(envVar, "fake-"+id+"-key")

			got, err := resolvedURIForModel(id + "://accounts/vendor/models/some-model")
			if err != nil {
				t.Fatalf("resolvedURIForModel: %v", err)
			}
			if k := parseOrFatal(t, got).Params["api_key"]; k != "fake-"+id+"-key" {
				t.Errorf("api_key = %q, want the %s value", k, envVar)
			}

			t.Setenv(envVar, "")
			_, err = resolvedURIForModel(id + "://accounts/vendor/models/some-model")
			assertMissingKeyError(t, err, envVar, id)
		})
	}
}

// TestEntryFromAim_CatalogIDIsRoutable: a catalog row whose provider id
// kit serves under an alias is routable, so the default listing keeps it
// once the key is there.
func TestEntryFromAim_CatalogIDIsRoutable(t *testing.T) {
	for id := range catalogAliasCases {
		if e := entryFromAim(aim.Model{Provider: id, ID: "some-model"}); !e.Routable {
			t.Errorf("%s row: Routable = false; kit resolves %s:// through its alias", id, id)
		}
	}
	if e := entryFromAim(aim.Model{Provider: "no-such-provider", ID: "m"}); e.Routable {
		t.Error("a provider no adapter serves must not be routable")
	}
}

// TestModelList_CatalogIDWithKeyIsShown: the listing's end-to-end
// predicate for a fireworks-ai row with FIREWORKS_API_KEY exported.
func TestModelList_CatalogIDWithKeyIsShown(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("FIREWORKS_API_KEY", "fake-fw-key")
	idx := NewAuthIndexFrom(context.Background(), catalogEnv, nil)

	rows := []ModelEntry{
		entryFromAim(aim.Model{Provider: "fireworks-ai", ID: "accounts/fireworks/models/x"}),
		entryFromAim(aim.Model{Provider: "togetherai", ID: "vendor/y"}),
	}
	kept, _ := FilterReachable(rows, idx)
	if len(kept) != 1 || kept[0].Provider != "fireworks-ai" {
		t.Errorf("kept = %+v, want only the fireworks-ai row (its key is set, togetherai's is not)", kept)
	}
}

// keyCase is one machine state a key lookup can see.
type keyCase struct {
	env  map[string]string
	yaml string
}

// keyCases covers every source kit reads, alone and in contention.
var keyCases = map[string]keyCase{
	"no key":            {},
	"LLM_API_KEY":       {env: map[string]string{"LLM_API_KEY": "k-llm"}},
	"GEMINI_API_KEY":    {env: map[string]string{"GEMINI_API_KEY": "k-gemini"}},
	"GOOGLE over GEM":   {env: map[string]string{"GOOGLE_API_KEY": "k-google", "GEMINI_API_KEY": "k-gemini"}},
	"ROUTELLM_API_KEY":  {env: map[string]string{"ROUTELLM_API_KEY": "k-routellm"}},
	"OLLAMA_API_KEY":    {env: map[string]string{"OLLAMA_API_KEY": "k-ollama"}},
	"LMSTUDIO_API_KEY":  {env: map[string]string{"LMSTUDIO_API_KEY": "k-lms", "LLM_API_KEY": "k-llm"}},
	"scheme env":        {env: map[string]string{"GROQ_API_KEY": "k-groq", "FIREWORKS_API_KEY": "k-fw"}},
	"file over env":     {env: map[string]string{"OPENROUTER_API_KEY": "k-env", "LLM_API_KEY": "k-llm"}, yaml: "providers:\n  openrouter:\n    api_key: k-file\n"},
	"file over LLM":     {env: map[string]string{"LLM_API_KEY": "k-llm"}, yaml: "providers:\n  openai:\n    api_key: k-file\n"},
	"api_key_env":       {env: map[string]string{"MY_OR_KEY": "k-named", "OPENROUTER_API_KEY": "k-env"}, yaml: "providers:\n  openrouter:\n    api_key_env: MY_OR_KEY\n"},
	"alias file block":  {yaml: "providers:\n  fireworks:\n    api_key: k-fw-file\n"},
	"gemini file block": {yaml: "providers:\n  gemini:\n    api_key: k-gem-file\n"},
}

// schemesUnderTest is every registered kit scheme plus the catalog ids
// kit serves through an alias.
func schemesUnderTest() []string {
	out := append([]string(nil), kitllm.Schemes()...)
	for id := range catalogAliasCases {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// TestKeyResolution_AgreesWithKit is the delegation invariant: for every
// scheme and every key source, the URI foo hands kit carries exactly the
// key kit's ApplyAPIKey resolves, and foo refuses exactly when kit
// reports the key missing.
func TestKeyResolution_AgreesWithKit(t *testing.T) {
	ctx := context.Background()
	for name, tc := range keyCases {
		t.Run(name, func(t *testing.T) {
			clearProviderKeys(t)
			writeLLMYAML(t, tc.yaml)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			for _, scheme := range schemesUnderTest() {
				uri := scheme + "://some-model"
				kitURI, kitErr := kitllm.ApplyAPIKey(ctx, nil, uri)
				got, err := resolvedURIForModel(uri)

				if errors.Is(kitErr, kitllm.ErrMissingKey) != (err != nil) {
					t.Errorf("%s: kit missing=%v, foo err=%v", scheme, errors.Is(kitErr, kitllm.ErrMissingKey), err)
					continue
				}
				if err != nil {
					continue
				}
				want := parseOrFatal(t, kitURI).Params["api_key"]
				if k := parseOrFatal(t, got).Params["api_key"]; k != want {
					t.Errorf("%s: foo sends api_key %q, kit resolves %q", scheme, k, want)
				}
			}
		})
	}
}

// TestAuthIndex_AgreesWithRun: `provider show` and the model-list filter
// report "has a key" exactly when a run with that scheme would pass its
// precheck, for every key source.
func TestAuthIndex_AgreesWithRun(t *testing.T) {
	for name, tc := range keyCases {
		t.Run(name, func(t *testing.T) {
			clearProviderKeys(t)
			writeLLMYAML(t, tc.yaml)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			store := stubStore()
			idx := NewAuthIndexFrom(context.Background(), catalogEnv, store)
			for _, scheme := range schemesUnderTest() {
				if got, want := idx.Lookup(scheme).Satisfied(), precheckPasses(store, scheme); got != want {
					t.Errorf("%s: index satisfied = %v, run precheck passes = %v", scheme, got, want)
				}
			}
		})
	}
}

// TestKitSeesFoosRegistry: kit reads catalog facts through llm.Default;
// foo hands it the registry the listing and the picker use, so all three
// read one catalog cache.
func TestKitSeesFoosRegistry(t *testing.T) {
	reg, err := kitllm.Default(context.Background())
	if err != nil {
		t.Fatalf("kitllm.Default: %v", err)
	}
	if reg != ensureRegistry() {
		t.Error("kit's default registry is not foo's shared registry")
	}
}

// fetchCounter is an aim source that counts fetches and serves
// nothing: any call to it from a run path is a models.dev fetch foo
// added.
type fetchCounter struct{ fetches *atomic.Int32 }

func (s fetchCounter) Fetch(context.Context) (map[string]*aim.Provider, error) {
	s.fetches.Add(1)
	return nil, errors.New("fetchCounter: no network in tests")
}

// useKitCatalog hands kit a registry over a catalog cache holding
// providers, whose source counts fetches, for one test.
func useKitCatalog(t *testing.T, providers map[string]*aim.Provider) *atomic.Int32 {
	t.Helper()
	dir := t.TempDir()
	if providers != nil {
		data, err := json.Marshal(providers)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "models-dev.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fetches := &atomic.Int32{}
	reg := aim.NewRegistry(
		aim.WithSource(fetchCounter{fetches: fetches}),
		aim.WithCacheOpts(aim.WithCacheDir(dir)),
	)
	kitllm.SetDefaultRegistry(func(context.Context) (*aim.Registry, error) { return reg, nil })
	t.Cleanup(func() { kitllm.SetDefaultRegistry(sharedRegistry) })
	return fetches
}

// TestRunPath_ReadsCatalogCacheWithoutFetching: a catalog provider no
// adapter registers by name (digitalocean, routed by protocol) takes its
// key from the cached catalog's facts, and neither key resolution nor
// client construction fetches the catalog — warm cache or cold.
func TestRunPath_ReadsCatalogCacheWithoutFetching(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "fake-do-token")
	const model = "digitalocean://some-model"

	fetches := useKitCatalog(t, map[string]*aim.Provider{
		"digitalocean": {
			ID: "digitalocean", Name: "DigitalOcean",
			Env: []string{"DIGITALOCEAN_ACCESS_TOKEN"},
			NPM: "@ai-sdk/openai-compatible",
			API: "https://127.0.0.1:9/v1",
		},
	})
	got, err := resolvedURIForModel(model)
	if err != nil {
		t.Fatalf("resolvedURIForModel: %v", err)
	}
	if k := parseOrFatal(t, got).Params["api_key"]; k != "fake-do-token" {
		t.Errorf("api_key = %q, want the catalog's key variable's value", k)
	}
	if _, err := NewClient(context.Background(), ClientOpts{Model: model}); err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if n := fetches.Load(); n != 0 {
		t.Errorf("warm cache: run path fetched the catalog %d time(s)", n)
	}

	cold := useKitCatalog(t, nil)
	if _, err := resolvedURIForModel("openrouter://vendor/model?api_key=fake"); err != nil {
		t.Fatalf("cold cache, registered scheme: %v", err)
	}
	_, _ = resolvedURIForModel(model) // unroutable without a catalog: kit's call
	if n := cold.Load(); n != 0 {
		t.Errorf("cold cache: run path fetched the catalog %d time(s)", n)
	}
}
