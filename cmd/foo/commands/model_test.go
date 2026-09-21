package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"hop.top/foo/internal/llm"
)

// stubCatalog feeds runModelList canned rows so the command path is
// exercised without a models.dev fetch.
type stubCatalog struct {
	entries []llm.ModelEntry
	err     error
}

func (s stubCatalog) ListModels(context.Context) ([]llm.ModelEntry, error) {
	return s.entries, s.err
}

// withCatalog swaps the package-level source for one test and restores
// it, so ordering between tests cannot leak a fixture.
//
// It pins the credential index too. The default view hides models whose
// provider has no API key, so without this every assertion below would
// depend on which keys happen to be exported on the machine running the
// suite — green on a laptop with an ANTHROPIC_API_KEY, red in CI. The
// pinned index satisfies every provider, which is the pre-filter
// behaviour these tests were written against; the tests that exercise
// the filter itself call withAuth to say otherwise.
func withCatalog(t *testing.T, src llm.CatalogSource) {
	t.Helper()
	prev := modelCatalogSource
	modelCatalogSource = src
	t.Cleanup(func() { modelCatalogSource = prev })
	withAuth(t, nil)
}

// withAuth pins the credential index for one test. envByProvider maps a
// provider id to the env var names it accepts; a provider absent from
// the map declares no requirement and is therefore satisfied. nil means
// "no provider requires a credential", i.e. nothing is ever hidden.
//
// Keys resolve from an explicit set rather than the process environment,
// so a test says which credentials exist instead of inheriting the
// operator's.
func withAuth(t *testing.T, envByProvider map[string][]string, configured ...string) {
	t.Helper()
	have := make(map[string]bool, len(configured))
	for _, key := range configured {
		have[llm.SecretName(key)] = true
	}
	lookup := func(_ context.Context, key string) (string, bool, error) {
		if have[key] {
			return "stub-value", true, nil
		}
		return "", false, nil
	}
	prev := providerAuthIndex
	providerAuthIndex = func(ctx context.Context) (*llm.AuthIndex, error) {
		return llm.NewAuthIndexFrom(ctx, envByProvider, lookup), nil
	}
	t.Cleanup(func() { providerAuthIndex = prev })
}

// sampleEntries returns n routable rows across two providers.
func sampleEntries() []llm.ModelEntry {
	return []llm.ModelEntry{
		{Source: llm.SourceCatalog, Provider: "anthropic", ID: "claude-x", Context: 200000, ToolCall: true, Reasoning: true, Released: "2026-01-01", Routable: true},
		{Source: llm.SourceCatalog, Provider: "openai", ID: "gpt-x", Context: 128000, ToolCall: true, Released: "2026-02-02", Routable: true},
		{Source: llm.SourceCatalog, Provider: "anthropic", ID: "claude-y", Context: 100000, Released: "2025-01-01", Routable: true},
		{Source: llm.SourceCatalog, Provider: "openai", ID: "gpt-y", Context: 8192, Released: "2024-02-02", Routable: true},
	}
}

// runList drives `foo model list` end to end through the real root
// command and returns stdout and stderr separately — the split matters,
// because the truncation hint must not land on stdout where it would
// corrupt piped JSON.
//
// It goes through New() rather than modelCmd() alone because renderData
// dispatches via root.Viper: a bare subcommand has no runtime behind it
// and panics. Driving the real tree also means these tests fail if the
// command is ever unregistered.
func runList(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	r := New("test")
	var out, errOut bytes.Buffer
	r.Cmd.SetOut(&out)
	r.Cmd.SetErr(&errOut)
	r.Cmd.SetArgs(append([]string{"model", "list"}, args...))
	err = r.Cmd.Execute()
	return out.String(), errOut.String(), err
}

// TestModelList_DefaultViewIsTruncatedWithHint is the headline
// behavior: the default view must not dump the catalog, and must tell
// the reader how to see the rest.
func TestModelList_DefaultViewIsTruncatedWithHint(t *testing.T) {
	// More rows than the default limit, all from one provider so the
	// count is easy to reason about.
	var many []llm.ModelEntry
	for i := 0; i < llm.DefaultListLimit+7; i++ {
		many = append(many, llm.ModelEntry{
			Source: llm.SourceCatalog, Provider: "openai",
			ID: string(rune('a'+i)) + "-model", Routable: true,
		})
	}
	withCatalog(t, stubCatalog{entries: many})

	stdout, stderr, err := runList(t)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// Header + DefaultListLimit rows.
	if got, want := strings.Count(strings.TrimSpace(stdout), "\n")+1, llm.DefaultListLimit+1; got != want {
		t.Errorf("stdout lines: got %d, want %d (header + %d rows)", got, want, llm.DefaultListLimit)
	}
	if !strings.Contains(stderr, "7 more model(s) not shown") {
		t.Errorf("stderr missing omitted count, got %q", stderr)
	}
	if !strings.Contains(stderr, "--limit") {
		t.Errorf("hint must name the flag that widens the view, got %q", stderr)
	}
	if strings.Contains(stdout, "not shown") {
		t.Error("truncation hint leaked onto stdout; it must go to stderr")
	}
}

// TestModelList_NoHintWhenNothingOmitted guards the opposite case: a
// short catalog must not claim models were hidden.
func TestModelList_NoHintWhenNothingOmitted(t *testing.T) {
	withCatalog(t, stubCatalog{entries: sampleEntries()})
	_, stderr, err := runList(t)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("no rows omitted, want empty stderr, got %q", stderr)
	}
}

// TestModelList_DefaultColumns pins the table contract: the columns a
// person needs to pick a model must be present.
func TestModelList_DefaultColumns(t *testing.T) {
	withCatalog(t, stubCatalog{entries: sampleEntries()})
	stdout, _, err := runList(t)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	header := strings.SplitN(stdout, "\n", 2)[0]
	for _, col := range []string{"PROVIDER", "ID", "CONTEXT", "TOOLS"} {
		if !strings.Contains(header, col) {
			t.Errorf("header missing %q column: %q", col, header)
		}
	}
	// The id must be printed verbatim: it is copy-pasted straight
	// into `foo model default`.
	if !strings.Contains(stdout, "claude-x") {
		t.Errorf("model id absent from output: %q", stdout)
	}
}

// TestModelList_RankedOrder proves the command renders ranked rows, not
// the source's arrival order.
func TestModelList_RankedOrder(t *testing.T) {
	withCatalog(t, stubCatalog{entries: sampleEntries()})
	stdout, _, err := runList(t)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// Grouped by provider, providers and ids alphabetical:
	// anthropic/claude-x, anthropic/claude-y, openai/gpt-x, openai/gpt-y.
	want := []string{"claude-x", "claude-y", "gpt-x", "gpt-y"}
	at := -1
	for _, id := range want {
		i := strings.Index(stdout, id)
		if i < 0 {
			t.Fatalf("%q missing from output %q", id, stdout)
		}
		if i < at {
			t.Fatalf("%q out of rank order in %q", id, stdout)
		}
		at = i
	}
}

