package llm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"hop.top/aim"
	"hop.top/kit/go/console/output"
	"hop.top/kit/go/storage/secret"
	envstore "hop.top/kit/go/storage/secret/env"
)

// An empty key is no key. A variable exported as "", an empty secret,
// `api_key: ""` in llm.yaml, and an api_key_env naming an empty
// variable must read "missing" on every surface that asks: `foo
// provider show` (Lookup().Status()), the `foo model list` reachability
// filter (FilterReachable), and a run's key precheck (applyKey). Kit's
// own lookup already skips empty values; the defect was foo's catalog
// branch accepting an empty value from the env-backed secret store.
//
// Whitespace is a value: kit sends "  " as written, so it counts as
// set on every surface too. What matters is that the surfaces agree.

// emptyKeyKinds are the provider shapes a key check can take, each
// served from an in-test catalog: openai has its own adapter,
// fixturecompat is a catalog provider kit reaches through its protocol,
// fixturecloud is a catalog provider no adapter serves.
var emptyKeyKinds = []struct {
	provider string
	envVar   string
	// routed: a kit adapter serves the provider, so a run can reach it
	// and kit's key plan (llm.yaml, LLM_API_KEY) applies.
	routed bool
}{
	{"openai", "OPENAI_API_KEY", true},
	{"fixturecompat", "FIXTURECOMPAT_API_KEY", true},
	{"fixturecloud", "FIXTURECLOUD_API_KEY", false},
}

// emptyKeyCatalog is the cached catalog behind emptyKeyKinds. Neither
// base URL is on loopback: a loopback provider's key is optional.
func emptyKeyCatalog() map[string]*aim.Provider {
	return map[string]*aim.Provider{
		"openai": {ID: "openai", Name: "OpenAI", Env: []string{"OPENAI_API_KEY"}, API: "https://api.openai.com/v1"},
		"fixturecompat": {
			ID: "fixturecompat", Name: "Fixture Compat",
			Env: []string{"FIXTURECOMPAT_API_KEY"},
			NPM: "@ai-sdk/openai-compatible",
			API: "https://fixturecompat.invalid/v1",
		},
		"fixturecloud": {
			ID: "fixturecloud", Name: "Fixture Cloud",
			Env: []string{"FIXTURECLOUD_API_KEY"},
			API: "https://fixturecloud.invalid/v1",
		},
	}
}

// keyState is the value a source holds; unset means the source holds
// nothing at all (variable unexported, no secret, no llm.yaml field).
type keyState struct {
	name  string
	unset bool
	value string
	// present: the value is a key every surface must accept.
	present bool
}

var keyStates = []keyState{
	{name: "unset", unset: true},
	{name: "empty", value: ""},
	{name: "whitespace", value: "  ", present: true},
	{name: "set", value: "fake-key", present: true},
}

// keySource places a key state on one machine. It returns the secret
// store a run would read. env names the variable to hold the key.
type keySource struct {
	name string
	// kitOnly: the source is part of kit's key plan, which applies only
	// to a provider an adapter serves.
	kitOnly bool
	apply   func(t *testing.T, xdg, provider, envVar string, st keyState) secret.Store
}

// envStore is foo's default store (`secrets.backend: env`, no prefix):
// it answers a set-but-empty variable with an empty secret rather than
// "not found", which is how the defect reached `provider show`.
func envStore() secret.Store { return envstore.New("") }

var keySources = []keySource{
	{name: "env, env-backed store", apply: func(t *testing.T, _, _, envVar string, st keyState) secret.Store {
		setState(t, envVar, st)
		return envStore()
	}},
	{name: "env, no store", apply: func(t *testing.T, _, _, envVar string, st keyState) secret.Store {
		setState(t, envVar, st)
		return nil
	}},
	{name: "env, store without it", apply: func(t *testing.T, _, _, envVar string, st keyState) secret.Store {
		setState(t, envVar, st)
		return newFakeStore()
	}},
	{name: "secret store", apply: func(_ *testing.T, _, _, envVar string, st keyState) secret.Store {
		if st.unset {
			return newFakeStore()
		}
		return newFakeStore(SecretName(envVar), st.value)
	}},
	{name: "LLM_API_KEY", kitOnly: true, apply: func(t *testing.T, _, _, _ string, st keyState) secret.Store {
		setState(t, "LLM_API_KEY", st)
		return envStore()
	}},
	{name: "llm.yaml api_key", kitOnly: true, apply: func(t *testing.T, xdg, provider, _ string, st keyState) secret.Store {
		if !st.unset {
			writeLLMConfig(t, xdg, fmt.Sprintf("providers:\n  %s:\n    api_key: %q\n", provider, st.value))
		}
		return envStore()
	}},
	{name: "llm.yaml api_key_env", kitOnly: true, apply: func(t *testing.T, xdg, provider, _ string, st keyState) secret.Store {
		writeLLMConfig(t, xdg, fmt.Sprintf("providers:\n  %s:\n    api_key_env: FOO_TEST_NAMED_KEY\n", provider))
		setState(t, "FOO_TEST_NAMED_KEY", st)
		return envStore()
	}},
}

