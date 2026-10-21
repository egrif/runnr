//go:build unix

package engine

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// setProcessGroup makes cancellation (an abort, a timeout) stop the whole
// command tree, the way an operator would: Ctrl-C first, so a tool like
// Terraform can stop cleanly (and release its lock), then SIGTERM, then
// SIGKILL.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		go interruptGroup(cmd.Process.Pid)
		return nil
	}
	cmd.WaitDelay = interruptGrace + 5*time.Second
}

// interruptGrace is how long an interrupted command gets before it's killed.
const interruptGrace = 20 * time.Second

// interruptGroup stops process group pid: SIGINT, then SIGTERM after 10s,
// then SIGKILL after 20s (each only if it's still there).
func interruptGroup(pid int) {
	alive := func() bool { return syscall.Kill(-pid, 0) == nil }
	_ = syscall.Kill(-pid, syscall.SIGINT)
	for _, step := range []struct {
		after time.Duration
		sig   syscall.Signal
	}{{interruptGrace / 2, syscall.SIGTERM}, {interruptGrace / 2, syscall.SIGKILL}} {
		end := time.Now().Add(step.after)
		for time.Now().Before(end) {
			if !alive() {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		_ = syscall.Kill(-pid, step.sig)
	}
}

// hangup sends SIGHUP to a terminal session's process group, as closing
// its terminal would.
func hangup(p *os.Process) {
	_ = syscall.Kill(-p.Pid, syscall.SIGHUP)
}

// interruptTerm stops a terminal session's commands (the pty's session is
// its own process group) as interruptGroup does.
func interruptTerm(pid int) { go interruptGroup(pid) }
