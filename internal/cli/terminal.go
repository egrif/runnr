package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/runbook"
)

// Terminal is an interactive engine.Interactor on stdin/stdout.
type Terminal struct {
	window  *engine.WindowRecord // the maintenance window, once it opens
	out     io.Writer
	color   bool
	total   int
	quiet   bool
	section string
	runID   string

	// addNote saves an operator note for a step (nil: notes unavailable).
	addNote func(step int, text string) error
	// session is the run being driven (set by the engine via Bind).
	session engine.Session
	// interrupts, when set, makes runnr ignore Ctrl-C while a shell or a
	// tty step has it.
	interrupts *interruptHolds
	// bg holds background steps' terminals, by step index.
	bg map[int]*bgView
	// sections maps section names to their descriptions.
	sections map[string]string
}

// header prints the step title, preceded by its section when that changes.
func (t *Terminal) header(i int, r *engine.StepResult, dim bool) string {
	var b strings.Builder
	if r.Section != "" && r.Section != t.section {
		t.section = r.Section
		fmt.Fprintf(&b, "\n%s\n", t.c("35;1", "══ "+r.Section))
		if t.sections != nil {
			if d := strings.TrimSpace(t.sections[r.Section]); d != "" {
				fmt.Fprintf(&b, "%s\n", t.c("35", "   "+strings.ReplaceAll(d, "\n", "\n   ")))
			}
		}
	}
	n := fmt.Sprintf("[%d/%d]", i+1, t.total)
	title := r.Name
	if r.Owner != "" {
		title += t.c("2", " ("+r.Owner+")")
	}
	if r.Optional {
		title += " " + t.c("33", "◇ optional, not required")
	}
	if dim {
		fmt.Fprintf(&b, "\n%s %s", t.c("2", n), t.c("2", title))
	} else {
		fmt.Fprintf(&b, "\n%s %s", t.c("36;1", n), t.c("1", title))
	}
	return b.String()
}

func NewTerminal(total int) *Terminal {
	return &Terminal{out: os.Stdout, color: isTTY(os.Stdout), total: total}
}

