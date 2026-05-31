package suggest

import "testing"

func TestClosest_ExactMatchPrefers(t *testing.T) {
	got := Closest("foo", []string{"foo", "bar"}, 2)
	if got != "foo" {
		t.Fatalf("want foo, got %q", got)
	}
}

func TestClosest_NearestWithinBudget(t *testing.T) {
	got := Closest("sumarize", []string{"summarize", "reviewer"}, 2)
	if got != "summarize" {
		t.Fatalf("want summarize, got %q", got)
	}
}

func TestClosest_NothingWithinBudget(t *testing.T) {
	got := Closest("zzzzz", []string{"summarize", "reviewer"}, 2)
	if got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}

func TestClosest_CaseInsensitive(t *testing.T) {
	got := Closest("SUMMARIZE", []string{"summarize"}, 2)
	if got != "summarize" {
		t.Fatalf("want summarize, got %q", got)
	}
}

func TestClosest_EmptyInput(t *testing.T) {
	if got := Closest("", []string{"x"}, 2); got != "" {
		t.Fatalf("empty want should return empty, got %q", got)
	}
	if got := Closest("x", nil, 2); got != "" {
		t.Fatalf("nil candidates should return empty, got %q", got)
	}
	if got := Closest("x", []string{"y"}, 0); got != "" {
		t.Fatalf("zero budget should return empty, got %q", got)
	}
}

func TestClosest_TiePicksFirst(t *testing.T) {
	got := Closest("ab", []string{"ax", "ay"}, 2)
	if got != "ax" {
		t.Fatalf("want ax (first tie), got %q", got)
	}
}
