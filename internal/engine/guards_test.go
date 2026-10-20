package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egrif/runnr/internal/runbook"
)

func TestSkipIfAndOutputMatchers(t *testing.T) {
	rb := mustParse(t, `
name: t
steps:
  - {name: done already, run: 'echo ran', skip_if: 'echo exists'}
  - {name: not done, run: 'echo ran', skip_if: 'false'}
  - {name: needs ok, run: 'echo "Plan: 1 to add"', ok_if: 'No changes', continue_on_error: true}
  - {name: fails on match, run: 'echo ERROR: nope', fail_if: '^ERROR', continue_on_error: true}
  - {name: fine, run: 'echo No changes.', ok_if: 'No changes', fail_if: '^ERROR'}
`)
	run := Execute(context.Background(), rb, Unattended{}, "", Options{})
	if got := statuses(run); got != "skipped,succeeded,failed,failed,succeeded" {
		t.Fatalf("%s", got)
	}
	if n := run.Steps[0].Note; n != "skip_if: already done (exists)" {
		t.Errorf("note %q", n)
	}
	if e := run.Steps[2].Error; !strings.Contains(e, "doesn't match ok_if") {
		t.Errorf("error %q", e)
	}
}

type typer struct {
	Unattended
	answer bool
	asked  []string
}

func (u *typer) ConfirmTyped(_ context.Context, _ int, _ runbook.Step, _, want string) (bool, error) {
	u.asked = append(u.asked, want)
	return u.answer, nil
}

func TestConfirmTyping(t *testing.T) {
	rb := mustParse(t, `
name: t
inputs: [{name: silo, default: staging-s5}]
steps:
  - {name: promote, run: 'echo promoted', confirm_typing: '{{.silo}}'}
`)
	if run := Execute(context.Background(), rb, Unattended{}, "", Options{}); run.Status != StatusFailed ||
		!strings.Contains(run.Steps[0].Error, `needs a person to type "staging-s5"`) {
		t.Fatalf("unattended: %s %q", run.Status, run.Steps[0].Error)
	}
	ui := &typer{answer: true}
	if run := Execute(context.Background(), rb, ui, "", Options{Interactive: true}); run.Status != StatusSucceeded || len(ui.asked) != 1 || ui.asked[0] != "staging-s5" {
		t.Fatalf("typed: %s %v", run.Status, ui.asked)
	}
	ui = &typer{answer: false}
	if run := Execute(context.Background(), rb, ui, "", Options{Interactive: true}); statuses(run) != "skipped" {
		t.Fatalf("declined: %s", statuses(run))
	}
}

func TestRequires(t *testing.T) {
	rb := mustParse(t, `
name: t
requires:
  - sh
  - {cmd: no-such-tool-runnr, install: brew install it}
  - {cmd: also-missing-runnr, optional: true}
steps: [{name: a, run: 'true'}]
`)
	run := Execute(context.Background(), rb, Unattended{}, "", Options{})
	if run.Status != StatusFailed || !strings.Contains(run.Error, "no-such-tool-runnr isn't installed (not on PATH) — brew install it") || strings.Contains(run.Error, "also-missing") {
		t.Fatalf("%s %q", run.Status, run.Error)
	}
	if run := Execute(context.Background(), rb, Unattended{}, "", Options{SkipRequires: true}); run.Status != StatusSucceeded {
		t.Fatal("SkipRequires")
	}
	reqs := runbook.CheckRequirements([]runbook.Requirement{{Cmd: "sh", Version: "999", Check: "echo 3.2.57"}, {Cmd: "sh", Version: ">= 3.1", Check: "echo 'version 3.2.57(1)-release'"}})
	if reqs[0].Problem != "sh is 3.2.57; 999 or later is needed" || reqs[1].Problem != "" {
		t.Fatalf("%+v", reqs)
	}
}

func TestVerifyOnResume(t *testing.T) {
	dir := t.TempDir()
	mark := filepath.Join(dir, "applied")
	rb := mustParse(t, `
name: t
steps:
  - {name: apply, run: 'touch `+mark+`; echo applied', verify: 'test -f `+mark+`'}
  - {name: next, run: 'exit 1'}
`)
	first := Execute(context.Background(), rb, Unattended{}, "", Options{})
	resume := func() *Run {
		return Execute(context.Background(), rb, Unattended{}, "", Options{StartAt: ResumePoint(first), Resumed: first})
	}
	if r := resume(); r.Steps[0].Status != StatusSkipped || r.Steps[0].Note != NoteCarried {
		t.Fatalf("still stands: %s %q", statuses(r), r.Steps[0].Note)
	}
	os.Remove(mark)
	r := resume()
	if r.Steps[0].Status != StatusSucceeded || r.Steps[0].Output != "applied\n" {
		t.Fatalf("redone: %s %q", statuses(r), r.Steps[0].Output)
	}
}

func TestSecretsMaskedURLEncoded(t *testing.T) {
	rb := mustParse(t, `
name: t
inputs: [{name: pw, secret: true}]
steps:
  - {name: a, run: 'echo "postgres://u:p%40ss%2Fw@h/db and p@ss/w"'}
`)
	run := Execute(context.Background(), rb, Unattended{}, "", Options{Vars: map[string]string{"pw": "p@ss/w"}})
	if out := run.Steps[0].Output; strings.Contains(out, "p%40ss") || strings.Contains(out, "p@ss/w") {
		t.Fatalf("leaked: %q", out)
	}
}
