// Package timeline lays a run out in time: each step's tries (and the
// shells and jobs alongside them) as bars, the run's notes, input changes,
// edits and resumes as marks, and the maintenance window and the time the
// run was stopped as bands. Long idle stretches (a run resumed days later)
// are cut short so the rest still reads. It renders as text (a terminal)
// or as a self-contained HTML page.
package timeline

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/egrif/runnr/internal/engine"
)

// Timeline is a run laid out in time.
type Timeline struct {
	Run        *engine.Run
	Start, End time.Time
	// Lanes are the run's steps, then the steps it ran that its runbook no
	// longer has.
	Lanes []Lane
	// Marks are moments: notes, input changes, edits, resumes.
	Marks []Mark
	// Bands are stretches of the whole run: the maintenance window, the
	// time it was stopped before a resume.
	Bands []Band
	// Moments are what happened, in order, to read beside the chart.
	Moments []Moment
	Axis    Axis
}

// Lane is a step.
type Lane struct {
	Num     int // 1-based (removed steps: their number when they ran)
	Name    string
	Section string
	Status  engine.Status
	Removed bool
	Spans   []Span
}

// Span is a stretch of a step: a try, a shell alongside it, a job.
type Span struct {
	From, To time.Time
	// Kind: try (its last), earlier (a try before the run was resumed),
	// background, shell or job.
	Kind   string
	Status engine.Status
	Label  string
	// Open: still going (To is now).
	Open bool
}

// Mark is a moment on a lane (Lane -1: the run).
type Mark struct {
	At   time.Time
	Kind string // note, input, edit, resume
	Lane int
	Text string
}

// Band is a stretch of the whole run.
type Band struct {
	From, To time.Time
	Kind     string // window, stopped
	Label    string
}

// Moment is a line of what happened.
type Moment struct {
	At   time.Time
	Kind string // start, end, failed, resume, window, note, input, edit
	Text string
}

// Build lays out r; now stands in for the end of what's still going.
func Build(r *engine.Run, now time.Time) *Timeline {
	t := &Timeline{Run: r, Start: r.Started, End: r.Ended}
	if t.End.IsZero() {
		t.End = now
	}
	end := func(s engine.StepResult) (time.Time, bool) {
		if !s.Ended.IsZero() {
			return s.Ended, false
		}
		if s.Status == engine.StatusRunning || s.Status == engine.StatusBackground {
			if r.Ended.IsZero() {
				return now, true
			}
			return r.Ended, false
		}
		return s.Started, false
	}
	lane := func(s engine.StepResult, num int, removed bool) Lane {
		l := Lane{Num: num, Name: s.Name, Section: s.Section, Status: s.Status, Removed: removed}
		for k, b := range s.Before {
			if b.Started.IsZero() {
				continue
			}
			to, _ := end(b)
			l.Spans = append(l.Spans, Span{From: b.Started, To: to, Kind: "earlier", Status: b.Status, Label: fmt.Sprintf("try %d, before the run was resumed", k+1)})
		}
		if !s.Started.IsZero() {
			to, open := end(s)
			kind := "try"
			if s.Kind == "background" {
				kind = "background"
			}
			l.Spans = append(l.Spans, Span{From: s.Started, To: to, Kind: kind, Status: s.Status, Label: firstLine(s.Error), Open: open})
		}
		day := s.Started
		if day.IsZero() {
			day = r.Started
		}
		for k, sh := range s.Shells {
			if from, to, ok := shellSpan(sh, day); ok {
				l.Spans = append(l.Spans, Span{From: from, To: to, Kind: "shell", Label: fmt.Sprintf("shell session %d", k+1)})
			}
		}
		for _, j := range s.Jobs {
			if j.Started.IsZero() {
				continue
			}
			to := j.Ended
			if to.IsZero() {
				to = t.End
			}
			l.Spans = append(l.Spans, Span{From: j.Started, To: to, Kind: "job", Label: j.Command})
		}
		return l
	}
	for i, s := range r.Steps {
		t.Lanes = append(t.Lanes, lane(s, i+1, false))
		for _, n := range s.Notes {
			t.Marks = append(t.Marks, Mark{At: n.Time, Kind: "note", Lane: i, Text: noteText(n)})
		}
	}
	for _, s := range r.Removed {
		t.Lanes = append(t.Lanes, lane(s, s.Index+1, true))
	}
	for _, n := range r.Notes {
		t.Marks = append(t.Marks, Mark{At: n.Time, Kind: "note", Lane: -1, Text: noteText(n)})
	}
	for _, c := range r.InputChanges {
		t.Marks = append(t.Marks, Mark{At: c.At, Kind: "input", Lane: -1, Text: fmt.Sprintf("%s: %s → %s%s", c.Name, c.Old, c.New, by(c.By))})
	}
	for _, e := range r.Edits {
		if e.Step >= 0 && e.Step < len(r.Steps) {
			t.Marks = append(t.Marks, Mark{At: e.At, Kind: "edit", Lane: e.Step, Text: fmt.Sprintf("step %d edited", e.Step+1)})
		}
	}
	for _, rs := range r.Resumptions {
		text := fmt.Sprintf("resumed from step %d (%s)", rs.From+1, rs.HowItEnded())
		if rs.Was != "" {
			text += fmt.Sprintf(" with runbook version %s (it had run version %s)", rs.RunbookVersion.Short(), rs.Was.Short())
		}
		t.Marks = append(t.Marks, Mark{At: rs.At, Kind: "resume", Lane: -1, Text: text})
		if !rs.Ended.IsZero() && rs.At.After(rs.Ended) {
			t.Bands = append(t.Bands, Band{From: rs.Ended, To: rs.At, Kind: "stopped", Label: "stopped (" + string(rs.Status) + ")"})
		}
	}
	if w := r.Window; w != nil && !w.Opened.IsZero() {
		to := w.Closed
		if to.IsZero() {
			to = t.End
		}
		t.Bands = append(t.Bands, Band{From: w.Opened, To: to, Kind: "window", Label: "maintenance window"})
	}
	slices.SortStableFunc(t.Marks, func(a, b Mark) int { return a.At.Compare(b.At) })

	// The whole of it, whatever the record says of its start and end.
	for _, l := range t.Lanes {
		for _, s := range l.Spans {
			t.widen(s.From, s.To)
		}
	}
	for _, m := range t.Marks {
		t.widen(m.At, m.At)
	}
	for _, b := range t.Bands {
		t.widen(b.From, b.To)
	}
	t.moments()
	t.Axis = newAxis(t)
	return t
}

