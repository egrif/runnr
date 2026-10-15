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
	Type    string         `json:"type"` // input | confirm | manual | failure
	Step    int            `json:"step"`
	Title   string         `json:"title"`
	Text    string         `json:"text,omitempty"`
	Options []string       `json:"options,omitempty"`
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

func (l *liveRun) AskInput(ctx context.Context, in runbook.Input) (string, error) {
	a, err := l.ask(ctx, Question{Type: "input", Step: -1, Title: "Input: " + in.Name, Text: in.Description, Input: &in})
	return a.Value, err
}

func (l *liveRun) Confirm(ctx context.Context, idx int, step runbook.Step, command string) (bool, error) {
	a, err := l.ask(ctx, Question{Type: "confirm", Step: idx, Title: "Run “" + step.Name + "”?", Text: command, Options: []string{"run", "skip", "abort"}})
	return a.Value == "run", err
}

func (l *liveRun) Manual(ctx context.Context, idx int, step runbook.Step, text string) (engine.ManualOutcome, string, error) {
	a, err := l.ask(ctx, Question{Type: "manual", Step: idx, Title: step.Name, Text: text, Options: []string{"done", "skip", "fail", "abort"}})
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
	for {
		a, err := l.ask(ctx, Question{Type: "failure", Step: idx, Title: "“" + step.Name + "” failed", Text: res.Error, Options: []string{"retry", "skip", "force", "abort"}})
		if err != nil {
			return "", "", err
		}
		switch a.Value {
		case "retry":
			return engine.FailRetry, "", nil
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
