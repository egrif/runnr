package project

import (
	"slices"
	"time"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/runbook"
	"github.com/egrif/runnr/internal/store"
)

// UnfinishedWithin is how recent a run must be for starting another for
// the same thing to ask first.
const UnfinishedWithin = 7 * 24 * time.Hour

// Unfinished is the latest run of rb for the same thing (its identifier
// inputs' values, as engine.RunIdentifiers gives them: ids), if that run
// is still running or stopped partway, and started within
// UnfinishedWithin; nil otherwise. Starting another asks first. except is
// the run being started (it may already be recorded); dry runs, imported
// runs and runs that stopped before their first step don't count.
func Unfinished(st *store.Store, rb *runbook.Runbook, ids []string, except string) *engine.Run {
	runs, err := st.ListRuns(rb.Name, 50)
	if err != nil {
		return nil
	}
	for i := range runs {
		r := &runs[i]
		if r.ID == except || r.DryRun || !r.Imported.IsZero() || !slices.Equal(engine.RunIdentifiers(r, rb), ids) ||
			(r.Begun.IsZero() && r.Status != engine.StatusRunning) {
			continue
		}
		if r.Status == engine.StatusSucceeded || time.Since(r.Started) > UnfinishedWithin {
			return nil
		}
		full, err := st.LoadRun(r.ID) // the list leaves out the steps
		if err != nil || (r.Status != engine.StatusRunning && engine.ResumePoint(full) < 0) {
			return nil
		}
		return full
	}
	return nil
}

// IdentifiersOf is what engine.RunIdentifiers would give for a run of rb
// with these inputs (an input not given takes its default).
func IdentifiersOf(rb *runbook.Runbook, vars map[string]string) []string {
	r := &engine.Run{Identifiers: rb.IdentifierInputs(), Inputs: map[string]string{}}
	for _, in := range rb.Inputs {
		if v, ok := vars[in.Name]; ok {
			r.Inputs[in.Name] = v
		} else if in.Default != "" {
			r.Inputs[in.Name] = in.Default
		}
	}
	return engine.RunIdentifiers(r, rb)
}
