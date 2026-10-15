//go:build !unix

package engine

import "os/exec"

func setProcessGroup(cmd *exec.Cmd) {}
