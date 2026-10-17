package server

import (
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/runbook"
)

// Question is a decision the engine is waiting on from a browser.
type Question struct {
	ID      string         `json:"id"`
	Type    string         `json:"type"` // input | confirm | danger | manual | failure | before
	Step    int            `json:"step"`
	Title   string         `json:"title"`
	Text    string         `json:"text,omitempty"`
	Options []string       `json:"options,omitempty"`
	Expect  string         `json:"expect,omitempty"`
	Input   *runbook.Input `json:"input,omitempty"`
	// Confirm is what must be typed to choose "run" (danger questions).
	Confirm string `json:"confirm,omitempty"`
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
	Question *Question     `json:"question,omitempty"`
	Answered string        `json:"answered,omitempty"`
	Note     *engine.Note  `json:"note,omitempty"`
	Controls *controlsJSON `json:"controls,omitempty"`
	Term     *TermEvent    `json:"term,omitempty"`
}

// liveRun is an in-progress (or recently finished) run driven by the GUI.
// It implements engine.Interactor.
type liveRun struct {
	id      string
	cancel  context.CancelFunc
	session engine.Session
	ctl     *engine.Controls
	terms   map[string]*engine.PipeConn
	tseq    int

	mu      sync.Mutex
	events  []WireEvent
	subs    map[chan WireEvent]struct{}
	pending map[string]chan Answer
	// qstep is the step each pending question is about.
	qstep map[string]int
	done  bool
	qseq  int
}

func newLiveRun(id string, cancel context.CancelFunc) *liveRun {
	return &liveRun{id: id, cancel: cancel, subs: map[chan WireEvent]struct{}{}, pending: map[string]chan Answer{}, qstep: map[string]int{}}
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
	delete(l.qstep, a.QuestionID)
	l.mu.Unlock()
	if !ok {
		return fmt.Errorf("no pending question %q", a.QuestionID)
	}
	ch <- a
	return nil
}

// answerEdited withdraws a question because its step was edited.
const answerEdited = "\x00edited"

// restartStep withdraws the questions waiting on step i, so it starts again
// with its new definition. It reports whether there were any.
func (l *liveRun) restartStep(i int) bool {
	l.mu.Lock()
	var ids []string
	for id, st := range l.qstep {
		if st == i {
			ids = append(ids, id)
		}
	}
	l.mu.Unlock()
	for _, id := range ids {
		_ = l.answer(Answer{QuestionID: id, Value: answerEdited})
	}
	return len(ids) > 0
}

func (l *liveRun) ask(ctx context.Context, q Question) (Answer, error) {
	ch := make(chan Answer, 1)
	l.mu.Lock()
	l.qseq++
	q.ID = fmt.Sprintf("q%d", l.qseq)
	l.pending[q.ID] = ch
	l.qstep[q.ID] = q.Step
	l.mu.Unlock()
	l.publish(WireEvent{Event: engine.Event{Kind: "question", Step: q.Step}, Question: &q})
	select {
	case a := <-ch:
		l.publish(WireEvent{Event: engine.Event{Kind: "answered", Step: q.Step, Text: a.Value}, Answered: q.ID})
		if a.Value == answerEdited {
			return a, engine.ErrStepEdited
		}
		if a.Value == "abort" && q.Type != "input" {
			return a, engine.ErrAborted
		}
		return a, nil
	case <-ctx.Done():
		l.mu.Lock()
		delete(l.pending, q.ID)
		delete(l.qstep, q.ID)
		l.mu.Unlock()
		return Answer{}, ctx.Err()
	}
}

func (l *liveRun) Notify(e engine.Event) { l.publish(WireEvent{Event: e}) }

func (l *liveRun) Bind(se engine.Session) { l.session = se }

// TermEvent carries an in-browser terminal session: term_start (ID,
// Title), term (Data: base64 output), term_end.
type TermEvent struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	Data  string `json:"data,omitempty"`
	// Shell is set for shells opened at a step, with that step's commands
	// and expected outcome, to show beside the terminal.
	Shell   bool   `json:"shell,omitempty"`
	Command string `json:"command,omitempty"`
	Expect  string `json:"expect,omitempty"`
	// Background sessions show as a tab without taking over the page.
	Background bool `json:"background,omitempty"`
	// Functions a shell can call, listed beside it.
	Functions []engine.FuncInfo `json:"functions,omitempty"`
}

// StartBackground implements engine.BackgroundHost: a browser terminal tab
// that keeps running while the run moves on.
func (l *liveRun) StartBackground(idx int, title string) engine.TermConn {
	return l.startTerm(idx, title, true)
}

// kindWord names a way's kind for people.
func kindWord(k string) string {
	if k == "run" {
		return "scripted"
	}
	return k
}

