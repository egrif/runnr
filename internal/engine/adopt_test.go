package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/egrif/runnr/internal/runbook"
	"github.com/egrif/runnr/internal/ver"
)

var v6, v7 = ver.Of([]byte("6")), ver.Of([]byte("7"))

// adopter adopts a newer version when step 1 asks for confirmation.
type adopter struct {
	Unattended
	sess    Session
	newer   *runbook.Runbook
	changed []int
	err     error
}

func (a *adopter) Bind(s Session) { a.sess = s }
func (a *adopter) Confirm(context.Context, int, runbook.Step, string) (bool, error) {
	a.changed, a.err = a.sess.(VersionAdopter).AdoptVersion(v7, a.newer.Steps)
	return true, nil
}

func TestAdoptVersion(t *testing.T) {
	rb := mustParse(t, "name: t\nsteps:\n  - {name: a, run: 'echo a', confirm: true}\n  - {name: b, run: 'echo old b'}\n  - {name: c, run: 'echo c'}\n")
	newer := mustParse(t, "name: t\nsteps:\n  - {name: a, run: 'echo A (already running)', confirm: true}\n  - {name: b, run: 'echo new b'}\n  - {name: c, run: 'echo c'}\n")
	ui := &adopter{newer: newer}
	r := Execute(context.Background(), rb, ui, "", Options{Interactive: true, RunbookVersion: v6})
	if ui.err != nil || r.Status != StatusSucceeded {
		t.Fatalf("%v %s", ui.err, r.Status)
	}
	// Step a was running: it's the current step, so it changes too (for a retry); b runs the new way.
	if r.Steps[1].Output != "new b\n" || r.RunbookVersion != v7 || r.EditsSaved != v7 || len(ui.changed) != 2 {
		t.Fatalf("output %q version %s saved %s changed %v", r.Steps[1].Output, r.RunbookVersion, r.EditsSaved, ui.changed)
	}

	fewer := mustParse(t, "name: t\nsteps:\n  - {name: a, run: 'echo a', confirm: true}\n")
	ui = &adopter{newer: fewer}
	Execute(context.Background(), rb, ui, "", Options{Interactive: true})
	if ui.err == nil || !strings.Contains(ui.err.Error(), "has 1 steps, this run has 3") {
		t.Fatalf("fewer steps: %v", ui.err)
	}
}
