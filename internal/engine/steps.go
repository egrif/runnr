package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egrif/runnr/internal/runbook"
)

// stepKind is how a step shows in records and views.
func stepKind(s runbook.Step) string {
	switch {
	case s.Background:
		return "background"
	case s.Kind() == "run" && s.TTY:
		return "tty"
	}
	return s.Kind()
}

// step runs step i, starting it again (with its new definition) whenever
// it's edited while waiting on the operator.
func (x *executor) step(ctx context.Context, i int, step runbook.Step) error {
	for {
		err := x.stepOnce(ctx, i, step)
		if !errors.Is(err, ErrStepEdited) {
			return err
		}
		step = x.def(i)
		res := &x.run.Steps[i]
		res.Error, res.ExitCode, res.Note = "", nil, ""
		x.notify(Event{Kind: EventOutput, Step: i, Stream: "info", Text: "✎ step edited: starting it again with the new version"})
	}
}

func (x *executor) stepOnce(ctx context.Context, i int, step runbook.Step) error {
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

	run, checked, err := x.checkDanger(ctx, i, step)
	if err != nil {
		return x.fail(i, err)
	}
	if !run {
		return nil
	}
	confirmed = confirmed || checked

	if step.Background && !x.opts.DryRun {
		return x.startBackground(ctx, i, step, confirmed)
	}

	for {
		a := x.attempt(ctx, i, step, res, confirmed)
		confirmed = true // retries don't ask again
		if a.hard != nil {
			return x.fail(i, a.hard)
		}
		switch a.kind {
		case outSkipped:
			res.Status, res.Note = StatusSkipped, a.note
			x.stepDone(i)
			return nil
		case outDone:
			x.apply(step, a.out)
			res.Command = x.mask(res.Command) // in case it contained a just-published secret
			res.Status, res.Error = StatusSucceeded, ""
			if a.note != "" {
				res.Note = a.note
			}
			if err := x.afterStep(ctx, i, step); err != errRetry {
				return err
			}
			continue
		}
		// Failed.
		if ctx.Err() != nil {
			return x.fail(i, ctx.Err())
		}
		res.Error = x.mask(a.err)
		if step.ContinueOnError {
			res.Status, res.Note = StatusFailed, "continue_on_error"
			x.stepDone(i)
			return nil
		}
		if err := x.onFailure(ctx, i, step, a.out); err != errRetry {
			return err
		}
	}
}

type outcome int

const (
	outDone outcome = iota
	outSkipped
	outFailed
)

// attemptResult is one try at a step's body.
type attemptResult struct {
	kind outcome
	out  cmdResult // what a command produced (applied on success)
	note string
	err  string // why it failed
	hard error  // ends the run (template errors, aborts)
}

// attempt runs a step's body once, recording into res. It's used for
// top-level steps and for the steps inside an alternative; events go to
// step i either way.
func (x *executor) attempt(ctx context.Context, i int, step runbook.Step, res *StepResult, confirmed bool) attemptResult {
	switch step.Kind() {
	case "manual":
		return x.attemptManual(ctx, i, step, res, step.Manual)
	case "checks":
		return x.attemptChecks(ctx, i, step, res, confirmed)
	case "alternatives":
		return x.attemptAlternatives(ctx, i, step, res, confirmed)
	}
	return x.attemptRun(ctx, i, step, res, confirmed)
}

func (x *executor) attemptManual(ctx context.Context, i int, step runbook.Step, res *StepResult, tmpl string) attemptResult {
	text, err := x.render(tmpl)
	if err != nil {
		return attemptResult{hard: fmt.Errorf("manual: %w", err)}
	}
	res.Command = x.mask(text)
	if x.opts.DryRun {
		return attemptResult{kind: outSkipped, note: "dry run"}
	}
	outcome, note, err := x.ui.Manual(ctx, i, step, x.mask(text))
	if err != nil {
		return attemptResult{hard: err}
	}
	switch outcome {
	case ManualDone:
		return attemptResult{kind: outDone, note: note}
	case ManualSkip:
		return attemptResult{kind: outSkipped, note: nonEmpty(note, "skipped by operator")}
	}
	return attemptResult{kind: outFailed, err: strings.TrimSpace("marked failed " + suffix(note))}
}

func (x *executor) confirm(ctx context.Context, i int, step runbook.Step, what string, confirmed bool) (bool, error) {
	if !step.Confirm || confirmed || x.opts.DryRun {
		return true, nil
	}
	return x.ui.Confirm(ctx, i, step, x.mask(what))
}

