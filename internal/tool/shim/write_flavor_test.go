package shim

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// busybox sed prints "This is not GNU sed version 4.0" for --version:
// the flavor comes from the multi-call binary the link resolves to.
func TestFlavor_BusyboxAppletLink(t *testing.T) {
	dir := tempDir(t)
	bb := writeScript(t, dir, "busybox", `echo "This is not GNU sed version 4.0"`)
	link := filepath.Join(dir, "sed")
	if err := os.Symlink(bb, link); err != nil {
		t.Fatal(err)
	}
	if got := (&Flavors{}).Detect(link); got != FlavorBusyBox {
		t.Errorf("Detect(sed -> busybox) = %s; want busybox", got)
	}
	plain := writeScript(t, dir, "plainsed", `echo "This is not GNU sed version 4.0"`)
	if got := (&Flavors{}).Detect(plain); got != FlavorBSD {
		t.Errorf("Detect(plain) = %s; want bsd", got)
	}
}

// A probe that does not finish says nothing about the binary: Detect
// falls back to bsd for that call and keeps no cache entry, so the next
// call (or the next foo run, through the cache file) probes again
// instead of pinning a GNU binary to bsd until it changes on disk.
func TestFlavor_UnfinishedProbeNotCached(t *testing.T) {
	prev := versionTimeout
	versionTimeout = 200 * time.Millisecond
	t.Cleanup(func() { versionTimeout = prev })

	dir := tempDir(t)
	slow := filepath.Join(dir, "slow")
	writeFile(t, slow, "")
	bin := writeScript(t, dir, "gnuwc", `if [ -f '`+slow+`' ]; then sleep 3; fi
echo "wc (GNU coreutils) 9.1"`)
	cache := filepath.Join(dir, "flavors.json")
	f := NewFlavors(cache)

	if got := f.Detect(bin); got != FlavorBSD {
		t.Fatalf("Detect while the probe times out = %s; want the bsd fallback", got)
	}
	if data, err := os.ReadFile(cache); err == nil && strings.Contains(string(data), bin) {
		t.Errorf("unfinished probe persisted: %s", data)
	}

	// From here the probe must finish, however loaded the machine.
	versionTimeout = time.Minute
	if err := os.Remove(slow); err != nil {
		t.Fatal(err)
	}
	if got := f.Detect(bin); got != FlavorGNU {
		t.Errorf("Detect once the probe finishes = %s; want gnu", got)
	}
	if got := NewFlavors(cache).Detect(bin); got != FlavorGNU {
		t.Errorf("Detect from the cache file = %s; want gnu", got)
	}
}
