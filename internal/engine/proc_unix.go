//go:build unix

package engine

import (
	"os"
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

// hangup sends SIGHUP to a terminal session's process group, as closing
// its terminal would.
func hangup(p *os.Process) {
	_ = syscall.Kill(-p.Pid, syscall.SIGHUP)
}
