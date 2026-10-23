package engine

import (
	"context"
	"strings"
	"testing"
	"time"
)

// pauser pauses before every step until the operator answers, which here
// never happens: only a suspend ends the wait.
type pauser struct{ Unattended }

func (pauser) BeforeStep(ctx context.Context, v StepView) (StepDecision, error) {
	<-ctx.Done()
	return StepRun, ctx.Err()
}
func (pauser) AfterStep(context.Context, StepView, StepResult) (AfterDecision, string, error) {
	return AfterContinue, "", nil
}

const suspendRB = "name: t\nshell: sh\nsteps:\n  - {name: one, run: 'sleep 0.6; echo ONE'}\n  - {name: two, run: 'echo TWO'}\n"

// Asked to suspend while a step runs: the step finishes, the run stops
// before the next, and a resume carries it on from there.
func TestSuspendAfterTheStep(t *testing.T) {
	rb := mustParse(t, suspendRB)
	ctl := NewControls(ControlsView{})
	time.AfterFunc(200*time.Millisecond, func() { ctl.Suspend(&SuspendError{By: "pat", Reason: "back tomorrow"}) })
	run := Execute(context.Background(), rb, Unattended{}, "", Options{Controls: ctl})
	if run.Status != StatusSuspended || run.Error != "suspended by pat: back tomorrow" || run.Steps[0].Status != StatusSucceeded || run.Steps[1].Status != StatusPending || ResumePoint(run) != 1 {
		t.Fatalf("%s %q %s %s", run.Status, run.Error, run.Steps[0].Status, run.Steps[1].Status)
	}
	again := Execute(context.Background(), rb, Unattended{}, run.ID, Options{Resumed: run, Continue: true, StartAt: 1, CarryDone: true})
	if again.Status != StatusSucceeded || !again.Steps[0].Carried || !strings.Contains(again.Steps[1].Output, "TWO") ||
		len(again.Resumptions) != 1 || again.Resumptions[0].Status != StatusSuspended {
		t.Fatalf("resume: %s %+v", again.Status, again.Resumptions)
	}
	if log := RawLog(again); !strings.Contains(log, "(it was suspended by pat: back tomorrow)") {
		t.Fatalf("raw log:\n%s", log)
	}
}

// Now: the running step is stopped, and it's the one to do again.
func TestSuspendNow(t *testing.T) {
	rb := mustParse(t, suspendRB)
	ctl := NewControls(ControlsView{})
	time.AfterFunc(200*time.Millisecond, func() { ctl.Suspend(&SuspendError{By: "pat", Now: true}) })
	start := time.Now()
	run := Execute(context.Background(), rb, Unattended{}, "", Options{Controls: ctl})
	if run.Status != StatusSuspended || run.Steps[0].Status != StatusSuspended || ResumePoint(run) != 0 || time.Since(start) > 550*time.Millisecond {
		t.Fatalf("%s %s after %s", run.Status, run.Steps[0].Status, time.Since(start))
	}
}

// Waiting on the operator (a pause before the step): it suspends at once,
// and the step it was about to run is still to do.
func TestSuspendWhileWaiting(t *testing.T) {
	rb := mustParse(t, suspendRB)
	ctl := NewControls(ControlsView{StepAll: true})
	time.AfterFunc(200*time.Millisecond, func() { ctl.Suspend(&SuspendError{By: "pat"}) })
	start := time.Now()
	run := Execute(context.Background(), rb, pauser{}, "", Options{Controls: ctl, Interactive: true})
	if run.Status != StatusSuspended || run.Steps[0].Status != StatusPending || !run.Steps[0].Started.IsZero() || time.Since(start) > 2*time.Second {
		t.Fatalf("%s %+v after %s", run.Status, run.Steps[0], time.Since(start))
	}
}

// A request can be withdrawn before it's taken.
func TestCancelSuspend(t *testing.T) {
	rb := mustParse(t, suspendRB)
	ctl := NewControls(ControlsView{})
	time.AfterFunc(100*time.Millisecond, func() { ctl.Suspend(&SuspendError{}) })
	time.AfterFunc(300*time.Millisecond, ctl.CancelSuspend)
	if run := Execute(context.Background(), rb, Unattended{}, "", Options{Controls: ctl}); run.Status != StatusSucceeded {
		t.Fatalf("%s", run.Status)
	}
}
