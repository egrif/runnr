package engine

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/egrif/runnr/internal/runbook"
)

// ErrStepEdited, returned by an interactor while a step waits on it, means
// the step was edited: it starts again with its new definition.
var ErrStepEdited = errors.New("step edited")

// StepEdit records a change made to a step during a run.
type StepEdit struct {
	Step int          `json:"step"`
	At   time.Time    `json:"at"`
	Def  runbook.Step `json:"def"`
	// Before is the step as the run started with it, for saving the edit
	// back only if the runbook still has that step.
	Before runbook.Step `json:"before"`
}

// Editor is the part of a Session that changes steps during a run.
type Editor interface {
	// StepDef is step idx's definition as the run will use it.
	StepDef(idx int) runbook.Step
	// EditStep replaces step idx (the current step or a later one).
	EditStep(idx int, s runbook.Step) error
	// Current is the step running or waiting now (-1 before the first).
	Current() int
	// Edits are the changes made so far.
	Edits() []StepEdit
	// MarkEditsSaved records that the run's edits are in the runbook now.
	MarkEditsSaved(version int)
}

var _ Editor = (*executor)(nil)

func (x *executor) def(i int) runbook.Step {
	x.em.Lock()
	defer x.em.Unlock()
	return x.rb.Steps[i]
}

func (x *executor) StepDef(i int) runbook.Step { return x.def(i) }

func (x *executor) Current() int {
	x.em.Lock()
	defer x.em.Unlock()
	return x.cur
}

func (x *executor) EditStep(i int, s runbook.Step) error {
	x.em.Lock()
	defer x.em.Unlock()
	if i < 0 || i >= len(x.rb.Steps) {
		return fmt.Errorf("no step %d", i+1)
	}
	if i < x.cur {
		return fmt.Errorf("step %d has already run; edits apply to the current step or later ones", i+1)
	}
	old := x.rb.Steps[i]
	if s.Section == "" {
		s.Section = old.Section // sections carry over from the step before
	}
	rb := *x.rb
	rb.Steps = slices.Clone(x.rb.Steps)
	rb.Steps[i] = s
	if err := rb.Validate(); err != nil {
		return err
	}
	if reflect.DeepEqual(old, s) {
		return nil
	}
	before := old
	for _, e := range x.edits {
		if e.Step == i {
			before = e.Before
			break
		}
	}
	x.rb.Steps[i] = s
	x.edits = append(x.edits, StepEdit{Step: i, At: time.Now(), Def: s, Before: before})
	x.editsSaved = 0
	return nil
}

func (x *executor) Edits() []StepEdit {
	x.em.Lock()
	defer x.em.Unlock()
	return slices.Clone(x.edits)
}

func (x *executor) MarkEditsSaved(version int) {
	x.em.Lock()
	defer x.em.Unlock()
	x.editsSaved = version
}

// withEdits adds the run's edits to a snapshot.
func (x *executor) withEdits(r *Run) {
	x.em.Lock()
	defer x.em.Unlock()
	r.Edits, r.EditsSaved = slices.Clone(x.edits), x.editsSaved
	for _, e := range x.edits {
		s := &r.Steps[e.Step]
		s.Edited, s.Name, s.Section, s.Owner = true, e.Def.Name, e.Def.Section, e.Def.Owner
		if s.Status == StatusPending {
			s.Kind = stepKind(e.Def)
		}
	}
}

// LatestEdits returns each edited step's last edit, in step order.
func LatestEdits(edits []StepEdit) []StepEdit {
	last := map[int]StepEdit{}
	for _, e := range edits {
		if prev, ok := last[e.Step]; ok {
			e.Before = prev.Before
		}
		last[e.Step] = e
	}
	out := make([]StepEdit, 0, len(last))
	for _, e := range last {
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b StepEdit) int { return a.Step - b.Step })
	return out
}

// ApplyEdits puts a run's step edits into rb (the runbook as saved now). A
// step that changed in the runbook since the run started is a conflict.
func ApplyEdits(rb *runbook.Runbook, edits []StepEdit) (*runbook.Runbook, error) {
	out := *rb
	out.Steps = slices.Clone(rb.Steps)
	for _, e := range LatestEdits(edits) {
		if e.Step >= len(out.Steps) || !reflect.DeepEqual(out.Steps[e.Step], e.Before) {
			return nil, fmt.Errorf("step %d (%s) has changed in the runbook since this run started; copy the edit across by hand", e.Step+1, e.Before.Name)
		}
		out.Steps[e.Step] = e.Def
	}
	return &out, out.Validate()
}

// VersionAdopter moves a live run onto a newer saved version of its
// runbook, for the steps it hasn't run yet.
type VersionAdopter interface {
	AdoptVersion(version int, steps []runbook.Step) (changed []int, err error)
}

var _ VersionAdopter = (*executor)(nil)

// AdoptVersion replaces the current step and later ones with steps (the
// runbook's steps at version) where they differ, and records the run as on
// that version. The runbook must have the same steps (count); edits made in
// this run that aren't saved yet must agree with it.
func (x *executor) AdoptVersion(version int, steps []runbook.Step) ([]int, error) {
	changed, err := x.adoptVersion(version, steps)
	if err == nil {
		x.save()
	}
	return changed, err
}

func (x *executor) adoptVersion(version int, steps []runbook.Step) ([]int, error) {
	x.em.Lock()
	defer x.em.Unlock()
	if len(steps) != len(x.rb.Steps) {
		return nil, fmt.Errorf("version %d has %d steps, this run has %d: abort this run, then run version %d", version, len(steps), len(x.rb.Steps), version)
	}
	cur := max(x.cur, 0)
	if x.editsSaved == 0 {
		for _, e := range LatestEdits(x.edits) {
			if !reflect.DeepEqual(steps[e.Step], e.Def) {
				return nil, fmt.Errorf("step %d (%s) was edited in this run and isn't saved, and version %d has it differently: save or redo the edit first", e.Step+1, e.Def.Name, version)
			}
		}
	}
	rb := *x.rb
	rb.Steps = slices.Clone(x.rb.Steps)
	var changed []int
	for i := cur; i < len(steps); i++ {
		if !reflect.DeepEqual(rb.Steps[i], steps[i]) {
			rb.Steps[i] = steps[i]
			changed = append(changed, i)
		}
	}
	if err := rb.Validate(); err != nil {
		return nil, fmt.Errorf("version %d: %w", version, err)
	}
	for _, i := range changed {
		x.edits = append(x.edits, StepEdit{Step: i, At: time.Now(), Def: steps[i], Before: x.rb.Steps[i]})
		x.rb.Steps[i] = steps[i]
	}
	// The run is on that version now: what it changed is in the runbook already.
	x.run.RunbookVersion = version
	x.editsSaved = version
	return changed, nil
}

// MarkEdited marks the steps a run's edits changed (for a record loaded
// before the run saved them).
func MarkEdited(r *Run) {
	for _, e := range r.Edits {
		if e.Step < len(r.Steps) {
			s := &r.Steps[e.Step]
			s.Edited, s.Name = true, e.Def.Name
		}
	}
}
