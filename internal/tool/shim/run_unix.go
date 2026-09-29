//go:build unix

package shim

import (
	"os/exec"
	"syscall"
)

// setProcessGroup starts the child in its own process group and makes
// context cancellation SIGKILL the whole group, so a command's own
// children die with it.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
