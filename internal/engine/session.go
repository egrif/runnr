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
	s := x.def(i)
	v := StepView{
		Index: i, Name: s.Name, Section: s.Section, Owner: s.Owner, Kind: x.run.Steps[i].Kind,
		When: s.When, Confirm: s.Confirm, Dangerous: s.Dangerous, Always: s.Always, Timeout: s.Timeout, Retries: s.Retries,
		Outputs: s.Outputs, SecretOutputs: s.SecretOutputs,
	}
	v.Background, v.StopAfter = s.Background, s.StopAfter
	var b strings.Builder
	switch s.Kind() {
	case "manual":
		b.WriteString(x.lenient(s.Manual))
	case "checks":
		for _, c := range s.Checks {
			cmd := x.mask(x.lenient(c.Run))
			v.Checks = append(v.Checks, CheckView{Name: c.Name, Command: cmd})
			fmt.Fprintf(&b, "✓ %s\n    %s\n", c.Name, strings.ReplaceAll(strings.TrimSpace(cmd), "\n", "\n    "))
		}
		if s.Repeat != nil {
			v.Repeat = fmt.Sprintf("every %s, for up to %s", s.Repeat.Every, s.Repeat.Timeout)
			fmt.Fprintf(&b, "(repeated %s until all pass)\n", v.Repeat)
		}
	case "alternatives":
		for j, a := range s.Alternatives {
			av := AltView{Name: a.Name, When: a.When}
			fmt.Fprintf(&b, "%d) %s\n", j+1, a.Name)
			if a.Run != "" {
				av.Command = x.mask(x.lenient(a.Run))
				fmt.Fprintf(&b, "    %s\n", strings.ReplaceAll(strings.TrimSpace(av.Command), "\n", "\n    "))
			}
			for k, sub := range a.Steps {
				av.Steps = append(av.Steps, sub.Name)
				fmt.Fprintf(&b, "    %d. %s\n", k+1, sub.Name)
			}
			v.Alternatives = append(v.Alternatives, av)
		}
	default:
		b.WriteString(x.lenient(s.Run))
	}
	v.Command = x.mask(b.String())
	v.Expect = x.mask(x.lenient(s.Expect))
	v.Description = x.mask(x.lenient(s.Description))
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
			v = x.maskedValue(v)
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
		step = x.def(idx)
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
		x.refreshState()
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
	x := &executor{rb: rb, ui: Unattended{}, vars: map[string]string{}, secretVars: map[string]bool{}, run: &Run{}, masker: runbook.NewMasker(rb)}
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
			x.addSecret(v, true)
		}
	}
	for _, s := range rb.Steps {
		x.run.Steps = append(x.run.Steps, StepResult{Kind: stepKind(s)})
	}
	return x.Plan()
}