// TestModelList_JSONFormat proves the command renders through the
// shared dispatch helper, so --format works like every other surface,
// and that the machine format carries the provenance field the table
// omits.
func TestModelList_JSONFormat(t *testing.T) {
	withCatalog(t, stubCatalog{entries: sampleEntries()})
	stdout, _, err := runList(t, "--format=json")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var rows []struct {
		Provider string `json:"provider"`
		ID       string `json:"id"`
		Context  int    `json:"context"`
		ToolCall bool   `json:"tool_call"`
		Source   string `json:"source"`
	}
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("stdout is not valid JSON (%v): %q", err, stdout)
	}
	if len(rows) != 4 {
		t.Fatalf("rows: got %d, want 4", len(rows))
	}
	if rows[0].Provider != "anthropic" || rows[0].ID != "claude-x" {
		t.Errorf("first row: got %+v", rows[0])
	}
	if rows[0].Context != 200000 || !rows[0].ToolCall {
		t.Errorf("first row fields: got %+v", rows[0])
	}
	if rows[0].Source != string(llm.SourceCatalog) {
		t.Errorf("source: got %q, want %q", rows[0].Source, llm.SourceCatalog)
	}
}

// TestModelList_YAMLAndCSVFormats smokes the remaining two formats
// dispatch is expected to serve.
func TestModelList_YAMLAndCSVFormats(t *testing.T) {
	for _, tc := range []struct{ format, want string }{
		{"yaml", "provider: anthropic"},
		{"csv", "PROVIDER,ID,CONTEXT"},
	} {
		t.Run(tc.format, func(t *testing.T) {
			withCatalog(t, stubCatalog{entries: sampleEntries()})
			stdout, _, err := runList(t, "--format="+tc.format)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if !strings.Contains(stdout, tc.want) {
				t.Errorf("%s output missing %q: %q", tc.format, tc.want, stdout)
			}
		})
	}
}

// TestModelList_LimitFlag covers the one knob the command owns,
// including the documented "0 means everything" escape.
func TestModelList_LimitFlag(t *testing.T) {
	t.Run("explicit limit", func(t *testing.T) {
		withCatalog(t, stubCatalog{entries: sampleEntries()})
		stdout, stderr, err := runList(t, "--limit=2", "--format=csv")
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		// header + 2 rows
		if got := strings.Count(strings.TrimSpace(stdout), "\n") + 1; got != 3 {
			t.Errorf("lines: got %d, want 3: %q", got, stdout)
		}
		if !strings.Contains(stderr, "2 more model(s)") {
			t.Errorf("stderr: %q", stderr)
		}
	})

	t.Run("zero means all", func(t *testing.T) {
		withCatalog(t, stubCatalog{entries: sampleEntries()})
		stdout, stderr, err := runList(t, "--limit=0", "--format=csv")
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		if got := strings.Count(strings.TrimSpace(stdout), "\n") + 1; got != 5 {
			t.Errorf("lines: got %d, want 5 (header + 4): %q", got, stdout)
		}
		if strings.TrimSpace(stderr) != "" {
			t.Errorf("no truncation, want empty stderr, got %q", stderr)
		}
	})
}

// TestModelList_EmptyCatalog: a source that legitimately knows nothing
// must render an empty result rather than erroring or claiming
// omissions.
func TestModelList_EmptyCatalog(t *testing.T) {
	withCatalog(t, stubCatalog{entries: nil})
	stdout, stderr, err := runList(t, "--format=json")
	if err != nil {
		t.Fatalf("empty catalog must not error: %v", err)
	}
	var rows []modelRow
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("empty catalog must emit valid JSON, got %q (%v)", stdout, err)
	}
	if len(rows) != 0 {
		t.Errorf("rows: got %d, want 0", len(rows))
	}
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("want no hint for empty catalog, got %q", stderr)
	}
}

// TestModelList_SourceErrorSurfaces: a catalog read failure must reach
// the user, not render an empty table that looks like "no models".
func TestModelList_SourceErrorSurfaces(t *testing.T) {
	withCatalog(t, stubCatalog{err: context.DeadlineExceeded})
	_, _, err := runList(t)
	if err == nil {
		t.Fatal("expected catalog error to surface as a command error")
	}
}

// TestModelListCmd_Wiring asserts the command is reachable at
// `foo model list` and declares itself a read. A green unit test on
// runModelList proves nothing if the subcommand was never registered.
func TestModelListCmd_Wiring(t *testing.T) {
	parent := modelCmd()
	var found *cobra.Command
	for _, sub := range parent.Commands() {
		if sub.Name() == "list" {
			found = sub
		}
	}
	if found == nil {
		t.Fatal("`list` not registered under `model`")
	}
	if found.Flags().Lookup("limit") == nil {
		t.Error("--limit flag not declared")
	}
	if found.Short == "" {
		t.Error("Short help is required for the command listing")
	}
	// Annotation key is kit's; assert the command is marked read-only
	// rather than left unannotated.
	if len(found.Annotations) == 0 {
		t.Error("no side-effect annotation set; want SideEffectRead")
	}
}

// captureFilter swaps the filtered-source factory for one that records
// the filter the command assembled and returns canned rows. It is the
// only way to assert flag wiring without a models.dev fetch: filtering
// is pushed down into the source, so the filter is not observable on
// the rendered rows.
func captureFilter(t *testing.T, entries []llm.ModelEntry) *llm.Filter {
	t.Helper()
	var got llm.Filter
	prevFactory := modelFilteredSource
	modelFilteredSource = func(f llm.Filter) llm.CatalogSource {
		got = f
		return stubCatalog{entries: entries}
	}
	// The injected source short-circuits the factory, so it must be
	// cleared for the filtered path to be reached at all.
	prevSrc := modelCatalogSource
	modelCatalogSource = nil
	t.Cleanup(func() {
		modelFilteredSource = prevFactory
		modelCatalogSource = prevSrc
	})
	// Same reason withCatalog does it: these tests assert on flag
	// wiring and display, and must not also depend on which API keys
	// the machine running them happens to export.
	withAuth(t, nil)
	return &got
}

// TestModelList_FilterFlagsRegistered fails if a flag is dropped from
// the surface: a green filter unit test proves nothing if the flag was
// never declared.
func TestModelList_FilterFlagsRegistered(t *testing.T) {
	parent := modelCmd()
	var list *cobra.Command
	for _, sub := range parent.Commands() {
		if sub.Name() == "list" {
			list = sub
		}
	}
	if list == nil {
		t.Fatal("`list` not registered")
	}
	for _, name := range []string{
		"provider", "family", "in", "out", "query",
		"tool-call", "reasoning", "open-weights", "structured-output",
	} {
		f := list.Flags().Lookup(name)
		if f == nil {
			t.Errorf("--%s not declared", name)
			continue
		}
		if f.Usage == "" {
			t.Errorf("--%s has no usage string", name)
		}
	}
}

// TestModelList_UnsetCapabilitiesStayNil is the regression test for the
// tristate trap. A bare `foo model list` must send nil for every
// capability: a non-nil false would silently drop every model lacking
// that capability, on every invocation, with nothing on screen to say
// so.
func TestModelList_UnsetCapabilitiesStayNil(t *testing.T) {
	// A filter is needed to reach the factory at all, so set a
	// non-capability field and assert the capabilities stay untouched.
	got := captureFilter(t, sampleEntries())
	if _, _, err := runList(t, "--provider=anthropic"); err != nil {
		t.Fatalf("execute: %v", err)
	}
	for _, c := range []struct {
		name string
		val  *bool
	}{
		{"tool-call", got.ToolCall},
		{"reasoning", got.Reasoning},
		{"open-weights", got.OpenWeights},
		{"structured-output", got.StructuredOutput},
	} {
		if c.val != nil {
			t.Errorf("--%s unset but lowered to %v; every invocation "+
				"would filter on it", c.name, *c.val)
		}
	}
}

