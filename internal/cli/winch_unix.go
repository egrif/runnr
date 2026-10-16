//go:build unix

package cli

import (
	"os"
	"os/signal"
	"syscall"
)

func notifyResize(ch chan<- os.Signal) { signal.Notify(ch, syscall.SIGWINCH) }
