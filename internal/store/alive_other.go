//go:build !unix

package store

// processAlive can't tell here, so it assumes the process is running (and
// the run is only marked aborted with --force).
func processAlive(int) bool { return true }
