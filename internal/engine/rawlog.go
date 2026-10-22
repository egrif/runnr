package engine

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// RawLog is a run as plain text, in order: the run, then each step that
// was reached with its command, output, checks, shell sessions, result and
// notes. Values are as recorded (credentials masked).
func RawLog(r *Run) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	at := func(t time.Time) string {
		if t.IsZero() {
			return "--:--:--"
		}
		return t.Local().Format("15:04:05")
	}
	took := func(from, to time.Time) string {
		if from.IsZero() || to.IsZero() {
			return ""
		}
		return to.Sub(from).Round(time.Millisecond).String()
	}

	ver := ""
	if r.RunbookVersion > 0 {
		ver = fmt.Sprintf(" v%d", r.RunbookVersion)
	}
	w("run %s · %s%s · %s", r.ID, r.Runbook, ver, r.Mode)
	if r.DryRun {
		w(" · dry run")
	}
	w("\nstarted %s · %s", r.Started.Local().Format("2006-01-02 15:04:05 MST"), r.Status)
	if d := took(r.Started, r.Ended); d != "" {
		w(" after %s", d)
	}
	w("\n")
	if r.Resumes != "" {
		w("resumes %s\n", r.Resumes)
	}
	if len(r.Inputs) > 0 {
		var in []string
		for _, k := range slices.Sorted(maps.Keys(r.Inputs)) {
			in = append(in, k+"="+r.Inputs[k])
		}
		w("inputs: %s\n", strings.Join(in, " "))
	}
	if win := r.Window; win != nil && !win.Opened.IsZero() {
		w("maintenance window: opened %s", at(win.Opened))
		if !win.Closed.IsZero() {
			w(", closed %s (%s)", at(win.Closed), took(win.Opened, win.Closed))
		}
		w("\n")
	}
	if r.Error != "" {
		w("error: %s\n", r.Error)
	}
	for _, n := range r.Notes {
		w("note (%s, %s): %s\n", n.Author, at(n.Time), n.Text)
	}

	for _, rs := range r.Resumptions {
		w("resumed %s from step %d (it had %s", rs.At.Local().Format("2006-01-02 15:04:05"), rs.From+1, rs.Status)
		if rs.Error != "" {
			w(": %s", rs.Error)
		}
		w(")")
		if rs.Was != 0 {
			w(" with runbook version %d (it had run version %d)", rs.RunbookVersion, rs.Was)
		}
		w("\n")
	}

	// one writes a step's try; an earlier one (before a resume) says so.
	one := func(i int, s StepResult, earlier bool) {
		w("\n")
		if s.Status == StatusPending {
			w("[%s] ○ %d. %s — not reached\n", at(time.Time{}), i+1, s.Name)
			return
		}
		head := fmt.Sprintf("%d. %s (%s", i+1, s.Name, s.Kind)
		if s.Way != "" {
			head += ", way: " + s.Way
		}
		if s.Optional {
			head += ", optional"
		}
		if earlier {
			head += ", before the run was resumed"
		}
		w("[%s] ▶ %s)\n", at(s.Started), head)
		// (A checks step's command is its plan; its checks are listed below.)
		if c := strings.TrimRight(s.Command, "\n"); c != "" && s.Kind != "checks" {
			prefix := "$ "
			if s.Kind == "manual" {
				prefix = "☞ "
			}
			w("%s%s\n", prefix, strings.ReplaceAll(c, "\n", "\n  "))
		}
		if o := strings.TrimRight(s.Output, "\n"); o != "" {
			w("%s\n", o)
		}
		for _, c := range s.Checks {
			mark := "✓"
			switch {
			case c.NotRun:
				mark = "·"
			case !c.Passed:
				mark = "✗"
			}
			w("  %s check: %s", mark, c.Name)
			if c.Attempt > 0 {
				w(" (attempt %d)", c.Attempt)
			}
			if c.NotRun {
				w(" — not run")
			}
			w("\n")
			if o := strings.TrimRight(c.Output, "\n"); o != "" {
				w("    %s\n", strings.ReplaceAll(o, "\n", "\n    "))
			}
			if c.Error != "" {
				w("    error: %s\n", c.Error)
			}
		}
		for n, sh := range s.Shells {
			w("  ┌ shell session %d\n  │ %s\n  └\n", n+1, strings.ReplaceAll(strings.TrimRight(sh, "\n"), "\n", "\n  │ "))
		}
		end := []string{string(s.Status)}
		if s.ExitCode != nil {
			end = append(end, fmt.Sprintf("exit %d", *s.ExitCode))
		}
		if s.Attempts > 1 {
			end = append(end, fmt.Sprintf("%d attempts", s.Attempts))
		}
		if d := took(s.Started, s.Ended); d != "" {
			end = append(end, d)
		}
		if s.Note != "" {
			end = append(end, s.Note)
		}
		w("[%s] %s %s\n", at(s.Ended), statusMark[s.Status], strings.Join(end, " · "))
		if s.Error != "" && s.Status != StatusSucceeded {
			w("  error: %s\n", s.Error)
		}
		for _, n := range s.Notes {
			w("  note (%s, %s): %s\n", n.Author, at(n.Time), n.Text)
		}
	}
	for i, s := range r.Steps {
		for _, e := range s.Before {
			one(i, e, true)
		}
		one(i, s, false)
	}
	if len(r.Removed) > 0 {
		w("\n── steps no longer in the runbook (from an earlier version) ──\n")
		for _, s := range r.Removed {
			one(s.Index, s, false)
		}
	}
	return b.String()
}

var statusMark = map[Status]string{
	StatusSucceeded: "✔", StatusFailed: "✘", StatusSkipped: "↷", StatusAborted: "■",
	StatusForced: "⚑", StatusRunning: "▶", StatusPending: "○", StatusBackground: "◐",
}
