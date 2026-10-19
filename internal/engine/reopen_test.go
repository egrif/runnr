package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egrif/runnr/internal/runbook"
)

// reopenUI re-opens the step before the manual one the first time it's
// asked to do it, then runs the re-opened step and does the manual step.
type reopenUI struct {
	scripted
	se       Session
	manuals  int
	befores  []string
	reopened int
}

func (u *reopenUI) Bind(s Session) { u.se = s }
func (u *reopenUI) BeforeStep(_ context.Context, v StepView) (StepDecision, error) {
	u.befores = append(u.befores, v.Name)
	return StepRun, nil
}
func (u *reopenUI) AfterStep(context.Context, StepView, StepResult) (AfterDecision, string, error) {
	return AfterContinue, "", nil
}
func (u *reopenUI) Manual(_ context.Context, i int, _ runbook.Step, _ string) (ManualOutcome, string, error) {
	u.manuals++
	if u.manuals > 1 {
		return ManualDone, "", nil
	}
	r := u.se.(Reopener)
	u.reopened = r.Reopenable()
	if err := r.Reopen(0); err == nil {
		panic("only the last step that succeeded can be re-opened")
	}
	if err := r.Reopen(u.reopened); err != nil {
		panic(err)
	}
	return "", "", ErrReopen
}

func TestReopenLastStep(t *testing.T) {
	count := filepath.Join(t.TempDir(), "count")
	rb := mustParse(t, `name: re
steps:
  - {name: setup, run: 'true'}
  - {name: work, run: 'echo x >> `+count+`'}
  - {name: off, run: 'true', when: 'false'}
  - {name: check it, manual: Look at it.}
  - {name: done, run: 'true'}
`)
	ui := &reopenUI{}
	run := Execute(context.Background(), rb, ui, "", Options{Interactive: true})
	if run.Status != StatusSucceeded {
		t.Fatalf("%s %s", run.Status, statuses(run))
	}
	// "work" (the last that succeeded, past a skipped step) was re-opened,
	// paused before running again, and ran twice.
	if ui.reopened != 1 || strings.Join(ui.befores, ",") != "work" {
		t.Errorf("reopened %d, paused before %v", ui.reopened, ui.befores)
	}
	if b, _ := os.ReadFile(count); string(b) != "x\nx\n" {
		t.Errorf("work ran %q", b)
	}
	if !strings.Contains(strings.Join(ui.output, "|"), "re-opened") {
		t.Errorf("output %q", ui.output)
	}
}
