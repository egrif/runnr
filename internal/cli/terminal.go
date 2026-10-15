package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/term"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/runbook"
)

// Terminal is an interactive engine.Interactor on stdin/stdout.
type Terminal struct {
	in      *bufio.Reader
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
	// interrupts, when set, makes runnr ignore Ctrl-C while a shell runs.
	interrupts *atomic.Bool
}

// AttachedIO lets tty steps take over the terminal.
func (t *Terminal) AttachedIO() (io.Reader, io.Writer, io.Writer) {
	return os.Stdin, os.Stdout, os.Stderr
}

// header prints the step title, preceded by its section when that changes.
func (t *Terminal) header(i int, r *engine.StepResult, dim bool) string {
	var b strings.Builder
	if r.Section != "" && r.Section != t.section {
		t.section = r.Section
		fmt.Fprintf(&b, "\n%s\n", t.c("35;1", "══ "+r.Section))
	}
	n := fmt.Sprintf("[%d/%d]", i+1, t.total)
	title := r.Name
	if r.Owner != "" {
		title += t.c("2", " ("+r.Owner+")")
	}
	if dim {
		fmt.Fprintf(&b, "\n%s %s", t.c("2", n), t.c("2", title))
	} else {
		fmt.Fprintf(&b, "\n%s %s", t.c("36;1", n), t.c("1", title))
	}
	return b.String()
}

func NewTerminal(total int) *Terminal {
	return &Terminal{in: bufio.NewReader(os.Stdin), out: os.Stdout, color: isTTY(os.Stdout), total: total}
}

func isTTY(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

func (t *Terminal) c(code, s string) string {
	if !t.color {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

var statusStyle = map[engine.Status]struct{ code, icon string }{
	engine.StatusSucceeded: {"32", "✔"},
	engine.StatusFailed:    {"31", "✘"},
	engine.StatusSkipped:   {"33", "↷"},
	engine.StatusAborted:   {"31", "■"},
	engine.StatusForced:    {"33", "⚑"},
	engine.StatusRunning:   {"36", "▶"},
}

func (t *Terminal) Notify(e engine.Event) {
	switch e.Kind {
	case engine.EventRunStarted:
		t.runID = e.Run.ID
		fmt.Fprintf(t.out, "%s %s  %s\n", t.c("1", "runnr"), e.Run.Runbook, t.c("2", e.Run.ID))
	case engine.EventStepStarted:
		fmt.Fprintln(t.out, t.header(e.Step, e.Result, false))
		if e.Result.Kind == "tty" {
			fmt.Fprintln(t.out, t.c("2", "  (attached to terminal)"))
		}
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
		if r.Note == engine.NoteBeforeStart {
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
	}
}

func (t *Terminal) readLine(ctx context.Context) (string, error) {
	type res struct {
		s   string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		s, err := t.in.ReadString('\n')
		ch <- res{strings.TrimSpace(s), err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-ch:
		if r.err == io.EOF && r.s == "" {
			return "", engine.ErrAborted
		}
		return r.s, nil
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
			var b []byte
			b, err = term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(t.out)
			v = string(b)
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
	extra := ""
	all := map[string]string{}
	if t.session != nil && idx >= 0 {
		extra += " · [v]iew"
		all["v"], all["view"] = "\x00view", "\x00view"
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
		default:
			return a, nil
		}
	}
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
	}{{true, v.Kind}, {v.Always, "always"}, {v.Confirm, "confirm"}, {v.When != "", "when " + v.When},
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
	if v.Kind != "manual" {
		opts["m"] = "m"
		prompt += " / [m] run it myself"
	}
	prompt += " / [s]kip / [c]ontinue without pausing / [a]bort"
	a, err := t.stepPrompt(ctx, v.Index, prompt, opts, "r")
	if err != nil {
		return "", err
	}
	return map[string]engine.StepDecision{"r": engine.StepRun, "m": engine.StepByHand, "s": engine.StepSkip, "c": engine.StepContinue, "a": engine.StepAbort}[a], nil
}

// Review shows the plan and lets the operator deselect steps (engine.Reviewer).
func (t *Terminal) Review(ctx context.Context, plan []engine.StepView, skip map[int]bool) (map[int]bool, error) {
	skip = maps.Clone(skip)
	for {
		fmt.Fprintln(t.out, t.c("1", "\nPlan")+t.c("2", fmt.Sprintf(" (%d steps)", len(plan))))
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
			flags := v.Kind
			if v.Always {
				flags += " · always"
			}
			if v.Confirm {
				flags += " · confirm"
			}
			if v.When != "" {
				flags += " · conditional"
			}
			fmt.Fprintf(t.out, "  %s %3d  %s %s\n", mark, v.Index+1, name, t.c("2", flags))
		}
		fmt.Fprint(t.out, t.c("33;1", "  ? ")+"Enter to start · numbers/ranges toggle steps (e.g. 3,5-7) · v N view a step · a abort: ")
		line, err := t.readLine(ctx)
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			return skip, nil
		case line == "a" || line == "abort":
			return nil, engine.ErrAborted
		case strings.HasPrefix(line, "v"):
			n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "view"), "v")))
			if err != nil || n < 1 || n > len(plan) {
				fmt.Fprintln(t.out, t.c("31", "  usage: v <step number>"))
				continue
			}
			t.printView(plan[n-1])
			fmt.Fprint(t.out, t.c("2", "  (enter to go back) "))
			if _, err := t.readLine(ctx); err != nil {
				return nil, err
			}
		default:
			idxs, err := parseRanges(line, len(plan))
			if err != nil {
				fmt.Fprintln(t.out, t.c("31", "  "+err.Error()))
				continue
			}
			for _, i := range idxs {
				skip[i] = !skip[i]
			}
		}
	}
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
	fmt.Fprintln(t.out, t.c("2", "  ── runnr shell: the run's variables are in the environment (try `env`).\n"+
		"     Publish values back with: echo name=value >> \"$RUNNR_OUTPUT\" (or $RUNNR_SECRET).\n"+
		"     Type `exit` to return."))
	if t.interrupts != nil {
		t.interrupts.Store(true)
		defer t.interrupts.Store(false)
	}
	err := t.session.Shell(ctx, idx)
	fmt.Fprintln(t.out, t.c("2", "  ── back in runnr"))
	return err
}

