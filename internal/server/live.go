package server

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/runbook"
)

// Question is a decision the engine is waiting on from a browser.
type Question struct {
	ID      string         `json:"id"`
	Type    string         `json:"type"` // input | confirm | manual | failure | before
	Step    int            `json:"step"`
	Title   string         `json:"title"`
	Text    string         `json:"text,omitempty"`
	Options []string       `json:"options,omitempty"`
	Expect  string         `json:"expect,omitempty"`
	Input   *runbook.Input `json:"input,omitempty"`
}

type Answer struct {
	QuestionID string `json:"question_id"`
	Value      string `json:"value"`
	Note       string `json:"note,omitempty"`
}

// WireEvent is what the browser receives over SSE.
type WireEvent struct {
	Seq int `json:"seq"`
	engine.Event
	Question *Question    `json:"question,omitempty"`
	Answered string       `json:"answered,omitempty"`
	Note     *engine.Note `json:"note,omitempty"`
}

// liveRun is an in-progress (or recently finished) run driven by the GUI.
// It implements engine.Interactor.
type liveRun struct {
	id     string
	cancel context.CancelFunc
	// windows: this server runs on the operator's machine, so terminal
	// steps and shells can open Terminal windows.
	windows bool
	session engine.Session

	mu      sync.Mutex
	events  []WireEvent
	subs    map[chan WireEvent]struct{}
	pending map[string]chan Answer
	done    bool
	qseq    int
}

func newLiveRun(id string, cancel context.CancelFunc) *liveRun {
	return &liveRun{id: id, cancel: cancel, subs: map[chan WireEvent]struct{}{}, pending: map[string]chan Answer{}}
}

func (l *liveRun) publish(e WireEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e.Seq = len(l.events)
	l.events = append(l.events, e)
	for ch := range l.subs {
		select {
		case ch <- e:
		default: // slow subscriber; it can resync by reconnecting
		}
	}
	if e.Kind == engine.EventRunFinished {
		l.done = true
		for ch := range l.subs {
			close(ch)
		}
		l.subs = map[chan WireEvent]struct{}{}
	}
}

// subscribe returns the backlog and a channel of subsequent events.
// The channel is nil if the run already finished.
func (l *liveRun) subscribe() ([]WireEvent, chan WireEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	backlog := append([]WireEvent(nil), l.events...)
	if l.done {
		return backlog, nil
	}
	ch := make(chan WireEvent, 1024)
	l.subs[ch] = struct{}{}
	return backlog, ch
}

func (l *liveRun) unsubscribe(ch chan WireEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.subs[ch]; ok {
		delete(l.subs, ch)
		close(ch)
	}
}

func (l *liveRun) answer(a Answer) error {
	l.mu.Lock()
	ch, ok := l.pending[a.QuestionID]
	delete(l.pending, a.QuestionID)
	l.mu.Unlock()
	if !ok {
		return fmt.Errorf("no pending question %q", a.QuestionID)
	}
	ch <- a
	return nil
}

func (l *liveRun) ask(ctx context.Context, q Question) (Answer, error) {
	ch := make(chan Answer, 1)
	l.mu.Lock()
	l.qseq++
	q.ID = fmt.Sprintf("q%d", l.qseq)
	l.pending[q.ID] = ch
	l.mu.Unlock()
	l.publish(WireEvent{Event: engine.Event{Kind: "question", Step: q.Step}, Question: &q})
	select {
	case a := <-ch:
		l.publish(WireEvent{Event: engine.Event{Kind: "answered", Step: q.Step, Text: a.Value}, Answered: q.ID})
		if a.Value == "abort" && q.Type != "input" {
			return a, engine.ErrAborted
		}
		return a, nil
	case <-ctx.Done():
		l.mu.Lock()
		delete(l.pending, q.ID)
		l.mu.Unlock()
		return Answer{}, ctx.Err()
	}
}

