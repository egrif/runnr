package engine

import (
	"errors"
	"fmt"
	"time"
)

// ErrReopen, returned by an interactor while the current step waits on the
// operator, means go back to the step Reopen was asked for: it pauses
// before running again, and the run carries on from there.
var ErrReopen = errors.New("reopen")

// Reopener is the part of a Session that goes back a step.
type Reopener interface {
	// Reopenable is the step that can be re-opened now (-1: none): the
	// last one that succeeded, while the current step hasn't run yet and
	// only skipped steps lie between them. Ask while the run is waiting on
	// the operator at the current step.
	Reopenable() int
	// Reopen asks to go back to step idx (which must be Reopenable); the
	// interactor then returns ErrReopen from the question it's waiting on.
	Reopen(idx int) error
}

var _ Reopener = (*executor)(nil)

func (x *executor) Reopenable() int {
	x.em.Lock()
	cur := x.cur
	x.em.Unlock()
	if !x.canPause() || cur <= 0 || cur >= len(x.run.Steps) || x.run.Steps[cur].Attempts > 0 {
		return -1
	}
	for j := cur - 1; j >= 0; j-- {
		s := x.run.Steps[j]
		if s.Status == StatusSkipped {
			continue
		}
		if s.Status == StatusSucceeded && s.Kind != "background" {
			return j
		}
		return -1
	}
	return -1
}

func (x *executor) Reopen(j int) error {
	if r := x.Reopenable(); r < 0 || r != j {
		return fmt.Errorf("step %d can't be re-opened now: only the last step that succeeded can, while the run waits before the next one", j+1)
	}
	x.em.Lock()
	x.reopenTo = j
	x.em.Unlock()
	return nil
}

// takeReopen returns the step asked to re-open (-1: none) and clears it.
func (x *executor) takeReopen() int {
	x.em.Lock()
	defer x.em.Unlock()
	j := x.reopenTo
	x.reopenTo = -1
	return j
}

// reopen goes back from step k (waiting, not run) to step j: the steps
// from j to k are pending again, and j pauses before it runs.
func (x *executor) reopen(k, j int) {
	if w := x.run.Window; w != nil {
		if w.OpenStep == k && x.windowOpenedBy == k {
			w.Opened = time.Time{} // k opened it when it started waiting
		}
		if w.CloseStep >= j && w.CloseStep < k && !w.Closed.IsZero() {
			w.Closed = time.Time{} // open again until the close step runs again
		}
	}
	for i := j; i <= k; i++ {
		old := x.run.Steps[i]
		x.run.Steps[i] = StepResult{Index: i, Name: old.Name, Section: old.Section, Owner: old.Owner, Kind: old.Kind, Status: StatusPending, Notes: old.Notes, Edited: old.Edited, Before: old.Before}
		x.notify(Event{Kind: EventStepProgress, Step: i, Result: x.result(i)})
	}
	x.em.Lock()
	x.pauseAt = j
	x.em.Unlock()
	x.notify(Event{Kind: EventOutput, Step: j, Stream: "info", Text: "↺ re-opened: it waits for you before running again"})
	if x.run.Window != nil {
		x.notify(Event{Kind: EventWindow, Step: j, Run: x.snapshot()})
	}
	x.save()
}

// takePause reports (once) whether step i was re-opened to pause before it.
func (x *executor) takePause(i int) bool {
	x.em.Lock()
	defer x.em.Unlock()
	if x.pauseAt != i {
		return false
	}
	x.pauseAt = -1
	return true
}
