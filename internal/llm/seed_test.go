package llm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSeedDefaultPool_CleanHome writes the full seed when no llm.yaml
// exists. Verifies the file lands at the expected XDG path and the
// payload contains the three tiers' canonical aliases.
func TestSeedDefaultPool_CleanHome(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	wrote, err := SeedDefaultPool()
	if err != nil {
		t.Fatalf("SeedDefaultPool: %v", err)
	}
	if !wrote {
		t.Fatal("expected wrote=true on clean home")
	}

	path := filepath.Join(tmp, "hop", "llm.yaml")
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read seeded file: %v", readErr)
	}
	body := string(data)
	for _, want := range []string{
		"cheap-openai", "cheap-anthropic", "cheap-google",
		"balanced-openai", "balanced-anthropic", "balanced-google",
		"premium-openai", "premium-anthropic",
		"pool:",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("seed missing %q:\n%s", want, body)
		}
	}
	// Google premium tier is intentionally omitted from the seed (no
	// publicly-released ultra-class 2.0 model). Guard against accidental
	// re-introduction with a stale/wrong ID.
	for _, unwanted := range []string{"gemini-2.0-ultra", "premium-google"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("seed contains %q which is not a verified GA model:\n%s", unwanted, body)
		}
	}
}

// TestSeedDefaultPool_Idempotent confirms a second call after a
// successful seed is a no-op (returns wrote=false, file mtime
// unchanged at the byte level).
func TestSeedDefaultPool_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	wrote1, err := SeedDefaultPool()
	if err != nil || !wrote1 {
		t.Fatalf("first seed: wrote=%v err=%v", wrote1, err)
	}
	path := filepath.Join(tmp, "hop", "llm.yaml")
	before, _ := os.ReadFile(path)

	wrote2, err := SeedDefaultPool()
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if wrote2 {
		t.Fatal("expected wrote=false on second seed")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Errorf("file mutated on second seed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestSeedDefaultPool_ExistingFileWithoutPool appends pool: to a file
// that already has a default: and providers: block. Preserves the
// existing keys.
func TestSeedDefaultPool_ExistingFileWithoutPool(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	if err := os.MkdirAll(filepath.Join(tmp, "hop"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	existing := `default: anthropic://claude-3-5-sonnet-latest
providers:
  anthropic:
    api_key: sk-ant-existing
`
	path := filepath.Join(tmp, "hop", "llm.yaml")
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatalf("seed pre-existing: %v", err)
	}

	wrote, err := SeedDefaultPool()
	if err != nil {
		t.Fatalf("SeedDefaultPool: %v", err)
	}
	if !wrote {
		t.Fatal("expected wrote=true when pool: missing")
	}

	data, _ := os.ReadFile(path)
	body := string(data)
	for _, want := range []string{
		"default: anthropic://claude-3-5-sonnet-latest",
		"sk-ant-existing",
		"pool:",
		"cheap-openai",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("after-append body missing %q:\n%s", want, body)
		}
	}
}

// TestSeedDefaultPool_ExistingPoolPreserved confirms an authored pool
// block is never overwritten — even an empty one expresses intent.
func TestSeedDefaultPool_ExistingPoolPreserved(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	if err := os.MkdirAll(filepath.Join(tmp, "hop"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	existing := `default: anthropic://claude-3-5-sonnet-latest
pool:
  - alias: solo
    scheme: openai
    model: gpt-4o
`
	path := filepath.Join(tmp, "hop", "llm.yaml")
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatalf("seed pre-existing: %v", err)
	}

	wrote, err := SeedDefaultPool()
	if err != nil {
		t.Fatalf("SeedDefaultPool: %v", err)
	}
	if wrote {
		t.Fatal("expected wrote=false when pool: already present")
	}

	data, _ := os.ReadFile(path)
	body := string(data)
	if strings.Contains(body, "cheap-openai") {
		t.Errorf("seed clobbered operator pool:\n%s", body)
	}
	if !strings.Contains(body, "alias: solo") {
		t.Errorf("operator alias lost:\n%s", body)
	}
}

// TestSeedDefaultPool_InvalidYAMLLeftAlone covers the "soft-fail on
// corrupt file" branch. SeedDefaultPool refuses to mutate a file it
// can't parse — the operator should fix the corruption first.
func TestSeedDefaultPool_InvalidYAMLLeftAlone(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	if err := os.MkdirAll(filepath.Join(tmp, "hop"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	corrupt := "this: is: not: valid yaml: ::: ::\n"
	path := filepath.Join(tmp, "hop", "llm.yaml")
	if err := os.WriteFile(path, []byte(corrupt), 0o644); err != nil {
		t.Fatalf("seed corrupt: %v", err)
	}

	wrote, err := SeedDefaultPool()
	if err != nil {
		t.Fatalf("err must be nil on soft fail: %v", err)
	}
	if wrote {
		t.Fatal("must not write when existing file is unparseable")
	}

	data, _ := os.ReadFile(path)
	if string(data) != corrupt {
		t.Errorf("file mutated despite parse failure:\n%s", string(data))
	}
}

func TestSeedPath_HonorsXDG(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	got, err := SeedPath()
	if err != nil {
		t.Fatalf("SeedPath: %v", err)
	}
	want := filepath.Join(tmp, "hop", "llm.yaml")
	if got != want {
		t.Errorf("SeedPath = %q, want %q", got, want)
	}
}
