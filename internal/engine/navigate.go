package engine

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Moving a run about, and stopping a step.
//
// GoTo, while the run waits on the operator at a step (before it, after it
// failed, or pausing after it), goes back to an earlier step (it and the
// steps after it run again), runs the current step again, or jumps ahead
// (the steps between are skipped). The step gone to waits for the
// operator before it runs. The interactor then returns ErrReopen from the
// question it's waiting on.
//
// StopStep interrupts the step whose command is running, as abort does
// (Ctrl-C, then SIGTERM, then SIGKILL), but only the step: it fails,
// "stopped", and the run asks what to do as for any failure.

// Navigator is implemented by sessions that can go to another step.
type Navigator interface {
	// GoTo asks to go to step idx. force goes back past a point of no
	// return (a step with confirm_typing that ran).
	GoTo(idx int, force bool) error
}

// StepStopper is implemented by sessions whose running step can be
// stopped.
type StepStopper interface {
	// StopStep stops step idx's command (-1: whichever is running).
	StopStep(idx int, by string) error
	// RunningStep is the step whose command can be stopped now (-1: none).
	RunningStep() int
}

var (
	_ Navigator   = (*executor)(nil)
	_ StepStopper = (*executor)(nil)
)

// ErrPastNoReturn: going back would run a point of no return again.
var ErrPastNoReturn = errors.New("past a point of no return")

func (x *executor) GoTo(j int, force bool) error {
	if !x.canPause() {
		return errors.New("only an interactive run can go to another step")
	}
	x.em.Lock()
	cur := x.cur
	x.em.Unlock()
	if cur < 0 || cur >= len(x.run.Steps) {
		return errors.New("the run isn't at a step")
	}
	if j < 0 || j >= len(x.run.Steps) {
		return fmt.Errorf("no step %d", j+1)
	}
	if j <= cur {
		var pnr []string
		for i := j; i <= cur; i++ {
			if b, ok := x.background[i]; ok && b != nil && !b.done() {
				return fmt.Errorf("step %d's background job is still running: end it first", i+1)
			}
			if i < cur && x.def(i).ConfirmTyping != "" && x.run.Steps[i].Status == StatusSucceeded {
				pnr = append(pnr, fmt.Sprintf("%d (%s)", i+1, x.run.Steps[i].Name))
			}
		}
		if len(pnr) > 0 && !force {
			return fmt.Errorf("%w: going back runs step %s again", ErrPastNoReturn, strings.Join(pnr, ", "))
		}
	}
	x.em.Lock()
	x.reopenTo = j
	x.em.Unlock()
	return nil
}

// jumpTo skips from step k (where the run waits) to step j (after it): the
// steps between that haven't finished are skipped, and j waits for the
// operator before it runs.
func (x *executor) jumpTo(k, j int) {
	for i := k; i < j; i++ {
		res := &x.run.Steps[i]
		switch res.Status {
		case StatusSucceeded, StatusForced, StatusSkipped, StatusBackground:
			continue
		}
		res.Status, res.Note = StatusSkipped, fmt.Sprintf("jumped over: went to step %d", j+1)
		x.stepDone(i)
		x.notify(Event{Kind: EventStepFinished, Step: i, Result: x.result(i), State: x.run.State})
	}
	x.em.Lock()
	x.pauseAt = j
	x.em.Unlock()
	x.notify(Event{Kind: EventOutput, Step: j, Stream: "info", Text: fmt.Sprintf("⤼ jumped here from step %d: it waits for you before running", k+1)})
	x.save()
}

func (b *bgStep) done() bool {
	select {
	case <-b.session.Done():
		return true
	default:
		return false
	}
}

// stepStop is the running step's stop.
type stepStop struct {
	idx    int
	cancel func(error)
	by     string
}

// errStepStopped is the cause of a step's context when it's stopped.
var errStepStopped = errors.New("stopped")

func (x *executor) RunningStep() int {
	x.em.Lock()
	defer x.em.Unlock()
	if x.stop == nil {
		return -1
	}
	return x.stop.idx
}

func (x *executor) StopStep(idx int, by string) error {
	x.em.Lock()
	defer x.em.Unlock()
	if x.stop == nil || (idx >= 0 && x.stop.idx != idx) {
		if idx >= 0 {
			return fmt.Errorf("step %d isn't running", idx+1)
		}
		return errors.New("no step is running")
	}
	x.stop.by = by
	x.stop.cancel(errStepStopped)
	return nil
}

// stoppedNote says who stopped a step, and when.
func stoppedNote(by string) string {
	s := "stopped"
	if by != "" {
		s += " by " + by
	}
	return s + " at " + time.Now().Format("15:04:05")
}
