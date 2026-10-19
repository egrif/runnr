package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
		res.Error, res.ExitCode, res.Note, res.Checks = "", nil, "", nil
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
	x.openWindow(i)

	confirmed := false
	if _, before, _ := x.ctl.Get(i); (x.takePause(i) || before) && x.canPause() {
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

	orig, way := step, -1
	if len(orig.Ways) > 0 {
		if way, err = x.pickWay(ctx, i, orig, -1); err != nil {
			return x.fail(i, err)
		}
		step = x.useWay(i, orig, way)
	}

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
		err := x.onFailure(ctx, i, orig, a.out)
		if err == errOtherWay {
			if way, err = x.pickWay(ctx, i, orig, way); err != nil {
				return x.fail(i, err)
			}
			step, confirmed = x.useWay(i, orig, way), false
			res.Checks = nil // a new way's checks start afresh
			continue
		}
		if err != errRetry {
			return err
		}
	}
}

// pickWay chooses how to do a step with ways: the preferred one, unless
// it's set to choose (interactive runs) or a way just failed.
func (x *executor) pickWay(ctx context.Context, i int, step runbook.Step, failed int) (int, error) {
	if len(step.Ways) > 1 && (step.Choose || failed >= 0) && x.opts.Interactive && !x.opts.DryRun {
		if c, ok := x.ui.(Chooser); ok {
			w, err := c.ChooseWay(ctx, x.View(i), failed)
			if err == nil && (w < 0 || w >= len(step.Ways)) {
				err = fmt.Errorf("no way %d", w+1)
			}
			return w, err
		}
	}
	if failed >= 0 {
		return 0, errors.New("no one here to choose another way")
	}
	return step.PreferredWay(), nil
}

// useWay is step i done its way w.
func (x *executor) useWay(i int, step runbook.Step, w int) runbook.Step {
	way := step.Ways[w]
	eff := step.WithWay(way)
	res := &x.run.Steps[i]
	res.Kind = stepKind(eff)
	if len(step.Ways) > 1 {
		res.Way = way.Name
		x.notify(Event{Kind: EventOutput, Step: i, Stream: "info", Text: "way: " + way.Name})
	}
	return eff
}

// openWindow notes the maintenance window opening as step i starts.
func (x *executor) openWindow(i int) {
	w := x.run.Window
	if w == nil || i != w.OpenStep || !w.Opened.IsZero() || x.opts.DryRun {
		return
	}
	w.Opened = time.Now()
	x.windowOpenedBy = i
	x.notify(Event{Kind: EventOutput, Step: i, Stream: "info", Text: "🔴 maintenance window open: production is down" + targetNote(w)})
	x.notify(Event{Kind: EventWindow, Step: i, Run: x.snapshot()})
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

// attempt runs a step's body once, recording into res.
func (x *executor) attempt(ctx context.Context, i int, step runbook.Step, res *StepResult, confirmed bool) attemptResult {
	switch step.Kind() {
	case "manual":
		return x.attemptManual(ctx, i, step, res, step.Manual)
	case "checks":
		return x.attemptChecks(ctx, i, step, res, confirmed)
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
	return attemptResult{kind: outFailed, err: "marked failed" + suffix(note)}
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
	mode := step.RetryChecks
	// What passed before (in an earlier attempt of these same checks) stays
	// passed when retries rerun only the failed ones, or each in turn.
	prev := res.Checks
	if len(prev) != len(step.Checks) || mode == "" || mode == runbook.RetryAll {
		prev = nil
	} else {
		for j, c := range step.Checks {
			if prev[j].Name != c.Name {
				prev = nil
				break
			}
		}
	}
	for {
		res.Attempts++
		results := make([]CheckResult, len(step.Checks))
		blocked := false
		for j, c := range step.Checks {
			if prev != nil && prev[j].Passed {
				results[j] = prev[j]
				continue
			}
			if blocked {
				results[j] = CheckResult{Name: c.Name, NotRun: true}
				continue
			}
			r := CheckResult{Name: c.Name, Attempt: res.Attempts}
			cmd, err := x.render(c.Run)
			if err != nil {
				return attemptResult{hard: fmt.Errorf("check %q: %w", c.Name, err)}
			}
			cs := checkStep(step, cmd)
			out := x.execStream(ctx, i, cs, cmd, false)
			if ctx.Err() != nil {
				return attemptResult{kind: outFailed, err: ctx.Err().Error()}
			}
			r.Passed, r.ExitCode, r.Output = out.err == nil, out.exitCode, x.mask(lastLines(out.combined, 20))
			if out.err != nil {
				r.Error = x.mask(out.err.Error())
				// Each in turn: the ones after it wait for it to pass.
				blocked = mode == runbook.RetryEach
			}
			results[j] = r
		}
		if mode == runbook.RetryFailed || mode == runbook.RetryEach {
			prev = results
		}
		res.Checks = results

		var failing []string
		lines := []string{fmt.Sprintf("checks, attempt %d:", res.Attempts)}
		for _, r := range results {
			if r.Passed {
				if r.Attempt != 0 && r.Attempt < res.Attempts {
					lines = append(lines, fmt.Sprintf("  ✅ %s (passed in attempt %d)", r.Name, r.Attempt))
				} else {
					lines = append(lines, "  ✅ "+r.Name)
				}
				continue
			}
			if r.NotRun {
				failing = append(failing, r.Name)
				lines = append(lines, "  ⏸ "+r.Name+" — waits for the one above to pass")
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

// checkStep is the step a check runs as: its command, publishing nothing,
// never in a terminal.
func checkStep(step runbook.Step, cmd string) runbook.Step {
	step.Run, step.Outputs, step.SecretOutputs, step.Capture, step.TTY = cmd, nil, nil, "", false
	return step
}
