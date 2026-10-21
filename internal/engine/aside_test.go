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
		conn, _ := a.StartTerm(0, "shell")
		go func() { a.err <- a.session.(AsideSheller).ShellAside(context.Background(), 0, conn) }()
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

// A shell left open alongside the run doesn't hold the run up: it carries
// on after the run ends, and what it leaves then isn't applied.
func TestShellAsideOutlivesTheRun(t *testing.T) {
	rb := mustParse(t, "name: t\nshell: sh\nsteps:\n  - {name: slow, run: 'sleep 0.5'}\n")
	ui := &asideUI{stepper: stepper{keys: "sleep 1; echo late=1 >> \"$RUNNR_OUTPUT\"; exit\n"}, err: make(chan error, 1)}
	start := time.Now()
	run := Execute(context.Background(), rb, ui, "", Options{Interactive: true})
	if run.Status != StatusSucceeded || time.Since(start) > 900*time.Millisecond {
		t.Fatalf("%s after %s", run.Status, time.Since(start))
	}
	select {
	case err := <-ui.err:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the shell didn't end")
	}
	if run.State["late"] != "" || len(run.Steps[0].Shells) != 0 {
		t.Fatalf("applied after the run: %v %v", run.State, run.Steps[0].Shells)
	}
}

// A shell outside a run has the runbook's environment: inputs (else their
// defaults), an earlier run's values, env and functions.
func TestOpenShell(t *testing.T) {
	rb := mustParse(t, `
name: t
shell: sh
inputs:
  - {name: who, default: world}
  - {name: where}
env: {GREETING: "hi {{.who}}"}
functions:
  shout: {body: 'echo "$1!"'}
steps:
  - {name: one, run: 'true'}
`)
	conn := &scriptConn{keys: strings.NewReader("echo \"[$GREETING $where $done]\"; shout ok; exit\n"), closed: make(chan struct{})}
	transcript, err := OpenShell(context.Background(), ShellSpec{Runbook: rb, Step: 0, Vars: map[string]string{"where": "here"}, State: map[string]string{"done": "yes"}}, conn)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(transcript, "[hi world here yes]") || !strings.Contains(transcript, "ok!") {
		t.Fatalf("transcript %q", transcript)
	}
}
