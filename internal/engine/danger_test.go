package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/egrif/runnr/internal/runbook"
)

// dangerUI records double checks and answers them from a queue.
type dangerUI struct {
	scripted
	answers []bool
	asked   []string
}

func (d *dangerUI) ConfirmDanger(_ context.Context, _ int, _ runbook.Step, _, why string) (bool, error) {
	d.asked = append(d.asked, why)
	a := d.answers[0]
	d.answers = d.answers[1:]
	return a, nil
}

const dangerRB = `
name: t
steps:
  - {name: setup, run: 'true', always: true}
  - {name: maybe, run: 'true', when: '{{eq "a" "b"}}'}
  - {name: prep, run: 'true'}
  - {name: cutover, run: 'echo cut', dangerous: true, confirm: true}
`

func TestDangerousInSequenceNeedsNoDoubleCheck(t *testing.T) {
	ui := &dangerUI{scripted: scripted{confirms: []bool{true}}}
	run := Execute(context.Background(), mustParse(t, dangerRB), ui, "", Options{Interactive: true, Sanctioned: true})
	if run.Status != StatusSucceeded || len(ui.asked) != 0 || len(ui.confirms) != 0 {
		t.Fatalf("%s %s asked=%q confirms left %d", run.Status, statuses(run), ui.asked, len(ui.confirms))
	}
}

// skip_if means the work's already done, so a step it skips is in sequence: a
// dangerous step after it needs no double check (like a when: that's false).
func TestDangerousAfterSkipIfNeedsNoDoubleCheck(t *testing.T) {
	rb := mustParse(t, `
name: t
steps:
  - {name: setup, run: 'true', always: true}
  - {name: delete the replica, run: 'echo deleting', skip_if: 'echo no replica'}
  - {name: cutover, run: 'echo cut', dangerous: true, confirm: true}
`)
	ui := &dangerUI{scripted: scripted{confirms: []bool{true}}, answers: []bool{false}}
	run := Execute(context.Background(), rb, ui, "", Options{Interactive: true, Sanctioned: true})
	if r := run.Steps[1]; r.Status != StatusSkipped || !strings.HasPrefix(r.Note, "skip_if:") {
		t.Fatalf("step 2: %+v", r)
	}
	if run.Status != StatusSucceeded || len(ui.asked) != 0 || run.Steps[2].Status != StatusSucceeded {
		t.Fatalf("%s %s asked=%q", run.Status, statuses(run), ui.asked)
	}
}

func TestDangerousOutOfSequence(t *testing.T) {
	rb := mustParse(t, dangerRB)
	for _, tc := range []struct {
		name string
		opts Options
		want string
	}{
		{"skipped step", Options{Sanctioned: true, Controls: NewControls(ControlsView{Skip: []int{2}})}, "3. prep (skipped: deselected)"},
		{"later start", Options{Sanctioned: true, StartAt: 3}, "3. prep (skipped: before start step)"},
		{"not sanctioned", Options{}, "isn't a recorded run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Declined: skipped, and the plain confirm isn't asked as well.
			ui := &dangerUI{answers: []bool{false}}
			tc.opts.Interactive = true
			run := Execute(context.Background(), rb, ui, "", tc.opts)
			if len(ui.asked) != 1 || !strings.Contains(ui.asked[0], tc.want) {
				t.Fatalf("asked %q, want %q", ui.asked, tc.want)
			}
			if r := run.Steps[3]; r.Status != StatusSkipped || r.Note != NoteDangerDeclined {
				t.Fatalf("%s %+v", statuses(run), r)
			}
			// Approved: runs, without a second confirm.
			ui = &dangerUI{answers: []bool{true}}
			if run := Execute(context.Background(), rb, ui, "", tc.opts); run.Steps[3].Status != StatusSucceeded {
				t.Fatalf("%s", statuses(run))
			}
		})
	}
}

func TestDangerousUnattendedOutOfSequenceFails(t *testing.T) {
	run := Execute(context.Background(), mustParse(t, dangerRB), Unattended{AssumeYes: true}, "", Options{Sanctioned: true, StartAt: 3})
	if run.Status != StatusFailed || !strings.Contains(run.Steps[3].Error, "double-check") {
		t.Fatalf("%s %+v", run.Status, run.Steps[3])
	}
	// In sequence, --yes is enough.
	if run := Execute(context.Background(), mustParse(t, dangerRB), Unattended{AssumeYes: true}, "", Options{Sanctioned: true}); run.Status != StatusSucceeded {
		t.Fatalf("%s %s", run.Status, statuses(run))
	}
}

