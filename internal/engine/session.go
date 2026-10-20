package engine

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egrif/runnr/internal/runbook"
)

// The executor is the Session handed to Binder interactors.
var _ Session = (*executor)(nil)

func (x *executor) View(i int) StepView {
	s := x.def(i)
	v := StepView{
		Index: i, Name: s.Name, Section: s.Section, Owner: s.Owner, Kind: x.run.Steps[i].Kind,
		When: s.When, Confirm: s.Confirm, Dangerous: s.Dangerous, Always: s.Always, Optional: s.Optional, Timeout: s.Timeout, Retries: s.Retries,
		Outputs: s.Outputs, SecretOutputs: s.SecretOutputs,
	}
	v.Background, v.StopAfter = s.Background, s.StopAfter
	v.Choose, v.Window = s.Choose, s.Window
	if d, ok := x.rb.PlannedOffset(i, x.vars); ok {
		sec := int(d.Seconds())
		v.Offset, v.OffsetSec = FormatOffset(d), &sec
	}
	for _, p := range x.rb.PlanPoints(x.vars) {
		if p.Step == i && p.Section == "" && p.Ignored {
			v.IgnoredOffset = FormatOffset(p.Offset)
		}
	}
	var b strings.Builder
	if len(s.Ways) > 1 {
		pref := s.PreferredWay()
		for j, w := range s.Ways {
			cmd := x.wayCommand(s, w, nil)
			v.Ways = append(v.Ways, WayView{Name: w.Name, Kind: w.Kind(), Command: x.mask(cmd), Preferred: j == pref})
			mark := ""
			if j == pref {
				mark = " (preferred)"
			}
			fmt.Fprintf(&b, "%d) %s%s\n    %s\n", j+1, w.Name, mark, strings.ReplaceAll(strings.TrimSpace(cmd), "\n", "\n    "))
		}
		if s.Repeat != nil {
			v.Repeat = fmt.Sprintf("every %s, for up to %s", s.Repeat.Every, s.Repeat.Timeout)
		}
	} else {
		b.WriteString(x.wayCommand(s, s.WayList()[0], &v))
	}
	v.Command = x.mask(b.String())
	v.Expect = x.mask(x.lenient(s.Expect))
	v.Description = x.mask(x.lenient(s.Description))
	v.Dir = x.lenient(s.Dir)
	return v
}

// wayCommand is what a way would run (or the instructions it gives),
// rendered as far as the run's state allows. With v, checks are listed on it.
func (x *executor) wayCommand(s runbook.Step, w runbook.Way, v *StepView) string {
	var b strings.Builder
	switch w.Kind() {
	case "manual":
		b.WriteString(x.lenient(w.Manual))
	case "checks":
		for _, c := range w.Checks {
			cmd := x.mask(x.lenient(c.Run))
			if v != nil {
				v.Checks = append(v.Checks, CheckView{Name: c.Name, Command: cmd})
			}
			fmt.Fprintf(&b, "✓ %s\n    %s\n", c.Name, strings.ReplaceAll(strings.TrimSpace(cmd), "\n", "\n    "))
		}
		if s.Repeat != nil {
			rep := fmt.Sprintf("every %s, for up to %s", s.Repeat.Every, s.Repeat.Timeout)
			if v != nil {
				v.Repeat = rep
			}
			fmt.Fprintf(&b, "(repeated %s until all pass)\n", rep)
		}
	default:
		b.WriteString(x.lenient(w.Run))
	}
	return b.String()
}

// FormatOffset shows a window offset as -2w, -30m, +0, +1d4h.
func FormatOffset(d time.Duration) string {
	if d < 0 {
		return runbook.FormatSpan(d)
	}
	return "+" + runbook.FormatSpan(d)
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
	tmp, err := x.tempDir("runnr-shell-")
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
	return NewPreviewer(rb, vars, state, false).Plan()
}
