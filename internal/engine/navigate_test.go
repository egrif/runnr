package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egrif/runnr/internal/runbook"
)

// goUI steps through every step; before step at (the first time), it goes
// to step to.
type goUI struct {
	stepper
	at, to int
	force  bool
	done   bool
	err    error
}

func (u *goUI) Bind(s Session) { u.session = s }
func (u *goUI) ConfirmTyped(context.Context, int, runbook.Step, string, string) (bool, error) {
	return true, nil
}
func (u *goUI) BeforeStep(ctx context.Context, v StepView) (StepDecision, error) {
	if v.Index == u.at && !u.done {
		u.done = true
		if u.err = u.session.(Navigator).GoTo(u.to, u.force); u.err == nil {
			return StepRun, ErrReopen
		}
	}
	return StepRun, nil
}

const threeSteps = `
name: t
shell: sh
steps:
  - {name: a, run: 'echo A'}
  - {name: b, run: 'echo B', confirm_typing: yes}
  - {name: c, run: 'echo C'}
`

func runGo(t *testing.T, at, to int, force bool) (*goUI, *Run) {
	ui := &goUI{at: at, to: to, force: force}
	run := Execute(context.Background(), mustParse(t, threeSteps), ui, "", Options{Interactive: true, Controls: NewControls(ControlsView{StepAll: true})})
	return ui, run
}

func outputs(ui *goUI) string {
	var out []string
	for _, o := range ui.output {
		if o == "A" || o == "B" || o == "C" {
			out = append(out, o)
		}
	}
	return strings.Join(out, "")
}

func TestGoToBack(t *testing.T) {
	ui, run := runGo(t, 2, 1, true)
	if ui.err != nil || run.Status != StatusSucceeded || outputs(ui) != "ABBC" {
		t.Fatalf("%v %s %q", ui.err, run.Status, outputs(ui))
	}
}

func TestGoToBackPastNoReturn(t *testing.T) {
	ui, run := runGo(t, 2, 0, false)
	if !errors.Is(ui.err, ErrPastNoReturn) || outputs(ui) != "ABC" || run.Status != StatusSucceeded {
		t.Fatalf("%v %q", ui.err, outputs(ui))
	}
}

func TestGoToAhead(t *testing.T) {
	ui, run := runGo(t, 0, 2, false)
	if ui.err != nil || run.Status != StatusSucceeded || outputs(ui) != "C" {
		t.Fatalf("%v %s %q", ui.err, run.Status, outputs(ui))
	}
	for _, i := range []int{0, 1} {
		if s := run.Steps[i]; s.Status != StatusSkipped || !strings.Contains(s.Note, "jumped over") {
			t.Fatalf("step %d: %s %q", i+1, s.Status, s.Note)
		}
	}
}

// stopUI stops step 0 soon after it starts, then skips it.
type stopUI struct {
	scripted
	session Session
}

func (u *stopUI) Bind(s Session) { u.session = s }
func (u *stopUI) Notify(e Event) {
	u.scripted.Notify(e)
	if e.Kind == EventStepStarted && e.Step == 0 {
		go func() {
			time.Sleep(300 * time.Millisecond)
			u.session.(StepStopper).StopStep(0, "pat")
		}()
	}
}

// Stopping a step fails it (not the run), and the run asks what to do.
func TestStopStep(t *testing.T) {
	rb := mustParse(t, "name: t\nshell: sh\nsteps:\n  - {name: slow, run: 'sleep 10'}\n  - {name: next, run: 'echo NEXT'}\n")
	ui := &stopUI{scripted: scripted{failures: []FailureAction{FailSkip}}}
	start := time.Now()
	run := Execute(context.Background(), rb, ui, "", Options{Interactive: true})
	if time.Since(start) > 5*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
	if run.Status != StatusSucceeded || run.Steps[0].Status != StatusSkipped || !strings.Contains(run.Steps[0].Error, "stopped by pat") || !strings.Contains(strings.Join(ui.output, "|"), "NEXT") {
		t.Fatalf("%s %+v %q", run.Status, run.Steps[0], ui.output)
	}
}

// failGoUI, when step 1 fails, goes back to step 0 (which fixes it).
type failGoUI struct {
	stepper
	err error
}

func (u *failGoUI) Bind(s Session)                                             { u.session = s }
func (u *failGoUI) BeforeStep(context.Context, StepView) (StepDecision, error) { return StepRun, nil }
func (u *failGoUI) OnFailure(_ context.Context, i int, _ runbook.Step, _ StepResult) (FailureAction, string, error) {
	if u.err = u.session.(Navigator).GoTo(0, false); u.err != nil {
		return FailAbort, "", nil
	}
	return "", "", ErrReopen
}

func TestGoToFromFailure(t *testing.T) {
	dir := t.TempDir()
	rb := mustParse(t, "name: t\nshell: sh\nsteps:\n  - {name: fix, run: 'n=$(cat "+dir+"/n 2>/dev/null || echo 0); echo $((n+1)) > "+dir+"/n'}\n  - {name: check, run: 'test $(cat "+dir+"/n) -ge 2'}\n")
	ui := &failGoUI{}
	run := Execute(context.Background(), rb, ui, "", Options{Interactive: true, Controls: NewControls(ControlsView{StepAll: true})})
	if ui.err != nil || run.Status != StatusSucceeded || run.Steps[1].Status != StatusSucceeded {
		t.Fatalf("%v %s %+v", ui.err, run.Status, run.Steps[1])
	}
}