// TestModelList_CapabilityTristates walks all three states for every
// capability flag through the real command line.
func TestModelList_CapabilityTristates(t *testing.T) {
	read := map[string]func(llm.Filter) *bool{
		"tool-call":         func(f llm.Filter) *bool { return f.ToolCall },
		"reasoning":         func(f llm.Filter) *bool { return f.Reasoning },
		"open-weights":      func(f llm.Filter) *bool { return f.OpenWeights },
		"structured-output": func(f llm.Filter) *bool { return f.StructuredOutput },
	}
	for flag, get := range read {
		t.Run(flag+"/unset", func(t *testing.T) {
			got := captureFilter(t, sampleEntries())
			if _, _, err := runList(t, "--provider=x"); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if v := get(*got); v != nil {
				t.Errorf("unset --%s: got %v, want nil", flag, *v)
			}
		})
		t.Run(flag+"/true", func(t *testing.T) {
			got := captureFilter(t, sampleEntries())
			if _, _, err := runList(t, "--"+flag); err != nil {
				t.Fatalf("execute: %v", err)
			}
			v := get(*got)
			if v == nil || !*v {
				t.Errorf("--%s: got %v, want true", flag, v)
			}
		})
		t.Run(flag+"/false", func(t *testing.T) {
			got := captureFilter(t, sampleEntries())
			if _, _, err := runList(t, "--"+flag+"=false"); err != nil {
				t.Fatalf("execute: %v", err)
			}
			v := get(*got)
			if v == nil {
				t.Fatalf("--%s=false lowered to nil; explicit false "+
					"must be distinguishable from unset", flag)
			}
			if *v {
				t.Errorf("--%s=false: got true", flag)
			}
		})
	}
}

// TestModelList_ScalarAndRepeatableFlags covers the non-tristate flags,
// including that --in/--out accumulate across repeats.
func TestModelList_ScalarAndRepeatableFlags(t *testing.T) {
	got := captureFilter(t, sampleEntries())
	_, _, err := runList(t,
		"--provider=openai", "--family=gpt-4",
		"--in=text", "--in=image", "--out=text")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.Provider != "openai" {
		t.Errorf("provider: got %q", got.Provider)
	}
	if got.Family != "gpt-4" {
		t.Errorf("family: got %q", got.Family)
	}
	if want := []string{"text", "image"}; !reflect.DeepEqual(got.Input, want) {
		t.Errorf("input: got %v, want %v (repeats must accumulate)", got.Input, want)
	}
	if want := []string{"text"}; !reflect.DeepEqual(got.Output, want) {
		t.Errorf("output: got %v, want %v", got.Output, want)
	}
}

// TestModelList_QueryFlagReachesFilter proves --query is carried rather
// than dropped.
func TestModelList_QueryFlagReachesFilter(t *testing.T) {
	got := captureFilter(t, sampleEntries())
	if _, _, err := runList(t, "--query=provider:anthropic reasoning:true"); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.Query != "provider:anthropic reasoning:true" {
		t.Errorf("query: got %q", got.Query)
	}
}

// TestModelList_BadQuerySurfacesError: a malformed expression must fail
// the command, naming the offending key, rather than quietly listing
// the unfiltered catalog.
func TestModelList_BadQuerySurfacesError(t *testing.T) {
	prev := modelCatalogSource
	modelCatalogSource = nil
	t.Cleanup(func() { modelCatalogSource = prev })

	_, _, err := runList(t, "--query=bogus:1")
	if err == nil {
		t.Fatal("expected error for unknown tag key")
	}
	if !strings.Contains(err.Error(), `unknown tag key "bogus"`) {
		t.Errorf("error must name the offending key verbatim, got %q", err)
	}
}

// TestModelList_NoFilterKeepsInjectedSource pins the fast path: with no
// filter flags the command must not build a filtered source, so the
// unfiltered behaviour stays byte-identical.
func TestModelList_NoFilterKeepsInjectedSource(t *testing.T) {
	called := false
	prevFactory := modelFilteredSource
	modelFilteredSource = func(llm.Filter) llm.CatalogSource {
		called = true
		return stubCatalog{}
	}
	t.Cleanup(func() { modelFilteredSource = prevFactory })

	withCatalog(t, stubCatalog{entries: sampleEntries()})
	if _, _, err := runList(t); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if called {
		t.Error("no filter flags given, but a filtered source was built")
	}
}

// TestModelList_FilteredRowsStillRankedAndTruncated proves filtering
// composes with the existing display pipeline rather than bypassing it.
func TestModelList_FilteredRowsStillRankedAndTruncated(t *testing.T) {
	captureFilter(t, sampleEntries())
	stdout, stderr, err := runList(t, "--provider=anthropic", "--limit=2", "--format=csv")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// header + 2 rows, ranked order preserved
	if got := strings.Count(strings.TrimSpace(stdout), "\n") + 1; got != 3 {
		t.Errorf("lines: got %d, want 3: %q", got, stdout)
	}
	if !strings.Contains(stderr, "2 more model(s)") {
		t.Errorf("truncation hint missing from stderr: %q", stderr)
	}
}

// endpointBody is a real /v1/models response, captured from a local
// ollama — the shape every OpenAI-compatible server returns.
const endpointBody = `{"object":"list","data":[` +
	`{"id":"qwen2.5:7b-instruct","object":"model","created":1787191418,"owned_by":"library"},` +
	`{"id":"llama3.2:3b","object":"model","created":1787184512,"owned_by":"library"}]}`

// serveEndpoint stands up a fake OpenAI-compatible server and returns
// the base URL to configure foo with.

// serveEndpoint stands up a fake OpenAI-compatible server and returns
// the base URL to configure foo with.
func serveEndpoint(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1"
}

// withLiveSourceSelection clears the fixture source so the command runs
// its real source-selection path, and neutralises any endpoint the
// developer has configured in their own llm.yaml.
//
// Without the second half these tests would pass or fail depending on
// whose machine they run on: a developer with providers.openai.base_url
// set would silently exercise endpoint mode in the catalog tests.

