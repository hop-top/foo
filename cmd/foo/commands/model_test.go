package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

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
func withCatalog(t *testing.T, src llm.CatalogSource) {
	t.Helper()
	prev := modelCatalogSource
	modelCatalogSource = src
	t.Cleanup(func() { modelCatalogSource = prev })
}

// sampleEntries returns n reachable rows across two providers.
func sampleEntries() []llm.ModelEntry {
	return []llm.ModelEntry{
		{Source: llm.SourceCatalog, Provider: "anthropic", ID: "claude-x", Context: 200000, ToolCall: true, Reasoning: true, Released: "2026-01-01", Reachable: true},
		{Source: llm.SourceCatalog, Provider: "openai", ID: "gpt-x", Context: 128000, ToolCall: true, Released: "2026-02-02", Reachable: true},
		{Source: llm.SourceCatalog, Provider: "anthropic", ID: "claude-y", Context: 100000, Released: "2025-01-01", Reachable: true},
		{Source: llm.SourceCatalog, Provider: "openai", ID: "gpt-y", Context: 8192, Released: "2024-02-02", Reachable: true},
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
			ID: string(rune('a'+i)) + "-model", Reachable: true,
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
	// Round-robin, newest first within provider:
	// anthropic/claude-x, openai/gpt-x, anthropic/claude-y, openai/gpt-y.
	want := []string{"claude-x", "gpt-x", "claude-y", "gpt-y"}
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
		"provider", "family", "input", "output", "query",
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
// including that --input/--output accumulate across repeats.
func TestModelList_ScalarAndRepeatableFlags(t *testing.T) {
	got := captureFilter(t, sampleEntries())
	_, _, err := runList(t,
		"--provider=openai", "--family=gpt-4",
		"--input=text", "--input=image", "--output=text")
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
