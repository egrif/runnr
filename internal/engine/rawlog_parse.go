package engine

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Reading a raw log (RawLog's text) back into a run, to import it. Most of
// the run comes back; what doesn't: times are to the second (and their
// date is worked out from the start's), step sections, the steps' places
// in a maintenance window, jobs, a step's edits, and the run's state. An
// output line that starts with two spaces right after a command reads as
// part of the command. Such runs are marked FromLog.

var (
	rlRunSep   = regexp.MustCompile(`(?m)^════ \d+ of \d+: run \S+ ════\n`)
	rlHead     = regexp.MustCompile(`^run (\S+) · (.+?)(?: v(\d+))? · ([a-z-]+)( · dry run)?$`)
	rlStarted  = regexp.MustCompile(`^started (\d{4}-\d\d-\d\d \d\d:\d\d:\d\d \S+) · ([a-z]+)(?: after (\S+))?$`)
	rlWindow   = regexp.MustCompile(`^maintenance window: opened (\S+)(?:, closed (\S+) \((\S*)\))?$`)
	rlNote     = regexp.MustCompile(`^ *note \((.*), (\d\d:\d\d:\d\d|--:--:--)\): (.*)$`)
	rlResumed  = regexp.MustCompile(`^resumed (\d{4}-\d\d-\d\d \d\d:\d\d:\d\d) from step (\d+) \((.*?)\)(?: with runbook version (\d+) \(it had run version (\d+)\))?$`)
	rlPending  = regexp.MustCompile(`^\[--:--:--\] ○ (\d+)\. (.*) — not reached$`)
	rlStart    = regexp.MustCompile(`^\[(\d\d:\d\d:\d\d|--:--:--)\] ▶ (\d+)\. (.*) \(([a-z-]+)((?:, [^,()]+)*)\)$`)
	rlEnd      = regexp.MustCompile(`^\[(\d\d:\d\d:\d\d|--:--:--)\] \S+ (succeeded|failed|skipped|aborted|suspended|forced|running|pending|background)((?: · .*)?)$`)
	rlCheck    = regexp.MustCompile(`^  ([✓✗·]) check: (.*?)(?: \(attempt (\d+)\))?( — not run)?$`)
	rlShell    = regexp.MustCompile(`^  ┌ shell session \d+$`)
	rlInputKey = regexp.MustCompile(`(?:^| )([A-Za-z_][A-Za-z0-9_.-]*)=`)
)

// ParseRawLog reads the runs in a raw log: one, or several one after
// another (as an export in one file writes them).
func ParseRawLog(text string) ([]*Run, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var parts []string
	if loc := rlRunSep.FindAllStringIndex(text, -1); loc != nil {
		for i, l := range loc {
			end := len(text)
			if i+1 < len(loc) {
				end = loc[i+1][0]
			}
			parts = append(parts, text[l[1]:end])
		}
	} else {
		parts = []string{text}
	}
	var runs []*Run
	for i, p := range parts {
		r, err := parseOneRawLog(strings.TrimRight(p, "\n"))
		if err != nil {
			if len(parts) > 1 {
				return nil, fmt.Errorf("run %d of %d: %w", i+1, len(parts), err)
			}
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, nil
}

// clock turns a log's times of day into times: on the day the run
// started, moving to the next day when the clock goes back.
type clock struct{ last time.Time }

func (c *clock) at(hms string) time.Time {
	if hms == "--:--:--" || c.last.IsZero() {
		return time.Time{}
	}
	t, err := time.ParseInLocation("15:04:05", hms, c.last.Location())
	if err != nil {
		return time.Time{}
	}
	y, m, d := c.last.Date()
	x := time.Date(y, m, d, t.Hour(), t.Minute(), t.Second(), 0, c.last.Location())
	// A few seconds back is rounding (a step's time to the millisecond
	// against the next one's to the second), not the next day.
	if x.Before(c.last.Add(-time.Minute)) {
		x = x.AddDate(0, 0, 1)
	}
	if x.After(c.last) {
		c.last = x
	}
	return x
}

// settle is a stretch that started in start's second and lasted d, given
// the time of day it ended (to the second): the start's lost fraction is
// what puts the end in that second.
func settle(start time.Time, d time.Duration, end time.Time) (time.Time, time.Time) {
	to := start.Add(d)
	if end.IsZero() {
		return start, to
	}
	// Times of day only: the end's date can't be trusted after days.
	gap := (end.Sub(to)%(24*time.Hour) + 24*time.Hour) % (24 * time.Hour)
	if gap > 0 && gap < time.Second {
		return start.Add(gap), to.Add(gap)
	}
	return start, to
}

// near is a time of day after from (a note on a step started then),
// without moving the clock on.
func near(from time.Time, hms string) time.Time {
	c := clock{from}
	return c.at(hms)
}

func parseOneRawLog(text string) (*Run, error) {
	lines := strings.Split(text, "\n")
	m := rlHead.FindStringSubmatch(lines[0])
	if m == nil {
		return nil, errors.New("not a runnr raw log (it starts with “run <id> · <runbook> · <mode>”)")
	}
	r := &Run{ID: m[1], Runbook: m[2], Mode: m[4], DryRun: m[5] != "", Inputs: map[string]string{}, FromLog: true}
	r.RunbookVersion, _ = strconv.Atoi(m[3])
	if len(lines) < 2 {
		return nil, errors.New("the log ends after its first line")
	}
	m = rlStarted.FindStringSubmatch(lines[1])
	if m == nil {
		return nil, fmt.Errorf("line 2 should say when it started: %q", lines[1])
	}
	started, err := time.ParseInLocation("2006-01-02 15:04:05 MST", m[1], time.Local)
	if err != nil {
		return nil, fmt.Errorf("when it started: %w", err)
	}
	r.Started, r.Status = started, Status(m[2])
	if d, err := time.ParseDuration(m[3]); err == nil {
		r.Ended = started.Add(d)
	}
	c := &clock{started}

	// The run's own lines, up to the first blank one.
	i := 2
	for ; i < len(lines) && lines[i] != ""; i++ {
		l := lines[i]
		switch {
		case strings.HasPrefix(l, "resumes "):
			r.Resumes = strings.TrimPrefix(l, "resumes ")
		case strings.HasPrefix(l, "inputs: "):
			in := strings.TrimPrefix(l, "inputs: ")
			ks := rlInputKey.FindAllStringSubmatchIndex(in, -1)
			for k, loc := range ks {
				end := len(in)
				if k+1 < len(ks) {
					end = ks[k+1][0]
				}
				r.Inputs[in[loc[2]:loc[3]]] = in[loc[1]:end]
			}
		case strings.HasPrefix(l, "maintenance window: "):
			if m := rlWindow.FindStringSubmatch(l); m != nil {
				w := &WindowRecord{Opened: near(started, m[1])}
				if m[2] != "" {
					w.Closed = near(w.Opened, m[2])
					if d, err := time.ParseDuration(m[3]); err == nil {
						w.Opened, w.Closed = settle(w.Opened, d, w.Closed)
					}
				}
				r.Window = w
			}
		case strings.HasPrefix(l, "error: "):
			r.Error = strings.TrimPrefix(l, "error: ")
			// An error can run over several lines.
			for i+1 < len(lines) && lines[i+1] != "" && !rlRunLine(lines[i+1]) {
				i++
				r.Error += "\n" + lines[i]
			}
		case rlNote.MatchString(l):
			m := rlNote.FindStringSubmatch(l)
			r.Notes = append(r.Notes, Note{Author: m[1], Time: near(started, m[2]), Text: m[3]})
		case rlResumed.MatchString(l):
			m := rlResumed.FindStringSubmatch(l)
			at, _ := time.ParseInLocation("2006-01-02 15:04:05", m[1], time.Local)
			from, _ := strconv.Atoi(m[2])
			rs := Resumption{At: at, From: from - 1}
			rs.Status, rs.Error = howItEnded(m[3])
			rs.RunbookVersion, _ = strconv.Atoi(m[4])
			rs.Was, _ = strconv.Atoi(m[5])
			r.Resumptions = append(r.Resumptions, rs)
		}
	}

	// The steps: each after a blank line.
	removed := false
	var before map[int][]StepResult = map[int][]StepResult{}
	for i < len(lines) {
		l := lines[i]
		i++
		switch {
		case l == "":
			continue
		case strings.HasPrefix(l, "── steps no longer in the runbook"):
			removed = true
			continue
		}
		var s StepResult
		var earlier bool
		var n int
		if m := rlPending.FindStringSubmatch(l); m != nil {
			n, _ = strconv.Atoi(m[1])
			s = StepResult{Name: m[2], Status: StatusPending}
		} else if m := rlStart.FindStringSubmatch(l); m != nil {
			n, _ = strconv.Atoi(m[2])
			s = StepResult{Name: m[3], Kind: m[4], Started: c.at(m[1])}
			for _, f := range strings.Split(m[5], ", ") {
				switch {
				case f == "":
				case strings.HasPrefix(f, "way: "):
					s.Way = strings.TrimPrefix(f, "way: ")
				case f == "optional":
					s.Optional = true
				case f == "before the run was resumed":
					earlier = true
				}
			}
			i = parseStepBody(lines, i, &s, c)
		} else {
			return nil, fmt.Errorf("line %d: expected a step, got %q", i, l)
		}
		s.Index = n - 1
		switch {
		case removed:
			r.Removed = append(r.Removed, s)
		case earlier:
			before[n-1] = append(before[n-1], s)
		default:
			if n-1 != len(r.Steps) {
				return nil, fmt.Errorf("line %d: step %d where step %d should be", i, n, len(r.Steps)+1)
			}
			s.Before = before[n-1]
			r.Steps = append(r.Steps, s)
		}
	}
	if r.Ended.IsZero() && r.Status != StatusRunning {
		r.Ended = c.last
	}
	return r, nil
}

// rlRunLine says whether l starts another of the run's own lines.
func rlRunLine(l string) bool {
	for _, p := range []string{"resumes ", "inputs: ", "maintenance window: ", "error: ", "resumed "} {
		if strings.HasPrefix(l, p) {
			return true
		}
	}
	return rlNote.MatchString(l)
}

// howItEnded reads Resumption.HowItEnded back.
func howItEnded(s string) (Status, string) {
	switch {
	case s == "it was suspended":
		return StatusSuspended, ""
	case strings.HasPrefix(s, "it was "):
		return StatusSuspended, strings.TrimPrefix(s, "it was ")
	case strings.HasPrefix(s, "it had "):
		st, e, _ := strings.Cut(strings.TrimPrefix(s, "it had "), ": ")
		return Status(st), e
	}
	return "", s
}

// parseStepBody reads a started step's lines after its first: its
// command, output, checks, shells, how it ended, its error and notes.
// It returns the index of the line after them.
func parseStepBody(lines []string, i int, s *StepResult, c *clock) int {
	if i < len(lines) && (strings.HasPrefix(lines[i], "$ ") || strings.HasPrefix(lines[i], "☞ ")) {
		cmd := []string{lines[i][len("$ "):]}
		if strings.HasPrefix(lines[i], "☞ ") {
			cmd[0] = strings.TrimPrefix(lines[i], "☞ ")
		}
		i++
		for i < len(lines) && strings.HasPrefix(lines[i], "  ") && !stepPart(lines[i]) {
			cmd = append(cmd, lines[i][2:])
			i++
		}
		s.Command = strings.Join(cmd, "\n") + "\n"
	}
	var out []string
	for ; i < len(lines); i++ {
		l := lines[i]
		if m := rlCheck.FindStringSubmatch(l); m != nil {
			ch := CheckResult{Name: m[2], Passed: m[1] == "✓", NotRun: m[4] != ""}
			ch.Attempt, _ = strconv.Atoi(m[3])
			var co []string
			for i+1 < len(lines) && strings.HasPrefix(lines[i+1], "    ") {
				i++
				if e, ok := strings.CutPrefix(lines[i], "    error: "); ok {
					ch.Error = e
				} else {
					co = append(co, lines[i][4:])
				}
			}
			if len(co) > 0 {
				ch.Output = strings.Join(co, "\n") + "\n"
			}
			s.Checks = append(s.Checks, ch)
			continue
		}
		if rlShell.MatchString(l) {
			var sh []string
			for i+1 < len(lines) && strings.HasPrefix(lines[i+1], "  │") {
				i++
				sh = append(sh, strings.TrimPrefix(strings.TrimPrefix(lines[i], "  │"), " "))
			}
			if i+1 < len(lines) && lines[i+1] == "  └" {
				i++
			}
			s.Shells = append(s.Shells, strings.Join(sh, "\n"))
			continue
		}
		if m := rlEnd.FindStringSubmatch(l); m != nil {
			s.Status = Status(m[2])
			end := c.at(m[1])
			var dur time.Duration
			var note []string
			for _, p := range strings.Split(strings.TrimPrefix(m[3], " · "), " · ") {
				switch {
				case p == "":
				case strings.HasPrefix(p, "exit "):
					if n, err := strconv.Atoi(strings.TrimPrefix(p, "exit ")); err == nil {
						s.ExitCode = &n
					}
				case strings.HasSuffix(p, " attempts") && len(note) == 0:
					s.Attempts, _ = strconv.Atoi(strings.TrimSuffix(p, " attempts"))
				default:
					if d, err := time.ParseDuration(p); err == nil && dur == 0 && len(note) == 0 {
						dur = d
					} else {
						note = append(note, p)
					}
				}
			}
			s.Note = strings.Join(note, " · ")
			s.Ended = end
			if !s.Started.IsZero() && dur > 0 {
				// The start is to the second and the duration to the
				// millisecond: the start's lost fraction is what puts the
				// end in its second.
				s.Started, s.Ended = settle(s.Started, dur, end)
				if s.Ended.After(c.last) {
					c.last = s.Ended // a step that ran for days
				}
			}
			i++
			// Its error (which can run over lines) and notes.
			if i < len(lines) && strings.HasPrefix(lines[i], "  error: ") {
				e := []string{strings.TrimPrefix(lines[i], "  error: ")}
				i++
				for i < len(lines) && lines[i] != "" && !rlNote.MatchString(lines[i]) {
					e = append(e, lines[i])
					i++
				}
				s.Error = strings.Join(e, "\n")
			}
			for i < len(lines) && rlNote.MatchString(lines[i]) {
				m := rlNote.FindStringSubmatch(lines[i])
				s.Notes = append(s.Notes, Note{Author: m[1], Time: near(s.Started, m[2]), Text: m[3]})
				i++
			}
			break
		}
		out = append(out, l)
	}
	if len(out) > 0 {
		s.Output = strings.Join(out, "\n") + "\n"
	}
	return i
}

// stepPart says whether a line indented two spaces is a step's check or
// shell rather than more of its command.
func stepPart(l string) bool { return rlCheck.MatchString(l) || rlShell.MatchString(l) }