func (t *Timeline) widen(from, to time.Time) {
	if !from.IsZero() && (t.Start.IsZero() || from.Before(t.Start)) {
		t.Start = from
	}
	if to.After(t.End) {
		t.End = to
	}
}

func (t *Timeline) moments() {
	r := t.Run
	add := func(at time.Time, kind, text string) {
		if !at.IsZero() {
			t.Moments = append(t.Moments, Moment{At: at, Kind: kind, Text: text})
		}
	}
	add(r.Started, "start", fmt.Sprintf("run started (%s, runbook %s version %s)", r.Mode, r.Runbook, r.RunbookVersion.Short()))
	for _, l := range t.Lanes {
		for _, s := range l.Spans {
			if (s.Kind == "try" || s.Kind == "earlier" || s.Kind == "background") && (s.Status == engine.StatusFailed || s.Status == engine.StatusAborted) {
				text := fmt.Sprintf("step %d (%s) %s", l.Num, l.Name, s.Status)
				if s.Kind == "earlier" {
					text += ", before the run was resumed"
				}
				if s.Label != "" && s.Kind != "earlier" {
					text += ": " + s.Label
				}
				add(s.To, "failed", text)
			}
		}
	}
	for _, m := range t.Marks {
		text := m.Text
		if m.Lane >= 0 && m.Kind == "note" {
			text = fmt.Sprintf("note on step %d (%s): %s", t.Lanes[m.Lane].Num, t.Lanes[m.Lane].Name, m.Text)
		} else if m.Kind == "note" {
			text = "note: " + m.Text
		} else if m.Kind == "input" {
			text = "input changed: " + m.Text
		}
		add(m.At, m.Kind, text)
	}
	for _, b := range t.Bands {
		if b.Kind == "window" {
			add(b.From, "window", "maintenance window opened")
			if w := r.Window; w != nil && !w.Closed.IsZero() {
				add(w.Closed, "window", "maintenance window closed after "+Dur(w.Closed.Sub(w.Opened)))
			}
		}
	}
	if !r.Ended.IsZero() {
		text := "run " + string(r.Status)
		if r.Error != "" && r.Status != engine.StatusSucceeded {
			text += ": " + firstLine(r.Error)
		}
		add(r.Ended, "end", text)
	}
	slices.SortStableFunc(t.Moments, func(a, b Moment) int { return a.At.Compare(b.At) })
}

func noteText(n engine.Note) string { return n.Text + by(n.Author) }

func by(who string) string {
	if who == "" {
		return ""
	}
	return " (" + who + ")"
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

var stampRe = regexp.MustCompile(`(?m)^\[(\d\d):(\d\d):(\d\d)\] `)

// shellSpan is when a shell's transcript runs, from its timed lines (the
// clock times are on day's date, or the days after it).
func shellSpan(transcript string, day time.Time) (from, to time.Time, ok bool) {
	ms := stampRe.FindAllStringSubmatch(transcript, -1)
	if len(ms) == 0 || day.IsZero() {
		return
	}
	at := func(m []string) time.Time {
		var h, mi, s int
		fmt.Sscan(m[1], &h)
		fmt.Sscan(m[2], &mi)
		fmt.Sscan(m[3], &s)
		y, mo, d := day.Local().Date()
		return time.Date(y, mo, d, h, mi, s, 0, time.Local)
	}
	from = at(ms[0])
	// A shell opened just before midnight, or on a step started the day before.
	if from.Before(day.Add(-time.Hour)) {
		from = from.AddDate(0, 0, 1)
	}
	to, prev := from, from
	for _, m := range ms[1:] {
		x := at(m)
		for x.Before(prev) {
			x = x.AddDate(0, 0, 1)
		}
		prev, to = x, x
	}
	return from, to, true
}

// Dur is a duration for people: 45s, 3m 20s, 2h 05m, 3d 4h.
func Dur(d time.Duration) string {
	switch {
	case d < 0:
		return "0s"
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Round(time.Second).Seconds()))
	case d < time.Hour:
		d = d.Round(time.Second)
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		d = d.Round(time.Minute)
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		d = d.Round(time.Hour)
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}
