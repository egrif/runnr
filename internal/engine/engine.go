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
	// StatusBackground: a background step that has started and is still running.
	StatusBackground Status = "background"
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
	Shells []string `json:"shells,omitempty"`
	// Checks holds the latest attempt of a group of checks.
	Checks []CheckResult `json:"checks,omitempty"`
	// Way is the way the step was done, when it has several.
	Way string `json:"way,omitempty"`
	// Alternative and Sub are from runs before ways replaced alternatives.
	Alternative string       `json:"alternative,omitempty"`
	Sub         []StepResult `json:"sub,omitempty"`
	// Edited: the step was changed during the run.
	Edited  bool      `json:"edited,omitempty"`
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
	// Window is the maintenance window, if the runbook has one.
	Window *WindowRecord `json:"window,omitempty"`
	// Edits are changes made to steps during the run; EditsSaved is the
	// runbook version they were saved as (0: not saved yet).
	Edits      []StepEdit `json:"edits,omitempty"`
	EditsSaved int        `json:"edits_saved,omitempty"`
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
	// as of the last finished step, with credentials masked.
	State map[string]string `json:"state,omitempty"`
	// Secrets names the secret variables in State and Inputs; their
	// values can't be restored from the record.
	Secrets []string `json:"secrets,omitempty"`
}

type EventKind string

const (
	EventRunStarted   EventKind = "run_started"
	EventStepStarted  EventKind = "step_started"
	EventOutput       EventKind = "output"
	EventStepFinished EventKind = "step_finished"
	EventRunFinished  EventKind = "run_finished"
	// EventStepProgress: a step's result changed without it finishing
	// (checks attempted, a sub-step done).
	EventStepProgress EventKind = "step_progress"
	// EventWindow: the maintenance window opened or closed (Run is set).
	EventWindow EventKind = "window"
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
	// FailOtherWay tries the step another way (steps with several ways).
	FailOtherWay FailureAction = "way"
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
	// Previous is an earlier run to compare against ("last time").
	Previous *Run
	// Libraries are the function libraries the runbook uses, resolved.
	Libraries []*runbook.Library
	// State seeds variables from an earlier run (resume). Inputs win, and
	// masked secrets are not restored.
	State map[string]string
	// Resumes records the run ID this run continues, if any.
	Resumes string
	// RunbookVersion records which saved version of the runbook ran.
	RunbookVersion int
	// Save is called with a snapshot after every state change worth keeping.
	Save func(*Run)
	// Sanctioned: a recorded run of a saved runbook. Dangerous steps run
	// without a double check only in a sanctioned run, in sequence.
	Sanctioned bool
	// Resumed is the run this one continues; steps that finished there
	// count as in sequence here.
	Resumed *Run
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
	Description   string   `json:"description,omitempty"`
	Expect        string   `json:"expect,omitempty"`
	Dir           string   `json:"dir,omitempty"`
	When          string   `json:"when,omitempty"`
	Confirm       bool     `json:"confirm,omitempty"`
	Dangerous     bool     `json:"dangerous,omitempty"`
	Always        bool     `json:"always,omitempty"`
	Timeout       string   `json:"timeout,omitempty"`
	Retries       int      `json:"retries,omitempty"`
	Outputs       []string `json:"outputs,omitempty"`
	SecretOutputs []string `json:"secret_outputs,omitempty"`
	Background    bool     `json:"background,omitempty"`
	StopAfter     string   `json:"stop_after,omitempty"`
	// Checks are filled in for checks steps; Ways for steps with several.
	Checks []CheckView `json:"checks,omitempty"`
	Repeat string      `json:"repeat,omitempty"`
	Ways   []WayView   `json:"ways,omitempty"`
	Choose bool        `json:"choose,omitempty"`
	// Window is open or close; Offset is the planned start relative to
	// the window opening ("-30m", "+12m"), OffsetSec the same in seconds.
	Window    string `json:"window,omitempty"`
	Offset    string `json:"offset,omitempty"`
	OffsetSec *int   `json:"offset_sec,omitempty"`
}

