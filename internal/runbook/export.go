package runbook

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"text/template"
)

// ExportOptions shape a Markdown export.
type ExportOptions struct {
	// Vars fills in inputs in the setup block (others use their default).
	Vars map[string]string
	// Libraries are the function libraries the runbook uses.
	Libraries []*Library
}

// ExportMarkdown writes a runbook as a Markdown document to follow by hand:
// a collapsed overview, a setup block (inputs, environment, functions),
// then each section and step with what it's doing and code to paste.
//
// Every code block is meant to be pasted, in order, into one interactive
// shell (the runbook's shell): placeholders become shell variables, a step
// that exits, changes directory or sets shell options runs in a subshell
// so it can't close or move your shell, and a step that publishes values
// still sets them in your shell.
func ExportMarkdown(rb *Runbook, o ExportOptions) (string, error) {
	shell := rb.Shell
	if shell == "" {
		shell = "sh"
	}
	lang := map[string]string{"zsh": "zsh", "bash": "bash"}[baseName(shell)]
	if lang == "" {
		lang = "sh"
	}
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	code := func(body string) {
		w("```%s\n%s\n```\n\n", lang, strings.TrimRight(body, "\n"))
	}
	open, closeIdx := rb.WindowSteps()
	exportInputs = map[string]string{}
	for _, in := range rb.Inputs {
		if v, ok := o.Vars[in.Name]; ok {
			exportInputs[in.Name] = v
		} else if in.Default != "" {
			exportInputs[in.Name] = in.Default
		}
	}
	defer func() { exportInputs = nil }()

	w("# %s\n\n", rb.Name)

	// Overview: the process at a glance, collapsed.
	w("<details>\n<summary><b>Overview</b></summary>\n\n")
	if d := strings.TrimSpace(rb.Description); d != "" {
		w("%s\n\n", d)
	}
	if e := strings.TrimSpace(rb.Expect); e != "" {
		w("**By the end:** %s\n\n", e)
	}
	secs := sectionOrder(rb)
	if len(secs) > 0 {
		for _, name := range secs {
			line := fmt.Sprintf("- **%s**", name)
			for _, s := range rb.Sections {
				if s.Name == name && strings.TrimSpace(s.Description) != "" {
					line += ": " + firstLine(s.Description)
				}
			}
			w("%s\n", line)
		}
		w("\n")
	}
	if mw := rb.MaintenanceWindow; mw != nil {
		line := "**Maintenance window** (production down)"
		if open >= 0 {
			line += fmt.Sprintf(": from step %d (%s)", open+1, rb.Steps[open].Name)
			if closeIdx >= 0 {
				line += fmt.Sprintf(" to step %d (%s)", closeIdx+1, rb.Steps[closeIdx].Name)
			}
		}
		if t := rb.WindowTarget(o.Vars); t != "" {
			line += ", target " + t
		}
		if lead, ok := mw.NotifyLead(); ok {
			line += ", announced " + FormatSpan(lead) + " ahead"
		}
		w("%s\n\n", line+".")
	}
	w("Paste the blocks in order into one %s session. Steps marked _manual_ are done by hand.\n\n</details>\n\n", shell)

	// Setup: inputs, environment, functions.
	w("## Setup\n\n")
	var setup strings.Builder
	var secrets []string
	// Pasting into an interactive shell: zsh treats # as a command unless
	// interactive_comments is on, and both expand ! in double quotes.
	setup.WriteString("[ -n \"$ZSH_VERSION\" ] && setopt interactive_comments no_bang_hist; [ -n \"$BASH_VERSION\" ] && set +H\n")
	setup.WriteString("# Inputs: set or check these first\n")
	for _, in := range rb.Inputs {
		note := strings.TrimSpace(firstLine(in.Description))
		if len(in.Choices) > 0 {
			note = strings.TrimSpace(note + " (" + strings.Join(in.Choices, " | ") + ")")
		}
		if in.Required {
			note = strings.TrimSpace("required. " + note)
		}
		comment := ""
		if note != "" {
			comment = "  # " + note
		}
		switch v, given := o.Vars[in.Name]; {
		case in.Secret:
			secrets = append(secrets, fmt.Sprintf("printf '%s: '; read -rs %s; echo", in.Name, in.Name))
			fmt.Fprintf(&setup, "# %s: secret, asked for at the end of this block%s\n", in.Name, comment)
		case given:
			fmt.Fprintf(&setup, "%s=%s%s\n", in.Name, shellQuote(v), comment)
		default:
			def := strings.ReplaceAll(in.Default, `"`, `\"`)
			if strings.HasPrefix(def, "~/") {
				def = "$HOME/" + def[2:] // ~ doesn't expand inside quotes
			}
			fmt.Fprintf(&setup, "%s=\"${%s:-%s}\"%s\n", in.Name, in.Name, def, comment)
		}
	}
	if len(rb.Env) > 0 {
		setup.WriteString("\n# Environment\n")
		for _, k := range sortedKeys(rb.Env) {
			fmt.Fprintf(&setup, "export %s=%s\n", k, exportValue(rb.Env[k]))
		}
	}
	for _, p := range rb.Profile {
		fmt.Fprintf(&setup, "source %s\n", p)
	}
	// What a step writes to $RUNNR_OUTPUT is read back after it.
	setup.WriteString("\n# Where steps can publish values (name=value lines)\nRUNNR_OUTPUT=\"$(mktemp)\"; RUNNR_SECRET=\"$RUNNR_OUTPUT\"\n")
	if len(secrets) > 0 {
		// One line, so pasting doesn't feed the next prompt the lines after it.
		setup.WriteString("\n# Secrets: type each when asked\n" + strings.Join(secrets, "; ") + "\n")
	}
	code(setup.String())
	fns := map[string]Function{}
	for _, l := range o.Libraries {
		for n, f := range l.Functions {
			fns[n] = f
		}
	}
	for n, f := range rb.Functions {
		fns[n] = f
	}
	if len(fns) > 0 {
		names := sortedKeys(fns)
		w("<details>\n<summary>Helper functions (%s)</summary>\n\n", strings.Join(names, ", "))
		code(FunctionScript(fns))
		w("</details>\n\n")
	}

	// Sections and steps.
	section := ""
	if len(secs) > 0 {
		w("## Primary states\n\n")
	} else {
		w("## Steps\n\n")
	}
	secNo := 0
	for i, st := range rb.Steps {
		if st.Section != "" && st.Section != section {
			section = st.Section
			secNo++
			if numbered.MatchString(section) {
				w("### %s\n\n", section) // its name has its number already
			} else {
				w("### %d. %s\n\n", secNo, section)
			}
		}
		if i == open {
			w("> **APP IS DOWN** — maintenance window opens\n\n")
		}
		flags := []string{}
		if st.Kind() == "manual" {
			flags = append(flags, "manual")
		}
		if st.Always {
			flags = append(flags, "setup")
		}
		if st.Optional {
			flags = append(flags, "optional: not required")
		}
		if st.Background {
			flags = append(flags, "in its own terminal")
		}
		if st.Confirm || st.Dangerous {
			flags = append(flags, "⚠ check before running")
		}
		title := template.HTMLEscapeString(fmt.Sprintf("%d. %s", i+1, st.Name))
		if len(flags) > 0 {
			title += " <i>(" + template.HTMLEscapeString(strings.Join(flags, ", ")) + ")</i>"
		}
		w("<details open>\n<summary><b>%s</b></summary>\n\n", title)
		if d := strings.TrimSpace(exportText(st.Description)); d != "" {
			for _, l := range strings.Split(d, "\n") {
				if l = strings.TrimSpace(l); l != "" {
					w("- %s\n", mdProse(l))
				}
			}
		}
		if st.When != "" {
			w("- Only when: `%s`\n", st.When)
		}
		if st.Background {
			w("- Start it in another terminal (paste Setup there first); it keeps running until %s.\n", stopAfterName(rb, st))
		}
		w("\n")
		ways := st.WayList()
		pref := 0
		if len(st.Ways) > 0 {
			pref = st.PreferredWay()
		}
		writeWay := func(wy Way) {
			s := st.WithWay(wy)
			switch s.Kind() {
			case "manual":
				for _, l := range strings.Split(strings.TrimSpace(exportText(s.Manual)), "\n") {
					w("> %s\n", mdProse(l))
				}
				w("\n")
			case "checks":
				code(checksScript(s))
			default:
				code(stepScript(s))
			}
		}
		if len(st.Ways) > 1 {
			w("**%s** (preferred)\n\n", ways[pref].Name)
		}
		writeWay(ways[pref])
		for j, wy := range ways {
			if j == pref {
				continue
			}
			w("<details>\n<summary>Or: %s</summary>\n\n", template.HTMLEscapeString(wy.Name))
			writeWay(wy)
			w("</details>\n\n")
		}
		w("</details>\n\n")
		if i == closeIdx {
			w("> **APP IS BACK** — maintenance window closes\n\n")
		}
	}
	return b.String(), nil
}

