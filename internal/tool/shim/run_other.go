//go:build !unix

package shim

import "os/exec"

// setProcessGroup has no process-group equivalent here; cancellation
// kills the direct child only.
func setProcessGroup(*exec.Cmd) {}
