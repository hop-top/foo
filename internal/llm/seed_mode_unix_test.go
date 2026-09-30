//go:build unix

package llm

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// withUmask pins the process umask for one test so the file mode seen
// on disk reflects the mode foo asked for, not the developer's umask
// (under 077 a 0644 create would pass for 0600).
func withUmask(t *testing.T, mask int) {
	t.Helper()
	old := syscall.Umask(mask)
	t.Cleanup(func() { syscall.Umask(old) })
}

// A seeded llm.yaml is where operators put providers.<scheme>.api_key,
// so it starts owner-only.
func TestSeedDefaultPool_CreatesOwnerOnly(t *testing.T) {
	withUmask(t, 0o022)
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	if wrote, err := SeedDefaultPool(); err != nil || !wrote {
		t.Fatalf("SeedDefaultPool = (%v, %v), want (true, nil)", wrote, err)
	}
	fi, err := os.Stat(filepath.Join(tmp, "hop", "llm.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("seeded llm.yaml mode = %#o, want 0600", got)
	}
}

func TestWriteNew_OwnerOnly(t *testing.T) {
	withUmask(t, 0o022)
	path := filepath.Join(t.TempDir(), "llm.yaml")

	if err := writeNew(path, []byte("pool: []\n")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("writeNew mode = %#o, want 0600", got)
	}
}
