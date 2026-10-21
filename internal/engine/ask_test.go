package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/egrif/runnr/internal/runbook"
)

type askUI struct {
	scripted
	answers map[string]string
	asked   []string
}

func (a *askUI) AskStep(_ context.Context, _ int, _ runbook.Step, kind, prompt string) (string, error) {
	a.asked = append(a.asked, kind+":"+prompt)
	return a.answers[prompt], nil
}

func TestRunnrAskAndConfirm(t *testing.T) {
	for _, sh := range []string{"sh", "bash", "zsh"} {
		t.Run(sh, func(t *testing.T) {
			rb := mustParse(t, `
name: t
shell: `+sh+`
steps:
  - {name: ask, run: 'name=$(runnr_ask "Which cluster?"); echo "cluster=$name"'}
  - {name: yes, run: 'if runnr_confirm "ready?"; then echo go; else echo stop; fi'}
  - {name: no, run: 'if runnr_confirm "really?"; then echo go; else echo stop; fi'}
`)
			ui := &askUI{answers: map[string]string{"Which cluster?": "orders-blue", "ready?": "y", "really?": "no"}}
			r := Execute(context.Background(), rb, ui, "", Options{Interactive: true})
			if r.Status != StatusSucceeded {
				t.Fatalf("%s %s", r.Status, statuses(r))
			}
			got := []string{strings.TrimSpace(r.Steps[0].Output), strings.TrimSpace(r.Steps[1].Output), strings.TrimSpace(r.Steps[2].Output)}
			if got[0] != "cluster=orders-blue" || got[1] != "go" || got[2] != "stop" || len(ui.asked) != 3 || ui.asked[1] != "confirm:ready?" {
				t.Fatalf("%q asked %v", got, ui.asked)
			}
		})
	}
	// Unattended: nobody to ask, so the question fails (and so does a step that needs it).
	rb := mustParse(t, "name: t\nsteps:\n  - {name: a, run: 'runnr_confirm \"ready?\" && echo go'}\n")
	r := Execute(context.Background(), rb, Unattended{}, "", Options{})
	if r.Steps[0].Status != StatusFailed || !strings.Contains(r.Steps[0].Output, "nobody to ask") {
		t.Fatalf("%s %q", r.Steps[0].Status, r.Steps[0].Output)
	}
}
