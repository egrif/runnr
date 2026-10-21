package engine

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/egrif/runnr/internal/runbook"
)

// Changing a run's inputs while it goes on. The new values are checked at
// once and take effect before the run's next command (the next step, or
// the step waiting now, if it hasn't run its command yet: the interactor
// starts it again), so a command that's running never sees its values
// change. Each change is recorded with the run; a resume uses the new
// values.

// InputSetter is implemented by sessions whose inputs can be changed.
type InputSetter interface {
	// SetInputs checks changes (input name → new value) and queues them.
	SetInputs(changes map[string]string, by string) (InputsChanged, error)
	// InputDefs are the runbook's inputs; Inputs, their values now
	// (secrets masked).
	InputDefs() []runbook.Input
	Inputs() map[string]string
}

func (x *executor) InputDefs() []runbook.Input { return x.rb.Inputs }
func (x *executor) Inputs() map[string]string {
	x.vmu.RLock()
	defer x.vmu.RUnlock()
	return maps.Clone(x.run.Inputs)
}

var _ InputSetter = (*executor)(nil)

// InputChange is a change to an input, as recorded with the run.
type InputChange struct {
	At   time.Time `json:"at"`
	Name string    `json:"name"`
	// Old and New are masked for secret inputs.
	Old string `json:"old"`
	New string `json:"new"`
	By  string `json:"by,omitempty"`
	// Step is the step the run was on when it took effect (-1: before the
	// first; len(steps): after the last).
	Step int `json:"step"`
}

// InputsChanged says what a change will do.
type InputsChanged struct {
	Changed []string `json:"changed"`
	// Stale lists, per input changed, the steps that already ran using it
	// (1-based).
	Stale map[string][]int `json:"stale,omitempty"`
	// Replaces are inputs a step had published a value for: the new value
	// takes its place.
	Replaces []string `json:"replaces,omitempty"`
}

type inputState struct {
	mu      sync.Mutex
	pending map[string]string
	by      map[string]string
	// published: variables a step (or a shell) has published, so an
	// input of that name had been overridden.
	published map[string]bool
}

func (x *executor) SetInputs(changes map[string]string, by string) (InputsChanged, error) {
	var res InputsChanged
	if x.run.Begun.IsZero() {
		return res, errors.New("the run hasn't started its steps yet: it's still settling its inputs")
	}
	x.em.Lock()
	cur := x.cur
	x.em.Unlock()
	if cur >= len(x.rb.Steps) {
		return res, errors.New("the run has finished its steps")
	}
	defs := map[string]runbook.Input{}
	for _, in := range x.rb.Inputs {
		defs[in.Name] = in
	}
	x.vmu.RLock()
	vars := maps.Clone(x.vars)
	x.vmu.RUnlock()
	x.inputs.mu.Lock()
	defer x.inputs.mu.Unlock()
	maps.Copy(vars, x.inputs.pending)
	var errs []error
	for name, v := range changes {
		in, ok := defs[name]
		switch {
		case !ok:
			errs = append(errs, fmt.Errorf("%q isn't one of this runbook's inputs", name))
			continue
		case v == "" && in.Required:
			errs = append(errs, fmt.Errorf("input %q is required", name))
			continue
		case v != "" && len(in.Choices) > 0 && !slices.Contains(in.Choices, v):
			errs = append(errs, fmt.Errorf("input %q: %q is not one of %s", name, v, strings.Join(in.Choices, ", ")))
			continue
		}
		if v == "" {
			v = in.Default
		}
		vars[name] = v
	}
	if err := x.rb.CheckWindowInputs(vars); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return res, errors.Join(errs...)
	}
	if x.inputs.pending == nil {
		x.inputs.pending, x.inputs.by = map[string]string{}, map[string]string{}
	}
	for name := range changes {
		x.vmu.RLock()
		same := x.vars[name] == vars[name] && !x.inputs.published[name]
		x.vmu.RUnlock()
		if _, queued := x.inputs.pending[name]; same && !queued {
			continue
		}
		x.inputs.pending[name], x.inputs.by[name] = vars[name], by
		res.Changed = append(res.Changed, name)
		if x.inputs.published[name] {
			res.Replaces = append(res.Replaces, name)
		}
		if st := x.usedBy(name, cur); len(st) > 0 {
			if res.Stale == nil {
				res.Stale = map[string][]int{}
			}
			res.Stale[name] = st
		}
	}
	slices.Sort(res.Changed)
	slices.Sort(res.Replaces)
	return res, nil
}

// usedBy lists the steps before cur that ran and refer to variable name
// (1-based).
func (x *executor) usedBy(name string, cur int) []int {
	re := varRef(name)
	var out []int
	for i := 0; i < cur && i < len(x.run.Steps); i++ {
		switch x.run.Steps[i].Status {
		case StatusPending, StatusSkipped:
			continue
		}
		b, _ := yaml.Marshal(x.def(i))
		if re.Match(b) {
			out = append(out, i+1)
		}
	}
	return out
}

// varRef matches a reference to a variable in a step: {{.name}} (or in a
// longer template) or $name / ${name} in its commands.
func varRef(name string) *regexp.Regexp {
	q := regexp.QuoteMeta(name)
	return regexp.MustCompile(`\{\{[^}]*\.` + q + `\b|\$\{?` + q + `\b`)
}

// takeInputs applies queued input changes (on the run's own goroutine,
// before a command).
func (x *executor) takeInputs() {
	x.inputs.mu.Lock()
	pending, by := x.inputs.pending, x.inputs.by
	x.inputs.pending, x.inputs.by = nil, nil
	for name := range pending {
		delete(x.inputs.published, name)
	}
	x.inputs.mu.Unlock()
	if len(pending) == 0 {
		return
	}
	x.em.Lock()
	cur := x.cur
	x.em.Unlock()
	secret := map[string]bool{}
	for _, in := range x.rb.Inputs {
		secret[in.Name] = in.Secret
	}
	names := slices.Sorted(maps.Keys(pending))
	x.vmu.Lock()
	for _, name := range names {
		v := pending[name]
		old := x.run.Inputs[name]
		x.vars[name] = v
		shown := v
		if secret[name] {
			x.secretVars[name] = true
			if v != "" {
				x.addSecret(v, true)
				shown = x.maskedValue(v)
			}
		} else {
			delete(x.secretVars, name)
		}
		x.run.Inputs[name] = shown
		x.run.InputChanges = append(x.run.InputChanges, InputChange{At: time.Now(), Name: name, Old: old, New: shown, By: by[name], Step: cur})
	}
	x.vmu.Unlock()
	if x.run.Window != nil {
		x.run.Window.Target = x.rb.WindowTarget(x.vars)
	}
	var parts []string
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s = %s", name, x.run.Inputs[name]))
	}
	x.notify(Event{Kind: EventOutput, Step: cur, Stream: "info", Text: "inputs changed: " + strings.Join(parts, ", ")})
	x.refreshState()
	x.notify(Event{Kind: EventInputs, Run: x.snapshot()})
	x.save()
}

// markPublished notes variables a step published (they override inputs of
// the same name until an input is changed).
func (x *executor) markPublished(names ...string) {
	x.inputs.mu.Lock()
	defer x.inputs.mu.Unlock()
	if x.inputs.published == nil {
		x.inputs.published = map[string]bool{}
	}
	for _, n := range names {
		x.inputs.published[n] = true
	}
}
