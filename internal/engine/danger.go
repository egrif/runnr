package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/egrif/runnr/internal/runbook"
)

// DangerChecker is implemented by interactors that can ask a human to
// double-check a dangerous step that isn't running in sequence. Answering
// false skips the step; ErrAborted stops the run.
type DangerChecker interface {
	ConfirmDanger(ctx context.Context, idx int, step runbook.Step, command, why string) (bool, error)
}

// NoteCarried marks a step a resumed run didn't redo because it finished in
// the run being resumed.
const NoteCarried = "done in the resumed run"

// NoteDangerDeclined marks a dangerous step the operator chose not to run.
const NoteDangerDeclined = "dangerous step not double-checked"

// inSequence reports whether a step's result lets later steps count as
// running in sequence: it ran (or was accepted), or was left out by the
// runbook itself rather than by someone choosing to skip it.
func inSequence(r StepResult) bool {
	switch r.Status {
	case StatusSucceeded, StatusForced, StatusBackground:
		return true
	case StatusSkipped:
		return r.Note == "condition false" || r.Note == NoteCarried || strings.HasPrefix(r.Note, "auto-skipped")
	}
	return false
}

// carryResumed marks the steps this run leaves out that already finished,
// in sequence, in the run it resumes.
func (x *executor) carryResumed() {
	prev := x.opts.Resumed
	if prev == nil || len(prev.Steps) != len(x.run.Steps) {
		return
	}
	for i := range x.run.Steps {
		if skip, _, _ := x.ctl.Get(i); skip && inSequence(prev.Steps[i]) {
			x.run.Steps[i].Status, x.run.Steps[i].Note = StatusSkipped, NoteCarried
		}
	}
}

// outOfSequence explains why step i isn't running in sequence in a
// sanctioned run, or returns "" if it is.
func (x *executor) outOfSequence(i int) string {
	if !x.opts.Sanctioned {
		return "This isn't a recorded run of the saved runbook (e.g. Run this from the editor, or a runbook file)."
	}
	var off []string
	for j := 0; j < i; j++ {
		if r := x.run.Steps[j]; !inSequence(r) {
			off = append(off, fmt.Sprintf("  %d. %s (%s)", j+1, r.Name, strings.TrimSuffix(string(r.Status)+": "+r.Note, ": ")))
		}
	}
	if len(off) == 0 {
		return ""
	}
	return "Earlier steps didn't all run:\n" + strings.Join(off, "\n")
}

// checkDanger double-checks a dangerous step that's out of sequence. run
// false means don't run it (it's already marked skipped); checked means a
// person double-checked it, which covers its confirmation too.
func (x *executor) checkDanger(ctx context.Context, i int, step runbook.Step) (run, checked bool, err error) {
	if !step.Dangerous || x.opts.DryRun {
		return true, false, nil
	}
	why := x.outOfSequence(i)
	if why == "" {
		return true, false, nil
	}
	dc, can := x.ui.(DangerChecker)
	if !can || !x.opts.Interactive {
		return false, false, fmt.Errorf("step %q is dangerous and not running in sequence, so it needs a person to double-check it (run interactively)\n%s", step.Name, why)
	}
	run, err = dc.ConfirmDanger(ctx, i, step, x.mask(x.View(i).Command), why)
	if err != nil {
		return false, false, err
	}
	if !run {
		res := &x.run.Steps[i]
		res.Status, res.Note = StatusSkipped, NoteDangerDeclined
		x.stepDone(i)
	}
	return run, run, nil
}
