package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
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
	engine.StatusRunning:   {"36", "▶"},
}

func (t *Terminal) Notify(e engine.Event) {
	switch e.Kind {
	case engine.EventRunStarted:
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

func (t *Terminal) Confirm(ctx context.Context, _ int, _ runbook.Step, command string) (bool, error) {
	fmt.Fprintln(t.out, t.c("2", "  will run:"))
	for _, l := range strings.Split(strings.TrimRight(command, "\n"), "\n") {
		fmt.Fprintln(t.out, t.c("2", "    "+l))
	}
	a, err := t.choose(ctx, "Run this step? [y]es / [s]kip / [a]bort:",
		map[string]string{"y": "y", "yes": "y", "s": "s", "skip": "s", "n": "s", "a": "a", "abort": "a"}, "")
	if err != nil {
		return false, err
	}
	if a == "a" {
		return false, engine.ErrAborted
	}
	return a == "y", nil
}

func (t *Terminal) Manual(ctx context.Context, _ int, _ runbook.Step, text string) (engine.ManualOutcome, string, error) {
	for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		fmt.Fprintln(t.out, "  "+t.c("35", "☞ ")+l)
	}
	a, err := t.choose(ctx, "[d]one / [s]kip / [f]ail / [a]bort:",
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

func (t *Terminal) OnFailure(ctx context.Context, _ int, _ runbook.Step, _ engine.StepResult) (engine.FailureAction, error) {
	a, err := t.choose(ctx, "Step failed. [r]etry / [s]kip / [a]bort:",
		map[string]string{"r": "r", "retry": "r", "s": "s", "skip": "s", "a": "a", "abort": "a"}, "a")
	if err != nil {
		return "", err
	}
	return map[string]engine.FailureAction{"r": engine.FailRetry, "s": engine.FailSkip, "a": engine.FailAbort}[a], nil
}
