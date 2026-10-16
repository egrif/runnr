//go:build !unix

package engine

import (
	"os"
	"os/exec"
)

func setProcessGroup(cmd *exec.Cmd) {}

func hangup(p *os.Process) { _ = p.Kill() }
