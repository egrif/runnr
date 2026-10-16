package engine

import (
	"context"
	"io"
	"os"
	"slices"
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
	ui := &scripted{manuals: []ManualOutcome{ManualDone, ManualFail}}
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
