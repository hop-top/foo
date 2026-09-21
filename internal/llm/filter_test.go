package llm

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"hop.top/aim"
)

// ptr is the terse *bool constructor these tables need. The tristate
// assertions below are about nil vs non-nil, so the address matters
// more than the value.
func ptr(b bool) *bool { return &b }

// TestFilter_IsZero guards the fast path in the command layer: a zero
// filter must keep the unfiltered source, and any single populated
// field must defeat that.
func TestFilter_IsZero(t *testing.T) {
	if !(Filter{}).IsZero() {
		t.Fatal("zero Filter reported non-zero")
	}
	cases := map[string]Filter{
		"provider":          {Provider: "openai"},
		"family":            {Family: "gpt-4"},
		"input":             {Input: []string{"image"}},
		"output":            {Output: []string{"text"}},
		"query":             {Query: "reasoning:true"},
		"tool_call true":    {ToolCall: ptr(true)},
		"tool_call false":   {ToolCall: ptr(false)},
		"reasoning false":   {Reasoning: ptr(false)},
		"open_weights":      {OpenWeights: ptr(true)},
		"structured_output": {StructuredOutput: ptr(false)},
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			if f.IsZero() {
				t.Errorf("Filter with %s set reported zero", name)
			}
		})
	}
}

// TestFilter_TristateLowering is the crux test. A capability that was
// never set must arrive at aim as nil, because a non-nil false would
// silently exclude every model lacking that capability. All three
// states are asserted for all four capability flags.
func TestFilter_TristateLowering(t *testing.T) {
	get := map[string]func(aim.Filter) *bool{
		"tool_call":         func(f aim.Filter) *bool { return f.ToolCall },
		"reasoning":         func(f aim.Filter) *bool { return f.Reasoning },
		"open_weights":      func(f aim.Filter) *bool { return f.OpenWeights },
		"structured_output": func(f aim.Filter) *bool { return f.StructuredOutput },
	}
	set := map[string]func(*Filter, *bool){
		"tool_call":         func(f *Filter, b *bool) { f.ToolCall = b },
		"reasoning":         func(f *Filter, b *bool) { f.Reasoning = b },
		"open_weights":      func(f *Filter, b *bool) { f.OpenWeights = b },
		"structured_output": func(f *Filter, b *bool) { f.StructuredOutput = b },
	}

	for name, getter := range get {
		t.Run(name, func(t *testing.T) {
			// Unset: must stay nil, or every invocation filters.
			var unset Filter
			af, err := unset.aimFilter()
			if err != nil {
				t.Fatalf("unset: %v", err)
			}
			if got := getter(af); got != nil {
				t.Errorf("unset %s: got %v, want nil — a non-nil "+
					"value here filters on every invocation", name, *got)
			}
			// Every other capability must also stay nil, so setting
			// one never drags the others along.
			for other, og := range get {
				if other == name {
					continue
				}
				if got := og(af); got != nil {
					t.Errorf("unset filter leaked %s=%v", other, *got)
				}
			}

			// Set: the pointer must survive lowering and carry the
			// value, for both true and false.
			for _, want := range []bool{true, false} {
				var f Filter
				set[name](&f, ptr(want))
				af, err := f.aimFilter()
				if err != nil {
					t.Fatalf("set %v: %v", want, err)
				}
				got := getter(af)
				if got == nil {
					t.Fatalf("%s=%v lowered to nil", name, want)
				}
				if *got != want {
					t.Errorf("%s: got %v, want %v", name, *got, want)
				}
			}
		})
	}
}

// TestFilter_TristateExplicitValues pins the true and false states,
// kept separate from the nil case so a failure names which state broke.
func TestFilter_TristateExplicitValues(t *testing.T) {
	cases := []struct {
		name  string
		build func(*bool) Filter
		read  func(aim.Filter) *bool
	}{
		{"tool_call", func(b *bool) Filter { return Filter{ToolCall: b} },
			func(f aim.Filter) *bool { return f.ToolCall }},
		{"reasoning", func(b *bool) Filter { return Filter{Reasoning: b} },
			func(f aim.Filter) *bool { return f.Reasoning }},
		{"open_weights", func(b *bool) Filter { return Filter{OpenWeights: b} },
			func(f aim.Filter) *bool { return f.OpenWeights }},
		{"structured_output", func(b *bool) Filter { return Filter{StructuredOutput: b} },
			func(f aim.Filter) *bool { return f.StructuredOutput }},
	}
	for _, c := range cases {
		for _, want := range []bool{true, false} {
			t.Run(c.name, func(t *testing.T) {
				af, err := c.build(ptr(want)).aimFilter()
				if err != nil {
					t.Fatalf("lower: %v", err)
				}
				got := c.read(af)
				if got == nil {
					t.Fatalf("%s=%v lowered to nil", c.name, want)
				}
				if *got != want {
					t.Errorf("%s: got %v, want %v", c.name, *got, want)
				}
			})
		}
	}
}