// withLiveSourceSelection clears the fixture source so the command runs
// its real source-selection path, and neutralises any endpoint the
// developer has configured in their own llm.yaml.
//
// Without the second half these tests would pass or fail depending on
// whose machine they run on: a developer with providers.openai.base_url
// set would silently exercise endpoint mode in the catalog tests.
func withLiveSourceSelection(t *testing.T) {
	t.Helper()
	prev := modelCatalogSource
	modelCatalogSource = nil
	t.Cleanup(func() { modelCatalogSource = prev })

	t.Setenv("LLM_BASE_URL", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

// TestModelList_EndpointFlagListsFromServer is the headline behavior of
// the live source: --endpoint lists what the server serves, not what
// models.dev knows. The catalog structurally cannot hold these ids.

// TestModelList_EndpointFlagListsFromServer is the headline behavior of
// the live source: --endpoint lists what the server serves, not what
// models.dev knows. The catalog structurally cannot hold these ids.
func TestModelList_EndpointFlagListsFromServer(t *testing.T) {
	withLiveSourceSelection(t)
	base := serveEndpoint(t, endpointBody)

	stdout, _, err := runList(t, "--endpoint="+base, "--format=json")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var rows []struct {
		Provider string `json:"provider"`
		ID       string `json:"id"`
		Source   string `json:"source"`
	}
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("not JSON (%v): %q", err, stdout)
	}
	if len(rows) != 2 {
		t.Fatalf("rows: got %d, want 2: %q", len(rows), stdout)
	}
	ids := []string{rows[0].ID, rows[1].ID}
	sort.Strings(ids)
	if ids[0] != "llama3.2:3b" || ids[1] != "qwen2.5:7b-instruct" {
		t.Errorf("ids = %v, want the server's inventory", ids)
	}
	for _, r := range rows {
		if r.Source != string(llm.SourceEndpoint) {
			t.Errorf("%s: source = %q, want %q", r.ID, r.Source, llm.SourceEndpoint)
		}
	}
}

// TestModelList_EndpointShowsSourceColumn covers requirement 3 on the
// human surface: with an endpoint in play the origin of each row must be
// visible in the table, not only in json/yaml. An endpoint row's blank
// CONTEXT means "not reported", and the reader needs to know that.

// TestModelList_EndpointShowsSourceColumn covers requirement 3 on the
// human surface: with an endpoint in play the origin of each row must be
// visible in the table, not only in json/yaml. An endpoint row's blank
// CONTEXT means "not reported", and the reader needs to know that.
func TestModelList_EndpointShowsSourceColumn(t *testing.T) {
	withLiveSourceSelection(t)
	base := serveEndpoint(t, endpointBody)

	stdout, _, err := runList(t, "--endpoint="+base)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	header := strings.SplitN(stdout, "\n", 2)[0]
	if !strings.Contains(header, "SOURCE") {
		t.Errorf("endpoint listing must show a SOURCE column, header was %q", header)
	}
	if !strings.Contains(stdout, string(llm.SourceEndpoint)) {
		t.Errorf("endpoint rows must be labelled %q in the table: %q", llm.SourceEndpoint, stdout)
	}
}

// TestModelList_CatalogViewHasNoSourceColumn is the other half: with a
// single source wired the column would repeat one value on every row, so
// it stays off.

// TestModelList_CatalogViewHasNoSourceColumn is the other half: with a
// single source wired the column would repeat one value on every row, so
// it stays off.
func TestModelList_CatalogViewHasNoSourceColumn(t *testing.T) {
	withCatalog(t, stubCatalog{entries: sampleEntries()})
	stdout, _, err := runList(t)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if strings.Contains(strings.SplitN(stdout, "\n", 2)[0], "SOURCE") {
		t.Errorf("catalog-only listing should not carry a SOURCE column: %q", stdout)
	}
}

// TestModelList_UnreachableEndpointErrorsNamingURL is the failure the
// brief singles out: a down ssh tunnel must not render as an empty list
// that reads "this server has no models".

// TestModelList_UnreachableEndpointErrorsNamingURL is the failure the
// brief singles out: a down ssh tunnel must not render as an empty list
// that reads "this server has no models".
func TestModelList_UnreachableEndpointErrorsNamingURL(t *testing.T) {
	withLiveSourceSelection(t)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL + "/v1"
	srv.Close()

	stdout, _, err := runList(t, "--endpoint="+base)
	if err == nil {
		t.Fatalf("unreachable endpoint must error, got output %q", stdout)
	}
	if !strings.Contains(err.Error(), base) {
		t.Errorf("error must name the endpoint URL\n error: %v\n want: %s", err, base)
	}
	if strings.Contains(stdout, "PROVIDER") {
		t.Errorf("unreachable endpoint rendered a table: %q", stdout)
	}
}

// TestModelList_ConfiguredEndpointIsUsedWithoutFlag covers requirement
// 2: with no --endpoint, a configured endpoint is resolved through the
// same ladder the completion path uses.

// TestModelList_ConfiguredEndpointIsUsedWithoutFlag covers requirement
// 2: with no --endpoint, a configured endpoint is resolved through the
// same ladder the completion path uses.
func TestModelList_ConfiguredEndpointIsUsedWithoutFlag(t *testing.T) {
	withLiveSourceSelection(t)
	base := serveEndpoint(t, endpointBody)
	t.Setenv("LLM_BASE_URL", base)

	stdout, _, err := runList(t, "--format=json")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(stdout, string(llm.SourceEndpoint)) {
		t.Errorf("configured endpoint was not used; got %q", stdout)
	}
	if !strings.Contains(stdout, "llama3.2:3b") {
		t.Errorf("configured endpoint's inventory absent: %q", stdout)
	}
}

// TestModelList_ExplicitEndpointBeatsConfigured pins the precedence:
// --endpoint is the per-invocation lever and outranks the configured
// one, mirroring how ?base_url= outranks llm.yaml and LLM_BASE_URL on
// the completion path.

// TestModelList_ExplicitEndpointBeatsConfigured pins the precedence:
// --endpoint is the per-invocation lever and outranks the configured
// one, mirroring how ?base_url= outranks llm.yaml and LLM_BASE_URL on
// the completion path.
func TestModelList_ExplicitEndpointBeatsConfigured(t *testing.T) {
	withLiveSourceSelection(t)
	configured := serveEndpoint(t, `{"data":[{"id":"from-config"}]}`)
	explicit := serveEndpoint(t, `{"data":[{"id":"from-flag"}]}`)
	t.Setenv("LLM_BASE_URL", configured)

	stdout, _, err := runList(t, "--endpoint="+explicit, "--format=json")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(stdout, "from-flag") {
		t.Errorf("--endpoint did not win: %q", stdout)
	}
	if strings.Contains(stdout, "from-config") {
		t.Errorf("configured endpoint leaked into an explicit --endpoint run: %q", stdout)
	}
}

// TestModelList_FallsBackToCatalogWithNoEndpoint is requirement 2's
// negative case: no endpoint configured anywhere means the catalog, and
// the command must not start probing localhost on its own.

// TestModelList_FallsBackToCatalogWithNoEndpoint is requirement 2's
// negative case: no endpoint configured anywhere means the catalog, and
// the command must not start probing localhost on its own.
func TestModelList_FallsBackToCatalogWithNoEndpoint(t *testing.T) {
	withLiveSourceSelection(t)

	src, endpoint := modelListSource(llm.Filter{}, "", false)
	if endpoint != "" {
		t.Errorf("resolved endpoint %q with none configured", endpoint)
	}
	if src != nil {
		t.Errorf("want nil source (the catalog default), got %T", src)
	}
}

// TestModelList_CatalogOnlyFlagRejectedWithEndpoint covers requirement
// 4. /v1/models has no metadata to filter on, so the combination is
// refused by name rather than silently returning everything or printing
// empty columns.
//
// The filter flags land in a concurrent change; this drives the check
// through a flag registered on the fly so the rejection logic is proven
// now and picks up the real flags as they are added to catalogOnlyFlags.

// TestModelList_CatalogOnlyFlagRejectedWithEndpoint covers requirement
// 4. /v1/models has no metadata to filter on, so the combination is
// refused by name rather than silently returning everything or printing
// empty columns.
//
// Driven through the real command surface rather than a stand-in flag:
// the filter flags exist now, so the guard is exercised against what
// ships. TestCatalogOnlyFlags_CoversEveryFilterFlag keeps the list and
// the flag surface from drifting apart.
func TestModelList_CatalogOnlyFlagRejectedWithEndpoint(t *testing.T) {
	cmd := modelListCmd()

	// Not passed: nothing to reject.
	if err := checkCatalogOnlyFlags(cmd); err != nil {
		t.Fatalf("unset flag must not be rejected: %v", err)
	}

	if err := cmd.Flags().Set("provider", "openai"); err != nil {
		t.Fatalf("set: %v", err)
	}
	err := checkCatalogOnlyFlags(cmd)
	if err == nil {
		t.Fatal("--provider with --endpoint must be rejected")
	}
	if !errors.Is(err, llm.ErrEndpointFlagUnsupported) {
		t.Errorf("error does not unwrap to ErrEndpointFlagUnsupported: %v", err)
	}
	if !strings.Contains(err.Error(), "--provider") {
		t.Errorf("error must name the offending flag, got: %v", err)
	}
}

// TestModelList_CatalogOnlyFlagAllowedWithoutEndpoint guards the
// converse: the same flag against the catalog is exactly what it is for.

// TestModelList_CatalogOnlyFlagAllowedWithoutEndpoint guards the
// converse: the same flag against the catalog is exactly what it is for.
func TestModelList_CatalogOnlyFlagAllowedWithoutEndpoint(t *testing.T) {
	withCatalog(t, stubCatalog{entries: sampleEntries()})
	if _, _, err := runList(t); err != nil {
		t.Fatalf("catalog listing must not be affected by the endpoint check: %v", err)
	}
}

// TestModelListCmd_EndpointFlagWiring asserts the flag is actually
// declared; a green test on the resolver proves nothing if the flag
// never reaches the command.

// TestModelListCmd_EndpointFlagWiring asserts the flag is actually
// declared; a green test on the resolver proves nothing if the flag
// never reaches the command.
func TestModelListCmd_EndpointFlagWiring(t *testing.T) {
	parent := modelCmd()
	var list *cobra.Command
	for _, sub := range parent.Commands() {
		if sub.Name() == "list" {
			list = sub
		}
	}
	if list == nil {
		t.Fatal("`list` not registered under `model`")
	}
	f := list.Flags().Lookup("endpoint")
	if f == nil {
		t.Fatal("--endpoint flag not declared")
	}
	if f.Usage == "" {
		t.Error("--endpoint has no usage string")
	}
}

// TestCatalogOnlyFlags_CoversEveryFilterFlag guards the seam between the
// filter flags and the endpoint guard. catalogOnlyFlags is a hand-kept
// list, so a filter flag added later is silently ignored against an
// endpoint rather than rejected — the user gets unfiltered rows and no
// indication the filter was dropped.
//
// Every registered filter flag must be named in catalogOnlyFlags; none
// of the entries may name a flag that does not exist.
func TestCatalogOnlyFlags_CoversEveryFilterFlag(t *testing.T) {
	cmd := modelListCmd()

	filterFlags := []string{
		"provider", "family", "in", "out", "query",
		"tool-call", "reasoning", "open-weights", "structured-output",
	}

	named := make(map[string]bool, len(catalogOnlyFlags))
	for _, n := range catalogOnlyFlags {
		named[n] = true
	}

	for _, f := range filterFlags {
		if cmd.Flags().Lookup(f) == nil {
			t.Errorf("filter flag %q is not registered on model list", f)
			continue
		}
		if !named[f] {
			t.Errorf("filter flag %q missing from catalogOnlyFlags; it would be silently ignored with --endpoint", f)
		}
	}

	// A stale entry naming a flag that no longer exists is inert, and
	// hides the fact that the guard is not doing what the list implies.
	for _, n := range catalogOnlyFlags {
		if cmd.Flags().Lookup(n) == nil {
			t.Errorf("catalogOnlyFlags names %q, which is not a registered flag", n)
		}
	}
}

// TestModelList_RefreshFlagRegistered: --refresh is the user-facing
// handle on both caches, so its absence is a silent loss of the whole
// feature — the command still works, it just always serves cached rows.
func TestModelList_RefreshFlagRegistered(t *testing.T) {
	cmd := modelListCmd()
	f := cmd.Flags().Lookup("refresh")
	if f == nil {
		t.Fatal("--refresh is not registered on model list")
	}
	if f.Value.Type() != "bool" {
		t.Errorf("--refresh type = %q, want bool", f.Value.Type())
	}
	if f.DefValue != "false" {
		t.Errorf("--refresh default = %q, want false: caching must be on by default", f.DefValue)
	}
}

// TestModelList_RefreshIsNotCatalogOnly: --refresh means something on
// both sources, so unlike the filter flags it must survive being
// combined with --endpoint rather than being rejected by name.
func TestModelList_RefreshIsNotCatalogOnly(t *testing.T) {
	for _, n := range catalogOnlyFlags {
		if n == "refresh" {
			t.Fatal("--refresh is listed as catalog-only; it would be rejected with --endpoint")
		}
	}

	cmd := modelListCmd()
	if err := cmd.Flags().Set("refresh", "true"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := checkCatalogOnlyFlags(cmd); err != nil {
		t.Errorf("--refresh rejected against an endpoint: %v", err)
	}
}

// TestModelList_RefreshReachesTheEndpointSource proves the flag is
// wired through selection rather than merely parsed.
//
// The assertion is behavioural, not structural: both branches return the
// same concrete type, so comparing types would pass even with refresh
// hard-coded to false. What distinguishes them is whether a warm cache
// entry is consulted, so the test warms one and counts server hits.
func TestModelList_RefreshReachesTheEndpointSource(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"data":[{"id":"llama3:8b"}]}`))
	}))
	defer srv.Close()

	withLiveSourceSelection(t)
	t.Setenv("FOO_CACHE", t.TempDir())
	t.Setenv("FOO_CACHE_TTL", "5m")
	url := srv.URL + "/v1"

	warm, _ := modelListSource(llm.Filter{}, url, false)
	if _, err := warm.ListModels(context.Background()); err != nil {
		t.Fatalf("warm: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("warming the cache made %d request(s), want 1", hits.Load())
	}

	// Without --refresh the warm entry must be served: no second hit.
	plain, _ := modelListSource(llm.Filter{}, url, false)
	if _, err := plain.ListModels(context.Background()); err != nil {
		t.Fatalf("plain: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("a cached listing hit the server: %d request(s), want 1", got)
	}

	// With --refresh the cache must be bypassed.
	refreshed, _ := modelListSource(llm.Filter{}, url, true)
	if _, err := refreshed.ListModels(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("--refresh did not reach the server: %d request(s), want 2", got)
	}
}

// TestModelList_CatalogProvenanceFooter: a table listing from the real
// catalog must say how old the rows are. The footer goes to stderr for
// the same reason the truncation hint does.
func TestModelList_CatalogProvenanceFooter(t *testing.T) {
	withCatalog(t, stubCatalog{entries: sampleEntries()})
	_, stderr, err := runList(t)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// An injected source has no aim cache behind it, so narrating one
	// would be a lie — the footer is gated on the rows really coming
	// from aim.
	if strings.Contains(stderr, "catalog:") {
		t.Errorf("provenance narrated for an injected source: %q", stderr)
	}
}

// TestModelList_ProvenanceSuppressedForEndpoint: aim's cache metadata
// describes models.dev and says nothing about a live server, so it must
// not be attached to an endpoint listing in either format.
func TestModelList_ProvenanceSuppressedForEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"llama3:8b"}]}`))
	}))
	defer srv.Close()

	withLiveSourceSelection(t)
	t.Setenv("FOO_CACHE", t.TempDir())

	stdout, stderr, err := runList(t, "--endpoint="+srv.URL+"/v1", "--format=json")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if strings.Contains(stderr, "catalog:") {
		t.Errorf("catalog provenance on an endpoint listing: %q", stderr)
	}
	// The endpoint payload must stay a bare array: wrapping it in the
	// catalog envelope would tell a parser the rows came from
	// models.dev.
	var rows []modelRow
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("endpoint JSON is not a bare row array (%v): %q", err, stdout)
	}
	if len(rows) != 1 || rows[0].ID != "llama3:8b" {
		t.Errorf("rows: %+v", rows)
	}
}

