package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/egrif/runnr/internal/runbook"
)

// editingUI fixes the failing step when asked what to do about it.
type editingUI struct {
	scripted
	se    Session
	tried int
}

func (e *editingUI) Bind(s Session) { e.se = s }
func (e *editingUI) OnFailure(_ context.Context, i int, _ runbook.Step, _ StepResult) (FailureAction, string, error) {
	e.tried++
	if e.tried > 1 {
		return FailAbort, "", nil
	}
	ed := e.se.(Editor)
	if err := ed.EditStep(0, runbook.Step{Name: "earlier", Run: "true"}); err == nil {
		panic("editing a finished step should fail")
	}
	fixed := ed.StepDef(i)
	fixed.Run = "echo fixed"
	if err := ed.EditStep(i, fixed); err != nil {
		panic(err)
	}
	return "", "", ErrStepEdited
}

func TestEditStepDuringRun(t *testing.T) {
	rb := mustParse(t, "name: t\nsteps:\n  - {name: a, run: 'true'}\n  - {name: b, run: 'exit 3'}\n  - {name: c, run: 'echo c'}\n")
	ui := &editingUI{}
	run := Execute(context.Background(), rb, ui, "", Options{Interactive: true})
	if run.Status != StatusSucceeded || !run.Steps[1].Edited || len(run.Edits) != 1 || !strings.Contains(strings.Join(ui.output, "|"), "fixed") {
		t.Fatalf("%s %s edits=%d out=%q", run.Status, statuses(run), len(run.Edits), ui.output)
	}
	if rb.Steps[1].Run != "exit 3" {
		t.Fatal("the caller's runbook must not change")
	}
	saved, err := ApplyEdits(rb, run.Edits)
	if err != nil || saved.Steps[1].Run != "echo fixed" {
		t.Fatalf("apply: %v", err)
	}
	// Someone changed that step in the runbook since: a conflict.
	changed := *rb
	changed.Steps = append([]runbook.Step(nil), rb.Steps...)
	changed.Steps[1].Run = "exit 4"
	if _, err := ApplyEdits(&changed, run.Edits); err == nil {
		t.Fatal("want a conflict")
	}
}