func TestDangerousResumeCountsEarlierRun(t *testing.T) {
	rb := mustParse(t, dangerRB)
	prev := Execute(context.Background(), rb, &dangerUI{scripted: scripted{confirms: []bool{false}}}, "", Options{Interactive: true, Sanctioned: true})
	if prev.Steps[3].Status != StatusSkipped {
		t.Fatalf("setup: %s", statuses(prev))
	}
	ui := &dangerUI{scripted: scripted{confirms: []bool{true}}}
	run := Execute(context.Background(), rb, ui, "", Options{Interactive: true, Sanctioned: true, StartAt: 3, Resumed: prev})
	if run.Status != StatusSucceeded || len(ui.asked) != 0 || run.Steps[2].Note != NoteCarried {
		t.Fatalf("%s %s asked=%q", run.Status, statuses(run), ui.asked)
	}
}

// A resume leaves out every step that finished in the resumed run, not only
// those before where it starts: it starts at the first unfinished step, and
// steps after it that were done already (here a restart) aren't done again.
// They're carried, so a dangerous step after them is in sequence. Steps
// marked always run again.
func TestResumeCarriesStepsDoneAfterStart(t *testing.T) {
	rb := mustParse(t, `
name: t
steps:
  - {name: a, run: 'true'}
  - {name: b, run: 'echo b-ran'}
  - {name: setup, run: 'echo setup-ran', always: true}
  - {name: restart, run: 'echo restart-ran'}
  - {name: fixed, run: 'echo fixed-ran'}
  - {name: cutover, run: 'echo cut', dangerous: true, confirm: true}
`)
	st := func(ss ...Status) *Run {
		r := &Run{ID: "prev", Status: StatusAborted}
		for i, s := range ss {
			r.Steps = append(r.Steps, StepResult{Index: i, Name: rb.Steps[i].Name, Status: s})
		}
		return r
	}
	prev := st(StatusSucceeded, StatusFailed, StatusSucceeded, StatusSucceeded, StatusForced, StatusAborted)
	start := ResumePoint(prev)
	if start != 1 {
		t.Fatalf("resume point %d", start)
	}
	ui := &dangerUI{scripted: scripted{confirms: []bool{true}}}
	run := Execute(context.Background(), rb, ui, "", Options{Interactive: true, Sanctioned: true, StartAt: start, Resumed: prev, CarryDone: true})
	if run.Status != StatusSucceeded || statuses(run) != "skipped,succeeded,succeeded,skipped,skipped,succeeded" || len(ui.asked) != 0 {
		t.Fatalf("%s %s asked=%q", run.Status, statuses(run), ui.asked)
	}
	for _, i := range []int{0, 3, 4} {
		if run.Steps[i].Note != NoteCarried {
			t.Fatalf("step %d: %q", i+1, run.Steps[i].Note)
		}
	}
	out := strings.Join(ui.output, "\n")
	if !strings.Contains(out, "b-ran") || !strings.Contains(out, "setup-ran") || strings.Contains(out, "restart-ran") || strings.Contains(out, "fixed-ran") {
		t.Fatalf("output: %q", ui.output)
	}

	// Without CarryDone (the operator chose the steps), only the steps
	// they left out are left out.
	ui = &dangerUI{scripted: scripted{confirms: []bool{true}}}
	run = Execute(context.Background(), rb, ui, "", Options{Interactive: true, Sanctioned: true, Resumed: prev, Controls: NewControls(ControlsView{Skip: []int{0, 3}})})
	if statuses(run) != "skipped,succeeded,succeeded,skipped,succeeded,succeeded" || run.Steps[3].Note != NoteCarried {
		t.Fatalf("%s %s", run.Status, statuses(run))
	}
}

// On a resume, an optional background step before the resume point that
// didn't finish (a watcher that exited) runs again; one that finished, or
// a step that isn't optional or in the background, doesn't.
func TestResumeRestartsWatchers(t *testing.T) {
	rb := mustParse(t, `
name: t
steps:
  - {name: watcher, run: 'echo watching', background: true, optional: true}
  - {name: other optional, run: 'echo other', optional: true}
  - {name: a, run: 'true'}
  - {name: b, run: 'echo b-ran'}
`)
	prev := &Run{ID: "prev", Status: StatusFailed}
	for i, st := range []Status{StatusFailed, StatusFailed, StatusSucceeded, StatusFailed} {
		prev.Steps = append(prev.Steps, StepResult{Index: i, Name: rb.Steps[i].Name, Status: st, Optional: rb.Steps[i].Optional})
	}
	start := ResumePoint(prev)
	if start != 3 {
		t.Fatalf("resume point %d", start)
	}
	ui := &scripted{}
	run := Execute(context.Background(), rb, ui, "", Options{StartAt: start, Resumed: prev, CarryDone: true})
	if run.Status != StatusSucceeded || statuses(run) != "succeeded,skipped,skipped,succeeded" {
		t.Fatalf("%s %s %q", run.Status, statuses(run), ui.output)
	}
}
