// Package engine executes runbooks. All human decisions go through an
// Interactor, so the same engine drives the CLI, the GUI and unattended runs.
package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/egrif/runnr/internal/runbook"
)

type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusSkipped   Status = "skipped"
	StatusAborted   Status = "aborted"
	// StatusForced: the step failed but an operator accepted it, with a reason.
	StatusForced Status = "forced"
)

// Note is an operator comment on a step or a run. Notes are stored apart
// from the run record, so they can be added while a run is live or later.
type Note struct {
	Time   time.Time `json:"time"`
	Author string    `json:"author,omitempty"`
	Text   string    `json:"text"`
}

type StepResult struct {
	Index    int    `json:"index"`
	Name     string `json:"name"`
	Section  string `json:"section,omitempty"`
	Owner    string `json:"owner,omitempty"`
	Kind     string `json:"kind"`
	Status   Status `json:"status"`
	Command  string `json:"command,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Output   string `json:"output,omitempty"`
	Error    string `json:"error,omitempty"`
	Note     string `json:"note,omitempty"`
	Attempts int    `json:"attempts,omitempty"`
	Notes    []Note `json:"notes,omitempty"`
	// Shells are transcripts of shells the operator opened at this step.
	Shells  []string  `json:"shells,omitempty"`
	Started time.Time `json:"started,omitzero"`
	Ended   time.Time `json:"ended,omitzero"`
}

type Run struct {
	ID             string `json:"id"`
	Runbook        string `json:"runbook"`
	Mode           string `json:"mode"`
	DryRun         bool   `json:"dry_run,omitempty"`
	StartAt        int    `json:"start_at,omitempty"`
	RunbookVersion int    `json:"runbook_version,omitempty"`
	Resumes        string `json:"resumes,omitempty"`
	// Previous is the run this one was compared against ("last time").
	Previous string            `json:"previous,omitempty"`
	Status   Status            `json:"status"`
	Error    string            `json:"error,omitempty"`
	Inputs   map[string]string `json:"inputs"`
	Started  time.Time         `json:"started"`
	Ended    time.Time         `json:"ended,omitzero"`
	Steps    []StepResult      `json:"steps"`
	Notes    []Note            `json:"notes,omitempty"`
	// State is every variable the run has (inputs and published values)
	// as of the last finished step; secrets are "****".
	State map[string]string `json:"state,omitempty"`
}

type EventKind string

const (
	EventRunStarted   EventKind = "run_started"
	EventStepStarted  EventKind = "step_started"
	EventOutput       EventKind = "output"
	EventStepFinished EventKind = "step_finished"
	EventRunFinished  EventKind = "run_finished"
)

type Event struct {
	Kind   EventKind   `json:"kind"`
	Step   int         `json:"step"`
	Stream string      `json:"stream,omitempty"`
	Text   string      `json:"text,omitempty"`
	Result *StepResult `json:"result,omitempty"`
	Run    *Run        `json:"run,omitempty"`
	// State accompanies step_finished: the run's variables, secrets masked.
	State map[string]string `json:"state,omitempty"`
}

type ManualOutcome string

const (
	ManualDone ManualOutcome = "done"
	ManualSkip ManualOutcome = "skip"
	ManualFail ManualOutcome = "fail"
)

type FailureAction string

const (
	FailRetry FailureAction = "retry"
	FailSkip  FailureAction = "skip"
	FailAbort FailureAction = "abort"
	// FailForce accepts the failure as success; the reason is required.
	FailForce FailureAction = "force"
)

// StepDecision is the answer to "about to run this step" in step mode.
type StepDecision string

const (
	StepRun      StepDecision = "run"
	StepSkip     StepDecision = "skip"
	StepContinue StepDecision = "continue" // run this and all later steps without pausing
	StepAbort    StepDecision = "abort"
)

// AfterDecision is the answer to "this step finished" for steps set to
// pause afterwards.
type AfterDecision string

const (
	AfterContinue AfterDecision = "continue"
	AfterRetry    AfterDecision = "retry" // run it again
	AfterFail     AfterDecision = "fail"  // treat it as failed (then the usual failure choices)
	AfterAbort    AfterDecision = "abort"
)

// Interactor supplies human decisions and receives progress events.
// Notify may be called from multiple goroutines, but never concurrently.
type Interactor interface {
	Notify(Event)
	AskInput(ctx context.Context, in runbook.Input) (string, error)
	Confirm(ctx context.Context, idx int, step runbook.Step, command string) (bool, error)
	Manual(ctx context.Context, idx int, step runbook.Step, instructions string) (ManualOutcome, string, error)
	// OnFailure returns what to do; for FailForce, reason must be non-empty.
	OnFailure(ctx context.Context, idx int, step runbook.Step, res StepResult) (action FailureAction, reason string, err error)
}

type Options struct {
	Vars   map[string]string
	DryRun bool
	// Interactive controls whether missing inputs are asked for.
	Interactive bool
	// StartAt skips steps before this index, except those marked always.
	StartAt int
	// Controls: steps to skip and where to pause (pausing needs an
	// interactive run and a Stepper). Nil means none.
	Controls *Controls
	// Review shows the plan before running (if the Interactor is a Reviewer).
	Review bool
	// Previous is an earlier run to compare against ("what it did last time").
	Previous *Run
	// State seeds variables from an earlier run (resume). Inputs win, and
	// masked secrets are not restored.
	State map[string]string
	// Resumes records the run ID this run continues, if any.
	Resumes string
	// RunbookVersion records which saved version of the runbook ran.
	RunbookVersion int
	// Save is called with a snapshot after every state change worth keeping.
	Save func(*Run)
	// ShellOnly runs only the steps marked always (the setup), then opens an
	// interactive shell with the resulting environment.
	ShellOnly bool
}

// StepView is a step as it would run now: templates rendered against the
// run's current state (values not known yet show as <name>).
type StepView struct {
	Index         int      `json:"index"`
	Name          string   `json:"name"`
	Section       string   `json:"section,omitempty"`
	Owner         string   `json:"owner,omitempty"`
	Kind          string   `json:"kind"`
	Command       string   `json:"command"`
	Expect        string   `json:"expect,omitempty"`
	Dir           string   `json:"dir,omitempty"`
	When          string   `json:"when,omitempty"`
	Confirm       bool     `json:"confirm,omitempty"`
	Always        bool     `json:"always,omitempty"`
	Timeout       string   `json:"timeout,omitempty"`
	Retries       int      `json:"retries,omitempty"`
	Outputs       []string `json:"outputs,omitempty"`
	SecretOutputs []string `json:"secret_outputs,omitempty"`
}

// Session gives an interactor read access to the run it's driving, plus
// its shell. Call it only from inside Interactor callbacks.
type Session interface {
	View(idx int) StepView
	Plan() []StepView
	// Controls are the run's skip/pause settings; changes apply to steps
	// not reached yet.
	Controls() *Controls
	// Previous is the run being resumed or rerun, or the last run (may be nil).
	Previous() *Run
	// Mask hides the run's secrets in text.
	Mask(string) string
	// State returns the run's variables with secrets masked.
	State() map[string]string
	// CanShell reports whether Shell can work here.
	CanShell() bool
	// Shell opens an interactive shell with the environment step idx would
	// get (-1: none). Values written to $RUNNR_OUTPUT/$RUNNR_SECRET there
	// are added to the run's state.
	Shell(ctx context.Context, idx int) error
}

// Binder receives the Session before the run starts.
type Binder interface{ Bind(Session) }

// ShellOpener lets an interactor wrap shells it opens (e.g. signal handling).
type ShellOpener interface {
	OpenShell(ctx context.Context, idx int) error
}

// Reviewer is shown the plan before steps run and may change the controls.
type Reviewer interface {
	Review(ctx context.Context, plan []StepView, c *Controls) error
}

// Stepper is asked before steps set to pause before (or all of them, in
// step-through mode), and after steps set to pause after.
type Stepper interface {
	BeforeStep(ctx context.Context, v StepView) (StepDecision, error)
	AfterStep(ctx context.Context, v StepView, res StepResult) (AfterDecision, string, error)
}

const maxOutput = 64 << 10

func NewRunID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

type executor struct {
	rb         *runbook.Runbook
	ui         Interactor
	opts       Options
	run        *Run
	vars       map[string]string
	secrets    []string
	secretVars map[string]bool
	ctl        *Controls
	mu         sync.Mutex
}

// Execute runs rb to completion and returns the final record.
// id may be empty, in which case one is generated.
func Execute(ctx context.Context, rb *runbook.Runbook, ui Interactor, id string, opts Options) *Run {
	if id == "" {
		id = NewRunID()
	}
	mode := "unattended"
	if opts.Interactive {
		mode = "interactive"
	}
	x := &executor{rb: rb, ui: ui, opts: opts, vars: map[string]string{}, secretVars: map[string]bool{}}
	x.run = &Run{ID: id, Runbook: rb.Name, Mode: mode, DryRun: opts.DryRun, StartAt: opts.StartAt, Resumes: opts.Resumes, RunbookVersion: opts.RunbookVersion, Status: StatusRunning, Started: time.Now(), Inputs: map[string]string{}}
	for i, s := range rb.Steps {
		kind := "run"
		if s.IsManual() {
			kind = "manual"
		} else if s.TTY {
			kind = "tty"
		}
		x.run.Steps = append(x.run.Steps, StepResult{Index: i, Name: s.Name, Section: s.Section, Owner: s.Owner, Kind: kind, Status: StatusPending})
	}

	if opts.Previous != nil {
		x.run.Previous = opts.Previous.ID
	}
	if b, ok := ui.(Binder); ok {
		b.Bind(x)
	}
	if opts.ShellOnly {
		x.opts.StartAt = len(rb.Steps)
	}
	x.ctl = opts.Controls
	if x.ctl == nil {
		x.ctl = NewControls(ControlsView{})
	}
	// Starting later is just skipping the earlier (non-always) steps, so
	// the review can show it and the operator can change it.
	for i := 0; i < x.opts.StartAt && i < len(rb.Steps); i++ {
		if skip, _, _ := x.ctl.Get(i); !rb.Steps[i].Always && !skip {
			x.ctl.Toggle("skip", i)
		}
	}
	x.save()
	err := x.resolveInputs(ctx)
	if err == nil {
		x.restoreState()
		x.run.State = x.State()
		if r, ok := ui.(Reviewer); ok && opts.Review && !opts.ShellOnly {
			err = r.Review(ctx, x.Plan(), x.ctl)
		}
	}
	if err == nil {
		x.notify(Event{Kind: EventRunStarted, Run: x.snapshot()})
		x.save()
		err = x.steps(ctx)
		if err == nil && opts.ShellOnly {
			if o, ok := ui.(ShellOpener); ok {
				err = o.OpenShell(ctx, -1)
			} else {
				err = x.Shell(ctx, -1)
			}
		}
	}
	x.finish(ctx, err)
	return x.run
}

func (x *executor) finish(ctx context.Context, err error) {
	x.run.Ended = time.Now()
	switch {
	case err == nil:
		x.run.Status = StatusSucceeded
	case ctx.Err() != nil || errors.Is(err, errAborted):
		x.run.Status = StatusAborted
		x.run.Error = x.mask(err.Error())
	default:
		x.run.Status = StatusFailed
		x.run.Error = x.mask(err.Error())
	}
	for i := range x.run.Steps {
		if x.run.Steps[i].Status == StatusRunning {
			x.run.Steps[i].Status = x.run.Status
		}
	}
	x.save()
	x.notify(Event{Kind: EventRunFinished, Run: x.snapshot()})
}

var errAborted = errors.New("aborted")

// NoteBeforeStart marks steps skipped because the run started later on.
const NoteBeforeStart = "before start step"

// NoteDeselected marks steps the operator excluded before the run.
const NoteDeselected = "deselected"

func (x *executor) resolveInputs(ctx context.Context) error {
	known := map[string]bool{}
	for _, in := range x.rb.Inputs {
		known[in.Name] = true
		v, ok := x.opts.Vars[in.Name]
		if !ok && x.opts.Interactive {
			var err error
			if v, err = x.ui.AskInput(ctx, in); err != nil {
				return err
			}
			ok = v != ""
		}
		if !ok || v == "" {
			v = in.Default
		}
		if v == "" && in.Required {
			return fmt.Errorf("missing required input %q", in.Name)
		}
		if v != "" && len(in.Choices) > 0 && !slices.Contains(in.Choices, v) {
			return fmt.Errorf("input %q: %q is not one of %s", in.Name, v, strings.Join(in.Choices, ", "))
		}
		x.vars[in.Name] = v
		if in.Secret && v != "" {
			x.secretVars[in.Name] = true
			x.secrets = append(x.secrets, v)
			x.run.Inputs[in.Name] = "****"
		} else {
			x.run.Inputs[in.Name] = v
		}
	}
	for k := range x.opts.Vars {
		if !known[k] {
			return fmt.Errorf("unknown input %q", k)
		}
	}
	return nil
}

func (x *executor) steps(ctx context.Context) error {
	for i, step := range x.rb.Steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		if skip, _, _ := x.ctl.Get(i); skip {
			note := NoteDeselected
			if i < x.opts.StartAt && !step.Always {
				note = NoteBeforeStart
			}
			x.run.Steps[i].Status, x.run.Steps[i].Note = StatusSkipped, note
			x.stepDone(i)
			continue
		}
		if err := x.step(ctx, i, step); err != nil {
			return err
		}
	}
	return nil
}

func (x *executor) step(ctx context.Context, i int, step runbook.Step) error {
	res := &x.run.Steps[i]
	if step.When != "" {
		cond, err := x.render(step.When)
		if err != nil {
			return x.fail(i, fmt.Errorf("when: %w", err))
		}
		if !runbook.Truthy(cond) {
			res.Status, res.Note = StatusSkipped, "condition false"
			x.stepDone(i)
			return nil
		}
	}
	res.Status, res.Started = StatusRunning, time.Now()
	x.notify(Event{Kind: EventStepStarted, Step: i, Result: x.result(i)})

	confirmed := false
	if _, before, _ := x.ctl.Get(i); before && x.canPause() {
		d, err := x.ui.(Stepper).BeforeStep(ctx, x.View(i))
		if err != nil {
			return x.fail(i, err)
		}
		switch d {
		case StepSkip:
			res.Status, res.Note = StatusSkipped, "skipped by operator"
			x.stepDone(i)
			return nil
		case StepContinue:
			x.ctl.SetStepAll(false)
		case StepAbort:
			return x.fail(i, errAborted)
		}
		confirmed = true // choosing to run counts as the confirmation
	}

	if step.IsManual() {
		return x.manual(ctx, i, step, step.Manual)
	}

	cmd, err := x.render(step.Run)
	if err != nil {
		return x.fail(i, fmt.Errorf("run: %w", err))
	}
	res.Command = x.mask(cmd)

	if step.TTY && !x.CanShell() && !x.opts.DryRun {
		// No terminal here: hand the command to a human instead.
		return x.manual(ctx, i, step, "Run in a terminal (needs interactive input):\n\n"+step.Run)
	}

	if step.Confirm && !confirmed && !x.opts.DryRun {
		ok, err := x.ui.Confirm(ctx, i, step, x.mask(cmd))
		if err != nil {
			return x.fail(i, err)
		}
		if !ok {
			res.Status, res.Note = StatusSkipped, "declined"
			x.stepDone(i)
			return nil
		}
	}
	if x.opts.DryRun {
		res.Status, res.Note = StatusSkipped, "dry run"
		x.stepDone(i)
		return nil
	}

	for {
		attempts := step.Retries + 1
		var out cmdResult
		for a := 1; a <= attempts; a++ {
			res.Attempts++
			out = x.exec(ctx, i, step, cmd)
			if out.err == nil || ctx.Err() != nil || a == attempts {
				break
			}
			delay := step.RetryDelayDuration()
			x.notify(Event{Kind: EventOutput, Step: i, Stream: "info", Text: fmt.Sprintf("attempt %d/%d failed (%s); retrying in %s", a, attempts, x.mask(out.err.Error()), delay)})
			select {
			case <-ctx.Done():
			case <-time.After(delay):
			}
		}
		res.Output = x.mask(out.combined)
		res.ExitCode = out.exitCode
		if out.err == nil {
			x.apply(step, out)
			res.Command = x.mask(res.Command) // in case it contained a just-published secret
			res.Status, res.Error = StatusSucceeded, ""
			if err := x.afterStep(ctx, i, step); err != errRetry {
				return err
			}
			continue
		}
		if ctx.Err() != nil {
			return x.fail(i, ctx.Err())
		}
		res.Error = x.mask(out.err.Error())
		if step.ContinueOnError {
			res.Status, res.Note = StatusFailed, "continue_on_error"
			x.stepDone(i)
			return nil
		}
		if err := x.onFailure(ctx, i, step, out); err != errRetry {
			return err
		}
	}
}

var errRetry = errors.New("retry")

func (x *executor) canPause() bool {
	_, ok := x.ui.(Stepper)
	return ok && x.opts.Interactive && !x.opts.DryRun
}

// afterStep finishes a successful step i, first letting the operator look
// at the result if it's set to pause afterwards. errRetry means run it
// again.
func (x *executor) afterStep(ctx context.Context, i int, step runbook.Step) error {
	res := &x.run.Steps[i]
	if _, _, after := x.ctl.Get(i); !after || !x.canPause() {
		x.stepDone(i)
		return nil
	}
	d, note, err := x.ui.(Stepper).AfterStep(ctx, x.View(i), *x.result(i))
	if err != nil {
		return x.fail(i, err)
	}
	switch d {
	case AfterRetry:
		res.Status = StatusRunning
		x.notify(Event{Kind: EventOutput, Step: i, Stream: "info", Text: "running again…"})
		return errRetry
	case AfterFail:
		res.Status, res.Error = StatusFailed, strings.TrimSpace("rejected after review "+note)
		return x.onFailure(ctx, i, step, cmdResult{})
	case AfterAbort:
		return x.fail(i, errAborted)
	}
	x.stepDone(i)
	return nil
}

// onFailure asks what to do about failed step i. It returns errRetry to
// run it again, nil if the step is settled, or the error that ends the run.
func (x *executor) onFailure(ctx context.Context, i int, step runbook.Step, out cmdResult) error {
	res := &x.run.Steps[i]
	action, reason, err := x.ui.OnFailure(ctx, i, step, *x.result(i))
	if err != nil {
		return x.fail(i, err)
	}
	switch action {
	case FailForce:
		if strings.TrimSpace(reason) == "" {
			return x.fail(i, errors.New("forcing a failed step requires a reason"))
		}
		// Accepted as success, so whatever it published is used.
		x.apply(step, out)
		res.Status, res.Note = StatusForced, "forced: "+strings.TrimSpace(reason)
		x.stepDone(i)
		return nil
	case FailRetry:
		x.notify(Event{Kind: EventOutput, Step: i, Stream: "info", Text: "retrying…"})
		return errRetry
	case FailSkip:
		res.Status, res.Note = StatusSkipped, "skipped after failure"
		x.stepDone(i)
		return nil
	}
	return x.fail(i, fmt.Errorf("step %d (%s) failed: %s", i+1, step.Name, res.Error))
}

// apply records what a successful (or forced) step produced.
func (x *executor) apply(step runbook.Step, out cmdResult) {
	if step.Capture != "" {
		x.vars[step.Capture] = strings.TrimSpace(out.stdout)
	}
	for k, v := range out.outputs {
		x.vars[k] = v
		delete(x.secretVars, k)
	}
	for k, v := range out.secrets {
		x.vars[k] = v
		x.secretVars[k] = true
		x.addSecret(v)
	}
}

func (x *executor) manual(ctx context.Context, i int, step runbook.Step, tmpl string) error {
	res := &x.run.Steps[i]
	text, err := x.render(tmpl)
	if err != nil {
		return x.fail(i, fmt.Errorf("manual: %w", err))
	}
	res.Command = x.mask(text)
	if x.opts.DryRun {
		res.Status, res.Note = StatusSkipped, "dry run"
		x.stepDone(i)
		return nil
	}
	outcome, note, err := x.ui.Manual(ctx, i, step, x.mask(text))
	if err != nil {
		return x.fail(i, err)
	}
	res.Note = note
	switch outcome {
	case ManualDone:
		res.Status = StatusSucceeded
	case ManualSkip:
		res.Status = StatusSkipped
	default:
		res.Status = StatusFailed
		if step.ContinueOnError {
			break
		}
		x.stepDone(i)
		return fmt.Errorf("manual step %d (%s) failed%s", i+1, step.Name, suffix(note))
	}
	x.stepDone(i)
	return nil
}

func suffix(note string) string {
	if note == "" {
		return ""
	}
	return ": " + note
}

func (x *executor) fail(i int, err error) error {
	res := &x.run.Steps[i]
	res.Status = StatusFailed
	if x.run.Steps[i].Error == "" {
		res.Error = x.mask(err.Error())
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errAborted) {
		res.Status = StatusAborted
	}
	x.stepDone(i)
	return err
}

func (x *executor) stepDone(i int) {
	x.run.Steps[i].Ended = time.Now()
	x.run.State = x.State()
	x.notify(Event{Kind: EventStepFinished, Step: i, Result: x.result(i), State: x.run.State})
	x.save()
}

func (x *executor) result(i int) *StepResult {
	r := x.run.Steps[i]
	return &r
}

func (x *executor) snapshot() *Run {
	r := *x.run
	r.Steps = slices.Clone(x.run.Steps)
	r.State = maps.Clone(x.run.State)
	return &r
}

func (x *executor) save() {
	if x.opts.Save != nil {
		x.opts.Save(x.snapshot())
	}
}

func (x *executor) notify(e Event) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.ui.Notify(e)
}

func (x *executor) mask(s string) string {
	for _, sec := range x.secrets {
		s = strings.ReplaceAll(s, sec, "****")
	}
	return s
}

func (x *executor) addSecret(v string) {
	// Very short values would mask unrelated text.
	if len(v) >= 4 && !slices.Contains(x.secrets, v) {
		x.secrets = append(x.secrets, v)
	}
}

// env layers: process env, runnr vars (inputs and outputs), runbook env, step env.
func (x *executor) env(step runbook.Step) ([]string, error) {
	extra, err := x.runnrEnv(step)
	return append(os.Environ(), extra...), err
}

// runnrEnv is what runnr adds to the process environment for step.
func (x *executor) runnrEnv(step runbook.Step) ([]string, error) {
	var env []string
	for k, v := range x.vars {
		env = append(env, k+"="+v)
	}
	env = append(env, "RUNNR_RUN_ID="+x.run.ID, "RUNNR_RUNBOOK="+x.rb.Name)
	for _, m := range []map[string]string{x.rb.Env, step.Env} {
		for k, v := range m {
			r, err := x.render(v)
			if err != nil {
				return nil, fmt.Errorf("env %s: %w", k, err)
			}
			env = append(env, k+"="+r)
		}
	}
	return env, nil
}

// ErrAborted lets interactors signal that the user aborted the run.
var ErrAborted = errAborted

// ResumePoint returns the first step of a previous run that neither
// succeeded nor was skipped, or -1 if there's nothing left to do.
func ResumePoint(r *Run) int {
	for i, s := range r.Steps {
		if s.Status != StatusSucceeded && s.Status != StatusSkipped && s.Status != StatusForced {
			return i
		}
	}
	return -1
}

// ResumeInputs returns a previous run's inputs, minus masked secrets
// (which must be supplied again).
func ResumeInputs(r *Run) map[string]string {
	vars := map[string]string{}
	for k, v := range r.Inputs {
		if v != "****" {
			vars[k] = v
		}
	}
	return vars
}

var varRefRe = regexp.MustCompile(`\.([A-Za-z_][A-Za-z0-9_]*)`)

// render expands a template. In a dry run, variables that earlier steps
// would have produced don't exist yet, so they render as <name>.
func (x *executor) render(text string) (string, error) {
	if x.opts.DryRun {
		return x.lenient(text), nil
	}
	return runbook.Render(text, x.vars)
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// ResumeState returns a previous run's state for Options.State.
func ResumeState(r *Run) map[string]string { return maps.Clone(r.State) }
