package engine

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/egrif/runnr/internal/runbook"
)

// The executor is the Session handed to Binder interactors.
var _ Session = (*executor)(nil)

func (x *executor) View(i int) StepView {
	s := x.rb.Steps[i]
	v := StepView{
		Index: i, Name: s.Name, Section: s.Section, Owner: s.Owner, Kind: x.run.Steps[i].Kind,
		When: s.When, Confirm: s.Confirm, Always: s.Always, Timeout: s.Timeout, Retries: s.Retries,
		Outputs: s.Outputs, SecretOutputs: s.SecretOutputs,
	}
	text := s.Run
	if s.IsManual() {
		text = s.Manual
	}
	v.Command = x.mask(x.lenient(text))
	v.Expect = x.mask(x.lenient(s.Expect))
	v.Dir = x.lenient(s.Dir)
	return v
}

func (x *executor) Controls() *Controls  { return x.ctl }
func (x *executor) Previous() *Run       { return x.opts.Previous }
func (x *executor) Mask(s string) string { return x.mask(s) }

func (x *executor) Plan() []StepView {
	out := make([]StepView, len(x.rb.Steps))
	for i := range out {
		out[i] = x.View(i)
	}
	return out
}

func (x *executor) State() map[string]string {
	m := make(map[string]string, len(x.vars))
	for k, v := range x.vars {
		if x.secretVars[k] {
			v = "****"
		}
		m[k] = v
	}
	return m
}

func (x *executor) CanShell() bool {
	_, ok := x.ui.(TermHost)
	return ok
}

func (x *executor) Shell(ctx context.Context, idx int) error { return x.openShell(ctx, idx) }

// shellViaUI opens a shell through the interactor's wrapper if it has one.
func (x *executor) shellViaUI(ctx context.Context, idx int) error {
	if o, ok := x.ui.(ShellOpener); ok {
		return o.OpenShell(ctx, idx)
	}
	return x.openShell(ctx, idx)
}

// openShell runs an interactive shell with the environment step idx would
// get (idx -1: no step-specific env or dir), then takes in anything
// published to $RUNNR_OUTPUT/$RUNNR_SECRET.
func (x *executor) openShell(ctx context.Context, idx int) error {
	var step runbook.Step
	title := "shell"
	if idx >= 0 && idx < len(x.rb.Steps) {
		step = x.rb.Steps[idx]
		title = fmt.Sprintf("shell · step %d: %s", idx+1, step.Name)
	}
	extra, err := x.runnrEnv(step)
	if err != nil {
		return err
	}
	dir, err := x.render(step.Dir)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "runnr-shell-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	outFile, secFile := filepath.Join(tmp, "output"), filepath.Join(tmp, "secret")
	extra = append(extra, "RUNNR_OUTPUT="+outFile, "RUNNR_SECRET="+secFile, "RUNNR_SHELL=1")

	_, transcript, err := x.inTerminal(ctx, idx, title, "", "", extra, dir)
	if t := strings.TrimSpace(x.mask(transcript)); t != "" && idx >= 0 {
		x.run.Steps[idx].Shells = append(x.run.Steps[idx].Shells, t)
		x.save()
	}
	if err != nil {
		return err
	}
	var res cmdResult
	x.readOutputs(idx, &res, outFile, secFile)
	x.apply(runbook.Step{}, res)
	var names []string
	for k := range res.outputs {
		names = append(names, k)
	}
	for k := range res.secrets {
		names = append(names, k+" (secret)")
	}
	if len(names) > 0 {
		slices.Sort(names)
		x.notify(Event{Kind: EventOutput, Step: idx, Stream: "info", Text: "shell published: " + strings.Join(names, ", ")})
		x.run.State = x.State()
	}
	return nil
}

// restoreState seeds variables from an earlier run's state. Inputs win;
// masked secrets can't be restored and are reported.
func (x *executor) restoreState() {
	var missing []string
	for k, v := range x.opts.State {
		if _, isInput := x.run.Inputs[k]; isInput {
			continue
		}
		if v == "****" {
			missing = append(missing, k)
			continue
		}
		x.vars[k] = v
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		x.notify(Event{Kind: EventOutput, Step: -1, Stream: "info", Text: "not restored from the earlier run (secrets): " + strings.Join(missing, ", ") + " — steps marked always re-derive them"})
	}
}

// lenient renders a template for display: unknown values show as <name>,
// and a broken template is shown as written.
func (x *executor) lenient(text string) string {
	vars := maps.Clone(x.vars)
	for _, m := range varRefRe.FindAllStringSubmatch(text, -1) {
		if _, ok := vars[m[1]]; !ok {
			vars[m[1]] = "<" + m[1] + ">"
		}
	}
	out, err := runbook.Render(text, vars)
	if err != nil {
		return text
	}
	return out
}

// Preview returns a runbook's plan rendered with the given inputs (and
// input defaults) and, optionally, an earlier run's state (as for a
// resume); values not known yet show as <name>.
func Preview(rb *runbook.Runbook, vars, state map[string]string) []StepView {
	x := &executor{rb: rb, ui: Unattended{}, vars: map[string]string{}, secretVars: map[string]bool{}, run: &Run{}}
	for k, v := range state {
		if v != "****" {
			x.vars[k] = v
		}
	}
	for _, in := range rb.Inputs {
		v := vars[in.Name]
		if v == "" {
			v = in.Default
		}
		if v != "" {
			x.vars[in.Name] = v
		}
		if in.Secret && v != "" {
			x.secrets = append(x.secrets, v)
		}
	}
	for _, s := range rb.Steps {
		kind := "run"
		if s.IsManual() {
			kind = "manual"
		} else if s.TTY {
			kind = "tty"
		}
		x.run.Steps = append(x.run.Steps, StepResult{Kind: kind})
	}
	return x.Plan()
}
