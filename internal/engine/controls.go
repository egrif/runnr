package engine

import (
	"slices"
	"sync"
)

// Controls are the operator's choices for a run: which steps to skip and
// where to pause. A live run's controls can change while it runs; the
// engine reads them as it reaches each step.
type Controls struct {
	mu      sync.Mutex
	stepAll bool
	// dangerous: pause before every dangerous step.
	dangerous bool
	skip      map[int]bool
	before    map[int]bool
	after     map[int]bool
	// suspend: a request to suspend the run (suspend.go).
	suspend   *SuspendError
	onSuspend func(*SuspendError)
}

// ControlsView is the JSON form of Controls. Step numbers are 0-based.
type ControlsView struct {
	StepAll bool `json:"step_all"`
	// PauseDangerous pauses before every dangerous step.
	PauseDangerous bool  `json:"pause_dangerous"`
	Skip           []int `json:"skip"`
	PauseBefore    []int `json:"pause_before"`
	PauseAfter     []int `json:"pause_after"`
	// Suspend is a request to suspend the run not yet taken (read only:
	// Set leaves it).
	Suspend *SuspendError `json:"suspend,omitempty"`
}

func NewControls(v ControlsView) *Controls {
	c := &Controls{}
	c.Set(v)
	return c
}

func set(xs []int) map[int]bool {
	m := map[int]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func list(m map[int]bool) []int {
	out := []int{}
	for k, on := range m {
		if on {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

func (c *Controls) Set(v ControlsView) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stepAll, c.dangerous, c.skip, c.before, c.after = v.StepAll, v.PauseDangerous, set(v.Skip), set(v.PauseBefore), set(v.PauseAfter)
}

func (c *Controls) View() ControlsView {
	c.mu.Lock()
	defer c.mu.Unlock()
	return ControlsView{StepAll: c.stepAll, PauseDangerous: c.dangerous, Skip: list(c.skip), PauseBefore: list(c.before), PauseAfter: list(c.after), Suspend: c.suspend}
}

// Get reports step i's settings; before includes step-through-all.
func (c *Controls) Get(i int) (skip, before, after bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.skip[i], c.stepAll || c.before[i], c.after[i]
}

// PauseDangerous reports whether the run pauses before dangerous steps.
func (c *Controls) PauseDangerous() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dangerous
}

func (c *Controls) SetPauseDangerous(on bool) {
	c.mu.Lock()
	c.dangerous = on
	c.mu.Unlock()
}

func (c *Controls) SetStepAll(on bool) {
	c.mu.Lock()
	c.stepAll = on
	c.mu.Unlock()
}

// Toggle flips one setting ("skip", "before" or "after") for step i.
func (c *Controls) Toggle(what string, i int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := map[string]map[int]bool{"skip": c.skip, "before": c.before, "after": c.after}[what]
	if m != nil {
		m[i] = !m[i]
	}
}
