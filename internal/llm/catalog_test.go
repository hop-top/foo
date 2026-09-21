package llm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"hop.top/aim"
)

// fixtureCatalog is a CatalogSource that returns canned rows, so ranking
// and truncation are tested without a models.dev fetch.
type fixtureCatalog struct {
	entries []ModelEntry
	err     error
}

func (f fixtureCatalog) ListModels(context.Context) ([]ModelEntry, error) {
	return f.entries, f.err
}

// ent builds a fixture row. Every ranking input is explicit because the
// ordering assertions below are meaningless if a field defaults.
func ent(provider, id, released string, routable bool) ModelEntry {
	return ModelEntry{
		Source:   SourceCatalog,
		Provider: provider,
		ID:       id,
		Released: released,
		Routable: routable,
	}
}

// key renders an entry as "provider/id" for order assertions.
func key(e ModelEntry) string { return e.Provider + "/" + e.ID }

func keys(entries []ModelEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, key(e))
	}
	return out
}

func requireOrder(t *testing.T, got []ModelEntry, want ...string) {
	t.Helper()
	gotKeys := keys(got)
	if len(gotKeys) != len(want) {
		t.Fatalf("length: got %d %v, want %d %v", len(gotKeys), gotKeys, len(want), want)
	}
	for i := range want {
		if gotKeys[i] != want[i] {
			t.Fatalf("position %d: got %q, want %q\nfull: %v\nwant: %v", i, gotKeys[i], want[i], gotKeys, want)
		}
	}
}

// TestRank_RotatesAcrossRoutableProviders is the central ranking
// guarantee: one provider's models stay contiguous. "big" holds three
// models and anthropic two; grouping must emit each provider's block
// whole rather than interleaving them.
func TestRank_GroupsByProvider(t *testing.T) {
	got := Rank([]ModelEntry{
		ent("big", "b-1", "2026-09-09", true),
		ent("big", "b-2", "2026-09-08", true),
		ent("big", "b-3", "2026-09-07", true),
		ent("anthropic", "a-1", "2026-01-02", true),
		ent("anthropic", "a-2", "2026-01-01", true),
	})
	requireOrder(t, got,
		"anthropic/a-1", "anthropic/a-2",
		"big/b-1", "big/b-2", "big/b-3",
	)
}

// TestRank_AlphabeticalWithinProvider pins the intra-provider order:
// model id ascending, independent of release date. Release order was
// the previous contract; ids are what a reader scans for.
func TestRank_AlphabeticalWithinProvider(t *testing.T) {
	got := Rank([]ModelEntry{
		ent("p", "old", "2024-01-01", true),
		ent("p", "newest", "2026-05-05", true),
		ent("p", "mid", "2025-03-03", true),
	})
	requireOrder(t, got, "p/mid", "p/newest", "p/old")
}

// TestRank_RoutableBeforeUnroutable proves the truncated default view
// never spends a slot on a provider foo has no adapter for, even when
// the unroutable model is far newer.
func TestRank_RoutableBeforeUnroutable(t *testing.T) {
	got := Rank([]ModelEntry{
		ent("exotic", "x-1", "2026-12-31", false),
		ent("exotic", "x-2", "2026-12-30", false),
		ent("openai", "o-1", "2020-01-01", true),
	})
	requireOrder(t, got, "openai/o-1", "exotic/x-1", "exotic/x-2")

	shown, omitted := Truncate(got, 1)
	if omitted != 2 {
		t.Fatalf("omitted: got %d, want 2", omitted)
	}
	if key(shown[0]) != "openai/o-1" {
		t.Fatalf("truncated head: got %q, want routable openai/o-1", key(shown[0]))
	}
}

// TestRank_DeterministicTies fixes the ordering keys: providers sort
// alphabetically and ids sort alphabetically inside each provider.
// Without both, the truncated view would vary run to run (Go map order).
func TestRank_DeterministicTies(t *testing.T) {
	in := []ModelEntry{
		ent("zeta", "z", "2026-01-01", true),
		ent("alpha", "b", "2026-01-01", true),
		ent("alpha", "a", "2026-01-01", true),
		ent("mid", "m", "2026-01-01", true),
	}
	want := []string{"alpha/a", "alpha/b", "mid/m", "zeta/z"}
	// Repeat: a map-order dependency shows up as an intermittent
	// failure, so one pass is not evidence of determinism.
	for i := 0; i < 50; i++ {
		requireOrder(t, Rank(in), want...)
	}
}

func TestTruncate(t *testing.T) {
	five := []ModelEntry{
		ent("p", "1", "", true), ent("p", "2", "", true), ent("p", "3", "", true),
		ent("p", "4", "", true), ent("p", "5", "", true),
	}
	for _, tc := range []struct {
		name        string
		limit       int
		wantShown   int
		wantOmitted int
	}{
		{"under limit", 10, 5, 0},
		{"exactly at limit", 5, 5, 0},
		{"over limit", 2, 2, 3},
		{"zero means all", 0, 5, 0},
		{"negative means all", -1, 5, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shown, omitted := Truncate(five, tc.limit)
			if len(shown) != tc.wantShown {
				t.Errorf("shown: got %d, want %d", len(shown), tc.wantShown)
			}
			if omitted != tc.wantOmitted {
				t.Errorf("omitted: got %d, want %d", omitted, tc.wantOmitted)
			}
		})
	}
}

