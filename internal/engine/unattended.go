package engine

import (
	"context"
	"fmt"

	"github.com/egrif/runnr/internal/runbook"
)

// Unattended answers every decision without a human. Confirm-gated steps
// only run when AssumeYes is set; manual steps follow their unattended policy.
type Unattended struct {
	OnEvent   func(Event)
	AssumeYes bool
}

func (u Unattended) Notify(e Event) {
	if u.OnEvent != nil {
		u.OnEvent(e)
	}
}

func (u Unattended) AskInput(context.Context, runbook.Input) (string, error) { return "", nil }

func (u Unattended) Confirm(_ context.Context, _ int, step runbook.Step, _ string) (bool, error) {
	if !u.AssumeYes {
		return false, fmt.Errorf("step %q requires confirmation; rerun with --yes to approve unattended", step.Name)
	}
	return true, nil
}

func (u Unattended) Manual(_ context.Context, _ int, step runbook.Step, _ string) (ManualOutcome, string, error) {
	switch step.UnattendedPolicy() {
	case runbook.UnattendedPass:
		return ManualDone, "auto-passed (unattended)", nil
	case runbook.UnattendedSkip:
		return ManualSkip, "auto-skipped (unattended)", nil
	}
	if step.TTY {
		return ManualFail, "needs a terminal (tty), so it can't run unattended", nil
	}
	return ManualFail, "manual step cannot run unattended", nil
}

func (u Unattended) OnFailure(context.Context, int, runbook.Step, StepResult) (FailureAction, string, error) {
	return FailAbort, "", nil
}