// TestStructuredFormat covers the predicate that decides where
// provenance goes. Getting it wrong is not cosmetic: a false positive
// hands the table renderer an untagged envelope, from which it resolves
// zero columns and prints nothing at all.
func TestStructuredFormat(t *testing.T) {
	for _, tc := range []struct {
		name         string
		format       string
		outputPath   string
		wantEnvelope bool
	}{
		{"default is table", "", "", false},
		{"explicit table", "table", "", false},
		{"csv", "csv", "", false},
		{"text", "text", "", false},
		{"json", "json", "", true},
		{"yaml", "yaml", "", true},
		{"output extension picks json", "", "out.json", true},
		{"output extension picks yaml", "", "out.yaml", true},
		{"output extension picks csv", "", "out.csv", false},
		{"unknown extension keeps the default", "", "out.bin", false},
		{"explicit format beats the extension", "table", "out.json", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Mirrors the real tree: kit puts --format and --output on
			// the root as persistent flags, and `model list` adds its
			// own local --output modality filter that shadows one of
			// them. structuredFormat must read past that shadow.
			root := &cobra.Command{Use: "foo"}
			root.PersistentFlags().String("format", "table", "")
			root.PersistentFlags().String("output", "", "")
			cmd := &cobra.Command{Use: "list"}
			cmd.Flags().StringArray("output", nil, "output modality filter")
			root.AddCommand(cmd)
			if tc.format != "" {
				if err := root.PersistentFlags().Set("format", tc.format); err != nil {
					t.Fatalf("set format: %v", err)
				}
			}
			if tc.outputPath != "" {
				if err := root.PersistentFlags().Set("output", tc.outputPath); err != nil {
					t.Fatalf("set output: %v", err)
				}
			}
			// The shadowing local flag is always populated, so a
			// regression that reads cmd.Flags() sees a modality list
			// where it expected a path.
			if err := cmd.Flags().Set("output", "text"); err != nil {
				t.Fatalf("set modality: %v", err)
			}
			if got := structuredFormat(cmd); got != tc.wantEnvelope {
				t.Errorf("structuredFormat = %v, want %v", got, tc.wantEnvelope)
			}
		})
	}
}

