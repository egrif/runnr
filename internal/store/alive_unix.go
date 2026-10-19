//go:build unix

package store

import (
	"errors"
	"syscall"
)

// processAlive says whether a process exists (signal 0 checks without
// sending anything; EPERM means it exists but isn't ours).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
