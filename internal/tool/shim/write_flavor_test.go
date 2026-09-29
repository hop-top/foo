package shim

import (
	"os"
	"path/filepath"
	"testing"
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
