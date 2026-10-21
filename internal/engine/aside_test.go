package engine

import (
	"context"
	"strings"
	"testing"
	"time"
)

// asideUI opens a shell alongside the run while step 1 runs.
type asideUI struct {
	stepper
	err chan error
}

func (a *asideUI) Bind(s Session) { a.session = s }
func (a *asideUI) Notify(e Event) {
	a.stepper.Notify(e)
	if e.Kind == EventStepStarted && e.Step == 0 {
		go func() { a.err <- a.session.(AsideSheller).ShellAside(0) }()
	}
}

// A shell opened alongside a running step: what it publishes reaches the
// run when the next step starts, and its transcript is kept with its step.
func TestShellAside(t *testing.T) {
	rb := mustParse(t, `
name: t
shell: sh
steps:
  - {name: slow, run: 'sleep 1'}
  - {name: uses it, run: 'echo "got $from_shell"'}
`)
	ui := &asideUI{stepper: stepper{keys: "echo from_shell=hello >> \"$RUNNR_OUTPUT\"; exit\n"}, err: make(chan error, 1)}
	run := Execute(context.Background(), rb, ui, "", Options{Interactive: true})
	select {
	case err := <-ui.err:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the shell didn't end")
	}
	if run.Status != StatusSucceeded || !strings.Contains(strings.Join(ui.output, "|"), "got hello") || len(run.Steps[0].Shells) != 1 {
		t.Fatalf("%s %q shells=%d", run.Status, ui.output, len(run.Steps[0].Shells))
	}
}

// A shell left open alongside the run is hung up when the run ends.
func TestShellAsideEndsWithTheRun(t *testing.T) {
	rb := mustParse(t, "name: t\nshell: sh\nsteps:\n  - {name: slow, run: 'sleep 0.5'}\n")
	ui := &asideUI{stepper: stepper{keys: "echo still here\n"}, err: make(chan error, 1)}
	start := time.Now()
	run := Execute(context.Background(), rb, ui, "", Options{Interactive: true})
	if run.Status != StatusSucceeded || time.Since(start) > 5*time.Second {
		t.Fatalf("%s after %s", run.Status, time.Since(start))
	}
}
