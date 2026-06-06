// Budget tier resolution: parse foo's --budget / FOO_BUDGET inputs into
// kit's BudgetTier enum with the same error-enrichment story foo uses
// for pattern/schema not-found messages (closest-match suggestion).
//
// Precedence: caller-supplied CLI value > FOO_BUDGET env > "balanced".
// Caller is responsible for layering a config-file value above the env
// fallback if one exists.

package llm

import (
	"fmt"
	"os"
	"strings"

	"hop.top/foo/internal/suggest"
	kitllm "hop.top/kit/go/ai/llm"
)

// BudgetTiers lists the canonical labels kit accepts. Kept here as a
// stable surface for did-you-mean enrichment without dragging kit's
// internal label table through suggest.
var BudgetTiers = []string{"cheap", "balanced", "premium"}

// DefaultBudget is the tier used when neither the CLI flag nor the env
// var supplies one.
const DefaultBudget = kitllm.BudgetBalanced

// DefaultBudgetLabel is the lowercase label for DefaultBudget — kept as
// a string for log/error output without re-converting at call sites.
const DefaultBudgetLabel = "balanced"

// ResolveBudget returns the BudgetTier for cliValue, falling back to
// FOO_BUDGET, then DefaultBudget. Invalid values produce a closest-match
// suggestion in the error so misspellings surface like pattern/schema
// not-found errors.
//
// Empty strings (both cliValue and env) skip layers without erroring.
func ResolveBudget(cliValue string) (kitllm.BudgetTier, error) {
	raw := strings.TrimSpace(cliValue)
	source := "--budget"
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("FOO_BUDGET"))
		source = "FOO_BUDGET"
	}
	if raw == "" {
		return DefaultBudget, nil
	}

	tier, err := kitllm.ParseBudgetTier(raw)
	if err == nil {
		return tier, nil
	}

	// Enrich with a closest match against the canonical list. Mirror
	// the pattern/schema not-found enrichment shape.
	guess := suggest.Closest(raw, BudgetTiers, 3)
	if guess != "" {
		return 0, fmt.Errorf(
			"invalid %s value %q; did you mean %q? (valid: %s)",
			source, raw, guess, strings.Join(BudgetTiers, ", "),
		)
	}
	return 0, fmt.Errorf(
		"invalid %s value %q; valid values: %s",
		source, raw, strings.Join(BudgetTiers, ", "),
	)
}
