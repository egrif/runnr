package engine

import (
	"maps"
	"slices"
	"strings"

	"github.com/egrif/runnr/internal/runbook"
)

// Previewer shows a runbook as a run would see it before it starts: text
// rendered with the inputs (values that steps publish later show as
// <name>, secrets masked), and how runnr would run each command.
type Previewer struct {
	x        *executor
	hasFuncs bool
}

// NewPreviewer previews rb with the given inputs (else their defaults) and,
// optionally, an earlier run's state. hasFuncs says whether the run will
// have functions to load (from its libraries or the runbook's own).
func NewPreviewer(rb *runbook.Runbook, vars, state map[string]string, hasFuncs bool) *Previewer {
	x := &executor{rb: rb, ui: Unattended{}, cur: -1, reopenTo: -1, pauseAt: -1, windowOpenedBy: -1, vars: map[string]string{}, secretVars: map[string]bool{}, run: &Run{}, masker: runbook.NewMasker(rb)}
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
	return &Previewer{x: x, hasFuncs: hasFuncs || len(rb.Functions) > 0}
}

// Plan is every step's view.
func (p *Previewer) Plan() []StepView { return p.x.Plan() }

// Render fills in a template as far as the inputs allow, masking secrets.
func (p *Previewer) Render(text string) string { return p.x.mask(p.x.lenient(text)) }

// Invocation is how runnr runs one command: Shell -c Script, in Dir, with
// the environment it adds to its own. When the runbook loads profile files
// or functions, Script sources them and then StepFile, which holds the
// command; otherwise Script is the command and StepFile is empty. Paths of
// the temporary files runnr writes are placeholders (XXXXXX).
type Invocation struct {
	Shell    string   `json:"shell"`
	Dir      string   `json:"dir,omitempty"`
	Timeout  string   `json:"timeout,omitempty"`
	TTY      bool     `json:"tty,omitempty"`
	Values   []string `json:"values"` // inputs and published values, as $name
	Env      []string `json:"env"`    // other variables runnr sets (NAME or NAME=value)
	Script   string   `json:"script"`
	StepFile string   `json:"step_file,omitempty"`
}

// Invocation shows how step i would run command (as rendered). For a way,
// pass step.WithWay(way); for a check, CheckInvocation.
func (p *Previewer) Invocation(i int, step runbook.Step, command string) Invocation {
	rb := p.x.rb
	inv := Invocation{Shell: step.Shell, Timeout: step.Timeout, TTY: step.TTY, Dir: p.x.lenient(step.Dir)}
	if inv.Shell == "" {
		inv.Shell = rb.Shell
	}
	if inv.Shell == "" {
		inv.Shell = "sh"
	}
	known := maps.Clone(p.x.vars)
	for _, in := range rb.Inputs {
		known[in.Name] = ""
	}
	for _, st := range rb.Steps[:min(i, len(rb.Steps))] {
		for _, n := range append(append(slices.Clone(st.Outputs), st.SecretOutputs...), st.Capture) {
			if n != "" {
				known[n] = ""
			}
		}
	}
	inv.Values = slices.Sorted(maps.Keys(known))
	inv.Env = []string{"RUNNR_RUN_ID", "RUNNR_RUNBOOK"}
	if p.hasFuncs {
		inv.Env = append(inv.Env, "RUNNR_FUNCTIONS")
	}
	inv.Env = append(inv.Env, "RUNNR_OUTPUT", "RUNNR_SECRET")
	for _, m := range []map[string]string{rb.Env, step.Env} {
		for _, k := range slices.Sorted(maps.Keys(m)) {
			inv.Env = append(inv.Env, k+"="+p.Render(m[k]))
		}
	}
	body := withOutputs(step, command)
	if len(rb.Profile) == 0 && !p.hasFuncs {
		inv.Script = body
		return inv
	}
	funcFile := ""
	if p.hasFuncs {
		funcFile = "/tmp/runnr-functions-XXXXXX.sh"
	}
	inv.Script = prelude(rb.Profile, "~", funcFile, "/tmp/runnr-step-XXXXXX/step.sh", displayQuote)
	inv.StepFile = body
	return inv
}

// CheckInvocation shows how step i would run one of its checks.
func (p *Previewer) CheckInvocation(i int, step runbook.Step, command string) Invocation {
	return p.Invocation(i, checkStep(step, command), command)
}

// displayQuote quotes like shQuote, but leaves a leading ~/ outside the
// quotes (it's the home directory the real path starts with).
func displayQuote(s string) string {
	if rest, ok := strings.CutPrefix(s, "~/"); ok {
		return "~/" + shQuote(rest)
	}
	return shQuote(s)
}
