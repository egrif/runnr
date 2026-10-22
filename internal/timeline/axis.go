package timeline

import (
	"slices"
	"time"
)

// Axis maps time onto 0..1 across the chart. Idle stretches long enough to
// swamp the rest (nothing running: a run stopped and resumed later) are
// cut to a fixed short width.
type Axis struct {
	Start, End time.Time
	Segs       []Seg
}

// Seg is a stretch of the axis; Cut ones are drawn short, whatever they
// last.
type Seg struct {
	From, To time.Time
	X0, X1   float64
	Cut      bool
}

// cutWidth is the share of the chart a cut stretch takes.
const cutWidth = 0.04

func newAxis(t *Timeline) Axis {
	type iv struct{ from, to time.Time }
	var busy []iv
	for _, l := range t.Lanes {
		for _, s := range l.Spans {
			busy = append(busy, iv{s.From, s.To})
		}
	}
	for _, m := range t.Marks {
		busy = append(busy, iv{m.At, m.At})
	}
	slices.SortFunc(busy, func(a, b iv) int { return a.from.Compare(b.from) })
	total := t.End.Sub(t.Start)
	long := max(30*time.Minute, total/5)
	var cuts []iv
	at := t.Start
	for _, b := range busy {
		if b.from.Sub(at) > long {
			cuts = append(cuts, iv{at, b.from})
		}
		if b.to.After(at) {
			at = b.to
		}
	}
	if t.End.Sub(at) > long {
		cuts = append(cuts, iv{at, t.End})
	}
	return spread(t.Start, t.End, func(yield func(from, to time.Time) bool) {
		for _, c := range cuts {
			if !yield(c.from, c.to) {
				return
			}
		}
	})
}

// spread lays out start..end with the cut stretches given (in order).
func spread(start, end time.Time, cuts func(func(from, to time.Time) bool)) Axis {
	a := Axis{Start: start, End: end}
	at := start
	for from, to := range cuts {
		if from.After(at) {
			a.Segs = append(a.Segs, Seg{From: at, To: from})
		}
		a.Segs = append(a.Segs, Seg{From: from, To: to, Cut: true})
		at = to
	}
	if end.After(at) || len(a.Segs) == 0 {
		a.Segs = append(a.Segs, Seg{From: at, To: end})
	}
	var live time.Duration
	ncut := 0
	for _, s := range a.Segs {
		if s.Cut {
			ncut++
		} else {
			live += s.To.Sub(s.From)
		}
	}
	room := 1 - cutWidth*float64(ncut)
	x := 0.0
	for i := range a.Segs {
		s := &a.Segs[i]
		s.X0 = x
		switch {
		case s.Cut:
			x += cutWidth
		case live > 0:
			x += room * float64(s.To.Sub(s.From)) / float64(live)
		default:
			x += room / float64(len(a.Segs)-ncut)
		}
		s.X1 = x
	}
	return a
}

// Zoomed is the axis over from..to alone, nothing cut.
func Zoomed(from, to time.Time) Axis {
	return spread(from, to, func(func(from, to time.Time) bool) {})
}

// X is where t falls (0..1; clamped).
func (a Axis) X(t time.Time) float64 {
	if !t.After(a.Start) {
		return 0
	}
	if !t.Before(a.End) {
		return 1
	}
	for _, s := range a.Segs {
		if t.Before(s.To) || t.Equal(s.To) {
			d := s.To.Sub(s.From)
			if d <= 0 {
				return s.X0
			}
			return s.X0 + (s.X1-s.X0)*float64(t.Sub(s.From))/float64(d)
		}
	}
	return 1
}

// Tick is a labelled time on the axis.
type Tick struct {
	At    time.Time
	X     float64
	Label string
}

var tickSteps = []time.Duration{
	time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second, 30 * time.Second,
	time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute,
	time.Hour, 2 * time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour,
}

// Ticks are about n labelled times, none closer than minGap (of 0..1) to
// another or to a cut.
func (a Axis) Ticks(n int, minGap float64) []Tick {
	var live time.Duration
	for _, s := range a.Segs {
		if !s.Cut {
			live += s.To.Sub(s.From)
		}
	}
	step := tickSteps[len(tickSteps)-1]
	for _, st := range tickSteps {
		if live/st <= time.Duration(n) {
			step = st
			break
		}
	}
	var out []Tick
	lastX, lastDay := -1.0, ""
	for _, s := range a.Segs {
		if s.Cut {
			lastX = s.X1
			continue
		}
		// Ticks on the local clock's round times.
		_, off := s.From.Local().Zone()
		shift := time.Duration(off) * time.Second
		t := s.From.Add(shift).Truncate(step).Add(-shift)
		if t.Before(s.From) {
			t = t.Add(step)
		}
		for ; !t.After(s.To); t = t.Add(step) {
			x := a.X(t)
			if x-lastX < minGap || s.X1-x < minGap/2 && s.To != a.End {
				continue
			}
			lt := t.Local()
			label := lt.Format("15:04")
			if step < time.Minute {
				label = lt.Format("15:04:05")
			}
			if day := lt.Format("Jan 2"); day != lastDay {
				if lastDay != "" || a.End.Sub(a.Start) > 20*time.Hour {
					label = day + " " + label
				}
				lastDay = day
			}
			out = append(out, Tick{At: t, X: x, Label: label})
			lastX = x
		}
	}
	return out
}