// TestMetaFromProvenance pins the envelope body, including the rule that
// a never-fetched catalog omits its timestamps rather than reporting
// year 1.
func TestMetaFromProvenance(t *testing.T) {
	t.Run("never fetched", func(t *testing.T) {
		m := metaFromProvenance(llm.CatalogProvenance{})
		if m.Cached {
			t.Error("cached = true for a never-fetched catalog")
		}
		if m.FetchedAt != "" || m.CacheAgeSeconds != 0 || m.TTLSeconds != 0 {
			t.Errorf("timestamps populated on a never-fetched catalog: %+v", m)
		}
		if m.Source != catalogSourceName {
			t.Errorf("source = %q, want %q", m.Source, catalogSourceName)
		}
		if m.Description == "" {
			t.Error("description is empty")
		}
	})

	t.Run("fetched", func(t *testing.T) {
		at := time.Date(2026, 9, 21, 6, 15, 16, 0, time.UTC)
		m := metaFromProvenance(llm.CatalogProvenance{
			Fetched:   true,
			FetchedAt: at,
			Age:       90 * time.Minute,
			TTL:       24 * time.Hour,
		})
		if !m.Cached {
			t.Error("cached = false for a fetched catalog")
		}
		if m.FetchedAt != "2026-09-21T06:15:16Z" {
			t.Errorf("fetched_at = %q, want RFC3339 UTC", m.FetchedAt)
		}
		if m.CacheAgeSeconds != 5400 {
			t.Errorf("cache_age_seconds = %d, want 5400", m.CacheAgeSeconds)
		}
		if m.TTLSeconds != 86400 {
			t.Errorf("ttl_seconds = %d, want 86400", m.TTLSeconds)
		}
		if m.Stale {
			t.Error("stale = true inside the TTL")
		}
	})
}

// TestModelList_RefreshForcesACatalogRefetch covers --refresh's catalog
// half at the command layer: the flag must reach the refetch, and must
// not fire without it.
//
// The refetch is stubbed rather than run, so the test asserts the
// command's wiring and never touches models.dev.
func TestModelList_RefreshForcesACatalogRefetch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		wantCall bool
	}{
		{"without --refresh", nil, false},
		{"with --refresh", []string{"--refresh"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withLiveSourceSelection(t)

			var calls atomic.Int64
			prevRefresh := refreshCatalog
			refreshCatalog = func(context.Context) error {
				calls.Add(1)
				return nil
			}
			t.Cleanup(func() { refreshCatalog = prevRefresh })

			// The listing itself must not fetch either. A filter makes
			// selection take the filtered-source branch, which is the
			// one seam that can be stubbed on the catalog path.
			prevFiltered := modelFilteredSource
			modelFilteredSource = func(llm.Filter) llm.CatalogSource {
				return stubCatalog{entries: sampleEntries()}
			}
			t.Cleanup(func() { modelFilteredSource = prevFiltered })

			if _, _, err := runList(t, append([]string{"--provider=openai"}, tc.args...)...); err != nil {
				t.Fatalf("execute: %v", err)
			}

			got := calls.Load() > 0
			if got != tc.wantCall {
				t.Errorf("catalog refetch called = %v, want %v", got, tc.wantCall)
			}
		})
	}
}

// TestModelList_RefreshReportsACatalogRefetchFailure: --refresh asked
// for fresh data by name, so a refetch failure must stop the command
// rather than quietly rendering the stale copy.
func TestModelList_RefreshReportsACatalogRefetchFailure(t *testing.T) {
	withLiveSourceSelection(t)

	wantErr := errors.New("models.dev unreachable")
	prevRefresh := refreshCatalog
	refreshCatalog = func(context.Context) error { return wantErr }
	t.Cleanup(func() { refreshCatalog = prevRefresh })

	prevFiltered := modelFilteredSource
	modelFilteredSource = func(llm.Filter) llm.CatalogSource {
		return stubCatalog{entries: sampleEntries()}
	}
	t.Cleanup(func() { modelFilteredSource = prevFiltered })

	stdout, _, err := runList(t, "--provider=openai", "--refresh")
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want the refetch failure", err)
	}
	// Nothing may have been rendered: the failure is reported before a
	// single stale row reaches the reader.
	if strings.Contains(stdout, "gpt-x") {
		t.Errorf("rows rendered despite a refetch failure: %q", stdout)
	}
}

// TestModelList_DoesNotShadowReservedGlobals is the regression guard for
// the --output collision: `model list` declared a local --output
// (modality filter) that masked kit's reserved persistent --output
// (write-to-path). `model list --output /tmp/x.json` wrote no file,
// printed no rows and exited 0 — the path was swallowed as a modality
// filter matching nothing.
//
// kit reserves a family-wide set of output-shaping globals
// (cli.go: "format", "cols", "columns", "template", "output", ...). A
// leaf must not redeclare one; cobra resolves the local flag first and
// the global becomes unreachable with no warning at all.
func TestModelList_DoesNotShadowReservedGlobals(t *testing.T) {
	// Names kit registers as persistent globals and owns family-wide.
	reserved := []string{"output", "format", "cols", "columns", "template"}

	cmd := modelListCmd()
	for _, name := range reserved {
		if f := cmd.Flags().Lookup(name); f != nil {
			t.Errorf("model list declares a local --%s, shadowing kit's reserved global of the same name", name)
		}
	}
}