func (x *executor) attemptRun(ctx context.Context, i int, step runbook.Step, res *StepResult, confirmed bool) attemptResult {
	cmd, err := x.render(step.Run)
	if err != nil {
		return attemptResult{hard: fmt.Errorf("run: %w", err)}
	}
	res.Command = x.mask(cmd)
	if step.TTY && !x.CanShell() && !x.opts.DryRun {
		// No terminal here: hand the command to a human instead.
		return x.attemptManual(ctx, i, step, res, "Run in a terminal (needs interactive input):\n\n"+step.Run)
	}
	if ok, err := x.confirm(ctx, i, step, cmd, confirmed); err != nil {
		return attemptResult{hard: err}
	} else if !ok {
		return attemptResult{kind: outSkipped, note: "declined"}
	}
	if x.opts.DryRun {
		return attemptResult{kind: outSkipped, note: "dry run"}
	}
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
	if out.err != nil {
		return attemptResult{kind: outFailed, out: out, err: out.err.Error()}
	}
	return attemptResult{kind: outDone, out: out}
}

// ---------- checks ----------

func (x *executor) attemptChecks(ctx context.Context, i int, step runbook.Step, res *StepResult, confirmed bool) attemptResult {
	res.Command = x.mask(x.View(i).Command)
	if ok, err := x.confirm(ctx, i, step, res.Command, confirmed); err != nil {
		return attemptResult{hard: err}
	} else if !ok {
		return attemptResult{kind: outSkipped, note: "declined"}
	}
	if x.opts.DryRun {
		return attemptResult{kind: outSkipped, note: "dry run"}
	}
	var deadline time.Time
	if step.Repeat != nil {
		deadline = time.Now().Add(step.Repeat.TimeoutDuration())
	}
	for {
		res.Attempts++
		results := make([]CheckResult, len(step.Checks))
		for j, c := range step.Checks {
			r := CheckResult{Name: c.Name}
			cmd, err := x.render(c.Run)
			if err != nil {
				return attemptResult{hard: fmt.Errorf("check %q: %w", c.Name, err)}
			}
			cs := step
			cs.Run, cs.Outputs, cs.SecretOutputs, cs.Capture, cs.TTY = cmd, nil, nil, "", false
			out := x.execStream(ctx, i, cs, cmd, false)
			if ctx.Err() != nil {
				return attemptResult{kind: outFailed, err: ctx.Err().Error()}
			}
			r.Passed, r.ExitCode, r.Output = out.err == nil, out.exitCode, x.mask(lastLines(out.combined, 20))
			if out.err != nil {
				r.Error = x.mask(out.err.Error())
			}
			results[j] = r
		}
		res.Checks = results

		var failing []string
		lines := []string{fmt.Sprintf("checks, attempt %d:", res.Attempts)}
		for _, r := range results {
			if r.Passed {
				lines = append(lines, "  ✅ "+r.Name)
				continue
			}
			failing = append(failing, r.Name)
			last := lastLines(r.Output, 1)
			if last == "" {
				last = r.Error
			}
			lines = append(lines, fmt.Sprintf("  ❌ %s — %s", r.Name, strings.TrimSpace(last)))
		}
		for _, l := range lines {
			x.notify(Event{Kind: EventOutput, Step: i, Stream: "info", Text: l})
		}
		res.Output = trimOutput(res.Output + strings.Join(lines, "\n") + "\n")
		x.notify(Event{Kind: EventStepProgress, Step: i, Result: x.result(i)})
		x.save()

		if len(failing) == 0 {
			return attemptResult{kind: outDone, note: fmt.Sprintf("all %d checks passed (attempt %d)", len(results), res.Attempts)}
		}
		failed := fmt.Sprintf("%d of %d checks failing: %s", len(failing), len(results), strings.Join(failing, ", "))
		if step.Repeat == nil {
			return attemptResult{kind: outFailed, err: failed}
		}
		every := step.Repeat.EveryDuration()
		if time.Now().Add(every).After(deadline) {
			return attemptResult{kind: outFailed, err: failed + " (gave up after " + step.Repeat.Timeout + ")"}
		}
		x.notify(Event{Kind: EventOutput, Step: i, Stream: "info", Text: fmt.Sprintf("  trying again in %s (until %s)", every, deadline.Format("15:04:05"))})
		select {
		case <-ctx.Done():
			return attemptResult{kind: outFailed, err: ctx.Err().Error()}
		case <-time.After(every):
		}
	}
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func trimOutput(s string) string {
	if len(s) > maxOutput {
		return "…[truncated]\n" + s[len(s)-maxOutput:]
	}
	return s
}

// ---------- alternatives ----------

func (x *executor) attemptAlternatives(ctx context.Context, i int, step runbook.Step, res *StepResult, confirmed bool) attemptResult {
	if x.opts.DryRun {
		return attemptResult{kind: outSkipped, note: "dry run"}
	}
	chosen := slices.IndexFunc(step.Alternatives, func(a runbook.Alternative) bool { return a.Name == res.Alternative })
	if chosen < 0 {
		var err error
		if chosen, err = x.chooseAlternative(ctx, i, step); err != nil {
			return attemptResult{hard: err}
		}
		if chosen < 0 {
			return attemptResult{kind: outFailed, err: "no alternative applies"}
		}
		res.Alternative = step.Alternatives[chosen].Name
		x.notify(Event{Kind: EventOutput, Step: i, Stream: "info", Text: "using: " + res.Alternative})
	}
	alt := step.Alternatives[chosen]

	if alt.Run != "" {
		s := step
		s.Alternatives, s.Run, s.TTY = nil, alt.Run, alt.TTY
		return x.attemptRun(ctx, i, s, res, confirmed)
	}

	// A sequence of steps. A retry resumes at the one that failed.
	if len(res.Sub) != len(alt.Steps) {
		res.Sub = make([]StepResult, len(alt.Steps))
		for j, s := range alt.Steps {
			res.Sub[j] = StepResult{Index: j, Name: s.Name, Kind: stepKind(s), Status: StatusPending}
		}
	}
	if ok, err := x.confirm(ctx, i, step, x.View(i).Command, confirmed); err != nil {
		return attemptResult{hard: err}
	} else if !ok {
		return attemptResult{kind: outSkipped, note: "declined"}
	}
	for j, s := range alt.Steps {
		s = inherit(step, s)
		sr := &res.Sub[j]
		if slices.Contains([]Status{StatusSucceeded, StatusSkipped, StatusForced}, sr.Status) {
			continue
		}
		x.notify(Event{Kind: EventOutput, Step: i, Stream: "info", Text: fmt.Sprintf("── %d/%d %s", j+1, len(alt.Steps), s.Name)})
		if s.When != "" {
			cond, err := x.render(s.When)
			if err != nil {
				return attemptResult{hard: fmt.Errorf("%s: when: %w", s.Name, err)}
			}
			if !runbook.Truthy(cond) {
				sr.Status, sr.Note = StatusSkipped, "condition false"
				continue
			}
		}
		sr.Status, sr.Started, sr.Error = StatusRunning, time.Now(), ""
		x.notify(Event{Kind: EventStepProgress, Step: i, Result: x.result(i)})
		a := x.attempt(ctx, i, s, sr, false)
		sr.Ended = time.Now()
		if a.hard != nil {
			sr.Status = StatusFailed
			return a
		}
		switch a.kind {
		case outSkipped:
			sr.Status, sr.Note = StatusSkipped, a.note
		case outDone:
			x.apply(s, a.out)
			sr.Status, sr.Note = StatusSucceeded, a.note
		default:
			sr.Status, sr.Error = StatusFailed, x.mask(a.err)
			if !s.ContinueOnError {
				x.notify(Event{Kind: EventStepProgress, Step: i, Result: x.result(i)})
				return attemptResult{kind: outFailed, out: a.out, err: fmt.Sprintf("%d. %s failed: %s", j+1, s.Name, a.err)}
			}
		}
		x.notify(Event{Kind: EventStepProgress, Step: i, Result: x.result(i)})
		x.save()
	}
	return attemptResult{kind: outDone, note: "used: " + alt.Name}
}

// inherit gives a step inside an alternative its parent's dir, shell and
// env, where it doesn't set its own.
func inherit(parent, s runbook.Step) runbook.Step {
	if s.Dir == "" {
		s.Dir = parent.Dir
	}
	if s.Shell == "" {
		s.Shell = parent.Shell
	}
	if len(parent.Env) > 0 {
		env := maps.Clone(parent.Env)
		maps.Copy(env, s.Env)
		s.Env = env
	}
	return s
}

// chooseAlternative asks the operator, or (unattended) takes the first
// alternative whose when holds. -1 means none applies.
func (x *executor) chooseAlternative(ctx context.Context, i int, step runbook.Step) (int, error) {
	if c, ok := x.ui.(Chooser); ok && x.opts.Interactive {
		return c.ChooseAlternative(ctx, x.View(i))
	}
	for j, a := range step.Alternatives {
		if a.When == "" {
			return j, nil
		}
		cond, err := x.render(a.When)
		if err != nil {
			return -1, fmt.Errorf("alternative %q: when: %w", a.Name, err)
		}
		if runbook.Truthy(cond) {
			return j, nil
		}
	}
	return -1, nil
}

// ---------- background ----------

// bgStep is a background step that's still running.
type bgStep struct {
	session   *termSession
	stopAfter int // step index; -1 = when the run ends
	started   time.Time
	tmp       string
}

func (x *executor) startBackground(ctx context.Context, i int, step runbook.Step, confirmed bool) error {
	res := &x.run.Steps[i]
	cmd, err := x.render(step.Run)
	if err != nil {
		return x.fail(i, fmt.Errorf("run: %w", err))
	}
	res.Command = x.mask(cmd)
	if ok, err := x.confirm(ctx, i, step, cmd, confirmed); err != nil {
		return x.fail(i, err)
	} else if !ok {
		res.Status, res.Note = StatusSkipped, "declined"
		x.stepDone(i)
		return nil
	}
	shell := nonEmpty(step.Shell, nonEmpty(x.rb.Shell, "sh"))
	extra, err := x.runnrEnv(step)
	if err != nil {
		return x.fail(i, err)
	}
	dir, err := x.render(step.Dir)
	if err != nil {
		return x.fail(i, fmt.Errorf("dir: %w", err))
	}
	tmp, err := os.MkdirTemp("", "runnr-bg-")
	if err != nil {
		return x.fail(i, err)
	}
	extra = append(extra, "RUNNR_OUTPUT="+filepath.Join(tmp, "output"), "RUNNR_SECRET="+filepath.Join(tmp, "secret"))
	if cmd, err = x.withPrelude(cmd, tmp); err != nil {
		return x.fail(i, err)
	}

	title := fmt.Sprintf("background · step %d: %s", i+1, step.Name)
	var conn TermConn
	if h, ok := x.ui.(BackgroundHost); ok {
		conn = h.StartBackground(i, title)
	} else {
		conn = NewBackgroundConn(40, 140)
	}
	// The session belongs to the run, not this step.
	s, err := x.startTerm(ctx, shell, cmd, extra, dir, conn)
	if err != nil {
		conn.Close()
		os.RemoveAll(tmp)
		return x.fail(i, err)
	}
	stopAfter := -1
	note := "running in the background until the run ends"
	if step.StopAfter != "" {
		stopAfter, _ = x.rb.StepIndex(step.StopAfter)
		note = fmt.Sprintf("running in the background until step %d (%s) finishes", stopAfter+1, x.def(stopAfter).Name)
	}
	if x.background == nil {
		x.background = map[int]*bgStep{}
	}
	x.background[i] = &bgStep{session: s, stopAfter: stopAfter, started: time.Now(), tmp: tmp}
	res.Status, res.Note = StatusBackground, note
	x.stepDone(i)
	return nil
}

// tendBackground records background steps that ended on their own, and
// stops those whose stop_after step (finished) is just reached. Pass
// finished = -1 to only check; final stops everything (run ending).
func (x *executor) tendBackground(finished int, final bool) {
	for i, b := range x.background {
		ended := false
		select {
		case <-b.session.done:
			ended = true
		default:
		}
		why := ""
		switch {
		case ended:
			why = "exited"
		case final:
			why = "stopped when the run ended"
		case finished >= 0 && b.stopAfter == finished:
			why = fmt.Sprintf("stopped after step %d", finished+1)
		default:
			continue
		}
		if !ended {
			b.session.stop()
		}
		code, transcript, _ := b.session.wait()
		os.RemoveAll(b.tmp)
		delete(x.background, i)

		res := &x.run.Steps[i]
		res.Output, res.ExitCode, res.Ended = x.mask(transcript), code, time.Now()
		res.Status, res.Note = StatusSucceeded, why
		if !ended {
			res.ExitCode = nil // a hang-up's "exit status" means nothing
		}
		if ended && code != nil && *code != 0 {
			res.Status, res.Note = StatusFailed, fmt.Sprintf("exited by itself with status %d", *code)
		}
		x.notify(Event{Kind: EventOutput, Step: i, Stream: "info", Text: fmt.Sprintf("background step %d (%s): %s", i+1, res.Name, res.Note)})
		x.refreshState()
		x.notify(Event{Kind: EventStepFinished, Step: i, Result: x.result(i), State: x.run.State})
		x.save()
	}
}

func suffix(note string) string {
	if note == "" {
		return ""
	}
	return ": " + note
}
