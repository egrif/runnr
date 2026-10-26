package engine

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	reason   string
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
func (s *scripted) OnFailure(context.Context, int, runbook.Step, StepResult) (FailureAction, string, error) {
	f := s.failures[0]
	s.failures = s.failures[1:]
	return f, s.reason, nil
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
	if run.Status != StatusFailed || statuses(run) != "failed,failed,pending" {
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
	if run.Status != StatusAborted || statuses(run) != "aborted,pending" {
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
	// A manual step marked failed gets the usual failure choices; abort here.
	ui := &scripted{manuals: []ManualOutcome{ManualDone, ManualFail}, failures: []FailureAction{FailAbort}}
	run := Execute(context.Background(), rb, ui, "", Options{Interactive: true})
	if run.Status != StatusFailed || statuses(run) != "succeeded,failed" {
		t.Fatalf("%s %s", run.Status, statuses(run))
	}
}

func TestOutputsAndSecretOutputs(t *testing.T) {
	rb := mustParse(t, `
name: t
steps:
  - name: setup
    run: |
      echo "region=use1" >> "$RUNNR_OUTPUT"
      echo "pass=sup3rsecret" >> "$RUNNR_SECRET"
  - {name: env, run: 'echo "$region {{.region}} $pass"'}
`)
	ui := &scripted{}
	run := Execute(context.Background(), rb, ui, "", Options{})
	if run.Status != StatusSucceeded {
		t.Fatal(run.Error)
	}
	if got := strings.Join(ui.output, "|"); got != "use1 use1 ****" {
		t.Fatalf("output %q", got)
	}
}

func TestStartAtAndAlways(t *testing.T) {
	rb := mustParse(t, `
name: t
steps:
  - {name: setup, run: 'echo "x=1" >> "$RUNNR_OUTPUT"', always: true}
  - {name: skipme, run: 'exit 1'}
  - {name: last, run: 'echo $x'}
`)
	ui := &scripted{}
	run := Execute(context.Background(), rb, ui, "", Options{StartAt: 2})
	if run.Status != StatusSucceeded || statuses(run) != "succeeded,skipped,succeeded" || ui.output[0] != "1" {
		t.Fatalf("%s %s %q", run.Status, statuses(run), ui.output)
	}
}

func TestResumePoint(t *testing.T) {
	rb := mustParse(t, "name: t\nsteps:\n  - {name: a, run: 'true'}\n  - {name: b, run: 'exit 1'}\n  - {name: c, run: 'true'}\n")
	run := Execute(context.Background(), rb, Unattended{}, "", Options{Vars: map[string]string{}})
	if got := ResumePoint(run); got != 1 {
		t.Fatalf("resume point %d (%s)", got, statuses(run))
	}
}

// An optional step that didn't finish (a watcher that exited) doesn't decide
// where a resume starts; at or after that point, it simply runs again.
func TestResumePointIgnoresOptionalSteps(t *testing.T) {
	rb := mustParse(t, "name: t\nsteps:\n  - {name: a, run: 'true'}\n  - {name: watcher, run: 'exit 1', optional: true}\n  - {name: c, run: 'true'}\n  - {name: d, run: 'exit 1'}\n")
	prev := Execute(context.Background(), rb, Unattended{}, "", Options{})
	if got := ResumePoint(prev); got != 3 {
		t.Fatalf("resume point %d (%s)", got, statuses(prev))
	}
	prev.Steps[3].Status = StatusSucceeded
	if got := ResumePoint(prev); got != -1 {
		t.Fatalf("only an optional step left: resume point %d (%s)", got, statuses(prev))
	}

	// Not done at or after the resume point: it runs again.
	rb = mustParse(t, "name: t\nsteps:\n  - {name: a, run: 'true'}\n  - {name: b, run: 'true'}\n  - {name: watcher, run: 'echo watching', optional: true}\n  - {name: d, run: 'echo d-ran'}\n")
	prev = &Run{ID: "prev", Status: StatusAborted}
	for i, st := range []Status{StatusSucceeded, StatusAborted, StatusFailed, StatusSucceeded} {
		prev.Steps = append(prev.Steps, StepResult{Index: i, Name: rb.Steps[i].Name, Status: st, Optional: rb.Steps[i].Optional})
	}
	start := ResumePoint(prev)
	if start != 1 {
		t.Fatalf("resume point %d (%s)", start, statuses(prev))
	}
	ui := &scripted{}
	run := Execute(context.Background(), rb, ui, "", Options{StartAt: start, Resumed: prev, CarryDone: true})
	if run.Status != StatusSucceeded || statuses(run) != "skipped,succeeded,succeeded,skipped" || !slices.Contains(ui.output, "watching") || slices.Contains(ui.output, "d-ran") {
		t.Fatalf("%s %s %q", run.Status, statuses(run), ui.output)
	}
}

// The tmux shell pane opens once a run has begun, and then loads its inputs.
// A resume saves the steps it carries over before its inputs are settled, so
// "a step isn't pending" can't mean "begun": HasBegun must wait for the inputs.
func TestResumeHasBegunOnlyOnceInputsAreRecorded(t *testing.T) {
	rb := mustParse(t, "name: t\ninputs:\n  - {name: env, required: true}\nsteps:\n  - {name: a, run: 'true'}\n  - {name: b, run: 'exit 1'}\n  - {name: c, run: 'true'}\n")
	prev := Execute(context.Background(), rb, Unattended{}, "", Options{Vars: map[string]string{"env": "staging"}})
	if got := ResumePoint(prev); got != 1 {
		t.Fatalf("resume point %d (%s)", got, statuses(prev))
	}

	type saved struct {
		begun, carried bool
		env            string
	}
	var saves []saved
	opts := Options{Vars: map[string]string{"env": "staging"}, Resumed: prev, Resumes: prev.ID, StartAt: ResumePoint(prev),
		Save: func(r *Run) {
			s := saved{begun: r.HasBegun(), env: r.Inputs["env"]}
			for _, st := range r.Steps {
				s.carried = s.carried || st.Status != StatusPending
			}
			saves = append(saves, s)
		}}
	run := Execute(context.Background(), rb, Unattended{}, "", opts)
	if run.Status != StatusFailed || run.Begun.IsZero() {
		t.Fatalf("status %s, begun %v", run.Status, run.Begun)
	}
	if len(saves) == 0 || !saves[0].carried || saves[0].env != "" || saves[0].begun {
		t.Fatalf("first save should carry step a, before the inputs, and not count as begun: %+v", saves)
	}
	for i, s := range saves {
		if s.begun && s.env != "staging" {
			t.Fatalf("save %d counts as begun without its inputs: %+v", i, saves)
		}
	}
	if last := saves[len(saves)-1]; !last.begun || last.env != "staging" {
		t.Fatalf("last save: %+v", last)
	}
}

func TestRetryDelay(t *testing.T) {
	rb := mustParse(t, "name: t\nsteps:\n  - {name: a, run: 'exit 1', retries: 2, retry_delay: 150ms}\n")
	start := time.Now()
	run := Execute(context.Background(), rb, Unattended{}, "", Options{})
	if run.Steps[0].Attempts != 3 || time.Since(start) < 300*time.Millisecond {
		t.Fatalf("attempts %d in %s", run.Steps[0].Attempts, time.Since(start))
	}
}

func TestTTYWithoutTerminalBecomesManual(t *testing.T) {
	rb := mustParse(t, "name: t\nsteps:\n  - {name: a, run: 'tf apply', tty: true, unattended: skip}\n")
	run := Execute(context.Background(), rb, Unattended{}, "", Options{})
	if run.Status != StatusSucceeded || run.Steps[0].Status != StatusSkipped || run.Steps[0].Kind != "tty" {
		t.Fatalf("%s %s", run.Status, statuses(run))
	}
}

func TestForceFailedStep(t *testing.T) {
	rb := mustParse(t, `
name: t
steps:
  - {name: flaky, run: 'echo "v=1" >> "$RUNNR_OUTPUT"; exit 2'}
  - {name: uses, run: 'echo got $v'}
`)
	ui := &scripted{failures: []FailureAction{FailForce}, reason: "known flake, verified by hand"}
	run := Execute(context.Background(), rb, ui, "", Options{Interactive: true})
	if run.Status != StatusSucceeded || statuses(run) != "forced,succeeded" {
		t.Fatalf("%s %s", run.Status, statuses(run))
	}
	if run.Steps[0].Note != "forced: known flake, verified by hand" || ui.output[0] != "got 1" {
		t.Fatalf("note %q output %q", run.Steps[0].Note, ui.output)
	}
	if ResumePoint(run) != -1 {
		t.Fatal("forced steps count as done for resume")
	}

	ui = &scripted{failures: []FailureAction{FailForce}}
	if run := Execute(context.Background(), rb, ui, "", Options{Interactive: true}); run.Status != StatusFailed {
		t.Fatalf("force without reason must fail, got %s", run.Status)
	}
}

func TestDeclaredOutputsAndState(t *testing.T) {
	rb := mustParse(t, `
name: t
inputs: [{name: pw, secret: true, default: inputsecret}]
steps:
  - name: setup
    shell: bash
    run: |
      region=use1
      token=tok-123456
      unset_one=
      unset unset_one
    outputs: [region, unset_one]
    secret_outputs: [token]
  - {name: use, run: 'echo "$region $token"'}
`)
	ui := &scripted{}
	run := Execute(context.Background(), rb, ui, "", Options{})
	if run.Status != StatusSucceeded || ui.output[0] != "use1 ****" {
		t.Fatalf("%s %s %q", run.Status, run.Error, ui.output)
	}
	want := map[string]string{"pw": "****", "region": "use1", "token": "****"}
	for k, v := range want {
		if run.State[k] != v {
			t.Fatalf("state %v", run.State)
		}
	}
	if _, ok := run.State["unset_one"]; ok {
		t.Fatalf("unset variables must not be published: %v", run.State)
	}

	// Resuming restores non-secret state; secrets need re-deriving.
	rb2 := mustParse(t, "name: t\nsteps:\n  - {name: a, run: 'true'}\n  - {name: b, run: 'echo \"[$region][$token]\"'}\n")
	ui = &scripted{}
	Execute(context.Background(), rb2, ui, "", Options{StartAt: 1, State: run.State})
	if ui.output[len(ui.output)-1] != "[use1][]" {
		t.Fatalf("restore: %q", ui.output)
	}
}

func TestDeselectedSteps(t *testing.T) {
	rb := mustParse(t, "name: t\nsteps:\n  - {name: a, run: 'true'}\n  - {name: b, run: 'exit 1'}\n  - {name: c, run: 'true'}\n")
	run := Execute(context.Background(), rb, Unattended{}, "", Options{Controls: NewControls(ControlsView{Skip: []int{1}})})
	if run.Status != StatusSucceeded || statuses(run) != "succeeded,skipped,succeeded" || run.Steps[1].Note != NoteDeselected {
		t.Fatalf("%s %s", run.Status, statuses(run))
	}
}

// stepper drives step mode and review; it also has a terminal whose
// "keyboard" is a script, so shells and by-hand steps can be tested.
type stepper struct {
	scripted
	decisions []StepDecision
	afters    []AfterDecision
	afterSeen []StepResult
	keys      string
	shellAt   string
	reviewed  []StepView
	session   Session
}

func (s *stepper) BeforeStep(ctx context.Context, v StepView) (StepDecision, error) {
	if v.Name == s.shellAt {
		// The operator opens a shell at this prompt before deciding.
		if err := s.session.Shell(ctx, v.Index); err != nil {
			return "", err
		}
	}
	d := s.decisions[0]
	s.decisions = s.decisions[1:]
	return d, nil
}
func (s *stepper) AfterStep(_ context.Context, v StepView, res StepResult) (AfterDecision, string, error) {
	s.afterSeen = append(s.afterSeen, res)
	d := s.afters[0]
	s.afters = s.afters[1:]
	return d, "output looked wrong", nil
}
func (s *stepper) Review(_ context.Context, plan []StepView, c *Controls) error {
	s.reviewed = plan
	c.Toggle("skip", 0)
	return nil
}
func (s *stepper) StartTerm(int, string) (TermConn, error) {
	return &scriptConn{keys: strings.NewReader(s.keys), closed: make(chan struct{})}, nil
}

// scriptConn is a terminal whose keyboard is a script. Once the script is
// typed it waits, like an operator who hasn't closed the window.
type scriptConn struct {
	keys   *strings.Reader
	out    strings.Builder
	closed chan struct{}
	once   sync.Once
}

func (c *scriptConn) Read(b []byte) (int, error) {
	if c.keys.Len() > 0 {
		return c.keys.Read(b)
	}
	<-c.closed
	return 0, io.EOF
}
func (c *scriptConn) Write(b []byte) (int, error) { return c.out.Write(b) }
func (c *scriptConn) Close() error                { c.once.Do(func() { close(c.closed) }); return nil }
func (c *scriptConn) Size() (uint16, uint16)      { return 24, 80 }
func (c *scriptConn) Resized() <-chan struct{}    { return nil }
func (s *stepper) Bind(se Session)                { s.session = se }

func TestStepModeShellAndReview(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	rb := mustParse(t, `
name: t
inputs: [{name: who, default: eric}]
steps:
  - {name: deselected, run: 'exit 1'}
  - {name: auto, run: 'echo auto', expect: 'prints auto for {{.who}}'}
  - {name: mine, run: 'echo "fixed=yes" >> "$RUNNR_OUTPUT"'}  # skipped: fixed in the shell instead
  - {name: skipme, run: 'exit 1'}
  - {name: rest, run: 'echo "fixed=$fixed"'}
  - {name: rest2, run: 'echo more'}
`)
	ui := &stepper{
		decisions: []StepDecision{StepRun, StepSkip, StepSkip, StepContinue},
		keys:      "echo fixed=by-hand >> \"$RUNNR_OUTPUT\"\nexit\n",
		shellAt:   "mine",
	}
	run := Execute(context.Background(), rb, ui, "", Options{Interactive: true, Controls: NewControls(ControlsView{StepAll: true}), Review: true})
	if run.Status != StatusSucceeded {
		t.Fatalf("%s %s", run.Status, run.Error)
	}
	if got := statuses(run); got != "skipped,succeeded,skipped,skipped,succeeded,succeeded" {
		t.Fatalf("statuses %s", got)
	}
	if len(run.Steps[2].Shells) != 1 || !strings.Contains(run.Steps[2].Shells[0], "fixed=by-hand") {
		t.Fatalf("shell transcript %q", run.Steps[2].Shells)
	}
	if !slices.Contains(ui.output, "fixed=by-hand") {
		t.Fatalf("by-hand shell output not published: %q", ui.output)
	}
	if ui.reviewed[1].Expect != "prints auto for eric" {
		t.Fatalf("review view %+v", ui.reviewed[1])
	}
	if ui.session.State()["fixed"] != "by-hand" {
		t.Fatalf("session state %v", ui.session.State())
	}
}

func TestPreview(t *testing.T) {
	rb := mustParse(t, "name: t\ninputs: [{name: env, default: staging}]\nsteps:\n  - {name: a, run: 'deploy {{.env}} {{.version}}', expect: ok}\n")
	v := Preview(rb, nil, nil)[0]
	if v.Command != "deploy staging <version>" || v.Kind != "run" {
		t.Fatalf("%+v", v)
	}
}

func TestPauseAfter(t *testing.T) {
	rb := mustParse(t, `
name: t
steps:
  - {name: counted, run: 'echo run >> "$RUNNR_OUTPUT.count"; echo hi'}
  - {name: rejected, run: 'echo looks fine'}
  - {name: last, run: 'true'}
`)
	// Step 1: look, run again, then continue. Step 2: reject → failure prompt → skip.
	ui := &stepper{afters: []AfterDecision{AfterRetry, AfterContinue, AfterFail}}
	ui.failures = []FailureAction{FailSkip}
	ctl := NewControls(ControlsView{PauseAfter: []int{0, 1}})
	run := Execute(context.Background(), rb, ui, "", Options{Interactive: true, Controls: ctl})
	if run.Status != StatusSucceeded || statuses(run) != "succeeded,skipped,succeeded" {
		t.Fatalf("%s %s", run.Status, statuses(run))
	}
	if run.Steps[0].Attempts != 2 || len(ui.afterSeen) != 3 || ui.afterSeen[0].Output != "hi\n" {
		t.Fatalf("attempts %d seen %+v", run.Steps[0].Attempts, ui.afterSeen)
	}

	// Unattended runs never pause.
	run = Execute(context.Background(), rb, ui, "", Options{Controls: ctl})
	if run.Status != StatusSucceeded {
		t.Fatal(run.Error)
	}
}

func TestControlsChangeMidRun(t *testing.T) {
	rb := mustParse(t, "name: t\nsteps:\n  - {name: a, run: 'true'}\n  - {name: b, run: 'exit 1'}\n")
	ctl := NewControls(ControlsView{})
	ui := &stepper{decisions: []StepDecision{StepRun}}
	ui.Notify(Event{})
	// Skip b once a has started, as a GUI user would mid-run.
	hook := &hookUI{stepper: ui, onStart: func(i int) {
		if i == 0 {
			ctl.Toggle("skip", 1)
		}
	}}
	run := Execute(context.Background(), rb, hook, "", Options{Interactive: true, Controls: ctl})
	if run.Status != StatusSucceeded || statuses(run) != "succeeded,skipped" {
		t.Fatalf("%s %s", run.Status, statuses(run))
	}
}

type hookUI struct {
	*stepper
	onStart func(int)
}

func (h *hookUI) Notify(e Event) {
	if e.Kind == EventStepStarted {
		h.onStart(e.Step)
	}
	h.stepper.Notify(e)
}

func TestTTYStepRecordsOutput(t *testing.T) {
	rb := mustParse(t, `
name: t
steps:
  - name: asks
    tty: true
    shell: bash
    outputs: [answer]
    run: |
      printf '\033[1mbold\033[0m progress 10%%\rprogress 100%%\n'
      read -p "Proceed? " answer
      echo "you said $answer"
      [ -t 0 ] && echo "stdin is a terminal"
  - {name: after, run: 'echo "answer=$answer"'}
`)
	ui := &stepper{keys: "yes\n"}
	run := Execute(context.Background(), rb, ui, "", Options{Interactive: true})
	if run.Status != StatusSucceeded {
		t.Fatalf("%s %s", run.Status, run.Error)
	}
	out := run.Steps[0].Output
	// Keys typed ahead are echoed when typed; the progress line keeps only its final redraw.
	for _, want := range []string{"progress 100%", "Proceed?", "you said yes", "stdin is a terminal"} {
		if !strings.Contains(out, want) {
			t.Fatalf("transcript missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b") || strings.Contains(out, "10%") {
		t.Fatalf("transcript not cleaned:\n%q", out)
	}
	if !slices.Contains(ui.output, "answer=yes") {
		t.Fatalf("output %q", ui.output)
	}
}

func TestCleanTranscript(t *testing.T) {
	got := CleanTranscript("\x1b]0;title\x07\x1b[32mok\x1b[0m\r\nloading 1\rloading 2\r\nab\bc\r\n")
	if got != "ok\nloading 2\nac\n" {
		t.Fatalf("%q", got)
	}
}

func TestOperatorEndsSession(t *testing.T) {
	// A shell nobody exits; ending the session must hang it up.
	t.Setenv("SHELL", "/bin/sh")
	rb := mustParse(t, "name: t\nsteps:\n  - {name: a, run: 'true'}\n")
	ui := &stepper{decisions: []StepDecision{StepRun}, shellAt: "a"}
	conn := &scriptConn{keys: strings.NewReader("echo started\n"), closed: make(chan struct{})}
	host := &connHost{stepper: ui, conn: conn}
	time.AfterFunc(500*time.Millisecond, func() { conn.Close() })
	done := make(chan *Run, 1)
	go func() {
		done <- Execute(context.Background(), rb, host, "", Options{Interactive: true, Controls: NewControls(ControlsView{StepAll: true})})
	}()
	select {
	case run := <-done:
		if run.Status != StatusSucceeded || len(run.Steps[0].Shells) != 1 || !strings.Contains(run.Steps[0].Shells[0], "started") {
			t.Fatalf("%s %q", run.Status, run.Steps[0].Shells)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ending the session didn't end the shell")
	}
}

type connHost struct {
	*stepper
	conn TermConn
}

func (h *connHost) StartTerm(int, string) (TermConn, error) { return h.conn, nil }
func (h *connHost) Bind(se Session)                         { h.stepper.session = se }

func TestProfile(t *testing.T) {
	dir := t.TempDir()
	prof := dir + "/profile.zsh"
	os.WriteFile(prof, []byte("echo noisy profile output\nalias greet='echo hello from alias'\nmyfn() { echo \"fn $1\" }\nexport FROM_PROFILE=yes\n"), 0o644)
	rb := mustParse(t, `
name: t
shell: zsh
profile: [`+prof+`, /nonexistent/file]
steps:
  - name: uses profile
    outputs: [x]
    run: |
      greet
      myfn arg
      echo "env=$FROM_PROFILE"
      x=1
  - {name: next, run: 'echo "x=$x"'}
`)
	ui := &scripted{}
	run := Execute(context.Background(), rb, ui, "", Options{})
	if run.Status != StatusSucceeded {
		t.Fatalf("%s %s %q", run.Status, run.Error, ui.output)
	}
	if strings.Join(ui.output, "|") != "hello from alias|fn arg|env=yes|x=1" {
		t.Fatalf("output %q", ui.output)
	}
}

func TestPartialMasking(t *testing.T) {
	rb := mustParse(t, `
name: t
sensitive:
  ticket: 'ticket {{SENSITIVE}}'
inputs: [{name: pw, secret: true, default: hunter22}]
steps:
  - name: setup
    shell: bash
    run: |
      db="postgres://app:s3cretpw@db.internal:5432/app"
      key="justasecret"
    secret_outputs: [db, key]
  - {name: use, run: 'echo "db=$db key=$key pw=$pw"; echo "opened ticket T-42"'}
`)
	ui := &scripted{}
	run := Execute(context.Background(), rb, ui, "", Options{})
	if run.Status != StatusSucceeded {
		t.Fatal(run.Error)
	}
	if got := strings.Join(ui.output, "|"); got != "db=postgres://app:****@db.internal:5432/app key=**** pw=****|opened ticket ****" {
		t.Fatalf("output %q", got)
	}
	if run.State["db"] != "postgres://app:****@db.internal:5432/app" || run.State["key"] != "****" || run.Inputs["pw"] != "****" {
		t.Fatalf("state %v inputs %v", run.State, run.Inputs)
	}
	if !slices.Equal(run.Secrets, []string{"db", "key", "pw"}) {
		t.Fatalf("secrets %v", run.Secrets)
	}
	// A partly-masked secret must not come back as a value on resume.
	st := ResumeState(run)
	if st["db"] != "****" || len(ResumeInputs(run)) != 0 {
		t.Fatalf("resume state %v inputs %v", st, ResumeInputs(run))
	}
}

// A value published (or captured) under a name that says it's a
// credential is kept secret, as if published to $RUNNR_SECRET.
func TestCredentialNamesStaySecret(t *testing.T) {
	rb := mustParse(t, `
name: t
steps:
  - {name: make, run: 'echo "db_password=Gen3ratedPw99" >> "$RUNNR_OUTPUT"; echo "region=use1" >> "$RUNNR_OUTPUT"'}
  - {name: grab, run: 'echo Capt0redPw', capture: admin_token}
  - {name: use, run: 'echo "$db_password $admin_token $region"'}
`)
	ui := &scripted{}
	run := Execute(context.Background(), rb, ui, "", Options{})
	if run.Status != StatusSucceeded {
		t.Fatal(run.Error)
	}
	b, _ := json.Marshal(run)
	if strings.Contains(string(b), "Gen3ratedPw99") || strings.Contains(string(b), "Capt0redPw") || !strings.Contains(string(b), "use1") {
		t.Fatalf("record %s", b)
	}
	if !slices.Equal(run.Secrets, []string{"admin_token", "db_password"}) {
		t.Fatalf("secrets %v", run.Secrets)
	}
}

func TestChecks(t *testing.T) {
	dir := t.TempDir()
	// "settles" passes from its third try; "steady" always passes.
	rb := mustParse(t, `
name: t
steps:
  - name: gate
    dir: `+dir+`
    checks:
      - {name: steady, run: 'true'}
      - {name: settles, run: 'n=$(cat n 2>/dev/null || echo 0); echo $((n+1)) > n; [ $((n+1)) -ge 3 ]'}
    repeat: {every: 50ms, timeout: 5s}
`)
	ui := &scripted{}
	run := Execute(context.Background(), rb, ui, "", Options{})
	if run.Status != StatusSucceeded || run.Steps[0].Attempts != 3 || len(run.Steps[0].Checks) != 2 || !run.Steps[0].Checks[1].Passed {
		t.Fatalf("%s %s attempts=%d checks=%+v", run.Status, run.Error, run.Steps[0].Attempts, run.Steps[0].Checks)
	}
	if !strings.Contains(run.Steps[0].Output, "❌ settles") || !strings.Contains(run.Steps[0].Output, "✅ settles") {
		t.Fatalf("output %q", run.Steps[0].Output)
	}

	// Without repeat, a failing group goes to the failure choices; retry re-runs it.
	rb = mustParse(t, `
name: t
steps:
  - name: gate
    dir: `+t.TempDir()+`
    checks:
      - {name: flaky, run: 'n=$(cat n 2>/dev/null || echo 0); echo $((n+1)) > n; [ $((n+1)) -ge 2 ]'}
`)
	ui = &scripted{failures: []FailureAction{FailRetry}}
	run = Execute(context.Background(), rb, ui, "", Options{Interactive: true})
	if run.Status != StatusSucceeded || run.Steps[0].Attempts != 2 {
		t.Fatalf("%s attempts=%d", run.Status, run.Steps[0].Attempts)
	}

	// Repeat gives up at the timeout.
	rb = mustParse(t, "name: t\nsteps:\n  - name: g\n    checks: [{name: never, run: 'false'}]\n    repeat: {every: 50ms, timeout: 200ms}\n")
	run = Execute(context.Background(), rb, Unattended{}, "", Options{})
	if run.Status != StatusFailed || !strings.Contains(run.Steps[0].Error, "gave up after 200ms") {
		t.Fatalf("%s %q", run.Status, run.Steps[0].Error)
	}
}

// chooser picks a way by name, and records what it was offered.
type chooser struct {
	scripted
	picks  []string
	asked  int
	failed []int
}

func (c *chooser) ChooseWay(_ context.Context, v StepView, failed int) (int, error) {
	c.asked++
	c.failed = append(c.failed, failed)
	pick := c.picks[0]
	c.picks = c.picks[1:]
	return slices.IndexFunc(v.Ways, func(w WayView) bool { return w.Name == pick }), nil
}

const waysRB = `
name: t
steps:
  - name: swap
    outputs: [how]
    ways:
      - {name: Scripted, run: 'how=script; echo via script'}
      - {name: Manual, manual: 'Do it by hand', preferred: true}
      - {name: quick, run: 'how=quick; echo via quick'}
  - {name: after, run: 'echo "how=$how"'}
`

func TestWaysPreferred(t *testing.T) {
	rb := mustParse(t, waysRB)
	// Not set to choose: the preferred way (Manual), without asking.
	ui := &chooser{scripted: scripted{manuals: []ManualOutcome{ManualDone}}}
	run := Execute(context.Background(), rb, ui, "", Options{Interactive: true})
	if run.Status != StatusSucceeded || ui.asked != 0 || run.Steps[0].Way != "Manual" || run.Steps[0].Kind != "manual" {
		t.Fatalf("%s asked=%d %+v", run.Status, ui.asked, run.Steps[0])
	}
	// Unattended: the preferred way too (a manual one follows its policy).
	run = Execute(context.Background(), rb, Unattended{}, "", Options{})
	if run.Status != StatusFailed || run.Steps[0].Way != "Manual" {
		t.Fatalf("unattended: %s %+v", run.Status, run.Steps[0])
	}
}

func TestWaysChooseAndOtherWayAfterFailure(t *testing.T) {
	rb := mustParse(t, strings.Replace(waysRB, "  - name: swap\n", "  - name: swap\n    choose: true\n", 1))
	ui := &chooser{picks: []string{"quick"}}
	run := Execute(context.Background(), rb, ui, "", Options{Interactive: true})
	if run.Status != StatusSucceeded || ui.asked != 1 || !slices.Contains(ui.output, "how=quick") || run.Steps[0].Way != "quick" {
		t.Fatalf("%s asked=%d %q %+v", run.Status, ui.asked, ui.output, run.Steps[0])
	}

	// A way fails: "try another way" offers the ways again, with the failed one marked.
	broken := strings.Replace(waysRB, "how=script; echo via script", "exit 1", 1)
	broken = strings.Replace(broken, ", preferred: true", "", 1) // Scripted (first) is preferred now
	ui = &chooser{picks: []string{"quick"}}
	ui.failures = []FailureAction{FailOtherWay}
	run = Execute(context.Background(), mustParse(t, broken), ui, "", Options{Interactive: true})
	if run.Status != StatusSucceeded || !slices.Equal(ui.failed, []int{0}) || run.Steps[0].Way != "quick" || !slices.Contains(ui.output, "how=quick") {
		t.Fatalf("%s failed=%v %q %+v", run.Status, ui.failed, ui.output, run.Steps[0])
	}
}

func TestMaintenanceWindow(t *testing.T) {
	rb := mustParse(t, `
name: t
maintenance_window: {target: 1m}
steps:
  - {name: prep, run: 'true'}
  - {name: down, run: 'sleep 0.1', window: open}
  - {name: work, run: 'sleep 0.1', window_offset: +5m}
  - {name: up, run: 'true', window: close}
  - {name: after, run: 'true'}
`)
	ui := &scripted{}
	run := Execute(context.Background(), rb, ui, "", Options{})
	w := run.Window
	if run.Status != StatusSucceeded || w == nil || w.Opened.IsZero() || w.Closed.IsZero() || w.OpenStep != 1 || w.CloseStep != 3 {
		t.Fatalf("%s %+v", run.Status, w)
	}
	if d := w.Closed.Sub(w.Opened); d < 200*time.Millisecond || w.Opened.Before(run.Steps[1].Started) || w.Closed.After(run.Steps[4].Started) {
		t.Fatalf("window %s", d)
	}
	if !strings.Contains(strings.Join(ui.output, "|"), "maintenance window closed") {
		t.Fatalf("%q", ui.output)
	}
	// A run that stops while production is down leaves the window open;
	// resuming it keeps the time it opened.
	failing := strings.Replace(strings.Replace(rbYAML(t, rb), "sleep 0.1", "exit 1", 2), "", "", 0)
	rb2 := mustParse(t, failing)
	prev := Execute(context.Background(), rb2, Unattended{}, "", Options{})
	if prev.Window.Opened.IsZero() || !prev.Window.Closed.IsZero() {
		t.Fatalf("stopped run window %+v", prev.Window)
	}
	resumed := Execute(context.Background(), rb, Unattended{}, "", Options{StartAt: 2, Resumed: prev, Controls: NewControls(ControlsView{Skip: []int{0, 1}})})
	if !resumed.Window.Opened.Equal(prev.Window.Opened) || resumed.Window.Closed.IsZero() {
		t.Fatalf("resumed window %+v (prev %+v)", resumed.Window, prev.Window)
	}
}

func rbYAML(t *testing.T, rb *runbook.Runbook) string {
	t.Helper()
	b, err := runbook.Format(rb)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFormatOffset(t *testing.T) {
	for d, want := range map[time.Duration]string{-30 * time.Minute: "-30m", 0: "+0", 75 * time.Minute: "+1h15m", time.Hour: "+1h", 45 * time.Second: "+45s"} {
		if got := FormatOffset(d); got != want {
			t.Errorf("FormatOffset(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestBackground(t *testing.T) {
	dir := t.TempDir()
	rb := mustParse(t, `
name: t
steps:
  - id: watcher
    name: watcher
    dir: `+dir+`
    background: true
    stop_after: middle
    run: 'echo watching; while true; do date +%s >> ticks; sleep 0.05; done'
  - {name: first, run: 'sleep 0.3'}
  - {id: middle, name: middle, run: 'true'}
  - {name: last, run: 'sleep 0.3'}
  - {name: forever, background: true, run: 'echo second; sleep 30'}
  - {name: end, run: 'true'}
`)
	ui := &scripted{}
	start := time.Now()
	run := Execute(context.Background(), rb, ui, "", Options{})
	if run.Status != StatusSucceeded || time.Since(start) > 10*time.Second {
		t.Fatalf("%s %s %s", run.Status, run.Error, time.Since(start))
	}
	w := run.Steps[0]
	if w.Status != StatusSucceeded || w.Note != "stopped after step 3" || !strings.Contains(w.Output, "watching") {
		t.Fatalf("watcher %+v", w)
	}
	// It stopped after "middle": no ticks while "last" ran.
	before, _ := os.ReadFile(dir + "/ticks")
	time.Sleep(200 * time.Millisecond)
	after, _ := os.ReadFile(dir + "/ticks")
	if len(before) == 0 || len(before) != len(after) {
		t.Fatalf("ticks %d -> %d", len(before), len(after))
	}
	if f := run.Steps[4]; f.Note != "stopped when the run ended" || !strings.Contains(f.Output, "second") {
		t.Fatalf("forever %+v", f)
	}
}

func TestFunctions(t *testing.T) {
	lib, err := runbook.ParseLibrary([]byte(`
name: helpers
functions:
  greet: {description: Say hello, body: 'echo "hello, $1"'}
  twice: {body: 'echo "lib twice"'}
`))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	rb := mustParse(t, `
name: t
libraries: [helpers]
functions:
  twice: {body: 'echo "$1$1"'}
steps:
  - {name: cmd, run: 'greet cmd; twice ab'}
  - {name: gate, checks: [{name: has greet, run: 'greet check | grep -q "hello, check"'}]}
  - name: way
    ways:
      - {name: only, run: 'greet sub'}
      - {name: other, manual: 'by hand'}
  - {name: bg, background: true, dir: `+dir+`, run: 'greet bg > out; sleep 5'}
  - {name: wait, run: 'sleep 0.3'}
`)
	ui := &scripted{}
	run := Execute(context.Background(), rb, ui, "", Options{Libraries: []*runbook.Library{lib}})
	if run.Status != StatusSucceeded {
		t.Fatalf("%s %s", run.Status, run.Error)
	}
	got := strings.Join(ui.output, "|")
	for _, want := range []string{"hello, cmd", "abab", "hello, sub"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	if b, _ := os.ReadFile(dir + "/out"); strings.TrimSpace(string(b)) != "hello, bg" {
		t.Fatalf("background: %q", b)
	}
}

func TestFunctionsInShells(t *testing.T) {
	for _, sh := range []string{"/bin/zsh", "/bin/bash"} {
		if _, err := os.Stat(sh); err != nil {
			continue
		}
		t.Run(sh, func(t *testing.T) {
			home := t.TempDir()
			// The user's own config still loads, before the run's functions.
			os.WriteFile(home+"/.zshrc", []byte("myalias_ok() { echo user-config }\n"), 0o644)
			// Ubuntu's /etc/zsh/zshrc runs compinit, which can stop to ask about
			// insecure directories (as on CI runners); a user can skip it so.
			os.WriteFile(home+"/.zshenv", []byte("skip_global_compinit=1\n"), 0o644)
			os.WriteFile(home+"/.bashrc", []byte("myalias_ok() { echo user-config; }\n"), 0o644)
			t.Setenv("HOME", home)
			t.Setenv("ZDOTDIR", "")
			t.Setenv("SHELL", sh)
			// A shell at a step is the shell the step runs in (here, the runbook's).
			rb := mustParse(t, "name: t\nshell: "+sh+"\nfunctions:\n  greet: {body: 'echo \"hello, $1\"'}\nsteps:\n  - {name: a, run: 'true'}\n")
			ui := &stepper{decisions: []StepDecision{StepRun}, shellAt: "a", keys: "greet shell; myalias_ok; exit\n"}
			run := Execute(context.Background(), rb, ui, "", Options{Interactive: true, Controls: NewControls(ControlsView{StepAll: true})})
			if run.Status != StatusSucceeded || len(run.Steps[0].Shells) != 1 {
				t.Fatalf("%s %+v", run.Status, run.Steps[0])
			}
			tr := run.Steps[0].Shells[0]
			if !strings.Contains(tr, "hello, shell") || !strings.Contains(tr, "user-config") {
				t.Fatalf("transcript %q", tr)
			}
		})
	}
}

func TestRetryChecksModes(t *testing.T) {
	// a passes; b passes from its 3rd run (a counter file); c always passes.
	for _, tc := range []struct {
		mode string
		runs string // how many times a, b, c ran
	}{
		{"", "3 3 3"},       // all: every check every attempt
		{"failed", "1 3 1"}, // only what failed
		{"each", "1 3 1"},   // b blocks c until it passes; c runs once
	} {
		t.Run(tc.mode, func(t *testing.T) {
			dir := t.TempDir()
			mode := ""
			if tc.mode != "" {
				mode = "\n    retry_checks: " + tc.mode
			}
			rb := mustParse(t, `
name: t
steps:
  - name: gate
    dir: `+dir+`
    repeat: {every: 10ms, timeout: 5s}`+mode+`
    checks:
      - {name: a, run: 'echo x >> a'}
      - {name: b, run: 'echo x >> b; [ $(wc -l < b) -ge 3 ]'}
      - {name: c, run: 'echo x >> c'}
`)
			run := Execute(context.Background(), rb, &scripted{}, "", Options{})
			if run.Status != StatusSucceeded || run.Steps[0].Attempts != 3 {
				t.Fatalf("%s attempts=%d", run.Status, run.Steps[0].Attempts)
			}
			var got []string
			for _, f := range []string{"a", "b", "c"} {
				b, _ := os.ReadFile(dir + "/" + f)
				got = append(got, strconv.Itoa(strings.Count(string(b), "x")))
			}
			if strings.Join(got, " ") != tc.runs {
				t.Fatalf("runs %v, want %s", got, tc.runs)
			}
		})
	}
}

func TestParallelChecks(t *testing.T) {
	// Each check waits for the others to have started (a file each), so
	// they only pass if they run at once; b fails its first attempt, and
	// with retry_checks: failed only b runs again.
	dir := t.TempDir()
	wait := "touch $1\nfor i in $(seq 100); do [ -e a ] && [ -e b ] && [ -e c ] && exit 0; sleep 0.05; done\nexit 1\n"
	if err := os.WriteFile(dir+"/wait.sh", []byte(wait), 0o600); err != nil {
		t.Fatal(err)
	}
	rb := mustParse(t, `
name: t
steps:
  - name: gate
    dir: `+dir+`
    parallel_checks: true
    retry_checks: failed
    repeat: {every: 10ms, timeout: 10s}
    ways:
      - name: gate
        checks:
          - {name: a, run: 'echo x >> runs-a; sh wait.sh a'}
          - {name: b, run: 'echo x >> runs-b; [ -e b-failed ] || { touch b-failed; sh wait.sh b; exit 1; }'}
          - {name: c, run: 'echo x >> runs-c; sh wait.sh c'}
`)
	run := Execute(context.Background(), rb, &scripted{}, "", Options{})
	if run.Status != StatusSucceeded || run.Steps[0].Attempts != 2 {
		t.Fatalf("%s attempts=%d: %s", run.Status, run.Steps[0].Attempts, run.Steps[0].Output)
	}
	var got []string
	for _, f := range []string{"a", "b", "c"} {
		b, _ := os.ReadFile(dir + "/runs-" + f)
		got = append(got, strconv.Itoa(strings.Count(string(b), "x")))
	}
	if strings.Join(got, " ") != "1 2 1" {
		t.Fatalf("runs %v, want 1 2 1", got)
	}
	for j, r := range run.Steps[0].Checks {
		if r.Name != []string{"a", "b", "c"}[j] || !r.Passed {
			t.Errorf("checks[%d] = %+v", j, r)
		}
	}
}

func TestAbortCauseRecorded(t *testing.T) {
	rb := mustParse(t, `name: ab
steps:
  - {name: wait, run: sleep 5}
`)
	ctx, cancel := context.WithCancelCause(context.Background())
	time.AfterFunc(200*time.Millisecond, func() { cancel(&AbortError{By: "ann", Reason: "wrong env"}) })
	run := Execute(ctx, rb, Unattended{}, "", Options{})
	if run.Status != StatusAborted || run.Error != "aborted by ann: wrong env" || run.Steps[0].Error != run.Error {
		t.Errorf("run = %s %q, step %q", run.Status, run.Error, run.Steps[0].Error)
	}
}

func TestOptionalSteps(t *testing.T) {
	rb := mustParse(t, `name: opt
steps:
  - {name: a, run: 'true'}
  - {name: nice to have, run: 'exit 3', optional: true}
  - {name: c, run: 'true'}
`)
	if rb.Steps[1].Optional != true {
		t.Fatal("optional not parsed")
	}
	// Unattended: it runs, fails, and the run carries on without it.
	run := Execute(context.Background(), rb, Unattended{}, "", Options{})
	if run.Status != StatusSucceeded || run.Steps[1].Status != StatusFailed || !run.Steps[1].Optional || run.Steps[2].Status != StatusSucceeded {
		t.Fatalf("%s %s", run.Status, statuses(run))
	}
	// Interactive: it asks before the optional step only; skipping it is fine.
	ui := &stepper{decisions: []StepDecision{StepSkip}}
	run = Execute(context.Background(), rb, ui, "", Options{Interactive: true})
	if run.Status != StatusSucceeded || run.Steps[1].Status != StatusSkipped || len(ui.decisions) != 0 {
		t.Fatalf("%s %s", run.Status, statuses(run))
	}
	if _, err := runbook.Parse([]byte("name: x\nsteps: [{name: s, run: 'true', always: true, optional: true}]\n")); err == nil {
		t.Fatal("always and optional together should be invalid")
	}
}

// A background step can have ways (all commands): the chosen one runs in
// the background.
func TestBackgroundWays(t *testing.T) {
	rb := mustParse(t, `
name: t
steps:
  - name: watcher
    background: true
    choose: true
    stop_after: work
    ways:
      - {name: fancy, run: 'echo fancy; sleep 30', preferred: true}
      - {name: plain, run: 'echo plain; sleep 30'}
  - {id: work, name: work, run: 'sleep 0.3'}
`)
	ui := &chooser{picks: []string{"plain"}}
	run := Execute(context.Background(), rb, ui, "", Options{Interactive: true})
	if run.Status != StatusSucceeded || ui.asked != 1 {
		t.Fatalf("%s %s asked %d", run.Status, run.Error, ui.asked)
	}
	w := run.Steps[0]
	if w.Way != "plain" || !strings.Contains(w.Output, "plain") || w.Note != "stopped after step 2" {
		t.Fatalf("watcher %+v", w)
	}
	if _, err := runbook.Parse([]byte("name: x\nsteps: [{name: s, background: true, ways: [{name: a, run: 'true', preferred: true}, {name: b, manual: do it}]}]\n")); err == nil {
		t.Fatal("a background step's ways must all be commands")
	}
}

func TestRawLog(t *testing.T) {
	rb := mustParse(t, `name: raw
steps:
  - {name: hello, run: 'echo hi there'}
  - {name: probe, checks: [{name: up, run: 'echo pong'}]}
  - {name: broken, run: 'echo oops; exit 3', continue_on_error: true}
  - {name: never, run: 'true', when: 'false'}
`)
	run := Execute(context.Background(), rb, Unattended{}, "", Options{Vars: map[string]string{}})
	log := RawLog(run)
	for _, want := range []string{"run " + run.ID + " · raw", "▶ 1. hello (run)", "$ echo hi there", "hi there", "✔ succeeded · exit 0",
		"✓ check: up (attempt 1)", "pong", "✘ failed · exit 3", "error: exit status 3", "↷ skipped", "condition false"} {
		if !strings.Contains(log, want) {
			t.Errorf("raw log lacks %q:\n%s", want, log)
		}
	}
}
