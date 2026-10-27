package timeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/ver"
)

// demoRun is a made-up cutover: it fails late in the day, stays stopped
// overnight, and is resumed (with a shell, a background job, notes and an
// input change) through its maintenance window.
func demoRun() *engine.Run {
	day := time.Date(2026, 10, 20, 0, 0, 0, 0, time.Local)
	at := func(d, h, m, s int) time.Time {
		return day.AddDate(0, 0, d).Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(s)*time.Second)
	}
	ok, failed := engine.StatusSucceeded, engine.StatusFailed
	step := func(name, section string, st engine.Status, from, to time.Time) engine.StepResult {
		return engine.StepResult{Name: name, Section: section, Kind: "run", Status: st, Started: from, Ended: to}
	}
	r := &engine.Run{ID: "20261020-160000-abc123", Runbook: "orders-db-cutover", Mode: "interactive", RunbookVersion: ver.Of([]byte("v3")), Status: ok,
		Started: at(0, 16, 0, 0), Ended: at(1, 10, 41, 0)}
	r.Steps = []engine.StepResult{
		step("check replication", "prepare", ok, at(0, 16, 0, 5), at(0, 16, 2, 10)),
		step("snapshot orders-db", "prepare", ok, at(0, 16, 2, 10), at(0, 16, 21, 40)),
		step("read-only mode", "cutover", ok, at(1, 10, 15, 0), at(1, 10, 15, 20)),
		step("promote replica", "cutover", ok, at(1, 10, 15, 20), at(1, 10, 27, 0)),
		step("point the app at it", "cutover", ok, at(1, 10, 27, 0), at(1, 10, 30, 30)),
		step("watch error rates", "verify", ok, at(1, 10, 30, 30), at(1, 10, 40, 0)),
	}
	// Step 3 failed the day before; the resume ran it again.
	r.Steps[2].Before = []engine.StepResult{step("read-only mode", "cutover", failed, at(0, 16, 21, 40), at(0, 16, 24, 0))}
	r.Steps[2].Before[0].Error = "connection refused"
	r.Steps[0].Carried, r.Steps[1].Carried = true, true
	r.Steps[3].Shells = []string{"[10:18:02] $ psql -c 'select pg_is_in_recovery()'\n[10:18:03] f\n[10:21:40] $ exit"}
	r.Steps[5].Kind = "background"
	r.Steps[5].Jobs = []engine.JobRecord{{Command: "tail -f app.log", Started: at(1, 10, 31, 0), Ended: at(1, 10, 39, 0)}}
	r.Steps[3].Notes = []engine.Note{{Time: at(1, 10, 20, 0), Author: "pat", Text: "lag at zero"}}
	r.Notes = []engine.Note{{Time: at(0, 16, 30, 0), Author: "pat", Text: "db team on it, resume tomorrow"}}
	r.InputChanges = []engine.InputChange{{At: at(1, 10, 14, 0), Name: "target", Old: "replica-a", New: "replica-b", By: "pat", Step: 2}}
	r.Resumptions = []engine.Resumption{{At: at(1, 10, 14, 30), From: 2, Status: failed, Error: "step 3 failed", Ended: at(0, 16, 24, 5)}}
	r.Window = &engine.WindowRecord{OpenStep: 2, CloseStep: 4, Opened: at(1, 10, 15, 0), Closed: at(1, 10, 30, 30)}
	r.Removed = []engine.StepResult{{Index: 6, Name: "old smoke test", Kind: "run", Status: ok, Started: at(0, 16, 1, 0), Ended: at(0, 16, 1, 30)}}
	return r
}

func TestTimeline(t *testing.T) {
	tl := Build(demoRun(), time.Now())
	// The night it was stopped (from the last note to the input change
	// before the resume) is cut short; the rest shares the width.
	var cut *Seg
	for i, s := range tl.Axis.Segs {
		if s.Cut {
			cut = &tl.Axis.Segs[i]
		}
	}
	if cut == nil || cut.To.Sub(cut.From) != 17*time.Hour+44*time.Minute || cut.X1-cut.X0 < cutWidth-1e-9 || cut.X1-cut.X0 > cutWidth+1e-9 {
		t.Fatalf("axis %+v", tl.Axis.Segs)
	}
	if x := tl.Axis.X(tl.Run.Steps[3].Started); x < 0.5 || x > 1 {
		t.Fatalf("step 4 at %v", x)
	}
	l := tl.Lanes[2]
	if len(l.Spans) != 2 || l.Spans[0].Kind != "earlier" || l.Spans[0].Status != engine.StatusFailed {
		t.Fatalf("lane 3 %+v", l.Spans)
	}
	var shell *Span
	for i, s := range tl.Lanes[3].Spans {
		if s.Kind == "shell" {
			shell = &tl.Lanes[3].Spans[i]
		}
	}
	if shell == nil || shell.To.Sub(shell.From) != 3*time.Minute+38*time.Second || shell.From.Day() != 21 {
		t.Fatalf("shell %+v", shell)
	}
	if !tl.Lanes[len(tl.Lanes)-1].Removed {
		t.Fatal("removed step missing")
	}

	txt := tl.Text(120, false)
	for _, want := range []string{"orders-db-cutover · 20261020-160000-abc123", "── cutover ──", "░", "┈ 1 shell", "┈ 1 job", "▀", "↻", "╌", "── no longer in the runbook ──",
		"step 3 (read-only mode) failed, before the run was resumed", "Oct 21 10:30:30  maintenance window closed after 15m 30s"} {
		if !strings.Contains(txt, want) {
			t.Errorf("text lacks %q:\n%s", want, txt)
		}
	}
	page := string(tl.HTML())
	for _, want := range []string{"<h2>The maintenance window, 10:15 → 10:30</h2>", `class="cut"`, "k-earlier s-failed", "k-shell", "No longer in the runbook", "17h 44m"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if dir := os.Getenv("RUNNR_TIMELINE_OUT"); dir != "" {
		os.WriteFile(filepath.Join(dir, "demo.txt"), []byte(tl.Text(120, true)), 0o644)
		os.WriteFile(filepath.Join(dir, "demo.html"), tl.HTML(), 0o644)
	}
}

func TestDur(t *testing.T) {
	for d, want := range map[time.Duration]string{8 * time.Millisecond: "8ms", 45 * time.Second: "45s", 200 * time.Second: "3m 20s", 125 * time.Minute: "2h 05m", 76 * time.Hour: "3d 4h"} {
		if got := Dur(d); got != want {
			t.Errorf("%v: %s", d, got)
		}
	}
}
