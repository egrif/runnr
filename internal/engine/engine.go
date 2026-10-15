// Package engine executes runbooks. All human decisions go through an
// Interactor, so the same engine drives the CLI, the GUI and unattended runs.
package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
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
)

type StepResult struct {
	Index    int       `json:"index"`
	Name     string    `json:"name"`
	Kind     string    `json:"kind"`
	Status   Status    `json:"status"`
	Command  string    `json:"command,omitempty"`
	ExitCode *int      `json:"exit_code,omitempty"`
	Output   string    `json:"output,omitempty"`
	Error    string    `json:"error,omitempty"`
	Note     string    `json:"note,omitempty"`
	Attempts int       `json:"attempts,omitempty"`
	Started  time.Time `json:"started,omitzero"`
	Ended    time.Time `json:"ended,omitzero"`
}

type Run struct {
	ID      string            `json:"id"`
	Runbook string            `json:"runbook"`
	Mode    string            `json:"mode"`
	DryRun  bool              `json:"dry_run,omitempty"`
	Status  Status            `json:"status"`
	Error   string            `json:"error,omitempty"`
	Inputs  map[string]string `json:"inputs"`
	Started time.Time         `json:"started"`
	Ended   time.Time         `json:"ended,omitzero"`
	Steps   []StepResult      `json:"steps"`
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
)

// Interactor supplies human decisions and receives progress events.
// Notify may be called from multiple goroutines, but never concurrently.
type Interactor interface {
	Notify(Event)
	AskInput(ctx context.Context, in runbook.Input) (string, error)
	Confirm(ctx context.Context, idx int, step runbook.Step, command string) (bool, error)
	Manual(ctx context.Context, idx int, step runbook.Step, instructions string) (ManualOutcome, string, error)
	OnFailure(ctx context.Context, idx int, step runbook.Step, res StepResult) (FailureAction, error)
}

type Options struct {
	Vars   map[string]string
	DryRun bool
	// Interactive controls whether missing inputs are asked for.
	Interactive bool
	// Save is called with a snapshot after every state change worth keeping.
	Save func(*Run)
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
	x.run = &Run{ID: id, Runbook: rb.Name, Mode: mode, DryRun: opts.DryRun, Status: StatusRunning, Started: time.Now(), Inputs: map[string]string{}}
	for i, s := range rb.Steps {
		kind := "run"
		if s.IsManual() {
			kind = "manual"
		}
		x.run.Steps = append(x.run.Steps, StepResult{Index: i, Name: s.Name, Kind: kind, Status: StatusPending})
	}

	x.save()
	err := x.resolveInputs(ctx)
	if err == nil {
		x.notify(Event{Kind: EventRunStarted, Run: x.snapshot()})
		x.save()
		err = x.steps(ctx)
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
		if x.run.Steps[i].Status == StatusPending || x.run.Steps[i].Status == StatusRunning {
			x.run.Steps[i].Status = StatusSkipped
		}
	}
	x.save()
	x.notify(Event{Kind: EventRunFinished, Run: x.snapshot()})
}

var errAborted = errors.New("aborted")

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
		if err := x.step(ctx, i, step); err != nil {
			return err
		}
	}
	return nil
}

func (x *executor) step(ctx context.Context, i int, step runbook.Step) error {
	res := &x.run.Steps[i]
	if step.When != "" {
		cond, err := runbook.Render(step.When, x.vars)
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
		return x.manual(ctx, i, step)
	}

	cmd, err := runbook.Render(step.Run, x.vars)
	if err != nil {
		return x.fail(i, fmt.Errorf("run: %w", err))
	}
	res.Command = x.mask(cmd)

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
			if out.err == nil || ctx.Err() != nil {
				break
			}
		}
		res.Output = x.mask(out.combined)
		res.ExitCode = out.exitCode
		if out.err == nil {
			if step.Capture != "" {
				x.vars[step.Capture] = strings.TrimSpace(out.stdout)
			}
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
		action, err := x.ui.OnFailure(ctx, i, step, *x.result(i))
		if err != nil {
			return x.fail(i, err)
		}
		switch action {
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

func (x *executor) manual(ctx context.Context, i int, step runbook.Step) error {
	res := &x.run.Steps[i]
	text, err := runbook.Render(step.Manual, x.vars)
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

func (x *executor) env(step runbook.Step) ([]string, error) {
	env := append(os.Environ(), "RUNNR_RUN_ID="+x.run.ID, "RUNNR_RUNBOOK="+x.rb.Name)
	for _, m := range []map[string]string{x.rb.Env, step.Env} {
		for k, v := range m {
			r, err := runbook.Render(v, x.vars)
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