func (t *Terminal) Confirm(ctx context.Context, idx int, _ runbook.Step, command string) (bool, error) {
	fmt.Fprintln(t.out, t.c("2", "  will run:"))
	for _, l := range strings.Split(strings.TrimRight(command, "\n"), "\n") {
		fmt.Fprintln(t.out, t.c("2", "    "+l))
	}
	a, err := t.stepPrompt(ctx, idx, "Run this step? [y]es / [s]kip / [a]bort",
		map[string]string{"y": "y", "yes": "y", "s": "s", "skip": "s", "a": "a", "abort": "a"}, "")
	if err != nil {
		return false, err
	}
	if a == "a" {
		return false, engine.ErrAborted
	}
	return a == "y", nil
}

func (t *Terminal) Manual(ctx context.Context, idx int, _ runbook.Step, text string) (engine.ManualOutcome, string, error) {
	for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		fmt.Fprintln(t.out, "  "+t.c("35", "☞ ")+l)
	}
	a, err := t.stepPrompt(ctx, idx, "[d]one / [s]kip / [f]ail / [a]bort",
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

func (t *Terminal) OnFailure(ctx context.Context, idx int, _ runbook.Step, _ engine.StepResult) (engine.FailureAction, string, error) {
	opts := map[string]string{"r": "r", "retry": "r", "s": "s", "skip": "s", "f": "f", "force": "f", "a": "a", "abort": "a"}
	prompt := "Step failed. [r]etry"
	if t.canShell() {
		opts["m"] = "m"
		prompt += " / [m] run it myself"
	}
	prompt += " / [s]kip / [f]orce success / [a]bort"
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
	return map[string]engine.FailureAction{"r": engine.FailRetry, "m": engine.FailByHand, "s": engine.FailSkip, "a": engine.FailAbort}[a], "", nil
}
