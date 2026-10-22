package engine

import (
	"context"
	"strings"
	"testing"
	"time"
)

// jobUI does something at step at's prompt, before it runs.
type jobUI struct {
	stepper
	at   int
	do   func(Session) error
	err  error
	done bool
}

func (u *jobUI) Bind(s Session) { u.session = s }
func (u *jobUI) BeforeStep(ctx context.Context, v StepView) (StepDecision, error) {
	if v.Index == u.at && !u.done {
		u.done = true
		u.err = u.do(u.session)
	}
	return StepRun, nil
}

// The operator's background commands: recorded with the step, with the
// run's environment; one still going ends with the run.
func TestStartJob(t *testing.T) {
	rb := mustParse(t, "name: t\nshell: sh\ninputs: [{name: who, default: pat}]\nsteps:\n  - {name: a, run: 'true'}\n  - {name: b, run: 'sleep 0.5'}\n")
	ui := &jobUI{at: 0, do: func(s Session) error {
		if err := s.(JobStarter).StartJob(0, `echo "job for $who"`); err != nil {
			return err
		}
		return s.(JobStarter).StartJob(0, "sleep 30")
	}}
	start := time.Now()
	run := Execute(context.Background(), rb, ui, "", Options{Interactive: true, Controls: NewControls(ControlsView{StepAll: true})})
	if ui.err != nil || run.Status != StatusSucceeded || time.Since(start) > 8*time.Second {
		t.Fatalf("%v %s %s", ui.err, run.Status, time.Since(start))
	}
	jobs := run.Steps[0].Jobs
	if len(jobs) != 2 || !strings.Contains(jobs[0].Output, "job for pat") || jobs[0].Note != "exited" || jobs[1].Note != "stopped when the run ended" {
		t.Fatalf("%+v", jobs)
	}
}

// A shell in a pane (runs detached): what it publishes reaches the run at
// the next step, and its transcript is kept with its step.
func TestShellInPane(t *testing.T) {
	rb := mustParse(t, "name: t\nshell: sh\nsteps:\n  - {name: a, run: 'sleep 0.5'}\n  - {name: b, run: 'echo \"got $from_pane\"'}\n")
	ui := &jobUI{at: 0, do: func(s Session) error { return s.(PaneSheller).ShellInPane(0) }}
	run := Execute(context.Background(), rb, ui, "", Options{Interactive: true, Detached: &fakeDetacher{}, Controls: NewControls(ControlsView{StepAll: true})})
	if ui.err != nil || run.Status != StatusSucceeded || !strings.Contains(strings.Join(ui.output, "|"), "got hi") {
		t.Fatalf("%v %s %q", ui.err, run.Status, ui.output)
	}
	if len(run.Steps[0].Shells) != 1 || !strings.Contains(run.Steps[0].Shells[0], "in the pane") {
		t.Fatalf("shells %q", run.Steps[0].Shells)
	}
	if (&executor{}).CanPane() {
		t.Fatal("CanPane without a detacher")
	}
}