// reachabilityEntries spans the three cases the default view must
// distinguish: routable with a key, routable without one, and keyed but
// with no compiled-in adapter.
func reachabilityEntries() []llm.ModelEntry {
	return []llm.ModelEntry{
		{Source: llm.SourceCatalog, Provider: "openai", ID: "gpt-x", Routable: true},
		{Source: llm.SourceCatalog, Provider: "google", ID: "gemini-x", Routable: true},
		{Source: llm.SourceCatalog, Provider: "groq", ID: "llama-x"},
	}
}

// reachabilityEnv is the credential requirement for those three, in
// aim's own shape.
// mistral is here only for the endpoint tests, which need a keyed
// provider name a server can plausibly report as owned_by. A provider
// absent from this map is "unknown", hence satisfied, which would make
// those tests pass whether or not the exemption exists.
var reachabilityEnv = map[string][]string{
	"openai":  {"OPENAI_API_KEY"},
	"google":  {"GOOGLE_API_KEY"},
	"groq":    {"GROQ_API_KEY"},
	"mistral": {"MISTRAL_API_KEY"},
}

// TestModelList_DefaultHidesUnreachable is the headline behavior: with
// only an openai key configured, a google model (adapter, no key) and a
// groq model (key requirement, no adapter) must both be gone, and the
// footer must say so and name --all.
func TestModelList_DefaultHidesUnreachable(t *testing.T) {
	withCatalog(t, stubCatalog{entries: reachabilityEntries()})
	withAuth(t, reachabilityEnv, "OPENAI_API_KEY")

	stdout, stderr, err := runList(t)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(stdout, "gpt-x") {
		t.Errorf("reachable model missing from default view: %q", stdout)
	}
	for _, hidden := range []string{"gemini-x", "llama-x"} {
		if strings.Contains(stdout, hidden) {
			t.Errorf("unreachable model %q present in default view: %q", hidden, stdout)
		}
	}
	if !strings.Contains(stderr, "2 model(s) hidden") {
		t.Errorf("footer missing the hidden count: %q", stderr)
	}
	if !strings.Contains(stderr, "--all") {
		t.Errorf("footer must name the flag that widens the view: %q", stderr)
	}
	if strings.Contains(stdout, "hidden") {
		t.Error("reachability footer leaked onto stdout; it must go to stderr")
	}
}

// TestModelList_AllShowsEverything is the override: --all must restore
// every row and claim nothing was hidden.
func TestModelList_AllShowsEverything(t *testing.T) {
	withCatalog(t, stubCatalog{entries: reachabilityEntries()})
	withAuth(t, reachabilityEnv, "OPENAI_API_KEY")

	stdout, stderr, err := runList(t, "--all")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	for _, id := range []string{"gpt-x", "gemini-x", "llama-x"} {
		if !strings.Contains(stdout, id) {
			t.Errorf("--all must list %q: %q", id, stdout)
		}
	}
	if strings.Contains(stderr, "hidden") {
		t.Errorf("--all hid nothing, so the footer must be silent: %q", stderr)
	}
}

// TestModelList_AllComposesWithFilters pins the interaction the brief
// calls out: --all widens the candidate set, it does not turn the
// narrowing filters off, and the two are not mutually exclusive.
func TestModelList_AllComposesWithFilters(t *testing.T) {
	got := captureFilter(t, reachabilityEntries())
	withAuth(t, reachabilityEnv, "OPENAI_API_KEY")

	stdout, _, err := runList(t, "--all", "--provider=groq")
	if err != nil {
		t.Fatalf("--all alongside --provider must be accepted: %v", err)
	}
	if got.Provider != "groq" {
		t.Errorf("--all swallowed the filter: Provider = %q, want %q", got.Provider, "groq")
	}
	// The stub source ignores the filter, so the assertion that matters
	// here is that --all still lifted the reachability gate on rows the
	// filter let through.
	if !strings.Contains(stdout, "llama-x") {
		t.Errorf("--all must lift the reachability gate under a filter: %q", stdout)
	}
}

// TestModelList_FilterWithoutAllStillHides is the other half: a filter
// on its own must not smuggle unreachable rows back in.
func TestModelList_FilterWithoutAllStillHides(t *testing.T) {
	captureFilter(t, reachabilityEntries())
	withAuth(t, reachabilityEnv, "OPENAI_API_KEY")

	stdout, stderr, err := runList(t, "--provider=groq")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if strings.Contains(stdout, "llama-x") {
		t.Errorf("filter without --all must still hide unreachable rows: %q", stdout)
	}
	if !strings.Contains(stderr, "--all") {
		t.Errorf("footer must still offer the override: %q", stderr)
	}
}

// TestModelList_NoKeysConfiguredExplainsItself covers the empty-table
// case. A bare count over an empty table reads as a broken catalog; the
// footer has to name the cause and a next step.
func TestModelList_NoKeysConfiguredExplainsItself(t *testing.T) {
	withCatalog(t, stubCatalog{entries: reachabilityEntries()})
	withAuth(t, reachabilityEnv)

	stdout, stderr, err := runList(t)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if strings.Contains(stdout, "gpt-x") {
		t.Errorf("no key configured, so no row is reachable: %q", stdout)
	}
	if !strings.Contains(stderr, "no provider API key is configured") {
		t.Errorf("footer must name the cause: %q", stderr)
	}
	if !strings.Contains(stderr, "--all") {
		t.Errorf("footer must name the override: %q", stderr)
	}
	if !strings.Contains(stderr, "OPENAI_API_KEY") && !strings.Contains(stderr, "foo provider show") {
		t.Errorf("footer must point at a fix: %q", stderr)
	}
}

// collidingEndpointBody is a /v1/models response whose owned_by names a
// real, keyed catalog provider.
//
// This is what makes the exemption testable at all. endpointProvider
// falls back to host:port for the generic owned_by values, and a
// host:port matches no catalog provider, so a credential gate applied to
// such rows would pass them anyway and the test would prove nothing.
// Servers that report a concrete owned_by — vLLM and LiteLLM proxies
// fronting a named upstream do — produce rows whose provider *is* a
// catalog id, and those are the rows a misapplied gate would eat.
const collidingEndpointBody = `{"object":"list","data":[` +
	`{"id":"mistral-small-local","object":"model","created":1787191418,"owned_by":"mistral"}` +
	`]}`

// TestModelList_EndpointSkipsReachabilityFiltering pins the exemption:
// endpoint rows are a server's own inventory with no catalog provider
// behind them, so the credential gate must neither hide them nor error
// — not even when the server labels a row with a provider name the
// catalog knows and demands a key for.
func TestModelList_EndpointSkipsReachabilityFiltering(t *testing.T) {
	withLiveSourceSelection(t)
	// mistral requires a key and none is configured. If the gate ran
	// over endpoint rows, this listing would come back empty.
	withAuth(t, reachabilityEnv)
	base := serveEndpoint(t, collidingEndpointBody)

	stdout, stderr, err := runList(t, "--endpoint="+base)
	if err != nil {
		t.Fatalf("--endpoint must not be subject to the credential gate: %v", err)
	}
	if !strings.Contains(stdout, "mistral-small-local") {
		t.Errorf("a served model must not be hidden for want of its upstream's API key: %q", stdout)
	}
	if strings.Contains(stderr, "hidden") {
		t.Errorf("no reachability footer belongs on an endpoint listing: %q", stderr)
	}
}

