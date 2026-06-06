// Profile derivation: translate foo's invocation flag state into a
// kit RequestProfile the pool picker can score. Pure function — no
// globals, no env reads — so callers thread state in explicitly.
//
// Capabilities map as follows (each contributes its tristate to the
// filter; nil = "don't care"):
//
//   - SchemaSelected  → Filter.StructuredOutput = &true
//   - ToolNames > 0   → Filter.ToolCall         = &true
//   - PatternSelected → no aim capability today (system prompts are
//     universal). Retained on the input so future kit additions can
//     route off it without breaking foo's caller.
//
// PromptTokensEstimate fills MaxInputTokens so the picker drops models
// whose context window can't hold the request. The caller is responsible
// for the estimate (foo uses len(prompt)/4 elsewhere).

package llm

import (
	kitllm "hop.top/kit/go/ai/llm"
)

// ProfileOpts is foo's pre-picker invocation snapshot. All fields are
// optional; the zero value yields an empty profile (no constraints).
type ProfileOpts struct {
	// SchemaSelected is true when --schema or --schema-multi is set.
	SchemaSelected bool
	// ToolNames lists the tools requested via -T flags.
	ToolNames []string
	// PatternSelected is true when -p / --pattern is set. Currently
	// informational; kept on the input for forward compatibility.
	PatternSelected bool
	// PromptTokensEstimate is foo's caller-side estimate (roughly
	// len(prompt)/4). Zero = no input-context constraint.
	PromptTokensEstimate int
	// MaxOutputTokens is the requested response budget. Zero = no
	// output-token constraint.
	MaxOutputTokens int
}

// DeriveProfile converts foo's invocation state into a kit
// RequestProfile suitable for PickProvider / PickProviderInPool.
//
// Pure function. Callers are responsible for supplying every input —
// no global state is consulted.
func DeriveProfile(opts ProfileOpts) kitllm.RequestProfile {
	p := kitllm.RequestProfile{}
	if opts.SchemaSelected {
		t := true
		p.Filter.StructuredOutput = &t
	}
	if len(opts.ToolNames) > 0 {
		t := true
		p.Filter.ToolCall = &t
	}
	if opts.PromptTokensEstimate > 0 {
		p.MaxInputTokens = opts.PromptTokensEstimate
	}
	if opts.MaxOutputTokens > 0 {
		p.MaxOutputTokens = opts.MaxOutputTokens
	}
	// PatternSelected does not yet drive an aim filter axis — no field
	// exists for "supports system prompts" in aim.Filter. Recorded on
	// the input so the wiring is in place for when one lands.
	return p
}
