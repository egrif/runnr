package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/egrif/runnr/internal/runbook"
)

func mustParse(t *testing.T, y string) *runbook.Runbook {
	t.Helper()
	rb, err := runbook.Parse([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	return rb
}

// scripted answers decisions from queues and records output.
type scripted struct {
	inputs   map[string]string
	confirms []bool
	manuals  []ManualOutcome
	failures []FailureAction
	output   []string
}

func (s *scripted) Notify(e Event) {
	if e.Kind == EventOutput {
		s.output = append(s.output, e.Text)
	}
}
func (s *scripted) AskInput(_ context.Context, in runbook.Input) (string, error) {
	return s.inputs[in.Name], nil
}
func (s *scripted) Confirm(context.Context, int, runbook.Step, string) (bool, error) {
	c := s.confirms[0]
	s.confirms = s.confirms[1:]
	return c, nil
}
func (s *scripted) Manual(context.Context, int, runbook.Step, string) (ManualOutcome, string, error) {
	m := s.manuals[0]
	s.manuals = s.manuals[1:]
	return m, "", nil
}
func (s *scripted) OnFailure(context.Context, int, runbook.Step, StepResult) (FailureAction, error) {
	f := s.failures[0]
	s.failures = s.failures[1:]
	return f, nil
}

func statuses(r *Run) string {
	var out []string
	for _, s := range r.Steps {
		out = append(out, string(s.Status))
	}
	return strings.Join(out, ",")
}

func TestCaptureTemplatingAndWhen(t *testing.T) {
	rb := mustParse(t, `
name: t
inputs:
  - {name: who, default: bob}
steps:
  - {name: a, run: "echo hi-{{.who}}", capture: greeting}
  - {name: b, run: "echo got {{.greeting}}"}
  - {name: c, run: "echo never", when: '{{eq .who "alice"}}'}
`)
	ui := &scripted{}
	run := Execute(context.Background(), rb, ui, "", Options{})
	if run.Status != StatusSucceeded {
		t.Fatalf("status %s: %s", run.Status, run.Error)
	}
	if got := statuses(run); got != "succeeded,succeeded,skipped" {
		t.Fatalf("statuses %s", got)
	}
	if strings.Join(ui.output, "|") != "hi-bob|got hi-bob" {
		t.Fatalf("output %q", ui.output)
	}
}

func TestUnattendedPolicies(t *testing.T) {
	rb := mustParse(t, `
name: t
steps:
  - {name: m1, manual: look, unattended: pass}
  - {name: m2, manual: look, unattended: skip}
  - {name: gated, run: "true", confirm: true}
`)
	run := Execute(context.Background(), rb, Unattended{}, "", Options{})
	if run.Status != StatusFailed || statuses(run) != "succeeded,skipped,failed" {
		t.Fatalf("without --yes: %s %s", run.Status, statuses(run))
	}
	run = Execute(context.Background(), rb, Unattended{AssumeYes: true}, "", Options{})
	if run.Status != StatusSucceeded {
		t.Fatalf("with --yes: %s %s", run.Status, run.Error)
	}
	rb = mustParse(t, "name: t\nsteps:\n  - {name: m, manual: look}\n")
	if run := Execute(context.Background(), rb, Unattended{}, "", Options{}); run.Status != StatusFailed {
		t.Fatalf("manual default policy should fail, got %s", run.Status)
	}
}

func TestFailureHandling(t *testing.T) {
	rb := mustParse(t, `
name: t
steps:
  - {name: soft, run: "exit 3", continue_on_error: true}
  - {name: hard, run: "exit 1"}
  - {name: after, run: "true"}
`)
	ui := &scripted{failures: []FailureAction{FailRetry, FailSkip}}
	run := Execute(context.Background(), rb, ui, "", Options{Interactive: true})
	if run.Status != StatusSucceeded || statuses(run) != "failed,skipped,succeeded" {
		t.Fatalf("%s %s", run.Status, statuses(run))
	}
	if run.Steps[0].ExitCode == nil || *run.Steps[0].ExitCode != 3 {
		t.Fatalf("exit code %v", run.Steps[0].ExitCode)
	}
	if run.Steps[1].Attempts != 2 {
		t.Fatalf("attempts %d", run.Steps[1].Attempts)
	}

	ui = &scripted{failures: []FailureAction{FailAbort}}
	run = Execute(context.Background(), rb, ui, "", Options{Interactive: true})
	if run.Status != StatusFailed || statuses(run) != "failed,failed,skipped" {
		t.Fatalf("abort: %s %s", run.Status, statuses(run))
	}
}

func TestRetries(t *testing.T) {
	dir := t.TempDir()
	rb := mustParse(t, `
name: t
steps:
  - name: flaky
    retries: 2
    run: 'n=$(cat count 2>/dev/null || echo 0); n=$((n+1)); echo $n > count; [ $n -ge 3 ]'
    dir: `+dir+`
`)
	run := Execute(context.Background(), rb, Unattended{}, "", Options{})
	if run.Status != StatusSucceeded || run.Steps[0].Attempts != 3 {
		t.Fatalf("%s attempts=%d %s", run.Status, run.Steps[0].Attempts, run.Error)
	}
}

func TestInputsAndSecrets(t *testing.T) {
	rb := mustParse(t, `
name: t
inputs:
  - {name: env, choices: [a, b], required: true}
  - {name: token, secret: true}
steps:
  - {name: s, run: "echo token={{.token}} env={{.env}}"}
`)
	ui := &scripted{inputs: map[string]string{"env": "b"}}
	run := Execute(context.Background(), rb, ui, "", Options{Interactive: true, Vars: map[string]string{"token": "hunter2"}})
	if run.Status != StatusSucceeded {
		t.Fatal(run.Error)
	}
	if ui.output[0] != "token=**** env=b" || strings.Contains(run.Steps[0].Command, "hunter2") || run.Inputs["token"] != "****" {
		t.Fatalf("secret leaked: %q %q %v", ui.output, run.Steps[0].Command, run.Inputs)
	}

	run = Execute(context.Background(), rb, Unattended{}, "", Options{})
	if run.Status != StatusFailed || !strings.Contains(run.Error, "missing required input") {
		t.Fatalf("missing input: %s %s", run.Status, run.Error)
	}
	run = Execute(context.Background(), rb, Unattended{}, "", Options{Vars: map[string]string{"env": "c"}})
	if !strings.Contains(run.Error, "not one of") {
		t.Fatalf("bad choice: %s", run.Error)
	}
	run = Execute(context.Background(), rb, Unattended{}, "", Options{Vars: map[string]string{"env": "a", "nope": "x"}})
	if !strings.Contains(run.Error, "unknown input") {
		t.Fatalf("unknown input: %s", run.Error)
	}
}

func TestTimeoutAndCancel(t *testing.T) {
	rb := mustParse(t, "name: t\nsteps:\n  - {name: slow, run: 'sleep 5', timeout: 100ms}\n")
	start := time.Now()
	run := Execute(context.Background(), rb, Unattended{}, "", Options{})
	if run.Status != StatusFailed || !strings.Contains(run.Steps[0].Error, "timed out") || time.Since(start) > 3*time.Second {
		t.Fatalf("timeout: %s %q %s", run.Status, run.Steps[0].Error, time.Since(start))
	}

	rb = mustParse(t, "name: t\nsteps:\n  - {name: slow, run: 'sleep 5'}\n  - {name: next, run: 'true'}\n")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	run = Execute(ctx, rb, Unattended{}, "", Options{})
	if run.Status != StatusAborted || statuses(run) != "aborted,skipped" {
		t.Fatalf("cancel: %s %s", run.Status, statuses(run))
	}
}

func TestDryRun(t *testing.T) {
	rb := mustParse(t, "name: t\nsteps:\n  - {name: a, run: 'exit 1', confirm: true}\n  - {name: b, manual: hi}\n")
	run := Execute(context.Background(), rb, Unattended{}, "", Options{DryRun: true})
	if run.Status != StatusSucceeded || run.Steps[0].Command != "exit 1" {
		t.Fatalf("%s %s", run.Status, run.Error)
	}
}

func TestManualInteractive(t *testing.T) {
	rb := mustParse(t, "name: t\nsteps:\n  - {name: a, manual: hi}\n  - {name: b, manual: hi}\n")
	ui := &scripted{manuals: []ManualOutcome{ManualDone, ManualFail}}
	run := Execute(context.Background(), rb, ui, "", Options{Interactive: true})
	if run.Status != StatusFailed || statuses(run) != "succeeded,failed" {
		t.Fatalf("%s %s", run.Status, statuses(run))
	}
}
