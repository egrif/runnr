package cli

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// interruptHolds are what Ctrl-C belongs to instead of the run: an
// interactive shell, a tty step's terminal, the prompt after a tty step
// failed. While anything holds it, SIGINT doesn't stop the run. (A tty
// step's terminal is raw, so Ctrl-C reaches its command and not runnr; but
// a Ctrl-C meant for the command often lands just after it has ended, and
// that mustn't throw the run away.)
type interruptHolds struct {
	mu    sync.Mutex
	next  int
	hints map[int]string // by hold, in order taken; "" for none
	out   io.Writer
	// stopStep, if set, stops the step whose command is running (it
	// reports whether one was): Ctrl-C stops the step, and a second one
	// soon after stops the run.
	stopStep func() bool
	stopped  time.Time
}

// stepFirst reports whether a Ctrl-C stopped the running step instead of
// the run (not when another came just before it).
func (h *interruptHolds) stepFirst() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	stop, recent := h.stopStep, time.Since(h.stopped) < 3*time.Second
	h.mu.Unlock()
	if stop == nil || recent || !stop() {
		return false
	}
	h.mu.Lock()
	h.stopped = time.Now()
	h.mu.Unlock()
	if h.out != nil {
		fmt.Fprintf(h.out, "\r\n  ■ Ctrl-C: stopping the step (Ctrl-C again to abort the run)\n")
	}
	return true
}

func (h *interruptHolds) setStopStep(f func() bool) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.stopStep = f
	h.mu.Unlock()
}

// hold takes Ctrl-C until release is called (more calls do nothing).
// hint, if not "", is shown when a Ctrl-C is ignored while this is the
// latest hold (a shell opened from a prompt shows none).
func (h *interruptHolds) hold(hint string) (release func()) {
	if h == nil {
		return func() {}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hints == nil {
		h.hints = map[int]string{}
	}
	id := h.next
	h.next++
	h.hints[id] = hint
	return func() {
		h.mu.Lock()
		delete(h.hints, id)
		h.mu.Unlock()
	}
}

// held reports whether Ctrl-C is held, and the latest hold's hint.
func (h *interruptHolds) held() (bool, string) {
	if h == nil {
		return false, ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	hint, latest := "", -1
	for id, s := range h.hints {
		if id > latest {
			hint, latest = s, id
		}
	}
	return len(h.hints) > 0, hint
}

// ignored is called for a Ctrl-C that was held: it shows the hint.
func (h *interruptHolds) ignored() {
	if _, hint := h.held(); hint != "" && h.out != nil {
		fmt.Fprintf(h.out, "\r\n  %s\n", hint)
	}
}
