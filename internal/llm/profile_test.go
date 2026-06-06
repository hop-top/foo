package llm

import (
	"testing"
)

// helper: asserts a *bool tristate equals the wanted concrete value.
func assertTri(t *testing.T, got *bool, want bool, label string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: got nil tristate, want %v", label, want)
	}
	if *got != want {
		t.Fatalf("%s: got %v, want %v", label, *got, want)
	}
}

func TestDeriveProfile_Empty(t *testing.T) {
	p := DeriveProfile(ProfileOpts{})
	if p.Filter.StructuredOutput != nil {
		t.Errorf("empty opts: StructuredOutput should be nil, got %v", *p.Filter.StructuredOutput)
	}
	if p.Filter.ToolCall != nil {
		t.Errorf("empty opts: ToolCall should be nil, got %v", *p.Filter.ToolCall)
	}
	if p.MaxInputTokens != 0 {
		t.Errorf("empty opts: MaxInputTokens should be 0, got %d", p.MaxInputTokens)
	}
	if p.MaxOutputTokens != 0 {
		t.Errorf("empty opts: MaxOutputTokens should be 0, got %d", p.MaxOutputTokens)
	}
}

func TestDeriveProfile_SchemaOnly(t *testing.T) {
	p := DeriveProfile(ProfileOpts{SchemaSelected: true})
	assertTri(t, p.Filter.StructuredOutput, true, "Filter.StructuredOutput")
	if p.Filter.ToolCall != nil {
		t.Errorf("schema only: ToolCall must remain nil")
	}
}

func TestDeriveProfile_ToolsOnly(t *testing.T) {
	p := DeriveProfile(ProfileOpts{ToolNames: []string{"foo_time"}})
	assertTri(t, p.Filter.ToolCall, true, "Filter.ToolCall")
	if p.Filter.StructuredOutput != nil {
		t.Errorf("tools only: StructuredOutput must remain nil")
	}
}

func TestDeriveProfile_PatternOnly(t *testing.T) {
	// Pattern axis does not yet map to an aim filter. The result is the
	// zero-value profile; this test pins the contract so future kit
	// additions force a deliberate update here.
	p := DeriveProfile(ProfileOpts{PatternSelected: true})
	if p.Filter.StructuredOutput != nil || p.Filter.ToolCall != nil {
		t.Errorf("pattern only: no filter axis should fire today")
	}
	if p.MaxInputTokens != 0 || p.MaxOutputTokens != 0 {
		t.Errorf("pattern only: no token bound should be set")
	}
}

func TestDeriveProfile_PromptTokensEstimate(t *testing.T) {
	p := DeriveProfile(ProfileOpts{PromptTokensEstimate: 8192})
	if p.MaxInputTokens != 8192 {
		t.Errorf("PromptTokensEstimate=8192 → MaxInputTokens, got %d", p.MaxInputTokens)
	}
}

func TestDeriveProfile_MaxOutputTokens(t *testing.T) {
	p := DeriveProfile(ProfileOpts{MaxOutputTokens: 4096})
	if p.MaxOutputTokens != 4096 {
		t.Errorf("MaxOutputTokens=4096 → got %d", p.MaxOutputTokens)
	}
}

func TestDeriveProfile_ZeroTokenEstimatesIgnored(t *testing.T) {
	p := DeriveProfile(ProfileOpts{PromptTokensEstimate: 0, MaxOutputTokens: 0})
	if p.MaxInputTokens != 0 {
		t.Errorf("zero PromptTokensEstimate must not write MaxInputTokens")
	}
	if p.MaxOutputTokens != 0 {
		t.Errorf("zero MaxOutputTokens must not write MaxOutputTokens")
	}
}

// Combined permutation 1: schema + tools + non-zero token estimate.
// Mirrors the "structured tool call" path users hit most often.
func TestDeriveProfile_SchemaAndToolsAndTokens(t *testing.T) {
	p := DeriveProfile(ProfileOpts{
		SchemaSelected:       true,
		ToolNames:            []string{"foo_time", "foo_version"},
		PromptTokensEstimate: 12000,
	})
	assertTri(t, p.Filter.StructuredOutput, true, "Filter.StructuredOutput")
	assertTri(t, p.Filter.ToolCall, true, "Filter.ToolCall")
	if p.MaxInputTokens != 12000 {
		t.Errorf("MaxInputTokens: got %d, want 12000", p.MaxInputTokens)
	}
}

// Combined permutation 2: every axis set. Confirms each maps onto its
// own slot rather than overwriting a sibling.
func TestDeriveProfile_AllAxes(t *testing.T) {
	p := DeriveProfile(ProfileOpts{
		SchemaSelected:       true,
		ToolNames:            []string{"foo_time"},
		PatternSelected:      true,
		PromptTokensEstimate: 4096,
		MaxOutputTokens:      2048,
	})
	assertTri(t, p.Filter.StructuredOutput, true, "Filter.StructuredOutput")
	assertTri(t, p.Filter.ToolCall, true, "Filter.ToolCall")
	if p.MaxInputTokens != 4096 {
		t.Errorf("MaxInputTokens: got %d, want 4096", p.MaxInputTokens)
	}
	if p.MaxOutputTokens != 2048 {
		t.Errorf("MaxOutputTokens: got %d, want 2048", p.MaxOutputTokens)
	}
}

// EmptyToolNames slice (vs. nil) is treated identically: presence is
// determined by length, not the slice's underlying pointer.
func TestDeriveProfile_EmptyToolNamesSliceTreatedAsAbsent(t *testing.T) {
	p := DeriveProfile(ProfileOpts{ToolNames: []string{}})
	if p.Filter.ToolCall != nil {
		t.Errorf("empty []string ToolNames must not engage ToolCall filter")
	}
}
