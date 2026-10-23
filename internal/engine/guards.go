package engine

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/egrif/runnr/internal/runbook"
)

// TypedConfirmer is implemented by interactors that can have a person type
// a value to run a step (confirm_typing). Answering false skips the step.
type TypedConfirmer interface {
	ConfirmTyped(ctx context.Context, idx int, step runbook.Step, command, want string) (bool, error)
}

// confirmTyped asks for a step's confirm_typing value, once per run of the
// step (retries don't ask again).
func (x *executor) confirmTyped(ctx context.Context, i int, step runbook.Step, what string) (bool, error) {
	if step.ConfirmTyping == "" || x.opts.DryRun || x.typed[i] {
		return true, nil
	}
	want, err := x.render(step.ConfirmTyping)
	if err != nil {
		return false, fmt.Errorf("confirm_typing: %w", err)
	}
	tc, ok := x.ui.(TypedConfirmer)
	if !ok || !x.opts.Interactive {
		return false, fmt.Errorf("step %q needs a person to type %q to run it (run interactively)", step.Name, want)
	}
	q, done := x.asking(ctx)
	yes, err := tc.ConfirmTyped(q, i, step, x.mask(what), want)
	done()
	if err == nil && yes {
		if x.typed == nil {
			x.typed = map[int]bool{}
		}
		x.typed[i] = true
	}
	return yes, err
}

// NoteSkipIf starts the note of a step that skip_if skipped (the work's already
// done); the command's first line of output may follow in parentheses.
const NoteSkipIf = "skip_if: already done"

// skipIf runs a step's skip_if command: true means the work's already done.
func (x *executor) skipIf(ctx context.Context, i int, step runbook.Step) (bool, string, error) {
	if step.SkipIf == "" {
		return false, "", nil
	}
	cmd, err := x.render(step.SkipIf)
	if err != nil {
		return false, "", fmt.Errorf("skip_if: %w", err)
	}
	if x.opts.DryRun {
		return false, "", nil
	}
	x.notify(Event{Kind: EventOutput, Step: i, Stream: "info", Text: "skip_if: " + x.mask(cmd)})
	out := x.execStream(ctx, i, checkStep(step, cmd), cmd, true)
	if ctx.Err() != nil {
		return false, "", ctx.Err()
	}
	if out.err != nil {
		return false, "", nil
	}
	note := NoteSkipIf
	if l := firstLine(x.mask(strings.TrimSpace(out.stdout))); l != "" {
		note += " (" + l + ")"
	}
	return true, note, nil
}

// outputProblem checks a command's output against ok_if and fail_if.
func outputProblem(step runbook.Step, output string) string {
	if step.FailIf != "" {
		if re, err := regexp.Compile(step.FailIf); err == nil && re.MatchString(output) {
			return fmt.Sprintf("the output matches fail_if (%s)", step.FailIf)
		}
	}
	if step.OkIf != "" {
		if re, err := regexp.Compile(step.OkIf); err == nil && !re.MatchString(output) {
			return fmt.Sprintf("the output doesn't match ok_if (%s)", step.OkIf)
		}
	}
	return ""
}

// requirements checks the programs the runbook and its libraries need.
func (x *executor) requirements() error {
	if x.opts.SkipRequires {
		return nil
	}
	reqs := runbook.Requirements(x.rb, x.opts.Libraries)
	if len(reqs) == 0 {
		return nil
	}
	var missing []string
	for _, r := range runbook.CheckRequirements(reqs) {
		if r.Problem == "" {
			continue
		}
		line := r.Problem
		if r.Install != "" {
			line += " — " + r.Install
		}
		if r.Why != "" {
			line += " (" + r.Why + ")"
		}
		if r.Optional || x.opts.DryRun {
			x.notify(Event{Kind: EventOutput, Step: -1, Stream: "info", Text: "⚠ " + line})
			continue
		}
		missing = append(missing, "  "+line)
	}
	if len(missing) > 0 {
		return errors.New("this runbook needs programs that aren't here:\n" + strings.Join(missing, "\n"))
	}
	return nil
}

// verifyResumed re-checks, before a resumed run goes on, that the last
// step done in the run it resumes still stands (its verify command). If it
// doesn't, that step is done again.
func (x *executor) verifyResumed(ctx context.Context) {
	if x.opts.Resumed == nil || x.opts.DryRun {
		return
	}
	last := -1
	for i, r := range x.run.Steps {
		if (r.Note == NoteCarried || r.Carried) && x.rb.Steps[i].Verify != "" {
			last = i
		}
	}
	if last < 0 {
		return
	}
	step := x.def(last)
	cmd, err := x.render(step.Verify)
	if err != nil {
		x.notify(Event{Kind: EventOutput, Step: last, Stream: "info", Text: "verify: " + err.Error()})
		return
	}
	x.notify(Event{Kind: EventOutput, Step: last, Stream: "info", Text: fmt.Sprintf("verifying step %d (%s), done in the resumed run: %s", last+1, step.Name, x.mask(cmd))})
	out := x.execStream(ctx, last, checkStep(step, cmd), cmd, true)
	if out.err == nil {
		x.notify(Event{Kind: EventOutput, Step: last, Stream: "info", Text: "✔ still stands"})
		return
	}
	x.notify(Event{Kind: EventOutput, Step: last, Stream: "info", Text: fmt.Sprintf("✘ step %d (%s) doesn't stand any more (%s): doing it again", last+1, step.Name, x.mask(out.err.Error()))})
	if skip, _, _ := x.ctl.Get(last); skip {
		x.ctl.Toggle("skip", last)
	}
	if r := &x.run.Steps[last]; r.Carried {
		// Done again: how it went before is an earlier try now.
		p := *r
		p.Before, p.Carried = nil, false
		r.Before = append(r.Before, p)
		*r = StepResult{Index: p.Index, Name: p.Name, Section: p.Section, Owner: p.Owner, Kind: p.Kind, Optional: p.Optional, Notes: p.Notes, Before: r.Before}
	}
	x.run.Steps[last].Status, x.run.Steps[last].Note = StatusPending, ""
}

// secretForms are the ways a secret value can appear in output: as is,
// and URL-encoded (in a connection URL).
func secretForms(v string) []string {
	forms := []string{v}
	for _, e := range []string{url.QueryEscape(v), url.PathEscape(v), url.UserPassword("u", v).String()[2:]} {
		if e != v && !contains(forms, e) {
			forms = append(forms, e)
		}
	}
	return forms
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return l
}
