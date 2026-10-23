package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Suspending a run stops it to be resumed later: it ends "suspended", and
// a resume carries it on from the step it stopped before. A request takes
// effect once the step running now has finished (its command isn't cut
// short), or at once if the run is waiting on the operator (a question, a
// pause) or the request says now (the running step is stopped, as an
// abort would, and done again on resume).

// SuspendError is why a run was suspended.
type SuspendError struct {
	By     string    `json:"by,omitempty"`
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at"`
	// Now: don't wait for the running step to finish.
	Now bool `json:"now,omitempty"`
}

func (e *SuspendError) Error() string {
	msg := "suspended"
	if e.By != "" {
		msg += " by " + e.By
	}
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	return msg
}

// Suspend asks the run to suspend (see SuspendError). A later request
// replaces an earlier one; now on either makes it now.
func (c *Controls) Suspend(e *SuspendError) {
	c.mu.Lock()
	if e.At.IsZero() {
		e.At = time.Now()
	}
	if c.suspend != nil && c.suspend.Now {
		e.Now = true
	}
	c.suspend = e
	hook := c.onSuspend
	c.mu.Unlock()
	if hook != nil {
		hook(e)
	}
}

// CancelSuspend withdraws a request that hasn't taken effect yet.
func (c *Controls) CancelSuspend() {
	c.mu.Lock()
	c.suspend = nil
	c.mu.Unlock()
}

// Suspending is the request to suspend, if there is one.
func (c *Controls) Suspending() *SuspendError {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.suspend
}

func (c *Controls) setSuspendHook(f func(*SuspendError)) {
	c.mu.Lock()
	c.onSuspend = f
	c.mu.Unlock()
}

// suspendState is the questions to the operator waiting now, which a
// request to suspend ends.
type suspendState struct {
	mu      sync.Mutex
	n       int
	waiting map[int]context.CancelCauseFunc
}

// asking is ctx for a question to the operator (or a pause): a request to
// suspend ends it, so the run doesn't wait on an answer to suspend.
func (x *executor) asking(ctx context.Context) (context.Context, func()) {
	q, cancel := context.WithCancelCause(ctx)
	if e := x.ctl.Suspending(); e != nil {
		cancel(e)
		return q, func() {}
	}
	x.sus.mu.Lock()
	if x.sus.waiting == nil {
		x.sus.waiting = map[int]context.CancelCauseFunc{}
	}
	x.sus.n++
	id := x.sus.n
	x.sus.waiting[id] = cancel
	x.sus.mu.Unlock()
	return q, func() {
		x.sus.mu.Lock()
		delete(x.sus.waiting, id)
		x.sus.mu.Unlock()
		cancel(nil)
	}
}

// onSuspend acts on a request: questions waiting end now; with now, so
// does the step running.
func (x *executor) onSuspend(e *SuspendError) {
	if e.Now && x.cancelRun != nil {
		x.cancelRun(e)
		return
	}
	x.sus.mu.Lock()
	defer x.sus.mu.Unlock()
	for _, cancel := range x.sus.waiting {
		cancel(e)
	}
}

// suspension is the request the run is ending for, if err (how its steps
// ended) comes of one; an abort from outside wins.
func (x *executor) suspension(ctx context.Context, err error) *SuspendError {
	if err == nil {
		return nil
	}
	if ae := (*AbortError)(nil); errors.As(context.Cause(ctx), &ae) {
		return nil
	}
	var se *SuspendError
	if errors.As(err, &se) || errors.As(context.Cause(ctx), &se) {
		return se
	}
	return x.ctl.Suspending()
}

// HowItEnded says how the run had ended before this resume: "it had
// failed: …", "it was suspended by …".
func (r Resumption) HowItEnded() string {
	if r.Status == StatusSuspended {
		if r.Error != "" {
			return "it was " + r.Error
		}
		return "it was suspended"
	}
	if r.Error != "" {
		return fmt.Sprintf("it had %s: %s", r.Status, r.Error)
	}
	return "it had " + string(r.Status)
}
