//go:build !unix

package cli

import "os"

func notifyResize(chan<- os.Signal) {}