func isTTY(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

func (t *Terminal) c(code, s string) string {
	if !t.color {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

var statusStyle = map[engine.Status]struct{ code, icon string }{
	engine.StatusSucceeded:  {"32", "✔"},
	engine.StatusFailed:     {"31", "✘"},
	engine.StatusSkipped:    {"33", "↷"},
	engine.StatusAborted:    {"31", "■"},
	engine.StatusForced:     {"33", "⚑"},
	engine.StatusRunning:    {"36", "▶"},
	engine.StatusBackground: {"36", "◐"},
}

func (t *Terminal) Notify(e engine.Event) {
	switch e.Kind {
	case engine.EventRunStarted:
		t.runID = e.Run.ID
		fmt.Fprintf(t.out, "%s %s  %s\n", t.c("1", "runnr"), e.Run.Runbook, t.c("2", e.Run.ID))
	case engine.EventStepStarted:
		fmt.Fprintln(t.out, t.header(e.Step, e.Result, false))
		if t.session != nil {
			v := t.session.View(e.Step)
			if w := t.windowLine(v); w != "" {
				fmt.Fprintln(t.out, w)
			}
			if d := strings.TrimSpace(v.Description); d != "" {
				fmt.Fprintln(t.out, t.c("2", "  "+strings.ReplaceAll(d, "\n", "\n  ")))
			}
		}
	case engine.EventWindow:
		t.window = e.Run.Window
	case engine.EventOutput:
		if t.quiet {
			return
		}
		prefix := t.c("2", "  │ ")
		if e.Stream == "stderr" {
			prefix = t.c("31", "  │ ")
		}
		fmt.Fprintln(t.out, prefix+e.Text)
	case engine.EventStepFinished:
		r := e.Result
		// A background step ending later is reported by an info line.
		if r.Note == engine.NoteBeforeStart || (r.Kind == "background" && r.Status != engine.StatusBackground) {
			return
		}
		st := statusStyle[r.Status]
		line := fmt.Sprintf("  %s %s", st.icon, r.Status)
		if !r.Started.IsZero() && !r.Ended.IsZero() {
			line += fmt.Sprintf(" (%s)", r.Ended.Sub(r.Started).Round(time.Millisecond))
		}
		if r.Started.IsZero() {
			// Skipped before starting: print the header we never printed.
			line = t.header(e.Step, r, true) + "\n" + line
		}
		if r.Note != "" {
			line += " — " + r.Note
		}
		if r.Error != "" && r.Status != engine.StatusSucceeded {
			line += " — " + r.Error
		}
		fmt.Fprintln(t.out, t.c(st.code, line))
	case engine.EventRunFinished:
		st := statusStyle[e.Run.Status]
		msg := fmt.Sprintf("\n%s run %s in %s", st.icon, e.Run.Status, e.Run.Ended.Sub(e.Run.Started).Round(time.Millisecond))
		if e.Run.Error != "" {
			msg += ": " + e.Run.Error
		}
		fmt.Fprintln(t.out, t.c(st.code+";1", msg))
		if w := e.Run.Window; w != nil && !w.Opened.IsZero() {
			if w.Closed.IsZero() {
				fmt.Fprintln(t.out, t.c("31;1", fmt.Sprintf("🔴 The maintenance window is still OPEN (%s so far): production may still be down.", time.Since(w.Opened).Round(time.Second))))
			} else {
				fmt.Fprintln(t.out, t.c("1", fmt.Sprintf("Maintenance window: %s%s", w.Closed.Sub(w.Opened).Round(time.Second), targetVs(w, w.Closed.Sub(w.Opened)))))
			}
		}
	}
}

func (t *Terminal) AskInput(ctx context.Context, in runbook.Input) (string, error) {
	for {
		label := t.c("1", in.Name)
		if in.Description != "" {
			label += t.c("2", " ("+in.Description+")")
		}
		if len(in.Choices) > 0 {
			label += " [" + strings.Join(in.Choices, "/") + "]"
		}
		if in.Default != "" {
			label += t.c("2", " default: "+in.Default)
		}
		fmt.Fprint(t.out, "? "+label+": ")
		var v string
		var err error
		if in.Secret && isTTY(os.Stdin) {
			v, err = t.readSecret(ctx)
		} else {
			v, err = t.readLine(ctx)
		}
		if err != nil {
			return "", err
		}
		if v == "" {
			v = in.Default
		}
		if v == "" && in.Required {
			fmt.Fprintln(t.out, t.c("31", "  required"))
			continue
		}
		if v != "" && len(in.Choices) > 0 && !contains(in.Choices, v) {
			fmt.Fprintln(t.out, t.c("31", "  must be one of "+strings.Join(in.Choices, ", ")))
			continue
		}
		return v, nil
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// choose prompts until the answer matches one of the option keys.
func (t *Terminal) choose(ctx context.Context, prompt string, opts map[string]string, def string) (string, error) {
	for {
		fmt.Fprint(t.out, t.c("33;1", "  ? ")+prompt+" ")
		s, err := t.readLine(ctx)
		if err != nil {
			return "", err
		}
		s = strings.ToLower(s)
		if s == "" {
			s = def
		}
		if v, ok := opts[s]; ok {
			return v, nil
		}
	}
}

// Bind receives the run's Session (engine.Binder).
func (t *Terminal) Bind(se engine.Session) { t.session = se }

func (t *Terminal) canShell() bool {
	return t.session != nil && t.session.CanShell() && isTTY(os.Stdin)
}

// stepPrompt is choose plus extras available at every step prompt:
// [v]iew the step, [e]nv (the run's state), [n]ote, and [!] shell. Each
// returns to the same question afterwards.
func (t *Terminal) stepPrompt(ctx context.Context, idx int, prompt string, opts map[string]string, def string) (string, error) {
	return t.stepPromptAt(ctx, idx, false, prompt, opts, def)
}

// beforePrompt is stepPrompt for a question asked before the step runs its
// command: changing the inputs there starts the step again with them.
func (t *Terminal) beforePrompt(ctx context.Context, idx int, prompt string, opts map[string]string, def string) (string, error) {
	return t.stepPromptAt(ctx, idx, true, prompt, opts, def)
}

func (t *Terminal) stepPromptAt(ctx context.Context, idx int, before bool, prompt string, opts map[string]string, def string) (string, error) {
	extra := ""
	all := map[string]string{}
	if t.session != nil && idx >= 0 {
		extra += " · [v]iew"
		all["v"], all["view"] = "\x00view", "\x00view"
	}
	if t.previousStep(idx) != nil {
		extra += " · [l]ast time"
		all["l"], all["last"] = "\x00last", "\x00last"
	}
	if t.session != nil {
		extra += " · [e]nv"
		all["e"], all["env"] = "\x00env", "\x00env"
	}
	if t.addNote != nil {
		extra += " · [n]ote"
		all["n"], all["note"] = "\x00note", "\x00note"
	}
	if t.canShell() {
		extra += " · [!] shell"
		all["!"], all["shell"] = "\x00shell", "\x00shell"
	}
	if len(t.running()) > 0 {
		extra += " · [w]atch background"
		all["w"], all["watch"] = "\x00watch", "\x00watch"
	}
	if is, ok := t.session.(engine.InputSetter); ok && idx >= 0 && len(is.InputDefs()) > 0 {
		extra += " · [i]nputs"
		all["i"], all["inputs"] = "\x00inputs", "\x00inputs"
	}
	if _, ok := t.session.(engine.Editor); ok && idx >= 0 && t.canShell() {
		extra += " · [c]hange step"
		all["c"], all["change"] = "\x00change", "\x00change"
	}
	for k, v := range opts {
		all[k] = v
	}
	for {
		a, err := t.choose(ctx, prompt+t.c("2", extra)+":", all, def)
		if err != nil {
			return "", err
		}
		switch a {
		case "\x00view":
			t.printView(t.session.View(idx))
		case "\x00env":
			t.printState(t.session.State())
		case "\x00last":
			t.printLast(idx)
		case "\x00note":
			fmt.Fprint(t.out, "  note: ")
			text, err := t.readLine(ctx)
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(text) == "" {
				continue
			}
			if err := t.addNote(idx, text); err != nil {
				fmt.Fprintln(t.out, t.c("31", "  couldn't save note: "+err.Error()))
			} else {
				fmt.Fprintln(t.out, t.c("2", "  note saved"))
			}
		case "\x00shell":
			if err := t.OpenShell(ctx, idx); err != nil {
				fmt.Fprintln(t.out, t.c("31", "  shell: "+err.Error()))
			}
		case "\x00watch":
			if err := t.watch(ctx); err != nil {
				return "", err
			}
		case "\x00inputs":
			changed, err := t.changeInputs(ctx)
			if err != nil {
				fmt.Fprintln(t.out, t.c("31", "  "+err.Error()))
			}
			if changed && before {
				return "", engine.ErrInputsChanged
			}
		case "\x00change":
			changed, err := t.changeStep(ctx, idx)
			if err != nil {
				fmt.Fprintln(t.out, t.c("31", "  "+err.Error()))
			}
			if changed {
				return "", engine.ErrStepEdited
			}
		default:
			return a, nil
		}
	}
}

// changeInputs asks which inputs to change and their new values. changed
// means some did: they take effect before the run's next command.
func (t *Terminal) changeInputs(ctx context.Context) (changed bool, err error) {
	is := t.session.(engine.InputSetter)
	defs := map[string]runbook.Input{}
	cur := is.Inputs()
	var line []string
	for _, in := range is.InputDefs() {
		defs[in.Name] = in
		line = append(line, in.Name+"="+cur[in.Name])
	}
	fmt.Fprintln(t.out, t.c("2", "  inputs now: "+strings.Join(line, "  ")))
	values := map[string]string{}
	for {
		fmt.Fprint(t.out, "  change which? (name, Enter when done): ")
		name, err := t.readLine(ctx)
		if err != nil {
			return false, err
		}
		name = strings.TrimSpace(name)
		if name == "" {
			break
		}
		in, ok := defs[name]
		if !ok {
			fmt.Fprintln(t.out, t.c("31", "  no input "+name))
			continue
		}
		if !in.Secret {
			if cur[name] != "" {
				in.Default = cur[name] // Enter keeps it
			}
		}
		v, err := t.AskInput(ctx, in)
		if err != nil {
			return false, err
		}
		values[name] = v
	}
	if len(values) == 0 {
		return false, nil
	}
	res, err := is.SetInputs(values, os.Getenv("USER"))
	if err != nil {
		return false, err
	}
	if len(res.Changed) == 0 {
		fmt.Fprintln(t.out, t.c("2", "  nothing changed"))
		return false, nil
	}
	fmt.Fprintln(t.out, t.c("32", "  changed "+strings.Join(res.Changed, ", ")+": they take effect before the next command"))
	for _, k := range res.Changed {
		if st := res.Stale[k]; len(st) > 0 {
			fmt.Fprintln(t.out, t.c("33", fmt.Sprintf("  %s: step(s) %s already ran with the old value", k, strings.Trim(fmt.Sprint(st), "[]"))))
		}
	}
	for _, k := range res.Replaces {
		fmt.Fprintln(t.out, t.c("33", "  "+k+": a step had published a value for it; the new one takes its place"))
	}
	return true, nil
}

// changeStep edits step idx for this run in $VISUAL/$EDITOR, until it's
// valid or left unchanged. changed means the step starts again with it.
func (t *Terminal) changeStep(ctx context.Context, idx int) (changed bool, err error) {
	ed := t.session.(engine.Editor)
	raw, err := runbook.FormatStep(ed.StepDef(idx))
	if err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp("", "runnr-step-*.yaml")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		return false, err
	}
	tmp.Close()
	editor := nonEmptyEnv("VISUAL", nonEmptyEnv("EDITOR", "vi"))
	for {
		conn, err := t.StartTerm(idx, fmt.Sprintf("editing step %d for this run", idx+1))
		if err != nil {
			return false, err
		}
		if err := engine.RunOnTerm(ctx, conn, "sh", "-c", editor+` "$1"`, "sh", tmp.Name()); err != nil {
			return false, fmt.Errorf("editor: %w", err)
		}
		edited, err := os.ReadFile(tmp.Name())
		if err != nil {
			return false, err
		}
		if bytes.Equal(edited, raw) {
			fmt.Fprintln(t.out, t.c("2", "  unchanged"))
			return false, nil
		}
		step, err := runbook.ParseStep(edited)
		if err == nil {
			err = ed.EditStep(idx, step)
		}
		if err == nil {
			fmt.Fprintln(t.out, t.c("33", "  ✎ step changed for this run; it starts again with the new version. Save it to the runbook at the end."))
			return true, nil
		}
		fmt.Fprintln(t.out, t.c("31", "  "+err.Error()))
		a, err := t.choose(ctx, "  [e]dit again / [d]iscard the change", map[string]string{"e": "e", "d": "d"}, "e")
		if err != nil || a == "d" {
			return false, err
		}
	}
}

// windowLine shows where a step stands against the maintenance window:
// its planned time and, while the window is open, the actual time.
func (t *Terminal) windowLine(v engine.StepView) string {
	w := t.window
	var parts []string
	if v.Window == runbook.WindowOpen {
		parts = append(parts, "opens the maintenance window")
	}
	if v.Window == runbook.WindowClose {
		parts = append(parts, "closes the maintenance window")
	}
	if v.OffsetSec != nil {
		p := "planned T" + v.Offset
		if w != nil && !w.Opened.IsZero() && w.Closed.IsZero() {
			now := time.Since(w.Opened)
			drift := now - time.Duration(*v.OffsetSec)*time.Second
			p += ", now T" + engine.FormatOffset(now.Round(time.Second))
			if drift > time.Minute {
				p += fmt.Sprintf(" (%s behind)", strings.TrimPrefix(engine.FormatOffset(drift.Round(time.Minute)), "+"))
			} else if drift < -time.Minute {
				p += fmt.Sprintf(" (%s ahead)", strings.TrimPrefix(engine.FormatOffset((-drift).Round(time.Minute)), "+"))
			}
		}
		parts = append(parts, p)
	} else if w != nil && !w.Opened.IsZero() && w.Closed.IsZero() {
		parts = append(parts, "window open "+time.Since(w.Opened).Round(time.Second).String()+targetVs(w, time.Since(w.Opened)))
	}
	if len(parts) == 0 {
		return ""
	}
	return t.c("35", "  ⏱ "+strings.Join(parts, " · "))
}

// targetVs compares a window length with its target.
func targetVs(w *engine.WindowRecord, d time.Duration) string {
	target, err := runbook.ParseSpan(w.Target)
	if err != nil {
		return ""
	}
	if d > target {
		return fmt.Sprintf(" (target %s, %s over)", w.Target, (d - target).Round(time.Second))
	}
	return fmt.Sprintf(" (target %s)", w.Target)
}

// printView shows a step as it would run now.
func (t *Terminal) printView(v engine.StepView) {
	fmt.Fprintln(t.out, t.viewText(v, "  "))
}

func (t *Terminal) viewText(v engine.StepView, in string) string {
	var b strings.Builder
	var flags []string
	for _, f := range []struct {
		on   bool
		name string
	}{{true, v.Kind}, {v.Optional, "optional"}, {v.Choose, "asks which way"}, {v.Always, "always"}, {v.Confirm, "confirms on its own"}, {v.Dangerous, "⚠ dangerous"}, {v.StopAfter != "", "until " + v.StopAfter}, {v.When != "", "when " + v.When},
		{v.Window != "", "window " + v.Window + "s"}, {v.Offset != "", "planned T" + v.Offset}, {v.IgnoredOffset != "", "T" + v.IgnoredOffset + " out of order, ignored"},
		{v.Timeout != "", "timeout " + v.Timeout}, {v.Retries > 0, fmt.Sprintf("retries %d", v.Retries)}} {
		if f.on {
			flags = append(flags, f.name)
		}
	}
	fmt.Fprintf(&b, "%s%s %s %s\n", in, t.c("36;1", fmt.Sprintf("step %d", v.Index+1)), t.c("1", v.Name), t.c("2", "["+strings.Join(flags, " · ")+"]"))
	var meta []string
	if v.Section != "" {
		meta = append(meta, v.Section)
	}
	if v.Owner != "" {
		meta = append(meta, "owner: "+v.Owner)
	}
	if len(meta) > 0 {
		fmt.Fprintf(&b, "%s%s\n", in, t.c("2", strings.Join(meta, " · ")))
	}
	if v.Description != "" {
		fmt.Fprintf(&b, "%s%s\n", in, strings.ReplaceAll(strings.TrimSpace(v.Description), "\n", "\n"+in))
	}
	if v.Dir != "" {
		fmt.Fprintf(&b, "%s%s %s\n", in, t.c("2", "dir:"), v.Dir)
	}
	label := "$ "
	if v.Kind == "manual" {
		label = "☞ "
	}
	for _, l := range strings.Split(strings.TrimRight(v.Command, "\n"), "\n") {
		fmt.Fprintf(&b, "%s  %s%s\n", in, t.c("2", label), l)
		label = "  "
	}
	if v.Expect != "" {
		for i, l := range strings.Split(strings.TrimRight(v.Expect, "\n"), "\n") {
			p := "expect: "
			if i > 0 {
				p = "        "
			}
			fmt.Fprintf(&b, "%s%s%s\n", in, t.c("32", p), l)
		}
	}
	if len(v.Outputs)+len(v.SecretOutputs) > 0 {
		outs := append([]string{}, v.Outputs...)
		for _, o := range v.SecretOutputs {
			outs = append(outs, o+" (secret)")
		}
		fmt.Fprintf(&b, "%s%s %s\n", in, t.c("2", "publishes:"), strings.Join(outs, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

func (t *Terminal) printState(state map[string]string) {
	if len(state) == 0 {
		fmt.Fprintln(t.out, t.c("2", "  (no variables yet)"))
		return
	}
	keys := make([]string, 0, len(state))
	for k := range state {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := state[k]
		if len(v) > 120 {
			v = v[:117] + "…"
		}
		fmt.Fprintf(t.out, "  %s=%s\n", t.c("36", k), v)
	}
}

// BeforeStep asks what to do with the next step (engine.Stepper).
func (t *Terminal) BeforeStep(ctx context.Context, v engine.StepView) (engine.StepDecision, error) {
	fmt.Fprintln(t.out, t.viewText(v, "  "))
	opts := map[string]string{"r": "r", "run": "r", "s": "s", "skip": "s", "c": "c", "continue": "c", "a": "a", "abort": "a"}
	prompt := "[r]un"
	if r := t.previousStep(v.Index); r != nil && r.Status != engine.StatusPending {
		fmt.Fprintln(t.out, t.c("2", fmt.Sprintf("  last time: %s %s %s (l to see)", statusIcon[r.Status], r.Status, r.Note)))
	}
	if v.Optional {
		fmt.Fprintln(t.out, t.c("33", "  ◇ Optional: this step isn't required. Run it, or skip it and carry on."))
	}
	prompt += " / [s]kip / [c]ontinue without pausing / [a]bort"
	a, err := t.beforePrompt(ctx, v.Index, prompt, opts, "r")
	if err != nil {
		return "", err
	}
	return map[string]engine.StepDecision{"r": engine.StepRun, "s": engine.StepSkip, "c": engine.StepContinue, "a": engine.StepAbort}[a], nil
}

var statusIcon = map[engine.Status]string{
	engine.StatusSucceeded: "✔", engine.StatusFailed: "✘", engine.StatusSkipped: "↷",
	engine.StatusAborted: "■", engine.StatusForced: "⚑", engine.StatusRunning: "▶", engine.StatusPending: "○",
	engine.StatusBackground: "◐",
}

// previousStep returns what step i did in the run being compared against.
func (t *Terminal) previousStep(i int) *engine.StepResult {
	if t.session == nil {
		return nil
	}
	p := t.session.Previous()
	if p == nil || i < 0 || i >= len(p.Steps) {
		return nil
	}
	return &p.Steps[i]
}

// printLast shows what step i did in the previous run.
func (t *Terminal) printLast(i int) {
	r := t.previousStep(i)
	if r == nil {
		fmt.Fprintln(t.out, t.c("2", "  (no earlier run to compare with)"))
		return
	}
	p := t.session.Previous()
	st := statusStyle[r.Status]
	line := fmt.Sprintf("  last time (run %s): %s %s", p.ID, statusIcon[r.Status], r.Status)
	if r.Note != "" {
		line += " — " + r.Note
	}
	fmt.Fprintln(t.out, t.c(st.code, line))
	if r.Command != "" {
		for _, l := range strings.Split(strings.TrimRight(r.Command, "\n"), "\n") {
			fmt.Fprintln(t.out, t.c("2", "    $ "+l))
		}
	}
	if out := strings.TrimRight(r.Output, "\n"); out != "" {
		lines := strings.Split(out, "\n")
		if len(lines) > 30 {
			fmt.Fprintln(t.out, t.c("2", fmt.Sprintf("    … %d earlier lines", len(lines)-30)))
			lines = lines[len(lines)-30:]
		}
		for _, l := range lines {
			fmt.Fprintln(t.out, t.c("2", "    │ ")+l)
		}
	}
	if r.Error != "" && r.Status != engine.StatusSucceeded {
		fmt.Fprintln(t.out, t.c("31", "    error: "+r.Error))
	}
	for _, n := range r.Notes {
		fmt.Fprintf(t.out, "    ✎ %s: %s\n", n.Author, n.Text)
	}
}

// Review shows the plan and lets the operator choose steps to skip and
// where to pause (engine.Reviewer).
func (t *Terminal) Review(ctx context.Context, plan []engine.StepView, c *engine.Controls) error {
	help := true
	for {
		cv := c.View()
		skip, before, after := map[int]bool{}, map[int]bool{}, map[int]bool{}
		for _, i := range cv.Skip {
			skip[i] = true
		}
		for _, i := range cv.PauseBefore {
			before[i] = true
		}
		for _, i := range cv.PauseAfter {
			after[i] = true
		}
		title := fmt.Sprintf(" (%d steps)", len(plan))
		if p := t.session.Previous(); p != nil {
			title += fmt.Sprintf(" · last time = run %s (%s)", p.ID, p.Status)
		}
		if cv.StepAll {
			title += " · stepping through every step"
		}
		fmt.Fprintln(t.out, t.c("1", "\nPlan")+t.c("2", title))
		section := ""
		for _, v := range plan {
			if v.Section != "" && v.Section != section {
				section = v.Section
				fmt.Fprintln(t.out, t.c("35;1", "  ══ "+section))
			}
			mark, name := t.c("32", "✓"), v.Name
			if skip[v.Index] {
				mark, name = t.c("2", "·"), t.c("2", v.Name+" (skip)")
			}
			var tags []string
			if before[v.Index] {
				tags = append(tags, t.c("33", "⏸ before"))
			}
			if after[v.Index] {
				tags = append(tags, t.c("33", "⏸ after"))
			}
			flags := v.Kind
			if v.Optional {
				flags += " · " + t.c("33", "optional")
			}
			if v.Confirm {
				flags += " · confirms on its own"
			}
			if v.Always {
				flags += " · always"
			}
			if v.When != "" {
				flags += " · conditional"
			}
			last := " "
			if r := t.previousStep(v.Index); r != nil && r.Status != engine.StatusPending {
				last = t.c(statusStyle[r.Status].code, statusIcon[r.Status])
			}
			fmt.Fprintf(t.out, "  %s %s %3d  %s %s %s\n", mark, last, v.Index+1, name, t.c("2", flags), strings.Join(tags, " "))
		}
		if help {
			fmt.Fprintln(t.out, t.c("2", "  Commands (step lists like 3,5-7):\n"+
				"    <steps>    skip / include             v <n>      view what step n will do\n"+
				"    b <steps>  pause before (toggle)      l <n>      what step n did last time\n"+
				"    a <steps>  pause after (toggle)       step       step through every step (toggle)\n"+
				"    enter      start                      q          quit without running\n"+
				"  The column after ✓ shows each step's result last time."))
			help = false
		}
		fmt.Fprint(t.out, t.c("33;1", "  ? ")+"review (enter to start, ? for help): ")
		line, err := t.readLine(ctx)
		if err != nil {
			return err
		}
		line = strings.TrimSpace(line)
		cmd, rest, _ := strings.Cut(line, " ")
		rest = strings.TrimSpace(rest)
		one := func() (int, bool) {
			n, err := strconv.Atoi(rest)
			if err != nil || n < 1 || n > len(plan) {
				fmt.Fprintln(t.out, t.c("31", "  which step? e.g. "+cmd+" 4"))
				return 0, false
			}
			return n - 1, true
		}
		toggle := func(what string) {
			idxs, err := parseRanges(rest, len(plan))
			if err != nil {
				fmt.Fprintln(t.out, t.c("31", "  "+err.Error()))
				return
			}
			for _, i := range idxs {
				c.Toggle(what, i)
			}
		}
		switch {
		case line == "":
			return nil
		case line == "q" || line == "quit":
			return engine.ErrAborted
		case line == "?" || line == "help":
			help = true
		case line == "step":
			c.SetStepAll(!cv.StepAll)
		case cmd == "v" || cmd == "view":
			if i, ok := one(); ok {
				t.printView(plan[i])
				t.pressEnter(ctx)
			}
		case cmd == "l" || cmd == "last":
			if i, ok := one(); ok {
				t.printLast(i)
				t.pressEnter(ctx)
			}
		case cmd == "b" || cmd == "before":
			toggle("before")
		case cmd == "a" || cmd == "after":
			toggle("after")
		default:
			rest = line
			toggle("skip")
		}
	}
}

func (t *Terminal) pressEnter(ctx context.Context) {
	fmt.Fprint(t.out, t.c("2", "  (enter to go back) "))
	_, _ = t.readLine(ctx)
}

// AfterStep lets the operator check a finished step (engine.Stepper).
func (t *Terminal) AfterStep(ctx context.Context, v engine.StepView, res engine.StepResult) (engine.AfterDecision, string, error) {
	line := fmt.Sprintf("  %s finished: %s", v.Name, res.Status)
	if res.ExitCode != nil {
		line += fmt.Sprintf(" (exit %d)", *res.ExitCode)
	}
	fmt.Fprintln(t.out, t.c("36", line))
	if v.Expect != "" {
		fmt.Fprintln(t.out, t.c("32", "  expected: ")+v.Expect)
	}
	a, err := t.stepPrompt(ctx, v.Index, "Looks right? [c]ontinue / [r]un again / [f]ail it / [a]bort",
		map[string]string{"c": "c", "continue": "c", "r": "r", "f": "f", "fail": "f", "a": "a", "abort": "a"}, "c")
	if err != nil {
		return "", "", err
	}
	note := ""
	if a == "f" {
		fmt.Fprint(t.out, "  what's wrong? ")
		if note, err = t.readLine(ctx); err != nil {
			return "", "", err
		}
	}
	return map[string]engine.AfterDecision{"c": engine.AfterContinue, "r": engine.AfterRetry, "f": engine.AfterFail, "a": engine.AfterAbort}[a], note, nil
}

// parseRanges turns "3,5-7" into 0-based indexes, checked against n.
func parseRanges(s string, n int) ([]int, error) {
	var out []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		lo, hi, isRange := strings.Cut(part, "-")
		a, err1 := strconv.Atoi(strings.TrimSpace(lo))
		b := a
		var err2 error
		if isRange {
			b, err2 = strconv.Atoi(strings.TrimSpace(hi))
		}
		if err1 != nil || err2 != nil || a < 1 || b > n || a > b {
			return nil, fmt.Errorf("can't read %q (steps are 1-%d)", part, n)
		}
		for i := a; i <= b; i++ {
			out = append(out, i-1)
		}
	}
	return out, nil
}

// OpenShell opens an interactive shell with the run's environment
// (engine.ShellOpener). Ctrl-C belongs to the shell meanwhile, not runnr.
func (t *Terminal) OpenShell(ctx context.Context, idx int) error {
	if !t.canShell() {
		return errors.New("no terminal to open a shell in")
	}
	if idx >= 0 && t.session != nil {
		fmt.Fprintln(t.out, t.c("2", "  ── this step's commands, to run or adapt in the shell:"))
		t.printView(t.session.View(idx))
	}
	fmt.Fprintln(t.out, t.c("2", "  ── runnr shell: the run's variables are in the environment (try `env`).\n"+
		"     Publish values back with: echo name=value >> \"$RUNNR_OUTPUT\" (or $RUNNR_SECRET).\n"+
		"     Type `exit` to return."))
	if fns := t.session.Functions(); len(fns) > 0 {
		fmt.Fprintln(t.out, t.c("2", "     Functions you can call:"))
		for _, f := range fns {
			fmt.Fprintf(t.out, "%s\n", t.c("2", fmt.Sprintf("       %-18s %s", f.Name, f.Description)))
		}
	}
	defer fmt.Fprintln(t.out, t.c("2", "  ── back in runnr"))
	defer t.interrupts.hold("")()
	return t.session.Shell(ctx, idx)
}

func (t *Terminal) Confirm(ctx context.Context, idx int, _ runbook.Step, command string) (bool, error) {
	fmt.Fprintln(t.out, t.c("2", "  will run:"))
	for _, l := range strings.Split(strings.TrimRight(command, "\n"), "\n") {
		fmt.Fprintln(t.out, t.c("2", "    "+l))
	}
	a, err := t.beforePrompt(ctx, idx, "Run this step? [y]es / [s]kip / [a]bort",
		map[string]string{"y": "y", "yes": "y", "s": "s", "skip": "s", "a": "a", "abort": "a"}, "")
	if err != nil {
		return false, err
	}
	if a == "a" {
		return false, engine.ErrAborted
	}
	return a == "y", nil
}

// ConfirmDanger asks for the step number to be typed, so a dangerous step
// out of sequence can't run on a reflexive "y".
func (t *Terminal) ConfirmDanger(ctx context.Context, idx int, step runbook.Step, command, why string) (bool, error) {
	fmt.Fprintln(t.out, t.c("1;31", "  ⚠ DANGEROUS, and not running in sequence"))
	for _, l := range strings.Split(why, "\n") {
		fmt.Fprintln(t.out, t.c("31", "  "+l))
	}
	fmt.Fprintln(t.out, t.c("2", "  will run:"))
	for _, l := range strings.Split(strings.TrimRight(command, "\n"), "\n") {
		fmt.Fprintln(t.out, t.c("2", "    "+l))
	}
	n := strconv.Itoa(idx + 1)
	a, err := t.beforePrompt(ctx, idx, "Type "+n+" to run it anyway / [s]kip / [a]bort",
		map[string]string{n: "y", "s": "s", "skip": "s", "a": "a", "abort": "a"}, "")
	if err != nil {
		return false, err
	}
	if a == "a" {
		return false, engine.ErrAborted
	}
	return a == "y", nil
}

// AskStep: a question the running step asks (runnr_ask, runnr_confirm).
func (t *Terminal) AskStep(ctx context.Context, idx int, _ runbook.Step, kind, prompt string) (string, error) {
	for {
		if kind == "confirm" {
			fmt.Fprint(t.out, "  "+t.c("1;33", "? ")+prompt+t.c("2", " [y/n] "))
		} else {
			fmt.Fprint(t.out, "  "+t.c("1;33", "? ")+prompt+" ")
		}
		a, err := t.readLine(ctx)
		if err != nil || kind != "confirm" {
			return a, err
		}
		switch strings.ToLower(strings.TrimSpace(a)) {
		case "y", "yes":
			return "yes", nil
		case "n", "no":
			return "no", nil
		}
	}
}

// ConfirmTyped: a point of no return; the operator types the value.
func (t *Terminal) ConfirmTyped(ctx context.Context, idx int, step runbook.Step, command, want string) (bool, error) {
	fmt.Fprintln(t.out, t.c("1;31", "  ⚠ "+step.Name))
	fmt.Fprintln(t.out, t.c("2", "  will run:"))
	for _, l := range strings.Split(strings.TrimRight(command, "\n"), "\n") {
		fmt.Fprintln(t.out, t.c("2", "    "+l))
	}
	if step.Expect != "" {
		fmt.Fprintln(t.out, "  "+t.c("36", "expect: ")+step.Expect)
	}
	for {
		a, err := t.beforePrompt(ctx, idx, "Type "+want+" to run it / [s]kip / [a]bort",
			map[string]string{want: "y", "s": "s", "skip": "s", "a": "a", "abort": "a"}, "")
		if err != nil {
			return false, err
		}
		switch a {
		case "a":
			return false, engine.ErrAborted
		case "s":
			return false, nil
		}
		return true, nil
	}
}

func (t *Terminal) Manual(ctx context.Context, idx int, _ runbook.Step, text string) (engine.ManualOutcome, string, error) {
	for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		fmt.Fprintln(t.out, "  "+t.c("35", "☞ ")+l)
	}
	a, err := t.beforePrompt(ctx, idx, "[d]one / [s]kip / [f]ail / [a]bort",
		map[string]string{"d": "d", "done": "d", "s": "s", "skip": "s", "f": "f", "fail": "f", "a": "a", "abort": "a"}, "")
	if err != nil {
		return "", "", err
	}
	switch a {
	case "d":
		return engine.ManualDone, "", nil
	case "s":
		return engine.ManualSkip, "skipped by operator", nil
	case "f":
		fmt.Fprint(t.out, "  reason: ")
		note, err := t.readLine(ctx)
		return engine.ManualFail, note, err
	}
	return "", "", engine.ErrAborted
}

func (t *Terminal) OnFailure(ctx context.Context, idx int, step runbook.Step, res engine.StepResult) (engine.FailureAction, string, error) {
	if res.Kind == "tty" {
		// Ctrl-C stopped the step's command (or was meant to): it doesn't
		// also abort the run.
		defer t.interrupts.hold("(Ctrl-C: the step has stopped. Choose what to do next; [a]bort ends the run.)")()
	}
	opts := map[string]string{"r": "r", "retry": "r", "s": "s", "skip": "s", "f": "f", "force": "f", "a": "a", "abort": "a"}
	prompt := "Step failed. [r]etry / [s]kip / [f]orce success / [a]bort"
	if len(step.Ways) > 1 {
		prompt = "Step failed. [r]etry / [o]ther way / [s]kip / [f]orce success / [a]bort"
		opts["o"], opts["other"] = "o", "o"
	}
	a, err := t.stepPrompt(ctx, idx, prompt, opts, "a")
	if err != nil {
		return "", "", err
	}
	if a == "f" {
		for {
			fmt.Fprint(t.out, "  reason for accepting this failure (required): ")
			reason, err := t.readLine(ctx)
			if err != nil {
				return "", "", err
			}
			if strings.TrimSpace(reason) != "" {
				return engine.FailForce, reason, nil
			}
		}
	}
	return map[string]engine.FailureAction{"r": engine.FailRetry, "o": engine.FailOtherWay, "s": engine.FailSkip, "a": engine.FailAbort}[a], "", nil
}
