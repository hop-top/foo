package llm

import (
	"strings"
	"testing"

	kitllm "hop.top/kit/go/ai/llm"
)

func TestResolveBudget_Default(t *testing.T) {
	t.Setenv("FOO_BUDGET", "")
	got, err := ResolveBudget("", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != DefaultBudget {
		t.Fatalf("default: got %v, want %v", got, DefaultBudget)
	}
}

func TestResolveBudget_CLIWinsOverEnv(t *testing.T) {
	t.Setenv("FOO_BUDGET", "cheap")
	got, err := ResolveBudget("premium", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != kitllm.BudgetPremium {
		t.Fatalf("CLI must win: got %v, want premium", got)
	}
}

func TestResolveBudget_EnvFallback(t *testing.T) {
	t.Setenv("FOO_BUDGET", "cheap")
	got, err := ResolveBudget("", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != kitllm.BudgetCheap {
		t.Fatalf("env fallback: got %v, want cheap", got)
	}
}

func TestResolveBudget_AcceptsCaseAndWhitespace(t *testing.T) {
	t.Setenv("FOO_BUDGET", "")
	got, err := ResolveBudget("  Premium  ", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != kitllm.BudgetPremium {
		t.Fatalf("got %v, want premium", got)
	}
}

func TestResolveBudget_InvalidCLI_SuggestsClosest(t *testing.T) {
	t.Setenv("FOO_BUDGET", "")
	_, err := ResolveBudget("cheep", "")
	if err == nil {
		t.Fatal("expected error for misspelled tier")
	}
	msg := err.Error()
	if !strings.Contains(msg, "did you mean") {
		t.Errorf("error must include did-you-mean hint: %q", msg)
	}
	if !strings.Contains(msg, "cheap") {
		t.Errorf("error must suggest %q: %q", "cheap", msg)
	}
	if !strings.Contains(msg, "--budget") {
		t.Errorf("error must name the source flag: %q", msg)
	}
}

func TestResolveBudget_InvalidEnv_NamesSource(t *testing.T) {
	t.Setenv("FOO_BUDGET", "wat")
	_, err := ResolveBudget("", "")
	if err == nil {
		t.Fatal("expected error for invalid env value")
	}
	if !strings.Contains(err.Error(), "FOO_BUDGET") {
		t.Errorf("error must name the env source: %q", err)
	}
}

func TestResolveBudget_TotallyUnknown_ListsValid(t *testing.T) {
	t.Setenv("FOO_BUDGET", "")
	_, err := ResolveBudget("zzzz", "")
	if err == nil {
		t.Fatal("expected error for unknown value")
	}
	msg := err.Error()
	for _, tier := range BudgetTiers {
		if !strings.Contains(msg, tier) {
			t.Errorf("error must list valid tier %q: %q", tier, msg)
		}
	}
}

func TestResolveBudget_ConfigFallback(t *testing.T) {
	t.Setenv("FOO_BUDGET", "")
	got, err := ResolveBudget("", "premium")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != kitllm.BudgetPremium {
		t.Fatalf("config fallback: got %v, want premium", got)
	}
}

func TestResolveBudget_EnvWinsOverConfig(t *testing.T) {
	t.Setenv("FOO_BUDGET", "cheap")
	got, err := ResolveBudget("", "premium")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != kitllm.BudgetCheap {
		t.Fatalf("env must win over config: got %v, want cheap", got)
	}
}

func TestResolveBudget_InvalidConfig_NamesSource(t *testing.T) {
	t.Setenv("FOO_BUDGET", "")
	_, err := ResolveBudget("", "wat")
	if err == nil {
		t.Fatal("expected error for invalid config value")
	}
	if !strings.Contains(err.Error(), "budget config key") {
		t.Errorf("error must name the config source: %q", err)
	}
}
