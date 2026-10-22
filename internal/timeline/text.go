package timeline

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/egrif/runnr/internal/engine"
)

type cell struct {
	r     rune
	color string
}

// Text renders the timeline for a terminal width columns wide, in colour
// if color.
func (t *Timeline) Text(width int, color bool) string {
	r := t.Run
	var b strings.Builder
	paint := func(c cell) string {
		if !color || c.color == "" {
			return string(c.r)
		}
		return "\x1b[" + c.color + "m" + string(c.r) + "\x1b[0m"
	}
	grey := func(s string) string {
		if !color {
			return s
		}
		return "\x1b[90m" + s + "\x1b[0m"
	}

	fmt.Fprintf(&b, "%s · %s · %s · %s → %s (%s)\n", r.Runbook, r.ID, r.Status,
		t.Start.Local().Format("2006-01-02 15:04:05"), clock(t.End, t.Start), Dur(t.End.Sub(t.Start)))

	// The label column: number, name, the last try's duration.
	labels := make([]string, len(t.Lanes))
	durs := make([]string, len(t.Lanes))
	lw := 0
	for i, l := range t.Lanes {
		labels[i] = fmt.Sprintf("%2d %s", l.Num, l.Name)
		for _, s := range l.Spans {
			if s.Kind == "try" || s.Kind == "background" {
				durs[i] = Dur(s.To.Sub(s.From))
			}
		}
		lw = max(lw, len([]rune(labels[i]))+1+len(durs[i]))
	}
	lw = min(max(lw, 12), 40)
	cw := max(width-lw-2, 20)
	label := func(name, dur string) string {
		room := lw - len(dur) - 1
		rs := []rune(name)
		if len(rs) > room {
			rs = append(rs[:room-1], '…')
		}
		return string(rs) + strings.Repeat(" ", lw-len(rs)-len(dur)) + dur + "  "
	}
	pos := func(x float64) int { return min(int(x*float64(cw)), cw-1) }
	span := func(row []cell, from, to time.Time, c cell) {
		a := pos(t.Axis.X(from))
		z := int(math.Ceil(t.Axis.X(to)*float64(cw))) - 1
		for k := a; k <= max(a, min(z, cw-1)); k++ {
			row[k] = c
		}
	}
	blank := func(fill rune) []cell {
		row := make([]cell, cw)
		for k := range row {
			row[k] = cell{fill, ""}
		}
		return row
	}
	line := func(lab string, row []cell) {
		b.WriteString(lab)
		for _, c := range row {
			b.WriteString(paint(c))
		}
		b.WriteString("\n")
	}

	// Times along the top, and the axis with its cuts.
	ticks := t.Axis.Ticks(max(cw/14, 2), 0)
	top := []rune(strings.Repeat(" ", cw))
	axis := blank('─')
	next := 0
	for _, tk := range ticks {
		p := pos(tk.X)
		lab := []rune(tk.Label)
		if p < next || p+len(lab) > cw {
			continue
		}
		copy(top[p:], lab)
		next = p + len(lab) + 2
		axis[p] = cell{'┬', ""}
	}
	for _, s := range t.Axis.Segs {
		if !s.Cut {
			continue
		}
		a, z := pos(s.X0), max(pos(s.X0), pos(s.X1)-1)
		for k := a; k <= z; k++ {
			axis[k] = cell{'╌', "90"}
		}
		if lab := []rune(" " + Dur(s.To.Sub(s.From)) + " "); z-a+1 >= len(lab) {
			for k, ch := range lab {
				axis[a+(z-a+1-len(lab))/2+k] = cell{ch, "90"}
			}
		}
	}
	b.WriteString(strings.Repeat(" ", lw+2) + string(top) + "\n")
	line(strings.Repeat(" ", lw+2), axis)

	// The run: when it was going, the window, when it was stopped, and its
	// own marks.
	run := blank(' ')
	span(run, t.Start, t.End, cell{'·', "90"})
	for _, bd := range t.Bands {
		switch bd.Kind {
		case "window":
			span(run, bd.From, bd.To, cell{'▀', "33"})
		case "stopped":
			span(run, bd.From, bd.To, cell{' ', ""})
		}
	}
	for _, m := range t.Marks {
		if m.Lane < 0 {
			run[pos(t.Axis.X(m.At))] = markCell(m.Kind)
		}
	}
	line(label("run", ""), run)

	section, removed := "", false
	for i, l := range t.Lanes {
		if l.Removed && !removed {
			removed = true
			b.WriteString(grey("── no longer in the runbook ──") + "\n")
		} else if !l.Removed && l.Section != section {
			section = l.Section
			if section != "" {
				b.WriteString(grey("── "+section+" ──") + "\n")
			}
		}
		// Shells and jobs alongside the step: a row of their own under it.
		side, sides := blank(' '), map[string]int{}
		for _, s := range l.Spans {
			if s.Kind == "shell" || s.Kind == "job" {
				span(side, s.From, s.To, cell{'┈', map[string]string{"shell": "35", "job": "36"}[s.Kind]})
				sides[s.Kind]++
			}
		}
		row := blank(' ')
		for _, s := range l.Spans {
			switch s.Kind {
			case "earlier":
				span(row, s.From, s.To, cell{'░', "2;" + statusColor(s.Status)})
			case "try", "background":
				span(row, s.From, s.To, tryCell(s))
			}
		}
		if l.Status == engine.StatusSkipped && len(l.Spans) == 0 {
			// A skip has no time of its own; show it at the run's start.
			row[0] = cell{'↷', "90"}
		}
		for _, m := range t.Marks {
			if m.Lane == i {
				row[pos(t.Axis.X(m.At))] = markCell(m.Kind)
			}
		}
		lab := label(labels[i], durs[i])
		if l.Removed {
			lab = grey(lab)
		}
		line(lab, row)
		if len(sides) > 0 {
			var what []string
			for _, k := range []string{"shell", "job"} {
				if n := sides[k]; n > 0 {
					what = append(what, fmt.Sprintf("%d %s%s", n, k, map[bool]string{true: "s"}[n > 1]))
				}
			}
			line(grey(label("    ┈ "+strings.Join(what, ", "), "")), side)
		}
	}
	b.WriteString(grey("█ ran (green ok, red ▓ failed, yellow forced) ░ earlier try ▒ background ┈ shell/job ▀ window ◆ note ✎ input/edit ↻ resumed ╌ cut short") + "\n")

	if len(t.Moments) > 0 {
		b.WriteString("\nKey moments\n")
		for _, m := range t.Moments {
			fmt.Fprintf(&b, "  %s  %s\n", clock(m.At, t.Start), m.Text)
		}
	}
	return b.String()
}