// stepScript is a step's command, made safe to paste: placeholders become
// shell variables; it runs in a subshell when it exits, moves or sets shell
// options; published values still land in your shell.
func stepScript(s Step) string {
	body := strings.TrimRight(exportText(s.Run), "\n")
	if tmplLogicRe.MatchString(s.Run) && strings.Contains(s.Run, "{{") {
		body = "# worked out for " + inputsNote(s.Run) + "; export again for other values\n" + body
	}
	pre := ""
	if s.Dir != "" {
		pre += fmt.Sprintf("cd %s || exit\n", exportValue(s.Dir))
	}
	for _, k := range sortedKeys(s.Env) {
		pre += fmt.Sprintf("export %s=%s\n", k, exportValue(s.Env[k]))
	}
	if s.Timeout != "" {
		body = "# times out after " + s.Timeout + " in runnr\n" + body
	}
	outs := append(append([]string{}, s.Outputs...), s.SecretOutputs...)
	if s.Capture != "" {
		body = fmt.Sprintf("%s=\"$(\n%s\n)\"; echo \"$%s\"", s.Capture, body, s.Capture)
		outs = append(outs, s.Capture)
	}
	published := ""
	if strings.Contains(s.Run, "RUNNR_OUTPUT") || strings.Contains(s.Run, "RUNNR_SECRET") {
		published = "\nset -a; . \"$RUNNR_OUTPUT\"; set +a  # values it published"
	}
	if len(outs) > 0 {
		// Run it in a subshell, then bring its values back into this shell.
		return fmt.Sprintf("# sets: %s\neval \"$( {\n%s%s\n} >&2; typeset -p %s 2>/dev/null )\"%s",
			strings.Join(outs, ", "), pre, body, strings.Join(outs, " "), published)
	}
	if pre != "" || needsSubshell(body) {
		return "(\n" + pre + body + "\n)" + published
	}
	if strings.Contains(body, "\n") {
		// Grouped, so the shell reads the whole block before running it:
		// an interactive command can't take the lines pasted after it as input.
		return "{\n" + body + "\n}" + published
	}
	return body + published
}