// setState exports name per st, restoring the caller's value after.
func setState(t *testing.T, name string, st keyState) {
	t.Helper()
	t.Setenv(name, st.value)
	if st.unset {
		unsetForTest(t, name)
	}
}

// unsetForTest unexports name for the test; t.Setenv first, so the
// caller's value comes back afterwards.
func unsetForTest(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
}

// TestEmptyKey_SurfacesAgree is the matrix: every provider kind, under
// every key source, in every key state. provider show, the model list
// filter and the run's precheck must all reach the same verdict, and
// that verdict is "missing" for an unset or empty key.
func TestEmptyKey_SurfacesAgree(t *testing.T) {
	for _, kind := range emptyKeyKinds {
		for _, src := range keySources {
			for _, st := range keyStates {
				t.Run(kind.provider+"/"+src.name+"/"+st.name, func(t *testing.T) {
					xdg := unsetKeyEnv(t)
					unsetForTest(t, "FOO_TEST_NAMED_KEY")
					useKitCatalog(t, emptyKeyCatalog())
					store := src.apply(t, xdg, kind.provider, kind.envVar, st)

					// A source outside kit's key plan cannot key a
					// provider no adapter serves: no run reaches it.
					want := st.present && (kind.routed || !src.kitOnly)
					wantStatus := map[bool]string{true: "configured", false: "missing"}[want]

					idx := NewAuthIndexFrom(context.Background(), map[string][]string{
						"openai":        {"OPENAI_API_KEY"},
						"fixturecompat": {"FIXTURECOMPAT_API_KEY"},
						"fixturecloud":  {"FIXTURECLOUD_API_KEY"},
					}, store)

					// foo provider show
					if got := idx.Lookup(kind.provider).Status(); got != wantStatus {
						t.Errorf("provider show: status = %q, want %q", got, wantStatus)
					}

					// foo model list (default view)
					row := entryFromAim(aim.Model{Provider: kind.provider, ID: "some-model"})
					if row.Routable != kind.routed {
						t.Fatalf("Routable = %v, want %v: the in-test catalog did not reach kit", row.Routable, kind.routed)
					}
					kept, _ := FilterReachable([]ModelEntry{row}, idx)
					if visible := len(kept) == 1; visible != (want && kind.routed) {
						t.Errorf("model list: visible = %v, want %v", visible, want && kind.routed)
					}

					// the run's key precheck
					uri, err := applyKey(context.Background(), store, kind.provider+"://some-model")
					if !kind.routed {
						// kit lends no credential to a host it cannot
						// reach; the run fails at Resolve instead.
						if err != nil {
							t.Fatalf("applyKey on an unrouted scheme: %v", err)
						}
						if p := parseOrFatal(t, uri).Params; p["api_key"] != "" {
							t.Errorf("unrouted scheme was given a key")
						}
						if _, err := NewClient(context.Background(), ClientOpts{Model: kind.provider + "://some-model", Secrets: store}); err == nil {
							t.Error("run on a provider no adapter serves succeeded")
						}
						return
					}
					if want {
						if err != nil {
							t.Fatalf("precheck refused a present key: %v", err)
						}
						if k := parseOrFatal(t, uri).Params["api_key"]; k != st.value {
							t.Errorf("api_key = %q, want %q", k, st.value)
						}
						return
					}
					var outErr *output.Error
					if !errors.As(err, &outErr) || outErr.Code != output.UnauthorizedError("").Code {
						t.Fatalf("precheck: err = %v, want the missing-key error (exit 5)", err)
					}
				})
			}
		}
	}
}

// TestEmptyKey_ConfiguredProvidersSkipsEmpty: the "nothing reachable"
// guidance lists configured providers; an empty variable must not put
// its provider on that list.
func TestEmptyKey_ConfiguredProvidersSkipsEmpty(t *testing.T) {
	unsetKeyEnv(t)
	useKitCatalog(t, emptyKeyCatalog())
	t.Setenv("FIXTURECLOUD_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "fake-oa")
	idx := NewAuthIndexFrom(context.Background(), map[string][]string{
		"openai":       {"OPENAI_API_KEY"},
		"fixturecloud": {"FIXTURECLOUD_API_KEY"},
	}, envStore())
	if got := idx.ConfiguredProviders(); len(got) != 1 || got[0] != "openai" {
		t.Errorf("ConfiguredProviders = %v, want [openai]", got)
	}
}