// clock is t's time of day, with its date if not on start's day.
func clock(t, start time.Time) string {
	lt := t.Local()
	if y, m, d := lt.Date(); y != start.Local().Year() || m != start.Local().Month() || d != start.Local().Day() {
		return lt.Format("Jan 2 15:04:05")
	}
	return lt.Format("15:04:05")
}

func statusColor(s engine.Status) string {
	switch s {
	case engine.StatusSucceeded:
		return "32"
	case engine.StatusFailed, engine.StatusAborted:
		return "31"
	case engine.StatusForced:
		return "33"
	case engine.StatusRunning:
		return "34"
	case engine.StatusBackground:
		return "36"
	}
	return "90"
}

func tryCell(s Span) cell {
	c := cell{'█', statusColor(s.Status)}
	switch {
	case s.Status == engine.StatusFailed || s.Status == engine.StatusAborted:
		c.r = '▓'
	case s.Kind == "background" || s.Status == engine.StatusBackground:
		c.r = '▒'
	case s.Status == engine.StatusSkipped:
		c.r = '↷'
	}
	return c
}

func markCell(kind string) cell {
	switch kind {
	case "note":
		return cell{'◆', "35"}
	case "resume":
		return cell{'↻', "1;34"}
	}
	return cell{'✎', "33"}
}
