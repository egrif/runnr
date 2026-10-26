package engine

import (
	"strings"
	"testing"
	"time"
)

// rawDemo is a made-up cutover with most of what a raw log shows: a
// resume (after a failed try the day before), notes, a window, checks, a
// shell, a manual step, a multi-line command and error, and a step no
// longer in the runbook.
func rawDemo() *Run {
	day := time.Date(2026, 10, 20, 0, 0, 0, 0, time.Local)
	at := func(d, h, m, s int) time.Time {
		return day.AddDate(0, 0, d).Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(s)*time.Second)
	}
	one, zero := 1, 0
	r := &Run{ID: "20261020-160000-abc123", Runbook: "orders-db-cutover", Mode: "interactive", RunbookVersion: 3, Status: StatusSucceeded,
		Started: at(0, 16, 0, 0), Ended: at(1, 10, 41, 0), Resumes: "20261019-090000-fff000",
		Inputs: map[string]string{"target": "replica b", "token": "****"}, Error: "",
		Window:      &WindowRecord{Opened: at(1, 10, 15, 0), Closed: at(1, 10, 30, 30)},
		Notes:       []Note{{Time: at(0, 16, 30, 0), Author: "pat", Text: "db team on it, resume tomorrow"}},
		Resumptions: []Resumption{{At: at(1, 10, 14, 30), From: 2, Status: StatusFailed, Error: "step 3 failed", RunbookVersion: 4, Was: 3}}}
	r.Steps = []StepResult{
		{Name: "check replication", Kind: "checks", Status: StatusSucceeded, Started: at(0, 16, 0, 5), Ended: at(0, 16, 2, 10), Attempts: 2,
			Checks: []CheckResult{{Name: "lag", Passed: true, Attempt: 2, Output: "lag 0s\n"}, {Name: "slots", Passed: false, Error: "exit 1", Output: "2 slots\nwant 1\n"}, {Name: "after", NotRun: true}}},
		{Name: "snapshot (orders-db)", Kind: "run", Way: "aws", Optional: true, Status: StatusSucceeded, Started: at(0, 16, 2, 10), Ended: at(0, 16, 21, 40).Add(250 * time.Millisecond),
			Command: "aws rds create-db-snapshot \\\n  --id orders\n", Output: "creating…\ndone\n", ExitCode: &zero, Note: "took a while · as usual"},
		{Name: "read-only mode", Kind: "run", Status: StatusSucceeded, Started: at(1, 10, 15, 0), Ended: at(1, 10, 15, 20), Command: "ro on\n", ExitCode: &zero,
			Before: []StepResult{{Name: "read-only mode", Kind: "run", Status: StatusFailed, Started: at(0, 16, 21, 40), Ended: at(0, 16, 24, 0), Command: "ro on\n", ExitCode: &one, Error: "connection refused\nretry later"}},
			Notes:  []Note{{Time: at(1, 10, 16, 0), Author: "sam", Text: "ok now"}}},
		{Name: "promote replica", Kind: "manual", Status: StatusSucceeded, Started: at(1, 10, 15, 20), Ended: at(1, 10, 27, 0), Command: "Promote it in the console\n",
			Shells: []string{"[10:18:02] $ psql -c 'select 1'\n[10:18:03] 1"}},
		{Name: "watch error rates", Kind: "run", Status: StatusSkipped, Note: "condition false"},
		{Name: "tidy up", Status: StatusPending},
	}
	r.Removed = []StepResult{{Index: 6, Name: "old smoke test", Kind: "run", Status: StatusSucceeded, Started: at(0, 16, 1, 0), Ended: at(0, 16, 1, 30), Output: "ok\n"}}
	return r
}

func TestParseRawLog(t *testing.T) {
	want := RawLog(rawDemo())
	runs, err := ParseRawLog(want)
	if err != nil {
		t.Fatal(err)
	}
	r := runs[0]
	if got := RawLog(r); got != want {
		t.Fatalf("round trip differs:\n--- want\n%s\n--- got\n%s", want, got)
	}
	if !r.FromLog || r.Inputs["target"] != "replica b" || len(r.Steps) != 6 || len(r.Steps[2].Before) != 1 || len(r.Removed) != 1 ||
		r.Steps[2].Started.Day() != 21 || r.Steps[2].Before[0].Started.Day() != 20 || r.Steps[1].Note != "took a while · as usual" ||
		r.Steps[2].Before[0].Error != "connection refused\nretry later" || r.Resumptions[0].From != 2 || r.Window.Closed.Sub(r.Window.Opened) != 15*time.Minute+30*time.Second {
		t.Fatalf("%+v", r)
	}
	// Several in one file, as an export writes them.
	var b strings.Builder
	b.WriteString("════ 1 of 2: run a ════\n" + want + "\n\n════ 2 of 2: run b ════\n" + want)
	if runs, err := ParseRawLog(b.String()); err != nil || len(runs) != 2 {
		t.Fatalf("%v %d", err, len(runs))
	}
	if _, err := ParseRawLog("hello\n"); err == nil {
		t.Fatal("read a log that isn't one")
	}
}
