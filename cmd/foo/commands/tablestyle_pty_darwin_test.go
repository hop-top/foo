//go:build darwin

package commands

import (
	"os"
	"syscall"
	"unsafe"
)

// ptsname returns the slave device path for the given pty master.
// Darwin exposes it through the TIOCPTYGNAME ioctl, which writes a
// NUL-terminated path into a 128-byte buffer.
func ptsname(f *os.File) (string, error) {
	var buf [128]byte
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(),
		syscall.TIOCPTYGNAME, uintptr(unsafe.Pointer(&buf[0]))); errno != 0 {
		return "", errno
	}
	for i, b := range buf {
		if b == 0 {
			return string(buf[:i]), nil
		}
	}
	return string(buf[:]), nil
}

// unlockpt grants access to the slave side. TIOCPTYGRANT and
// TIOCPTYUNLK are the Darwin equivalents of Linux's TIOCSPTLCK.
func unlockpt(f *os.File) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(),
		syscall.TIOCPTYGRANT, 0); errno != 0 {
		return errno
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(),
		syscall.TIOCPTYUNLK, 0); errno != 0 {
		return errno
	}
	return nil
}