// ChooseWay implements engine.Chooser: the ways as options, preferred first.
func (l *liveRun) ChooseWay(ctx context.Context, v engine.StepView, failed int) (int, error) {
	var opts []string
	var text strings.Builder
	order := make([]int, 0, len(v.Ways))
	for j, w := range v.Ways {
		if w.Preferred {
			order = append([]int{j}, order...)
		} else {
			order = append(order, j)
		}
	}
	for _, j := range order {
		w := v.Ways[j]
		opts = append(opts, w.Name)
		note := ""
		if w.Preferred {
			note += " · preferred"
		}
		if j == failed {
			note += " · just failed"
		}
		fmt.Fprintf(&text, "%s (%s%s)\n    %s\n", w.Name, kindWord(w.Kind), note, strings.ReplaceAll(strings.TrimSpace(w.Command), "\n", "\n    "))
	}
	title := "How should “" + v.Name + "” be done?"
	if failed >= 0 {
		title = "Try “" + v.Name + "” another way"
	}
	a, err := l.askStep(ctx, Question{Type: "choose", Step: v.Index, Title: title, Text: text.String(), Expect: v.Expect, Options: append(opts, "abort")})
	if err != nil {
		return 0, err
	}
	return slices.IndexFunc(v.Ways, func(w engine.WayView) bool { return w.Name == a.Value }), nil
}

// StartTerm implements engine.TermHost with a terminal in the browser:
// output is streamed to everyone watching the run (secrets masked), and
// keystrokes come back through POST /api/runs/{id}/term.
func (l *liveRun) StartTerm(idx int, title string) (engine.TermConn, error) {
	return l.startTerm(idx, title, false), nil
}

func (l *liveRun) startTerm(idx int, title string, background bool) *engine.PipeConn {
	l.mu.Lock()
	l.tseq++
	id := fmt.Sprintf("t%d", l.tseq)
	l.mu.Unlock()
	conn := engine.NewPipeConn(func(b []byte) {
		text := string(b)
		if l.session != nil {
			text = l.session.Mask(text)
		}
		l.publish(WireEvent{Event: engine.Event{Kind: "term", Step: idx}, Term: &TermEvent{ID: id, Data: base64.StdEncoding.EncodeToString([]byte(text))}})
	}, 30, 120)
	l.mu.Lock()
	if l.terms == nil {
		l.terms = map[string]*engine.PipeConn{}
	}
	l.terms[id] = conn
	l.mu.Unlock()
	start := &TermEvent{ID: id, Title: title, Shell: strings.HasPrefix(title, "shell"), Background: background}
	if idx >= 0 && l.session != nil {
		v := l.session.View(idx)
		start.Command, start.Expect = v.Command, v.Expect
	}
	if start.Shell && l.session != nil {
		start.Functions = l.session.Functions()
	}
	l.publish(WireEvent{Event: engine.Event{Kind: "term_start", Step: idx}, Term: start})
	go func() {
		<-conn.Done()
		l.mu.Lock()
		delete(l.terms, id)
		l.mu.Unlock()
		l.publish(WireEvent{Event: engine.Event{Kind: "term_end", Step: idx}, Term: &TermEvent{ID: id}})
	}()
	return conn
}

func (l *liveRun) term(id string) *engine.PipeConn {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.terms[id]
}
func (l *liveRun) canShell() bool { return l.session != nil && l.session.CanShell() }

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

// AfterStep implements engine.Stepper for steps set to pause afterwards.
func (l *liveRun) AfterStep(ctx context.Context, v engine.StepView, res engine.StepResult) (engine.AfterDecision, string, error) {
	text := "Finished: " + string(res.Status)
	if res.ExitCode != nil {
		text += fmt.Sprintf(" (exit %d)", *res.ExitCode)
	}
	a, err := l.askStep(ctx, Question{Type: "after", Step: v.Index, Title: "Check “" + v.Name + "”", Text: text, Expect: v.Expect, Options: []string{"continue", "retry", "fail", "abort"}})
	if err != nil {
		return "", "", err
	}
	return engine.AfterDecision(a.Value), a.Note, nil
}

// BeforeStep implements engine.Stepper for step mode.
func (l *liveRun) BeforeStep(ctx context.Context, v engine.StepView) (engine.StepDecision, error) {
	opts := []string{"run", "skip", "continue", "abort"}
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

// ConfirmDanger: the GUI makes the operator type the step number to run it.
func (l *liveRun) ConfirmDanger(ctx context.Context, idx int, step runbook.Step, command, why string) (bool, error) {
	n := strconv.Itoa(idx + 1)
	for {
		a, err := l.askStep(ctx, Question{Type: "danger", Step: idx, Title: "⚠ “" + step.Name + "” is dangerous, and not running in sequence",
			Text: why + "\n\nWill run:\n" + command, Options: []string{"run", "skip", "abort"}, Confirm: n})
		if err != nil {
			return false, err
		}
		switch a.Value {
		case "run":
			if strings.TrimSpace(a.Note) != n {
				continue // not double-checked; ask again
			}
			return true, nil
		case "abort":
			return false, engine.ErrAborted
		}
		return false, nil
	}
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
	opts := []string{"retry", "skip", "force", "abort"}
	if len(step.Ways) > 1 {
		opts = []string{"retry", "way", "skip", "force", "abort"}
	}
	for {
		a, err := l.askStep(ctx, Question{Type: "failure", Step: idx, Title: "“" + step.Name + "” failed", Text: res.Error, Options: opts})
		if err != nil {
			return "", "", err
		}
		switch a.Value {
		case "retry":
			return engine.FailRetry, "", nil
		case "skip":
			return engine.FailSkip, "", nil
		case "way":
			return engine.FailOtherWay, "", nil
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
