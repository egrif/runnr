//go:build unix

package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// An aborted step is interrupted (Ctrl-C) first, so it can clean up.
func TestAbortInterruptsFirst(t *testing.T) {
	dir := t.TempDir()
	mark := filepath.Join(dir, "cleaned")
	rb := mustParse(t, `
name: t
steps:
  - {name: long, run: 'trap "echo cleaning; touch `+mark+`; exit 130" INT; echo started; while :; do sleep 0.1; done'}
`)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(700 * time.Millisecond); cancel() }()
	start := time.Now()
	r := Execute(ctx, rb, Unattended{}, "", Options{})
	if _, err := os.Stat(mark); err != nil {
		t.Fatalf("the step wasn't interrupted (no cleanup): %s %q", r.Status, r.Steps[0].Output)
	}
	if time.Since(start) > 5*time.Second || r.Status != StatusAborted || !strings.Contains(r.Steps[0].Output, "cleaning") {
		t.Fatalf("%s in %s: %q", r.Status, time.Since(start), r.Steps[0].Output)
	}
}