// TestFilter_ScalarLowering covers the non-tristate fields.
func TestFilter_ScalarLowering(t *testing.T) {
	af, err := Filter{
		Provider: "openai",
		Family:   "gpt-4",
		Input:    []string{"text", "image"},
		Output:   []string{"text"},
	}.aimFilter()
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	if af.Provider != "openai" {
		t.Errorf("provider: got %q", af.Provider)
	}
	if af.Family != "gpt-4" {
		t.Errorf("family: got %q", af.Family)
	}
	if !reflect.DeepEqual(af.Input, []string{"text", "image"}) {
		t.Errorf("input: got %v", af.Input)
	}
	if !reflect.DeepEqual(af.Output, []string{"text"}) {
		t.Errorf("output: got %v", af.Output)
	}
}

// TestFilter_QueryParsed proves --query alone reaches aim as structured
// tags rather than as free text.
func TestFilter_QueryParsed(t *testing.T) {
	af, err := Filter{Query: "provider:anthropic reasoning:true"}.aimFilter()
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	if af.Provider != "anthropic" {
		t.Errorf("provider from query: got %q, want anthropic", af.Provider)
	}
	if af.Reasoning == nil || !*af.Reasoning {
		t.Errorf("reasoning from query: got %v, want true", af.Reasoning)
	}
}

// TestFilter_ExplicitFlagsWinOverQuery documents and pins the
// composition rule: where a flag and the query name the same field, the
// flag wins; where they name different fields, both survive.
func TestFilter_ExplicitFlagsWinOverQuery(t *testing.T) {
	af, err := Filter{
		Query:     "provider:anthropic reasoning:true",
		Provider:  "openai",   // collides with the query
		Reasoning: ptr(false), // collides with the query
	}.aimFilter()
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	if af.Provider != "openai" {
		t.Errorf("explicit --provider lost to query: got %q", af.Provider)
	}
	if af.Reasoning == nil || *af.Reasoning {
		t.Errorf("explicit --reasoning=false lost to query: got %v", af.Reasoning)
	}
}

// TestFilter_QueryAndFlagsCompose is the other half of the rule: a
// query tag the flags do not mention must survive.
func TestFilter_QueryAndFlagsCompose(t *testing.T) {
	af, err := Filter{
		Query:    "reasoning:true",
		Provider: "openai",
	}.aimFilter()
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	if af.Provider != "openai" {
		t.Errorf("provider: got %q", af.Provider)
	}
	if af.Reasoning == nil || !*af.Reasoning {
		t.Errorf("query reasoning erased by flag: got %v", af.Reasoning)
	}
}

// TestFilter_ModalitiesMergeRatherThanOverride pins the one field that
// appends instead of replacing. aim treats modalities as subset
// containment, so merging narrows — consistent with AND everywhere else.
func TestFilter_ModalitiesMergeRatherThanOverride(t *testing.T) {
	af, err := Filter{
		Query: "in:text",
		Input: []string{"image"},
	}.aimFilter()
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	want := []string{"text", "image"}
	if !reflect.DeepEqual(af.Input, want) {
		t.Errorf("input: got %v, want %v", af.Input, want)
	}
}

// TestFilter_BadQuerySurfacesAimError checks the parse error reaches the
// caller verbatim — it already names the offending key, so re-wording
// would only make it harder to match against aim's own docs.
func TestFilter_BadQuerySurfacesAimError(t *testing.T) {
	_, err := Filter{Query: "bogus:1"}.aimFilter()
	if err == nil {
		t.Fatal("expected error for unknown tag key")
	}
	if got, want := err.Error(), `aim: unknown tag key "bogus"`; got != want {
		t.Errorf("error text: got %q, want %q", got, want)
	}
}

// TestFilteredAimCatalog_BadQueryFailsBeforeFetch proves a malformed
// query is reported rather than silently degrading to an unfiltered
// listing.
func TestFilteredAimCatalog_BadQueryFailsBeforeFetch(t *testing.T) {
	src := NewFilteredAimCatalog(aim.NewRegistry(), Filter{Query: "nope:x"})
	_, err := src.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error from malformed query")
	}
	if !strings.Contains(err.Error(), `unknown tag key "nope"`) {
		t.Errorf("error text: got %q", err.Error())
	}
}
