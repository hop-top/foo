package llm

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hop.top/aim"
)

// fixedNow is the render instant every age assertion below is measured
// against, so the arithmetic is pinned rather than racing a real clock.
var fixedNow = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

// TestCatalogProvenance_NeverFetched: a first run has no meta.json, and
// must read as "never fetched" rather than as a cache from year 1.
func TestCatalogProvenance_NeverFetched(t *testing.T) {
	p := catalogProvenanceFrom(aim.Meta{}, fixedNow)

	if p.Fetched {
		t.Error("Fetched = true for an absent meta.json")
	}
	if !p.FetchedAt.IsZero() || p.Age != 0 || p.TTL != 0 || p.Stale {
		t.Errorf("non-zero fields on a never-fetched provenance: %+v", p)
	}
	if got := p.Describe(); !strings.Contains(got, "no cache before this run") {
		t.Errorf("Describe() = %q, want the first-run phrasing", got)
	}
}

// TestCatalogProvenance_FreshAndStale pins the age arithmetic and the
// staleness verdict against a fixed clock.
func TestCatalogProvenance_FreshAndStale(t *testing.T) {
	for _, tc := range []struct {
		name      string
		age, ttl  time.Duration
		wantStale bool
		wantPhr   string
	}{
		{"minutes old", 44 * time.Minute, 24 * time.Hour, false, "cached 44m ago"},
		{"hours old", 3*time.Hour + 20*time.Minute, 24 * time.Hour, false, "cached 3h ago"},
		{"seconds old", 9 * time.Second, 24 * time.Hour, false, "cached 9s ago"},
		{"days old and stale", 50 * time.Hour, 24 * time.Hour, true, "cached 2d ago"},
		{"exactly at the TTL is stale", 24 * time.Hour, 24 * time.Hour, true, "cached 1d ago"},
		{"one second inside the TTL is fresh", 24*time.Hour - time.Second, 24 * time.Hour, false, "cached 23h ago"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := catalogProvenanceFrom(aim.Meta{
				Present:   true,
				LastFetch: fixedNow.Add(-tc.age),
				TTL:       tc.ttl,
			}, fixedNow)

			if !p.Fetched {
				t.Fatal("Fetched = false for a present meta")
			}
			if p.Age != tc.age {
				t.Errorf("Age = %v, want %v", p.Age, tc.age)
			}
			if p.TTL != tc.ttl {
				t.Errorf("TTL = %v, want %v", p.TTL, tc.ttl)
			}
			if p.Stale != tc.wantStale {
				t.Errorf("Stale = %v, want %v", p.Stale, tc.wantStale)
			}
			got := p.Describe()
			if !strings.Contains(got, tc.wantPhr) {
				t.Errorf("Describe() = %q, want it to contain %q", got, tc.wantPhr)
			}
			// The refresh hint is the actionable half and belongs only
			// on the state where it is worth acting.
			if hasHint := strings.Contains(got, "--refresh"); hasHint != tc.wantStale {
				t.Errorf("Describe() = %q: --refresh hint present=%v, want %v", got, hasHint, tc.wantStale)
			}
		})
	}
}

// TestCatalogProvenance_UnknownTTLIsNotStale: aim recording no window
// means staleness is unknowable, and guessing "stale" would send the
// user to --refresh on every single run.
func TestCatalogProvenance_UnknownTTLIsNotStale(t *testing.T) {
	p := catalogProvenanceFrom(aim.Meta{
		Present:   true,
		LastFetch: fixedNow.Add(-100 * time.Hour),
	}, fixedNow)

	if p.Stale {
		t.Error("Stale = true with no TTL recorded")
	}
	if got := p.Describe(); strings.Contains(got, "--refresh") {
		t.Errorf("Describe() = %q, want no refresh hint when the window is unknown", got)
	}
}

// TestHumanizeAge covers the unit boundaries and the clock-skew guard.
// time.Duration.String() would print "3h12m9.432s" here, which is both
// unreadable in a footer and different on every run.
func TestHumanizeAge(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{59 * time.Second, "59s"},
		{time.Minute, "1m"},
		{59 * time.Minute, "59m"},
		{time.Hour, "1h"},
		{3*time.Hour + 59*time.Minute, "3h"},
		{23 * time.Hour, "23h"},
		{24 * time.Hour, "1d"},
		{47 * time.Hour, "1d"},
		{-5 * time.Minute, "0s"},
	} {
		if got := humanizeAge(tc.in); got != tc.want {
			t.Errorf("humanizeAge(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestReadCatalogProvenance_DoesNotFetch guards the file's central
// claim: reading provenance is a disk read of aim's metadata and must
// never reach models.dev. Pointing XDG_CACHE_HOME at an empty dir means
// any fetch would be the only way to produce a Present meta.
func TestReadCatalogProvenance_DoesNotFetch(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	p := ReadCatalogProvenance(aim.NewRegistry())
	if p.Fetched {
		t.Errorf("ReadCatalogProvenance populated a cold cache: %+v", p)
	}
}

// fakeAimSource stands in for models.dev so a refresh is observable
// without the network.
type fakeAimSource struct {
	fetches atomic.Int64
	err     error
}

func (s *fakeAimSource) Fetch(context.Context) (map[string]*aim.Provider, error) {
	s.fetches.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	return map[string]*aim.Provider{
		"openai": {ID: "openai", Models: map[string]*aim.Model{
			"gpt-x": {ID: "gpt-x", Provider: "openai"},
		}},
	}, nil
}

// TestRefreshCatalog_ForcesAFetch is the catalog half of --refresh. A
// warm, unexpired cache must still be refetched, which is the one thing
// that distinguishes --refresh from an ordinary listing.
func TestRefreshCatalog_ForcesAFetch(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	src := &fakeAimSource{}
	reg := aim.NewRegistry(aim.WithSource(src))

	// Populate the cache, then confirm an ordinary read is served from
	// it — otherwise the forced fetch below proves nothing.
	if _, err := reg.Models(context.Background(), aim.Filter{}); err != nil {
		t.Fatalf("warm: %v", err)
	}
	warm := src.fetches.Load()
	if warm != 1 {
		t.Fatalf("warming fetched %d time(s), want 1", warm)
	}
	if _, err := reg.Cache().Refresh(context.Background(), false); err != nil {
		t.Fatalf("unforced refresh: %v", err)
	}
	if got := src.fetches.Load(); got != warm {
		t.Fatalf("an unforced refresh refetched (%d), so the cache was never warm", got)
	}

	if err := RefreshCatalog(context.Background(), reg); err != nil {
		t.Fatalf("RefreshCatalog: %v", err)
	}
	if got := src.fetches.Load(); got != warm+1 {
		t.Errorf("RefreshCatalog did not force a fetch: %d, want %d", got, warm+1)
	}
}

// TestRefreshCatalog_SurfacesFetchErrors: the user asked for fresh data
// by name, so quietly serving the stale copy would answer a different
// question.
func TestRefreshCatalog_SurfacesFetchErrors(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	src := &fakeAimSource{err: errors.New("models.dev unreachable")}
	reg := aim.NewRegistry(aim.WithSource(src))

	err := RefreshCatalog(context.Background(), reg)
	if err == nil {
		t.Fatal("RefreshCatalog swallowed a fetch failure")
	}
	if !strings.Contains(err.Error(), "refresh model catalog") {
		t.Errorf("error does not name the operation: %v", err)
	}
}