// TestListModels_RanksSourceOutput proves ListModels ranks rather than
// passing the source's order through. The fixture is handed to it in an
// order no ranking rule would produce.
func TestListModels_RanksSourceOutput(t *testing.T) {
	src := fixtureCatalog{entries: []ModelEntry{
		ent("unwired", "u", "2026-12-01", false),
		ent("openai", "old", "2020-01-01", true),
		ent("openai", "new", "2026-06-01", true),
	}}
	got, err := ListModels(context.Background(), src)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	requireOrder(t, got, "openai/new", "openai/old", "unwired/u")
}

func TestListModels_PropagatesSourceError(t *testing.T) {
	sentinel := errors.New("source down")
	_, err := ListModels(context.Background(), fixtureCatalog{err: sentinel})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error: got %v, want wrap of %v", err, sentinel)
	}
}

// TestEntryFromAim_ProjectsFields covers the aim→ModelEntry projection,
// including the nil-Cost case: aim makes Cost a pointer precisely
// because open-weight entries omit it, and a naive dereference panics.
func TestEntryFromAim_ProjectsFields(t *testing.T) {
	got := entryFromAim(aim.Model{
		ID:          "m-1",
		Name:        "Model One",
		Provider:    "openai",
		ToolCall:    true,
		Reasoning:   true,
		ReleaseDate: "2026-02-02",
		Limit:       aim.Limits{Context: 4242},
		Cost:        &aim.Cost{Input: 1.5, Output: 7.5},
	})
	if got.ID != "m-1" || got.Name != "Model One" || got.Provider != "openai" {
		t.Errorf("identity fields: %+v", got)
	}
	if got.Context != 4242 {
		t.Errorf("Context: got %d, want 4242 (from Limit.Context)", got.Context)
	}
	if !got.ToolCall || !got.Reasoning {
		t.Errorf("capability flags: %+v", got)
	}
	if got.Released != "2026-02-02" {
		t.Errorf("Released: got %q", got.Released)
	}
	if got.InputCost != 1.5 || got.OutputCost != 7.5 {
		t.Errorf("cost: got in=%v out=%v, want 1.5/7.5", got.InputCost, got.OutputCost)
	}
	if got.Source != SourceCatalog {
		t.Errorf("Source: got %q, want %q", got.Source, SourceCatalog)
	}
	if !got.Routable {
		t.Error("openai is a compiled-in scheme; want Routable")
	}

	t.Run("nil cost", func(t *testing.T) {
		nilCost := entryFromAim(aim.Model{ID: "free", Provider: "openai"})
		if nilCost.InputCost != 0 || nilCost.OutputCost != 0 {
			t.Errorf("nil Cost must yield zero costs, got %+v", nilCost)
		}
	})

	t.Run("unroutable provider", func(t *testing.T) {
		e := entryFromAim(aim.Model{ID: "x", Provider: "some-aggregator-foo-cannot-reach"})
		if e.Routable {
			t.Error("provider with no compiled-in adapter must not be Routable")
		}
	})
}

// TestRoutableProviders_DerivedFromKit guards against the set being
// hardcoded: it must track kit's registered schemes.
func TestRoutableProviders_DerivedFromKit(t *testing.T) {
	got := routableProviders()
	for _, want := range []string{"openai", "anthropic", "google"} {
		if !got[want] {
			t.Errorf("scheme %q registered in kit but missing from routable set", want)
		}
	}
	if got["definitely-not-a-scheme"] {
		t.Error("unregistered scheme reported routable")
	}
}

// TestAimCatalog_WrapsRegistryError checks the catalog error carries
// foo's own prefix, so an operator can tell a catalog read from any
// other failure in the command.
func TestAimCatalog_WrapsRegistryError(t *testing.T) {
	// Point the cache at an empty temp dir. Without this aim serves a
	// previously-cached catalog from the real XDG dir and the source
	// is never consulted, so the test passes vacuously on any machine
	// that has run foo before.
	reg := aim.NewRegistry(
		aim.WithSource(failingSource{}),
		aim.WithCacheOpts(aim.WithCacheDir(t.TempDir())),
	)
	_, err := NewAimCatalog(reg).ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error from failing source")
	}
	if !strings.Contains(err.Error(), "foo: read model catalog") {
		t.Errorf("error missing foo prefix: %v", err)
	}
}

type failingSource struct{}

func (failingSource) Fetch(context.Context) (map[string]*aim.Provider, error) {
	return nil, errors.New("boom")
}

// TestRank_ContextBreaksIDTies pins the third sort key. Catalog data
// gives a provider unique ids, so this is a determinism guard rather
// than a case that arises upstream — but without it two rows sharing a
// provider and an id would order by map iteration.
func TestRank_ContextBreaksIDTies(t *testing.T) {
	small := ent("p", "dup", "2026-01-01", true)
	small.Context = 8192
	large := ent("p", "dup", "2026-01-01", true)
	large.Context = 1000000

	for i := 0; i < 50; i++ {
		got := Rank([]ModelEntry{small, large})
		if got[0].Context != 1000000 {
			t.Fatalf("iteration %d: head context = %d, want the larger 1000000",
				i, got[0].Context)
		}
	}
}
