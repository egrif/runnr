//go:build unix

package engine

import (
	"os/exec"
	"syscall"
)

// setProcessGroup makes cancellation kill the whole command tree.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
