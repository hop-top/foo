package tool

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

type stubTool struct{ name string }

func (s stubTool) Name() string                { return s.name }
func (s stubTool) Description() string         { return "stub " + s.name }
func (s stubTool) Parameters() json.RawMessage { return json.RawMessage(`{}`) }
func (s stubTool) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

func newTestRegistry(t *testing.T, names ...string) *Registry {
	t.Helper()
	r := NewRegistry()
	for _, n := range names {
		if err := r.Register(stubTool{name: n}); err != nil {
			t.Fatalf("register %q: %v", n, err)
		}
	}
	return r
}

func toolNames(r *Registry) []string {
	var out []string
	for _, t := range r.List() {
		out = append(out, t.Name())
	}
	return out
}

func TestSelect_KnownNames(t *testing.T) {
	r := newTestRegistry(t, "b", "a", "c")

	got, err := r.Select([]string{"c", "a"})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if want := []string{"a", "c"}; !reflect.DeepEqual(toolNames(got), want) {
		t.Errorf("selected = %v; want %v", toolNames(got), want)
	}
}

// TestSelect_UnknownNameErrors is the headline defect: a typo used to
// be dropped silently, so the model ran with fewer tools (or none) and
// nothing said why.
func TestSelect_UnknownNameErrors(t *testing.T) {
	r := newTestRegistry(t, "foo_version", "foo_time")

	got, err := r.Select([]string{"foo_time", "nope"})
	if err == nil {
		t.Fatalf("Select with an unknown name must error; got registry %v", toolNames(got))
	}
	var unknown *UnknownToolError
	if !errors.As(err, &unknown) {
		t.Fatalf("error %T (%v) is not *UnknownToolError", err, err)
	}
	if want := []string{"nope"}; !reflect.DeepEqual(unknown.Unknown, want) {
		t.Errorf("Unknown = %v; want %v", unknown.Unknown, want)
	}
	if want := []string{"foo_time", "foo_version"}; !reflect.DeepEqual(unknown.Available, want) {
		t.Errorf("Available = %v; want %v (sorted)", unknown.Available, want)
	}
}

// TestSelect_ReportsEveryUnknown makes sure a user with two typos
// hears about both at once rather than fixing them one run at a time.
func TestSelect_ReportsEveryUnknown(t *testing.T) {
	r := newTestRegistry(t, "foo_time")

	_, err := r.Select([]string{"b", "foo_time", "a", "b"})
	var unknown *UnknownToolError
	if !errors.As(err, &unknown) {
		t.Fatalf("error %T (%v) is not *UnknownToolError", err, err)
	}
	if want := []string{"b", "a"}; !reflect.DeepEqual(unknown.Unknown, want) {
		t.Errorf("Unknown = %v; want %v (argument order, deduplicated)", unknown.Unknown, want)
	}
}

// TestSelect_DeduplicatesRepeats guards against `-T x -T x` sending the
// model the same tool definition twice.
func TestSelect_DeduplicatesRepeats(t *testing.T) {
	r := newTestRegistry(t, "x", "y")

	got, err := r.Select([]string{"x", "x"})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if want := []string{"x"}; !reflect.DeepEqual(toolNames(got), want) {
		t.Errorf("selected = %v; want %v", toolNames(got), want)
	}
	if defs := got.ToolDefs(); len(defs) != 1 {
		t.Errorf("ToolDefs = %d entries; want 1", len(defs))
	}
}

func TestSourceOf(t *testing.T) {
	if got := SourceOf(stubTool{name: "a"}); got != SourceBuiltin {
		t.Errorf("SourceOf(in-process tool) = %q; want %q", got, SourceBuiltin)
	}
	ext := NewExternalTool("demo", "d", "/opt/bin/foo-tool-demo", nil)
	if got := SourceOf(ext); got != "/opt/bin/foo-tool-demo" {
		t.Errorf("SourceOf(external) = %q; want the binary path", got)
	}
}
