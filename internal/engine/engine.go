// Package engine executes runbooks. All human decisions go through an
// Interactor, so the same engine drives the CLI, the GUI and unattended runs.
package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
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
	Index    int       `json:"index"`
	Name     string    `json:"name"`
	Section  string    `json:"section,omitempty"`
	Owner    string    `json:"owner,omitempty"`
	Kind     string    `json:"kind"`
	Status   Status    `json:"status"`
	Command  string    `json:"command,omitempty"`
	ExitCode *int      `json:"exit_code,omitempty"`
	Output   string    `json:"output,omitempty"`
	Error    string    `json:"error,omitempty"`
	Note     string    `json:"note,omitempty"`
	Attempts int       `json:"attempts,omitempty"`
	Notes    []Note    `json:"notes,omitempty"`
	Started  time.Time `json:"started,omitzero"`
	Ended    time.Time `json:"ended,omitzero"`
}

type Run struct {
	ID             string            `json:"id"`
	Runbook        string            `json:"runbook"`
	Mode           string            `json:"mode"`
	DryRun         bool              `json:"dry_run,omitempty"`
	StartAt        int               `json:"start_at,omitempty"`
	RunbookVersion int               `json:"runbook_version,omitempty"`
	Resumes        string            `json:"resumes,omitempty"`
	Status         Status            `json:"status"`
	Error          string            `json:"error,omitempty"`
	Inputs         map[string]string `json:"inputs"`
	Started        time.Time         `json:"started"`
	Ended          time.Time         `json:"ended,omitzero"`
	Steps          []StepResult      `json:"steps"`
	Notes          []Note            `json:"notes,omitempty"`
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
	// Resumes records the run ID this run continues, if any.
	Resumes string
	// RunbookVersion records which saved version of the runbook ran.
	RunbookVersion int
	// Save is called with a snapshot after every state change worth keeping.
	Save func(*Run)
	// ShellOnly runs only the steps marked always (the setup), then opens an
	// interactive shell with the resulting environment. Needs a ShellHost.
	ShellOnly bool
}

// Attacher is implemented by interactors that own a real terminal. Steps
// marked tty run attached to it; without one they become manual steps.
type Attacher interface {
	AttachedIO() (stdin io.Reader, stdout, stderr io.Writer)
}

// ShellHost is an Attacher that can open an interactive shell mid-run. The
// engine gives it a function that opens one with the run's environment
// (inputs, published values, env) for the given step (-1 for none).
// Values written to $RUNNR_OUTPUT/$RUNNR_SECRET in that shell flow back
// into the run.
type ShellHost interface {
	Attacher
	SetShell(open func(ctx context.Context, step int) error)
	// OpenShell opens the shell (via the function given to SetShell),
	// wrapping it in whatever the host needs, e.g. signal handling.
	OpenShell(ctx context.Context, step int) error
}

const maxOutput = 64 << 10

func NewRunID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

type executor struct {
	rb      *runbook.Runbook
	ui      Interactor
	opts    Options
	run     *Run
	vars    map[string]string
	secrets []string
	mu      sync.Mutex
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
	x := &executor{rb: rb, ui: ui, opts: opts, vars: map[string]string{}}
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

	if h, ok := ui.(ShellHost); ok {
		h.SetShell(x.openShell)
	}
	if opts.ShellOnly {
		x.opts.StartAt = len(rb.Steps)
	}
	x.save()
	err := x.resolveInputs(ctx)
	if err == nil {
		x.notify(Event{Kind: EventRunStarted, Run: x.snapshot()})
		x.save()
		err = x.steps(ctx)
		if err == nil && opts.ShellOnly {
			if h, ok := ui.(ShellHost); ok {
				err = h.OpenShell(ctx, -1)
			} else {
				err = errors.New("no terminal to open a shell in")
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
		if i < x.opts.StartAt && !step.Always {
			x.run.Steps[i].Status, x.run.Steps[i].Note = StatusSkipped, NoteBeforeStart
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

	if step.IsManual() {
		return x.manual(ctx, i, step, step.Manual)
	}

	cmd, err := x.render(step.Run)
	if err != nil {
		return x.fail(i, fmt.Errorf("run: %w", err))
	}
	res.Command = x.mask(cmd)

	if _, ok := x.ui.(Attacher); step.TTY && !ok {
		// No terminal here: hand the command to a human instead.
		return x.manual(ctx, i, step, "Run in a terminal (needs interactive input):\n\n"+step.Run)
	}

	if step.Confirm && !x.opts.DryRun {
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
			x.stepDone(i)
			return nil
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
			continue
		case FailSkip:
			res.Status, res.Note = StatusSkipped, "skipped after failure"
			x.stepDone(i)
			return nil
		default:
			return x.fail(i, fmt.Errorf("step %d (%s) failed: %s", i+1, step.Name, res.Error))
		}
	}
}

// apply records what a successful (or forced) step produced.
func (x *executor) apply(step runbook.Step, out cmdResult) {
	if step.Capture != "" {
		x.vars[step.Capture] = strings.TrimSpace(out.stdout)
	}
	for k, v := range out.outputs {
		x.vars[k] = v
	}
	for k, v := range out.secrets {
		x.vars[k] = v
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
	x.notify(Event{Kind: EventStepFinished, Step: i, Result: x.result(i)})
	x.save()
}

func (x *executor) result(i int) *StepResult {
	r := x.run.Steps[i]
	return &r
}

func (x *executor) snapshot() *Run {
	r := *x.run
	r.Steps = slices.Clone(x.run.Steps)
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
	env := os.Environ()
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
	if !x.opts.DryRun {
		return runbook.Render(text, x.vars)
	}
	vars := maps.Clone(x.vars)
	for _, m := range varRefRe.FindAllStringSubmatch(text, -1) {
		if _, ok := vars[m[1]]; !ok {
			vars[m[1]] = "<" + m[1] + ">"
		}
	}
	return runbook.Render(text, vars)
}

// openShell runs an interactive shell attached to the terminal, with the
// environment step idx would get (idx -1: no step-specific env or dir).
func (x *executor) openShell(ctx context.Context, idx int) error {
	a, ok := x.ui.(Attacher)
	if !ok {
		return errors.New("no terminal to open a shell in")
	}
	var step runbook.Step
	if idx >= 0 && idx < len(x.rb.Steps) {
		step = x.rb.Steps[idx]
	}
	env, err := x.env(step)
	if err != nil {
		return err
	}
	dir, err := x.render(step.Dir)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "runnr-shell-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	outFile, secFile := filepath.Join(tmp, "output"), filepath.Join(tmp, "secret")
	env = append(env, "RUNNR_OUTPUT="+outFile, "RUNNR_SECRET="+secFile, "RUNNR_SHELL=1")

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = nonEmpty(x.rb.Shell, "sh")
	}
	cmd := exec.CommandContext(ctx, shell)
	cmd.Env, cmd.Dir = env, dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = a.AttachedIO()
	err = cmd.Run()
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) { // a shell's exit status is just its last command's
		return err
	}

	var res cmdResult
	x.readOutputs(idx, &res, outFile, secFile)
	x.apply(runbook.Step{}, res)
	var names []string
	for k := range res.outputs {
		names = append(names, k)
	}
	for k := range res.secrets {
		names = append(names, k+" (secret)")
	}
	if len(names) > 0 && idx >= 0 {
		slices.Sort(names)
		x.notify(Event{Kind: EventOutput, Step: idx, Stream: "info", Text: "shell published: " + strings.Join(names, ", ")})
	}
	return nil
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
