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
