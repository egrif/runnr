package htmlview

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/runbook"
)

// The "as runnr runs it" view is a script to paste into one shell (the
// runbook's): what runnr gives every step (inputs, its own variables, the
// runbook's environment, profile files, functions, the setup steps), then
// each step run the way runnr runs it, in a shell of its own (a subshell),
// with what it publishes coming back as environment variables for the
// steps after it.

// scripter builds those scripts for one runbook.
type scripter struct {
	rb    *runbook.Runbook
	p     *engine.Previewer
	vars  map[string]string
	libs  []*runbook.Library
	shell string
}

// base is the setup that doesn't depend on the steps: inputs, runnr's own
// variables, the runbook's environment, profile files and functions.
func (s *scripter) base() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Setup: what runnr gives every step. Paste it first, into one %s.\n", s.shell)
	b.WriteString("[ -n \"$ZSH_VERSION\" ] && setopt interactive_comments no_bang_hist; [ -n \"$BASH_VERSION\" ] && set +H\n")
	if len(s.rb.Inputs) > 0 {
		b.WriteString("\n# Inputs\n")
	}
	for _, in := range s.rb.Inputs {
		if in.Secret {
			fmt.Fprintf(&b, "printf '%[1]s (secret): '; read -rs %[1]s; echo; export %[1]s\n", in.Name)
			continue
		}
		v, ok := s.vars[in.Name]
		if !ok || v == "" {
			v = in.Default
		}
		line := fmt.Sprintf("export %s=%s", in.Name, q(v))
		if d := firstLine(in.Description); d != "" {
			line += "  # " + d
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n# runnr's own: steps write name=value lines to $RUNNR_OUTPUT to publish values\n")
	fmt.Fprintf(&b, "export RUNNR_RUNBOOK=%s RUNNR_RUN_ID=by-hand\n", q(s.rb.Name))
	b.WriteString("export RUNNR_OUTPUT=\"$(mktemp)\"; export RUNNR_SECRET=\"$RUNNR_OUTPUT\"\n")
	if len(s.rb.Env) > 0 {
		b.WriteString("\n# The runbook's environment\n")
		for _, k := range slices.Sorted(maps.Keys(s.rb.Env)) {
			fmt.Fprintf(&b, "export %s=%s\n", k, q(s.p.Render(s.rb.Env[k])))
		}
	}
	if len(s.rb.Profile) > 0 {
		b.WriteString("\n# Sourced before every step\n")
		for _, f := range s.rb.Profile {
			fmt.Fprintf(&b, "[ -r %[1]s ] && . %[1]s >/dev/null 2>&1\n", pathQ(f))
		}
	}
	// Libraries in order, then the runbook's own (which win), as runnr loads them.
	var names []string
	var fns strings.Builder
	for _, l := range s.libs {
		if len(l.Functions) > 0 {
			names = append(names, l.Name)
			fns.WriteString(runbook.FunctionScript(l.Functions))
		}
	}
	if len(s.rb.Functions) > 0 {
		names = append(names, "the runbook's own")
		fns.WriteString(runbook.FunctionScript(s.rb.Functions))
	}
	if fns.Len() > 0 {
		fmt.Fprintf(&b, "\n# Functions (%s)\n%s", strings.Join(names, ", "), fns.String())
	}
	return strings.TrimRight(b.String(), "\n")
}

// setup is base plus the setup (always) steps before step upto (all of them
// when upto is past the end).
func (s *scripter) setup(base string, upto int) string {
	var b strings.Builder
	b.WriteString(base)
	first := true
	for i, st := range s.rb.Steps {
		if i >= upto {
			break
		}
		if !st.Always {
			continue
		}
		if first {
			b.WriteString("\n\n# Setup steps (always): they publish values the steps after them use")
			first = false
		}
		b.WriteString("\n\n" + s.stepScript(i, st, st.WayList()[0]))
	}
	return b.String()
}

var dollarRef = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)`)

// stepScript is one way of step i, run as runnr runs it.
func (s *scripter) stepScript(i int, st runbook.Step, w runbook.Way) string {
	if len(st.Ways) > 0 {
		st = st.WithWay(w)
	}
	var b strings.Builder
	shell := st.Shell
	if shell == "" {
		shell = s.shell
	}
	title := fmt.Sprintf("# Step %d: %s", i+1, st.Name)
	if len(s.rb.Steps[i].Ways) > 1 {
		title += " (way: " + w.Name + ")"
	}
	how := []string{"runnr runs it in a " + shell + " of its own"}
	if d := s.p.Render(st.Dir); d != "" {
		how = append(how, "in "+d)
	}
	if st.Timeout != "" {
		how = append(how, "timeout "+st.Timeout)
	}
	if st.TTY {
		how = append(how, "in a terminal you type into")
	}
	if st.Background {
		how = append(how, "in the background")
	}
	if st.Optional {
		how = append(how, "optional")
	}
	b.WriteString(title + "\n# " + strings.Join(how, ", ") + "\n")

	if w.Kind() == "checks" {
		b.WriteString("# Each check runs in its own shell and passes when it exits 0.\n")
		if st.Repeat != nil {
			fmt.Fprintf(&b, "# runnr repeats them every %s, for up to %s, until all pass.\n", st.Repeat.Every, st.Repeat.Timeout)
		}
		for _, c := range w.Checks {
			cmd := strings.TrimSpace(s.p.Render(c.Run))
			fmt.Fprintf(&b, "( %s ) && echo %s || echo %s\n", indentCont(cmd), q("✓ "+c.Name), q("✗ "+c.Name+" (failed)"))
		}
		return strings.TrimRight(b.String(), "\n")
	}

	cmd := strings.TrimSpace(s.p.Render(st.Run))
	if needs := s.needs(i, cmd); len(needs) > 0 {
		b.WriteString("# Uses from earlier steps: " + strings.Join(needs, ", ") + "\n")
	}
	var body strings.Builder
	if d := s.p.Render(st.Dir); d != "" {
		fmt.Fprintf(&body, "cd %s || exit\n", q(d))
	}
	for _, k := range slices.Sorted(maps.Keys(st.Env)) {
		fmt.Fprintf(&body, "export %s=%s\n", k, q(s.p.Render(st.Env[k])))
	}
	if outs := append(slices.Clone(st.Outputs), st.SecretOutputs...); len(outs) > 0 {
		// What runnr adds so the step's outputs are published when it exits.
		body.WriteString("__runnr_outputs() {\n")
		for _, n := range outs {
			fmt.Fprintf(&body, "  [ -n \"${%[1]s+x}\" ] && printf '%%s=%%s\\n' %[1]s \"$%[1]s\" >> \"$RUNNR_OUTPUT\"\n", n)
		}
		body.WriteString("  return 0\n}\ntrap __runnr_outputs EXIT\n")
	}
	body.WriteString(cmd)
	sub := "(\n" + indent(body.String()) + "\n)"
	if st.Capture != "" {
		fmt.Fprintf(&b, "%s=\"$%s\"; export %s; printf '%%s\\n' \"$%s\"\n", st.Capture, sub, st.Capture, st.Capture)
	} else {
		b.WriteString(sub + "\n")
	}
	// Values it published come back as environment variables, as in a run.
	b.WriteString("while IFS= read -r __l; do [ -n \"$__l\" ] && export \"$__l\"; done < \"$RUNNR_OUTPUT\"")
	return b.String()
}

// needs lists the values cmd uses that earlier steps (not setup steps,
// which the setup runs) publish.
func (s *scripter) needs(i int, cmd string) []string {
	from := map[string]int{}
	for j, st := range s.rb.Steps[:i] {
		if st.Always {
			continue
		}
		for _, n := range append(append(slices.Clone(st.Outputs), st.SecretOutputs...), st.Capture) {
			if n != "" {
				from[n] = j
			}
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, m := range dollarRef.FindAllStringSubmatch(cmd, -1) {
		if j, ok := from[m[1]]; ok && !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, fmt.Sprintf("$%s (step %d)", m[1], j+1))
		}
	}
	for _, m := range templateRef.FindAllStringSubmatch(cmd, -1) {
		if j, ok := from[m[1]]; ok && !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, fmt.Sprintf("$%s (step %d)", m[1], j+1))
		}
	}
	return out
}

// templateRef finds the <name> placeholders the preview leaves for values
// not known yet.
var templateRef = regexp.MustCompile(`<([A-Za-z_][A-Za-z0-9_]*)>`)

func q(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// pathQ quotes a path, leaving a leading ~/ outside the quotes.
func pathQ(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return "~/" + q(rest)
	}
	return q(p)
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(s, "\n", "\n  ")
}

// indentCont indents the lines after the first (a command inside "( … )").
func indentCont(s string) string { return strings.ReplaceAll(s, "\n", "\n  ") }

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}
