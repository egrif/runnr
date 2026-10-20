package runtest

import (
	"context"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/runbook"
)

// operator answers a run's prompts as a scenario says, and records what it
// was asked.
type operator struct {
	def   Answers
	steps map[int]Answers // by step index
	in    map[string]string

	mu     sync.Mutex
	used   map[string]int // answers given per step and kind
	asked  []string       // what it was asked, for the report
	output []string
}

func newOperator(o Operator, steps map[int]Answers) *operator {
	return &operator{def: o.Answers, steps: steps, in: o.Inputs, used: map[string]int{}}
}

// answer is the next word for this step and kind of prompt, or def.
func (o *operator) answer(idx int, kind, def string) string {
	pick := func(a Answers) Words {
		return map[string]Words{"confirm": a.Confirm, "danger": a.Danger, "typed": a.Typed, "manual": a.Manual, "failure": a.Failure,
			"before": a.Before, "after": a.After, "way": a.Way}[kind]
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	ws := pick(o.steps[idx])
	if len(ws) == 0 {
		ws = pick(o.def)
	}
	w := def
	if len(ws) > 0 {
		key := kind + "@" + strconv.Itoa(idx)
		w = ws[min(o.used[key], len(ws)-1)]
		o.used[key]++
	}
	o.asked = append(o.asked, kind+" at step "+strconv.Itoa(idx+1)+": "+w)
	return w
}

func (o *operator) Notify(e engine.Event) {
	if e.Kind == engine.EventOutput {
		o.mu.Lock()
		o.output = append(o.output, e.Text)
		o.mu.Unlock()
	}
}

func (o *operator) AskInput(_ context.Context, in runbook.Input) (string, error) {
	if v, ok := o.in[in.Name]; ok {
		return v, nil
	}
	return in.Default, nil
}

func (o *operator) Confirm(_ context.Context, idx int, _ runbook.Step, _ string) (bool, error) {
	return o.answer(idx, "confirm", "yes") == "yes", nil
}

func (o *operator) ConfirmDanger(_ context.Context, idx int, _ runbook.Step, _, _ string) (bool, error) {
	return o.answer(idx, "danger", "type") == "type", nil
}

func (o *operator) ConfirmTyped(_ context.Context, idx int, _ runbook.Step, _, _ string) (bool, error) {
	return o.answer(idx, "typed", "type") == "type", nil
}

func (o *operator) Manual(_ context.Context, idx int, _ runbook.Step, _ string) (engine.ManualOutcome, string, error) {
	switch o.answer(idx, "manual", "done") {
	case "skip":
		return engine.ManualSkip, "skipped by the test", nil
	case "fail":
		return engine.ManualFail, "failed by the test", nil
	}
	return engine.ManualDone, "", nil
}

func (o *operator) OnFailure(_ context.Context, idx int, _ runbook.Step, _ engine.StepResult) (engine.FailureAction, string, error) {
	switch o.answer(idx, "failure", "abort") {
	case "retry":
		return engine.FailRetry, "", nil
	case "skip":
		return engine.FailSkip, "", nil
	case "way":
		return engine.FailOtherWay, "", nil
	case "force":
		return engine.FailForce, "forced by the test", nil
	}
	return engine.FailAbort, "", nil
}

func (o *operator) BeforeStep(_ context.Context, v engine.StepView) (engine.StepDecision, error) {
	switch o.answer(v.Index, "before", "run") {
	case "skip":
		return engine.StepSkip, nil
	case "abort":
		return engine.StepAbort, nil
	}
	return engine.StepRun, nil
}

func (o *operator) AfterStep(_ context.Context, v engine.StepView, _ engine.StepResult) (engine.AfterDecision, string, error) {
	switch o.answer(v.Index, "after", "continue") {
	case "retry":
		return engine.AfterRetry, "", nil
	case "fail":
		return engine.AfterFail, "failed by the test", nil
	case "abort":
		return engine.AfterAbort, "", nil
	}
	return engine.AfterContinue, "", nil
}

// ChooseWay picks the way named for the step, else the preferred one (or,
// after a way failed, the first other one).
func (o *operator) ChooseWay(_ context.Context, v engine.StepView, failed int) (int, error) {
	if name := o.answer(v.Index, "way", ""); name != "" {
		for i, w := range v.Ways {
			if strings.EqualFold(w.Name, name) {
				return i, nil
			}
		}
	}
	for i, w := range v.Ways {
		if w.Preferred && i != failed {
			return i, nil
		}
	}
	for i := range v.Ways {
		if i != failed {
			return i, nil
		}
	}
	return 0, nil
}

// StartTerm gives tty steps a terminal nobody types into.
func (o *operator) StartTerm(int, string) (engine.TermConn, error) {
	return &quietTerm{closed: make(chan struct{})}, nil
}

type quietTerm struct {
	once   sync.Once
	closed chan struct{}
}

func (t *quietTerm) Read([]byte) (int, error)    { <-t.closed; return 0, io.EOF }
func (t *quietTerm) Write(p []byte) (int, error) { return len(p), nil }
func (t *quietTerm) Close() error                { t.once.Do(func() { close(t.closed) }); return nil }
func (t *quietTerm) Size() (uint16, uint16)      { return 40, 120 }
func (t *quietTerm) Resized() <-chan struct{}    { return nil }
