//go:build linux

package commands

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// ptsname returns the slave device path for the given pty master.
// Linux reports the pty index via TIOCGPTN; the path is derived from it.
func ptsname(f *os.File) (string, error) {
	var n uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(),
		syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))); errno != 0 {
		return "", errno
	}
	return fmt.Sprintf("/dev/pts/%d", n), nil
}

// unlockpt clears the slave-side lock so it can be opened.
func unlockpt(f *os.File) error {
	var unlock int32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(),
		syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		return errno
	}
	return nil
}