// WindowRecord tracks the maintenance window (production down) in a run.
type WindowRecord struct {
	Target    string    `json:"target,omitempty"`
	OpenStep  int       `json:"open_step"`  // -1: none marked
	CloseStep int       `json:"close_step"` // -1: none marked
	Opened    time.Time `json:"opened,omitzero"`
	Closed    time.Time `json:"closed,omitzero"`
}

// WayView is one way to do a step, as it would run.
type WayView struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Command   string `json:"command"`
	Preferred bool   `json:"preferred,omitempty"`
}

// CheckResult is one check's outcome in the latest attempt.
type CheckResult struct {
	Name     string `json:"name"`
	Passed   bool   `json:"passed"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Output   string `json:"output,omitempty"`
	Error    string `json:"error,omitempty"`
}

type CheckView struct {
	Name    string `json:"name"`
	Command string `json:"command"`
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
	// Functions lists the shell functions steps and shells can call.
	Functions() []FuncInfo
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

// Chooser picks which way to do a step with several (v.Ways), for steps
// set to choose and after a way fails (failed is its index, else -1).
// Without one, or unattended, the preferred way is used.
type Chooser interface {
	ChooseWay(ctx context.Context, v StepView, failed int) (int, error)
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
	secrets    []secretValue
	masker     *runbook.Masker
	secretVars map[string]bool
	ctl        *Controls
	// background sessions still running, by step index.
	background map[int]*bgStep
	// funcFile holds the run's shell functions (libraries and the runbook's).
	funcFile  string
	functions []FuncInfo
	mu        sync.Mutex
	// em guards the step definitions (editable during the run), the
	// current step and the edits.
	em         sync.Mutex
	cur        int
	edits      []StepEdit
	editsSaved int
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
	// A private copy: steps can be edited during the run.
	own := *rb
	own.Steps = slices.Clone(rb.Steps)
	rb = &own
	x := &executor{rb: rb, ui: ui, opts: opts, cur: -1, vars: map[string]string{}, secretVars: map[string]bool{}, masker: runbook.NewMasker(rb)}
	x.run = &Run{ID: id, Runbook: rb.Name, Mode: mode, DryRun: opts.DryRun, StartAt: opts.StartAt, Resumes: opts.Resumes, RunbookVersion: opts.RunbookVersion, Status: StatusRunning, Started: time.Now(), Inputs: map[string]string{}}
	for i, s := range rb.Steps {
		x.run.Steps = append(x.run.Steps, StepResult{Index: i, Name: s.Name, Section: s.Section, Owner: s.Owner, Kind: stepKind(s), Status: StatusPending})
	}

	if opts.Previous != nil {
		x.run.Previous = opts.Previous.ID
	}
	if mw := rb.MaintenanceWindow; mw != nil {
		o, c := rb.WindowSteps()
		x.run.Window = &WindowRecord{Target: mw.Target, OpenStep: o, CloseStep: c}
		// Resuming while production is down: the window is still open.
		if p := opts.Resumed; p != nil && p.Window != nil && !p.Window.Opened.IsZero() && p.Window.Closed.IsZero() {
			x.run.Window.Opened = p.Window.Opened
		}
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
	x.carryResumed()
	x.save()
	err := x.setupFunctions()
	if err == nil {
		err = x.resolveInputs(ctx)
	}
	if err == nil {
		// Inputs can override the window's target and planned offsets.
		err = rb.CheckWindowInputs(x.vars)
		if x.run.Window != nil {
			x.run.Window.Target = rb.WindowTarget(x.vars)
		}
	}
	if err == nil {
		x.restoreState()
		x.refreshState()
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
	return x.snapshot()
}

func (x *executor) finish(ctx context.Context, err error) {
	x.em.Lock()
	x.cur = len(x.rb.Steps) // no more edits
	x.em.Unlock()
	x.tendBackground(-1, true)
	if x.funcFile != "" {
		os.Remove(x.funcFile)
	}
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

// errOtherWay: the operator wants to try a failed step another way.
var errOtherWay = errors.New("other way")

func targetNote(w *WindowRecord) string {
	if w.Target == "" {
		return ""
	}
	return " (target " + w.Target + ")"
}

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
			x.addSecret(v, true)
			x.run.Inputs[in.Name] = x.maskedValue(v)
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
	for i := range x.rb.Steps {
		x.em.Lock()
		x.cur = i
		x.em.Unlock()
		step := x.def(i)
		if err := ctx.Err(); err != nil {
			return err
		}
		if skip, _, _ := x.ctl.Get(i); skip {
			if x.run.Steps[i].Note == NoteCarried {
				x.stepDone(i)
				x.tendBackground(i, false)
				continue
			}
			note := NoteDeselected
			if i < x.opts.StartAt && !step.Always {
				note = NoteBeforeStart
			}
			x.run.Steps[i].Status, x.run.Steps[i].Note = StatusSkipped, note
			x.stepDone(i)
			x.tendBackground(i, false)
			continue
		}
		if err := x.step(ctx, i, step); err != nil {
			return err
		}
		x.tendBackground(i, false)
	}
	return nil
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
	case FailOtherWay:
		return errOtherWay
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
		x.addSecret(v, false)
	}
}

func (x *executor) fail(i int, err error) error {
	if errors.Is(err, ErrStepEdited) {
		return err // step() starts it again
	}
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
	if w := x.run.Window; w != nil && i == w.CloseStep && !w.Opened.IsZero() && w.Closed.IsZero() &&
		slices.Contains([]Status{StatusSucceeded, StatusForced, StatusSkipped}, x.run.Steps[i].Status) {
		w.Closed = x.run.Steps[i].Ended
		x.notify(Event{Kind: EventOutput, Step: i, Stream: "info", Text: fmt.Sprintf("🟢 maintenance window closed after %s%s", w.Closed.Sub(w.Opened).Round(time.Second), targetNote(w))})
		x.notify(Event{Kind: EventWindow, Step: i, Run: x.snapshot()})
	}
	x.refreshState()
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
	x.withEdits(&r)
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

// secretValue is a known secret and how it's shown.
type secretValue struct{ value, shown string }

// mask hides credentials: known secret values (only their sensitive part,
// when a sensitive pattern recognises them), then anything matching a
// sensitive pattern.
func (x *executor) mask(s string) string {
	for _, sec := range x.secrets {
		s = strings.ReplaceAll(s, sec.value, sec.shown)
	}
	if x.masker != nil {
		s = x.masker.Mask(s)
	}
	return s
}

// maskedValue is how a secret value is shown: with just its credential
// part hidden if a sensitive pattern finds one, else entirely.
func (x *executor) maskedValue(v string) string {
	if x.masker != nil {
		if m := x.masker.Mask(v); m != v {
			return m
		}
	}
	return "****"
}

// addSecret starts hiding v. Very short published values would mask
// unrelated text, so they're only hidden when force is set (secret inputs).
func (x *executor) addSecret(v string, force bool) {
	if v == "" || (len(v) < 4 && !force) || slices.ContainsFunc(x.secrets, func(s secretValue) bool { return s.value == v }) {
		return
	}
	x.secrets = append(x.secrets, secretValue{v, x.maskedValue(v)})
	// Longest first, so a URL is replaced before the password inside it.
	slices.SortFunc(x.secrets, func(a, b secretValue) int { return len(b.value) - len(a.value) })
}

// refreshState records the run's current variables (masked) and which are secret.
func (x *executor) refreshState() {
	x.run.State = x.State()
	x.run.Secrets = x.run.Secrets[:0]
	for k := range x.secretVars {
		x.run.Secrets = append(x.run.Secrets, k)
	}
	slices.Sort(x.run.Secrets)
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
	if x.funcFile != "" {
		env = append(env, "RUNNR_FUNCTIONS="+x.funcFile)
	}
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
		if !slices.Contains([]Status{StatusSucceeded, StatusSkipped, StatusForced, StatusBackground}, s.Status) {
			return i
		}
	}
	return -1
}

// ResumeInputs returns a previous run's inputs, minus secrets (which must
// be supplied again).
func ResumeInputs(r *Run) map[string]string {
	vars := map[string]string{}
	for k, v := range r.Inputs {
		if v != "****" && !slices.Contains(r.Secrets, k) {
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

// ResumeState returns a previous run's state for Options.State, with
// secrets (which the record doesn't hold) as "****".
func ResumeState(r *Run) map[string]string {
	st := maps.Clone(r.State)
	for _, k := range r.Secrets {
		if _, ok := st[k]; ok {
			st[k] = "****"
		}
	}
	return st
}