// TestModelList_EndpointRowsSurviveGenericOwner is the companion for the
// ordinary case, where owned_by is generic and the provider degrades to
// host:port. It cannot distinguish the gate being skipped from the gate
// passing such rows, which is exactly why the test above exists.
func TestModelList_EndpointRowsSurviveGenericOwner(t *testing.T) {
	withLiveSourceSelection(t)
	withAuth(t, reachabilityEnv)
	base := serveEndpoint(t, endpointBody)

	stdout, _, err := runList(t, "--endpoint="+base)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(stdout, "qwen2.5:7b-instruct") {
		t.Errorf("endpoint rows must survive: %q", stdout)
	}
}

// TestModelList_EndpointAcceptsAll checks --all is tolerated alongside
// --endpoint rather than rejected as a catalog-only flag: it is a
// redundant request, not an impossible one.
func TestModelList_EndpointAcceptsAll(t *testing.T) {
	withLiveSourceSelection(t)
	withAuth(t, reachabilityEnv)
	base := serveEndpoint(t, collidingEndpointBody)

	stdout, _, err := runList(t, "--endpoint="+base, "--all")
	if err != nil {
		t.Fatalf("--endpoint with --all must be accepted: %v", err)
	}
	if !strings.Contains(stdout, "mistral-small-local") {
		t.Errorf("endpoint rows missing: %q", stdout)
	}
}

// TestModelList_AllFlagRegistered fails if the flag is dropped from the
// surface: the behavior tests above drive runList, which would still
// pass against a flag cobra never declared.
func TestModelList_AllFlagRegistered(t *testing.T) {
	parent := modelCmd()
	for _, sub := range parent.Commands() {
		if sub.Name() != "list" {
			continue
		}
		f := sub.Flags().Lookup("all")
		if f == nil {
			t.Fatal("--all not registered on `model list`")
		}
		if f.Value.Type() != "bool" {
			t.Errorf("--all type = %q, want bool", f.Value.Type())
		}
		return
	}
	t.Fatal("list subcommand not found")
}

// TestProviderShow_AgreesWithModelList is the point of sharing the
// resolver: `foo provider show groq` must report the same verdict the
// listing filters on. Before this, show answered "available" for groq
// while the catalog knew it needs GROQ_API_KEY.
func TestProviderShow_AgreesWithModelList(t *testing.T) {
	withAuth(t, reachabilityEnv, "OPENAI_API_KEY")

	for _, tc := range []struct{ scheme, want, key string }{
		{"groq", "missing", "groq_api_key"},
		{"openai", "configured", "openai_api_key"},
		{"google", "missing", "google_api_key"},
		{"ollama", "available", ""},
	} {
		got := runProviderShow(t, tc.scheme)
		if got.Status != tc.want {
			t.Errorf("provider show %s: status = %q, want %q", tc.scheme, got.Status, tc.want)
		}
		if got.SecretKey != tc.key {
			t.Errorf("provider show %s: secret_key = %q, want %q", tc.scheme, got.SecretKey, tc.key)
		}
		// The listing's own predicate, read from the same index.
		auth, err := providerAuthIndex(context.Background())
		if err != nil {
			t.Fatalf("auth index: %v", err)
		}
		row := llm.ModelEntry{Provider: tc.scheme, Routable: true}
		if wantReach := tc.want != "missing"; row.Reachable(auth) != wantReach {
			t.Errorf("provider show %s says %q but the listing's Reachable says %v",
				tc.scheme, tc.want, row.Reachable(auth))
		}
	}
}

// TestProviderShow_GeminiAliasResolvesToGoogle covers the one scheme
// whose kit name differs from its catalog id. A bare lookup would find
// no record and call a provider that plainly needs a key "available".
func TestProviderShow_GeminiAliasResolvesToGoogle(t *testing.T) {
	withAuth(t, reachabilityEnv, "GOOGLE_API_KEY")

	got := runProviderShow(t, "gemini")
	if got.Status != "configured" {
		t.Errorf("gemini status = %q, want configured via the google record", got.Status)
	}
	if got.Scheme != "gemini" {
		t.Errorf("scheme echoed as %q; want the name the user typed", got.Scheme)
	}
	if got.AuthType != "api_key" {
		t.Errorf("auth_type = %q, want api_key", got.AuthType)
	}
}

// TestProviderShow_JSONShape pins the wire contract, which sharing the
// resolver must not have changed.
func TestProviderShow_JSONShape(t *testing.T) {
	withAuth(t, reachabilityEnv, "OPENAI_API_KEY")

	r := New("test")
	var out bytes.Buffer
	r.Cmd.SetOut(&out)
	r.Cmd.SetErr(&bytes.Buffer{})
	r.Cmd.SetArgs([]string{"provider", "show", "openai", "--format", "json"})
	if err := r.Cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", out.String(), err)
	}
	for key, want := range map[string]any{
		"scheme":     "openai",
		"auth_type":  "api_key",
		"secret_key": "openai_api_key",
		"status":     "configured",
	} {
		if got[key] != want {
			t.Errorf("json %s = %v, want %v", key, got[key], want)
		}
	}
}

// runProviderShow drives `foo provider show <scheme> --format json` and
// decodes the row, so assertions read the same fields the user sees
// rather than an internal value.
func runProviderShow(t *testing.T, scheme string) providerStatus {
	t.Helper()
	r := New("test")
	var out bytes.Buffer
	r.Cmd.SetOut(&out)
	r.Cmd.SetErr(&bytes.Buffer{})
	r.Cmd.SetArgs([]string{"provider", "show", scheme, "--format", "json"})
	if err := r.Cmd.Execute(); err != nil {
		t.Fatalf("provider show %s: %v", scheme, err)
	}
	var got providerStatus
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", out.String(), err)
	}
	return got
}

// TestProviderShow_ResolvesSchemeAliases proves `provider show` goes
// through LookupScheme rather than a bare provider lookup.
//
// A scheme whose catalog id is spelled differently would otherwise find
// no record, read as "no declared requirement", and print "available" —
// the same wrong answer the old four-case switch gave, just for fewer
// providers. The alias table itself, and its completeness against the
// live catalog, are pinned in internal/llm.
func TestProviderShow_ResolvesSchemeAliases(t *testing.T) {
	withAuth(t, map[string][]string{
		"google":       {"GOOGLE_API_KEY"},
		"fireworks-ai": {"FIREWORKS_API_KEY"},
		"togetherai":   {"TOGETHER_API_KEY"},
	}, "GOOGLE_API_KEY")

	for scheme, want := range map[string]string{
		"gemini":    "configured",
		"fireworks": "missing",
		"together":  "missing",
	} {
		got := runProviderShow(t, scheme)
		if got.Status != want {
			t.Errorf("provider show %s: status = %q, want %q", scheme, got.Status, want)
		}
		if got.Status == "available" {
			t.Errorf("scheme %q aliases a keyed provider; reporting available is the old defect", scheme)
		}
		if got.Scheme != scheme {
			t.Errorf("scheme echoed as %q, want the name the user typed (%q)", got.Scheme, scheme)
		}
	}

	// A scheme with no alias and no catalog record is genuinely local;
	// "available" is right and must not regress into demanding a key
	// nobody publishes.
	if got := runProviderShow(t, "routellm"); got.Status != "available" {
		t.Errorf("routellm has no upstream provider record; status = %q, want available", got.Status)
	}
}