// checksScript runs a step's checks, each on its own, and says which pass;
// with repeat, it keeps trying until all pass or time runs out.
func checksScript(s Step) string {
	var b strings.Builder
	b.WriteString("(\n")
	if s.Dir != "" {
		fmt.Fprintf(&b, "cd %s || exit\n", exportValue(s.Dir))
	}
	b.WriteString("checks() {\n  ok=0\n")
	for _, c := range s.Checks {
		run := strings.TrimRight(exportText(c.Run), "\n")
		fmt.Fprintf(&b, "  if ( %s ) >/dev/null 2>&1; then echo \"✅ %s\"; else echo \"❌ %s\"; ok=1; fi\n",
			strings.ReplaceAll(run, "\n", "\n    "), escDQ(c.Name), escDQ(c.Name))
	}
	b.WriteString("  return $ok\n}\n")
	if s.Repeat != nil {
		every := int(s.Repeat.EveryDuration().Seconds())
		tries := int(s.Repeat.TimeoutDuration()/s.Repeat.EveryDuration()) + 1
		fmt.Fprintf(&b, "# every %s, for up to %s\nfor try in $(seq %d); do checks && break; [ $try = %d ] && { echo \"gave up after %s\"; exit 1; }; echo \"trying again in %ds\"; sleep %d; done\n",
			s.Repeat.Every, s.Repeat.Timeout, tries, tries, s.Repeat.Timeout, every, every)
	} else {
		b.WriteString("checks\n")
	}
	b.WriteString(")")
	return b.String()
}

