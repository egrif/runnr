package engine

import (
	"context"
	"os"
	"regexp"
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
// on after the run ends, and what it leaves then isn't applied. Its
// transcript so far is recorded with the step, each line timed.
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
	if run.State["late"] != "" || len(run.Steps[0].Shells) != 1 || !regexp.MustCompile(`(?m)^\[\d\d:\d\d:\d\d\] .*echo late=1`).MatchString(run.Steps[0].Shells[0]) {
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

// A shell opened early sees what later steps publish, secrets too: it
// reads the run's values again before a prompt when they've changed.
func TestShellSeesLaterValues(t *testing.T) {
	for _, sh := range []string{"/bin/zsh", "/bin/bash"} {
		if _, err := os.Stat(sh); err != nil {
			continue
		}
		t.Run(sh, func(t *testing.T) {
			home := t.TempDir()
			os.WriteFile(home+"/.zshenv", []byte("skip_global_compinit=1\n"), 0o644)
			t.Setenv("HOME", home)
			t.Setenv("ZDOTDIR", "")
			rb := mustParse(t, "name: t\nshell: "+sh+"\nsteps:\n  - {name: a, run: 'sleep 0.3'}\n  - {name: b, run: 'late=yes; pw=s3cr3t', outputs: [late], secret_outputs: [pw]}\n  - {name: c, run: 'sleep 2'}\n")
			ui := &asideUI{stepper: stepper{keys: "sleep 1.2\necho \"X=$late P=$pw\"; exit\n"}, err: make(chan error, 1)}
			run := Execute(context.Background(), rb, ui, "", Options{Interactive: true})
			if err := <-ui.err; err != nil {
				t.Fatal(err)
			}
			if run.Status != StatusSucceeded {
				t.Fatalf("%s %+v", run.Status, run.Steps[0])
			}
			if tr := strings.Join(run.Steps[0].Shells, "\n"); !strings.Contains(tr, "X=yes") {
				t.Fatalf("transcript %q", tr)
			}
		})
	}
}

// A shell left open across steps is recorded with its step as the run goes
// on, in one place, kept up to date.
func TestShellAsideRecordedWhileOpen(t *testing.T) {
	rb := mustParse(t, "name: t\nshell: sh\nsteps:\n  - {name: a, run: 'sleep 0.6'}\n  - {name: b, run: 'sleep 0.6'}\n  - {name: c, run: 'true'}\n")
	ui := &asideUI{stepper: stepper{keys: "echo fi\"\"rst\nsleep 0.3; echo sec\"\"ond\nsleep 2; exit\n"}, err: make(chan error, 1)}
	run := Execute(context.Background(), rb, ui, "", Options{Interactive: true})
	if sh := run.Steps[0].Shells; run.Status != StatusSucceeded || len(sh) != 1 || !regexp.MustCompile(`(?m)^\[\d\d:\d\d:\d\d\] .*first$`).MatchString(sh[0]) || !strings.Contains(sh[0], "second") {
		t.Fatalf("%s %q", run.Status, sh)
	}
	select {
	case <-ui.err:
	case <-time.After(5 * time.Second):
		t.Fatal("the shell didn't end")
	}
}

func TestLineRecorder(t *testing.T) {
	r := &lineRecorder{}
	r.Write([]byte("$ "))
	if r.text() != "" {
		t.Fatalf("a prompt alone: %q", r.text())
	}
	r.Write([]byte("ech\bho \x1b[1mhi\x1b[0m\r\nhi\n\n$ "))
	if !regexp.MustCompile(`^\[\d\d:\d\d:\d\d\] \$ echo hi\n\[\d\d:\d\d:\d\d\] hi\n\n\[\d\d:\d\d:\d\d\] \$ $`).MatchString(r.text()) {
		t.Fatalf("%q", r.text())
	}
}
