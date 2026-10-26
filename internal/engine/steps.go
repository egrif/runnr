package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
		why := "✎ step edited: starting it again with the new version"
		if errors.Is(err, ErrInputsChanged) {
			why = "inputs changed: starting the step again with the new values"
		}
		x.notify(Event{Kind: EventOutput, Step: i, Stream: "info", Text: why})
	}
}

func (x *executor) stepOnce(ctx context.Context, i int, step runbook.Step) error {
	x.takeInputs()
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
	delete(x.typed, i)
	x.notify(Event{Kind: EventStepStarted, Step: i, Result: x.result(i)})
	if skip, note, err := x.skipIf(ctx, i, step); err != nil {
		return x.fail(i, err)
	} else if skip {
		res.Status, res.Note = StatusSkipped, note
		x.stepDone(i)
		return nil
	}
	if err := x.waitFor(ctx, i, step); err != nil {
		return x.fail(i, err)
	}
	x.openWindow(i)

	confirmed := false
	// Optional steps aren't required: an interactive run asks first.
	if _, before, _ := x.ctl.Get(i); (x.takePause(i) || before || (step.Optional && !x.opts.DryRun)) && x.canPause() {
		q, done := x.asking(ctx)
		d, err := x.ui.(Stepper).BeforeStep(q, x.View(i))
		done()
		if se := x.suspension(ctx, err); se != nil {
			// Suspended before it ran: it's still to do.
			*res = StepResult{Index: res.Index, Name: res.Name, Section: res.Section, Owner: res.Owner, Kind: res.Kind, Status: StatusPending, Optional: res.Optional, Before: res.Before}
			return se
		}
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
		x.takeInputs() // before each try: a change made while it was failing counts for the retry
		// The try can be stopped on its own (StopStep): it fails, and the
		// run asks what to do as for any failure.
		sctx, cancel := context.WithCancelCause(ctx)
		x.em.Lock()
		x.stop = &stepStop{idx: i, cancel: cancel}
		x.em.Unlock()
		a := x.attempt(sctx, i, step, res, confirmed)
		x.em.Lock()
		by := x.stop.by
		x.stop = nil
		x.em.Unlock()
		if ctx.Err() == nil && errors.Is(context.Cause(sctx), errStepStopped) {
			why := stoppedNote(by)
			x.notify(Event{Kind: EventOutput, Step: i, Stream: "info", Text: "■ " + why})
			if a.kind != outDone || a.hard != nil {
				a = attemptResult{kind: outFailed, err: why, out: a.out}
			}
		}
		cancel(nil)
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
			// In case they show a secret it just published (or captured).
			res.Command, res.Output = x.mask(res.Command), x.mask(res.Output)
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
		if step.Optional && !x.opts.Interactive {
			// Not required: nobody's there to decide, so carry on without it.
			res.Status, res.Note = StatusFailed, "optional: carried on without it"
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
			q, done := x.asking(ctx)
			w, err := c.ChooseWay(q, x.View(i), failed)
			done()
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
	q, done := x.asking(ctx)
	outcome, note, err := x.ui.Manual(q, i, step, x.mask(text))
	done()
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
	if step.ConfirmTyping != "" {
		// Not covered by choosing to run, or a danger check: a point of no
		// return is confirmed by typing.
		return x.confirmTyped(ctx, i, step, what)
	}
	if !step.Confirm || confirmed || x.opts.DryRun {
		return true, nil
	}
	q, done := x.asking(ctx)
	defer done()
	return x.ui.Confirm(q, i, step, x.mask(what))
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
		case <-time.After(x.wait(delay)):
		}
	}
	res.Output = x.mask(out.combined)
	res.ExitCode = out.exitCode
	if out.err != nil {
		return attemptResult{kind: outFailed, out: out, err: out.err.Error()}
	}
	if p := outputProblem(step, out.combined); p != "" {
		x.notify(Event{Kind: EventOutput, Step: i, Stream: "info", Text: "✘ " + p})
		return attemptResult{kind: outFailed, out: out, err: p}
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
	var simulated time.Duration // time passed waiting, in a hurried run
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
		run := func(j int, cmd string) {
			out := x.execStream(ctx, i, checkStep(step, cmd), cmd, false)
			r := CheckResult{Name: step.Checks[j].Name, Attempt: res.Attempts}
			r.Passed, r.ExitCode, r.Output = out.err == nil, out.exitCode, x.mask(lastLines(out.combined, 20))
			if out.err != nil {
				r.Error = x.mask(out.err.Error())
			}
			results[j] = r
		}
		var wg sync.WaitGroup
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
			cmd, err := x.render(c.Run)
			if err != nil {
				wg.Wait()
				return attemptResult{hard: fmt.Errorf("check %q: %w", c.Name, err)}
			}
			if step.ParallelChecks {
				wg.Add(1)
				go func() { defer wg.Done(); run(j, cmd) }()
				continue
			}
			run(j, cmd)
			if ctx.Err() != nil {
				return attemptResult{kind: outFailed, err: ctx.Err().Error()}
			}
			// Each in turn: the ones after it wait for it to pass.
			blocked = mode == runbook.RetryEach && !results[j].Passed
		}
		wg.Wait()
		if ctx.Err() != nil {
			return attemptResult{kind: outFailed, err: ctx.Err().Error()}
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
		if time.Now().Add(simulated + every).After(deadline) {
			return attemptResult{kind: outFailed, err: failed + " (gave up after " + step.Repeat.Timeout + ")"}
		}
		x.notify(Event{Kind: EventOutput, Step: i, Stream: "info", Text: fmt.Sprintf("  trying again in %s (until %s)", every, deadline.Format("15:04:05"))})
		select {
		case <-ctx.Done():
			return attemptResult{kind: outFailed, err: ctx.Err().Error()}
		case <-time.After(x.wait(every)):
			if x.opts.Hurry {
				simulated += every
			}
		}
	}
}