var (
	codeSpanRe = regexp.MustCompile("`[^`]*`")
	shellVarRe = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*\}|\$[A-Za-z_][A-Za-z0-9_]*`)
)

// mdProse makes text safe as Markdown prose: placeholders like ${days}
// become inline code, and any other $ is escaped, so viewers that render
// $...$ as math don't swallow it.
func mdProse(s string) string {
	var b strings.Builder
	last := 0
	for _, m := range codeSpanRe.FindAllStringIndex(s, -1) {
		b.WriteString(proseOutsideCode(s[last:m[0]]))
		b.WriteString(s[m[0]:m[1]])
		last = m[1]
	}
	b.WriteString(proseOutsideCode(s[last:]))
	return b.String()
}

func proseOutsideCode(s string) string {
	var b strings.Builder
	last := 0
	for _, m := range shellVarRe.FindAllStringIndex(s, -1) {
		b.WriteString(strings.ReplaceAll(s[last:m[0]], "$", `\$`))
		b.WriteString("`" + s[m[0]:m[1]] + "`")
		last = m[1]
	}
	b.WriteString(strings.ReplaceAll(s[last:], "$", `\$`))
	return b.String()
}

var numbered = regexp.MustCompile(`^\d+[.)]\s`)

var subshellRe = regexp.MustCompile(`(?m)(^|[;&|{(\s])(exit|cd|set\s+-|setopt|trap)\b`)

func needsSubshell(body string) bool { return subshellRe.MatchString(body) }

var (
	tmplVarRe   = regexp.MustCompile(`\.([A-Za-z_][A-Za-z0-9_]*)`)
	tmplLogicRe = regexp.MustCompile(`{{-?\s*(if|else|range|with)\b|\b(eq|ne|lt|le|gt|ge|and|or|not|index)\b`)
)

// exportInputs are the inputs' values for this export (given or default),
// used where a template has logic that can't become a shell variable.
var exportInputs map[string]string

// exportText renders templates so {{.name}} becomes ${name}, quote
// becomes double quotes (so the variable still expands) and default
// becomes ${name:-default}. A template with logic ({{if eq .env "prod"}})
// is worked out with the inputs' values instead. Text that won't render is
// left as it is.
func exportText(text string) string {
	if !strings.Contains(text, "{{") {
		return text
	}
	logic := tmplLogicRe.MatchString(text)
	data := map[string]string{}
	for _, m := range tmplVarRe.FindAllStringSubmatch(text, -1) {
		data[m[1]] = "${" + m[1] + "}"
		if v, ok := exportInputs[m[1]]; ok && logic {
			data[m[1]] = v
		}
	}
	funcs := template.FuncMap{}
	for k, v := range TemplateFuncs {
		funcs[k] = v
	}
	funcs["quote"] = func(v string) string { return `"` + v + `"` }
	funcs["default"] = func(def, v string) string {
		if strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}") {
			return strings.TrimSuffix(v, "}") + ":-" + def + "}"
		}
		return v
	}
	t, err := template.New("").Funcs(funcs).Parse(text)
	if err != nil {
		return text
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return text
	}
	return b.String()
}

// exportValue is a value for an assignment or cd: rendered, double-quoted.
func exportValue(v string) string {
	r := exportText(v)
	if strings.HasPrefix(r, `"`) {
		return r
	}
	return `"` + strings.ReplaceAll(r, `"`, `\"`) + `"`
}

// inputsNote lists the inputs a template uses, with the values used.
func inputsNote(text string) string {
	var parts []string
	seen := map[string]bool{}
	for _, m := range tmplVarRe.FindAllStringSubmatch(text, -1) {
		if v, ok := exportInputs[m[1]]; ok && !seen[m[1]] {
			seen[m[1]] = true
			parts = append(parts, m[1]+"="+v)
		}
	}
	return strings.Join(parts, " ")
}

func stopAfterName(rb *Runbook, st Step) string {
	if st.StopAfter == "" {
		return "the end"
	}
	if j, err := rb.StepIndex(st.StopAfter); err == nil {
		return fmt.Sprintf("step %d (%s) is done", j+1, rb.Steps[j].Name)
	}
	return st.StopAfter + " is done"
}

func sectionOrder(rb *Runbook) []string {
	var out []string
	for i, st := range rb.Steps {
		if st.Section != "" && (i == 0 || rb.Steps[i-1].Section != st.Section) {
			out = append(out, st.Section)
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func escDQ(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`").Replace(s)
}