func (l *liveRun) Notify(e engine.Event) { l.publish(WireEvent{Event: e}) }

func (l *liveRun) LaunchWindows() bool    { return l.windows }
func (l *liveRun) Bind(se engine.Session) { l.session = se }
func (l *liveRun) canShell() bool         { return l.session != nil && l.session.CanShell() }

// askStep asks q, handling "shell" answers (open a shell window, then ask
// again) itself. Other answers are returned.
func (l *liveRun) askStep(ctx context.Context, q Question) (Answer, error) {
	if l.canShell() {
		q.Options = append(q.Options, "shell")
	}
	for {
		a, err := l.ask(ctx, q)
		if err != nil || a.Value != "shell" {
			return a, err
		}
		if err := l.session.Shell(ctx, q.Step); err != nil {
			l.Notify(engine.Event{Kind: engine.EventOutput, Step: q.Step, Stream: "info", Text: "shell: " + err.Error()})
		}
	}
}

// BeforeStep implements engine.Stepper for step mode.
func (l *liveRun) BeforeStep(ctx context.Context, v engine.StepView) (engine.StepDecision, error) {
	opts := []string{"run"}
	if v.Kind != "manual" && l.canShell() {
		opts = append(opts, "manual")
	}
	opts = append(opts, "skip", "continue", "abort")
	a, err := l.askStep(ctx, Question{Type: "before", Step: v.Index, Title: "Next: " + v.Name, Text: v.Command, Expect: v.Expect, Options: opts})
	if err != nil {
		return "", err
	}
	return engine.StepDecision(a.Value), nil
}

func (l *liveRun) AskInput(ctx context.Context, in runbook.Input) (string, error) {
	a, err := l.ask(ctx, Question{Type: "input", Step: -1, Title: "Input: " + in.Name, Text: in.Description, Input: &in})
	return a.Value, err
}

func (l *liveRun) Confirm(ctx context.Context, idx int, step runbook.Step, command string) (bool, error) {
	a, err := l.askStep(ctx, Question{Type: "confirm", Step: idx, Title: "Run “" + step.Name + "”?", Text: command, Options: []string{"run", "skip", "abort"}})
	return a.Value == "run", err
}

func (l *liveRun) Manual(ctx context.Context, idx int, step runbook.Step, text string) (engine.ManualOutcome, string, error) {
	a, err := l.askStep(ctx, Question{Type: "manual", Step: idx, Title: step.Name, Text: text, Options: []string{"done", "skip", "fail", "abort"}})
	if err != nil {
		return "", "", err
	}
	switch a.Value {
	case "done":
		return engine.ManualDone, a.Note, nil
	case "skip":
		return engine.ManualSkip, nonEmpty(a.Note, "skipped by operator"), nil
	}
	return engine.ManualFail, a.Note, nil
}

func (l *liveRun) OnFailure(ctx context.Context, idx int, step runbook.Step, res engine.StepResult) (engine.FailureAction, string, error) {
	opts := []string{"retry"}
	if l.canShell() {
		opts = append(opts, "manual")
	}
	opts = append(opts, "skip", "force", "abort")
	for {
		a, err := l.askStep(ctx, Question{Type: "failure", Step: idx, Title: "“" + step.Name + "” failed", Text: res.Error, Options: opts})
		if err != nil {
			return "", "", err
		}
		switch a.Value {
		case "retry":
			return engine.FailRetry, "", nil
		case "manual":
			return engine.FailByHand, "", nil
		case "skip":
			return engine.FailSkip, "", nil
		case "force":
			if strings.TrimSpace(a.Note) == "" {
				continue // a reason is required; ask again
			}
			return engine.FailForce, a.Note, nil
		}
		return engine.FailAbort, "", nil
	}
}

// note pushes an operator note to anyone watching the run.
func (l *liveRun) note(step int, n engine.Note) {
	l.publish(WireEvent{Event: engine.Event{Kind: "note", Step: step}, Note: &n})
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
