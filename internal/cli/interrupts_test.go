//go:build unix

package cli

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/runbook"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func interrupt(t *testing.T) {
	t.Helper()
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // for the signal to arrive
}

func waitHeld(t *testing.T, h *interruptHolds, want bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if held, _ := h.held(); held == want {
			return
		}
	}
	t.Fatalf("held never became %v", want)
}

// Ctrl-C that lands at the failure prompt of a tty step (a Ctrl-C meant for
// its command, which has just stopped) doesn't abort the run: the prompt
// stays, and says how to abort. After any other step, Ctrl-C there aborts
// the run, as before.
func TestCtrlCAtTTYFailurePrompt(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	stdin := os.Stdin
	os.Stdin = r // the keyboard reads from here
	defer func() { os.Stdin = stdin }()

	ctx, stop, holds := interruptible()
	defer stop()
	out := &lockedBuf{}
	holds.out = out
	term := &Terminal{out: &lockedBuf{}, total: 1, interrupts: holds}
	step := runbook.Step{Name: "watch", Run: "watch-it --watch", TTY: true}

	type answer struct {
		a   engine.FailureAction
		err error
	}
	ask := func(kind string) chan answer {
		ch := make(chan answer, 1)
		go func() {
			a, _, err := term.OnFailure(ctx, 0, step, engine.StepResult{Name: step.Name, Kind: kind, Status: engine.StatusFailed})
			ch <- answer{a, err}
		}()
		return ch
	}

	got := ask("tty")
	time.Sleep(200 * time.Millisecond) // for it to ask
	interrupt(t)
	if ctx.Err() != nil {
		t.Fatal("Ctrl-C at a tty step's failure prompt aborted the run")
	}
	if !strings.Contains(out.String(), "[a]bort ends the run") {
		t.Errorf("no hint after the Ctrl-C: %q", out.String())
	}
	if _, err := w.Write([]byte("s\n")); err != nil {
		t.Fatal(err)
	}
	if a := <-got; a.err != nil || a.a != engine.FailSkip {
		t.Fatalf("answer %v %v, want skip", a.a, a.err)
	}
	waitHeld(t, holds, false)

	got = ask("run")
	time.Sleep(100 * time.Millisecond)
	interrupt(t)
	if ctx.Err() == nil {
		t.Fatal("Ctrl-C at an ordinary step's failure prompt should abort the run")
	}
	if a := <-got; a.err == nil {
		t.Fatalf("answer %v, want an error", a.a)
	}
}

func TestInterruptHolds(t *testing.T) {
	var h *interruptHolds
	h.hold("x")() // nil: nothing held, nothing to release
	h = &interruptHolds{}
	a := h.hold("first")
	if held, hint := h.held(); !held || hint != "first" {
		t.Fatalf("%v %q", held, hint)
	}
	b := h.hold("") // e.g. a shell opened from the prompt: no hint
	if held, hint := h.held(); !held || hint != "" {
		t.Fatalf("%v %q", held, hint)
	}
	b()
	b()
	if held, hint := h.held(); !held || hint != "first" {
		t.Fatalf("after releasing one: %v %q", held, hint)
	}
	a()
	if held, _ := h.held(); held {
		t.Fatal("still held")
	}
}