// wait is how long to really wait for d: a moment, in a hurried run.
func (x *executor) wait(d time.Duration) time.Duration {
	if x.opts.Hurry {
		return min(d, 10*time.Millisecond)
	}
	return d
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
	session   bgJob
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
	job, tmp, ref, err := x.launchJob(ctx, i, step, cmd, fmt.Sprintf("step %d: %s", i+1, step.Name))
	if err != nil {
		return x.fail(i, err)
	}
	res.Detached = ref
	stopAfter := -1
	note := "running in the background until the run ends"
	if step.StopAfter != "" {
		stopAfter, _ = x.rb.StepIndex(step.StopAfter)
		note = fmt.Sprintf("running in the background until step %d (%s) finishes", stopAfter+1, x.def(stopAfter).Name)
	}
	if x.background == nil {
		x.background = map[int]*bgStep{}
	}
	x.background[i] = &bgStep{session: job, stopAfter: stopAfter, started: time.Now(), tmp: tmp}
	res.Status, res.Note = StatusBackground, note
	x.stepDone(i)
	return nil
}

// launchJob starts cmd in the background with step i's environment: in a
// window of its own if runs are detached (tmux), else on a terminal of the
// run's. tmp is the job's directory ($RUNNR_OUTPUT is in it); ref finds a
// detached job again.
func (x *executor) launchJob(ctx context.Context, i int, step runbook.Step, cmd, name string) (job bgJob, tmp, ref string, err error) {
	shell := nonEmpty(step.Shell, nonEmpty(x.rb.Shell, "sh"))
	x.vmu.RLock()
	extra, err := x.runnrEnv(step)
	var dir string
	if err == nil {
		dir, err = x.render(step.Dir)
		if err != nil {
			err = fmt.Errorf("dir: %w", err)
		}
	}
	x.vmu.RUnlock()
	if err != nil {
		return nil, "", "", err
	}
	if tmp, err = x.tempDir("runnr-bg-"); err != nil {
		return nil, "", "", err
	}
	outs := []string{"RUNNR_OUTPUT=" + filepath.Join(tmp, "output"), "RUNNR_SECRET=" + filepath.Join(tmp, "secret")}
	extra = append(extra, outs...)
	if cmd, err = x.withPrelude(cmd, tmp); err != nil {
		os.RemoveAll(tmp)
		return nil, "", "", err
	}
	if d := x.opts.Detached; d != nil {
		env := append(x.baseEnv(), extra...)
		dj, err := d.Start(i, name, DetachedSpec{Shell: shell, Command: cmd, Env: env, Dir: dir, Tmp: tmp})
		if err != nil {
			os.RemoveAll(tmp)
			return nil, "", "", err
		}
		return dj, tmp, dj.Ref(), nil
	}
	var conn TermConn
	if h, ok := x.ui.(BackgroundHost); ok {
		conn = h.StartBackground(i, "background · "+name)
	} else {
		conn = NewBackgroundConn(40, 140)
	}
	// The session belongs to the run, not this step.
	s, err := x.startTerm(ctx, shell, cmd, extra, dir, conn)
	if err != nil {
		conn.Close()
		os.RemoveAll(tmp)
		return nil, "", "", err
	}
	return s, tmp, "", nil
}

// tendBackground records background steps that ended on their own, and
// stops those whose stop_after step (finished) is just reached. Pass
// finished = -1 to only check; final stops everything (run ending).
func (x *executor) tendBackground(finished int, final bool) {
	x.tendJobs(final)
	for i, b := range x.background {
		ended := false
		select {
		case <-b.session.Done():
			ended = true
		default:
		}
		why := ""
		switch {
		case ended:
			why = "exited"
		case final:
			why = noteStoppedAtEnd
		case finished >= 0 && b.stopAfter == finished:
			why = fmt.Sprintf("stopped after step %d", finished+1)
		default:
			continue
		}
		if !ended {
			b.session.Stop()
		}
		code, transcript, _ := b.session.Wait()
		if b.tmp != "" {
			os.RemoveAll(b.tmp)
		}
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

// noteStoppedAtEnd marks a background step stopped as the run ended.
const noteStoppedAtEnd = "stopped when the run ended"

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
