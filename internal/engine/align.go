package engine

import (
	"slices"

	"github.com/egrif/runnr/internal/runbook"
)

// Resuming a run with a runbook that has changed since: the run carries on
// with the new version. Its steps are matched to the new ones by name (in
// order, for names used more than once); a matched step keeps how it went,
// a new step is pending, and a step that's no longer in the runbook is kept
// in the run's record (Removed).

// RunbookChanged reports whether rb's steps differ from the run's (by count
// or name).
func RunbookChanged(r *Run, rb *runbook.Runbook) bool {
	if len(r.Steps) != len(rb.Steps) {
		return true
	}
	for i, s := range rb.Steps {
		if r.Steps[i].Name != s.Name {
			return true
		}
	}
	return false
}

// AlignRun is r with its steps matched to rb's. moved maps each of r's step
// indexes to rb's (-1: no longer in the runbook).
func AlignRun(r *Run, rb *runbook.Runbook) (aligned *Run, moved map[int]int) {
	out := *r
	out.Steps = make([]StepResult, len(rb.Steps))
	out.Removed = slices.Clone(r.Removed)
	moved = map[int]int{}
	used := make([]bool, len(r.Steps))
	for j, s := range rb.Steps {
		out.Steps[j] = StepResult{Index: j, Name: s.Name, Section: s.Section, Owner: s.Owner, Kind: stepKind(s), Status: StatusPending, Optional: s.Optional}
		for i, old := range r.Steps {
			if !used[i] && old.Name == s.Name {
				used[i] = true
				old.Index, old.Section, old.Owner, old.Optional = j, s.Section, s.Owner, s.Optional
				out.Steps[j] = old
				moved[i] = j
				break
			}
		}
	}
	for i, old := range r.Steps {
		if !used[i] {
			moved[i] = -1
			if old.Status != StatusPending {
				// Its notes become the run's (store.MoveRunNotes).
				old.Notes = nil
				out.Removed = append(out.Removed, old)
			}
		}
	}
	return &out, moved
}
