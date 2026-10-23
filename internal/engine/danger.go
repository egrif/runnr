package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

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
		return r.Note == "condition false" || r.Note == NoteCarried || strings.HasPrefix(r.Note, "auto-skipped") ||
			strings.HasPrefix(r.Note, NoteSkipIf) // the work's already done
	}
	return false
}

// skipResumedDone, on a resume, leaves out the steps from StartAt on that
// finished in the resumed run (so a resume doesn't redo work after the step
// it starts at). Steps marked always still run.
func (x *executor) skipResumedDone() {
	prev := x.opts.Resumed
	if !x.opts.CarryDone || prev == nil || len(prev.Steps) != len(x.run.Steps) {
		return
	}
	for i := max(x.opts.StartAt, 0); i < len(x.run.Steps); i++ {
		if s := prev.Steps[i].Status; (s == StatusSucceeded || s == StatusForced) && !x.rb.Steps[i].Always {
			if skip, _, _ := x.ctl.Get(i); !skip {
				x.ctl.Toggle("skip", i)
			}
		}
	}
}

// restartWatchers, on a resume, runs again the optional background steps
// before StartAt that didn't finish in the resumed run (a status watcher
// that exited or was stopped): they don't decide where a resume starts,
// but they shouldn't silently stay off either. The review can leave one out.
func (x *executor) restartWatchers() {
	prev := x.opts.Resumed
	if prev == nil || len(prev.Steps) != len(x.run.Steps) {
		return
	}
	for i := 0; i < min(x.opts.StartAt, len(x.rb.Steps)); i++ {
		s := x.rb.Steps[i]
		if !s.Optional || !s.Background {
			continue
		}
		st := prev.Steps[i].Status
		// Or one the run stopped when it was suspended.
		stopped := prev.Status == StatusSuspended && prev.Steps[i].Note == noteStoppedAtEnd
		if st == StatusFailed || st == StatusAborted || stopped {
			if skip, _, _ := x.ctl.Get(i); skip {
				x.ctl.Toggle("skip", i)
			}
		}
	}
}

// carryResumed marks the steps this run leaves out that already finished,
// in sequence, in the run it resumes.
func (x *executor) carryResumed() {
	prev := x.opts.Resumed
	if prev == nil || len(prev.Steps) != len(x.run.Steps) {
		return
	}
	if x.opts.Continue {
		x.continueRun(prev)
		return
	}
	for i := range x.run.Steps {
		if skip, _, _ := x.ctl.Get(i); skip && inSequence(prev.Steps[i]) {
			x.run.Steps[i].Status, x.run.Steps[i].Note = StatusSkipped, NoteCarried
		}
	}
}

// Resumption is a time a run was resumed.
type Resumption struct {
	At time.Time `json:"at"`
	// From is the step it carried on from (0-based).
	From int `json:"from"`
	// How the run had ended.
	Status Status    `json:"status"`
	Error  string    `json:"error,omitempty"`
	Ended  time.Time `json:"ended,omitzero"`
	// RunbookVersion is the version it carried on with; Was, the one before
	// (when they differ).
	RunbookVersion int `json:"runbook_version,omitempty"`
	Was            int `json:"was_version,omitempty"`
}

// continueRun takes up the run being resumed, in place: its start, input
// changes and resumptions; the steps it leaves out that finished in
// sequence, as they went; and, for the steps it does again, their earlier
// tries.
func (x *executor) continueRun(prev *Run) {
	x.run.Started, x.run.InputChanges, x.run.Resumes = prev.Started, prev.InputChanges, ""
	if x.run.Previous == "" {
		x.run.Previous = prev.Previous
	}
	res := Resumption{At: time.Now(), From: max(x.opts.StartAt, 0), Status: prev.Status, Error: prev.Error, Ended: prev.Ended, RunbookVersion: x.opts.RunbookVersion}
	if prev.RunbookVersion != x.opts.RunbookVersion {
		res.Was = prev.RunbookVersion
	}
	x.run.Resumptions = append(slices.Clone(prev.Resumptions), res)
	x.run.Removed = prev.Removed
	for i := range x.run.Steps {
		p := prev.Steps[i]
		if skip, _, _ := x.ctl.Get(i); skip && inSequence(p) {
			p.Carried = true
			x.run.Steps[i] = p
			continue
		}
		if p.Status != StatusPending {
			before := p.Before
			// Its notes stay with the step (the store keeps them by step).
			p.Before, p.Carried, p.Notes = nil, false, nil
			x.run.Steps[i].Before = append(slices.Clone(before), p)
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
	q, done := x.asking(ctx)
	run, err = dc.ConfirmDanger(q, i, step, x.mask(x.View(i).Command), why)
	done()
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
