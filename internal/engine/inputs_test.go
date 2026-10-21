package engine

import (
	"context"
	"strings"
	"testing"
)

// inputsUI changes inputs when step at starts.
type inputsUI struct {
	scripted
	session Session
	at      int
	changes map[string]string
	got     InputsChanged
	err     error
}

func (u *inputsUI) Bind(s Session) { u.session = s }
func (u *inputsUI) Notify(e Event) {
	u.scripted.Notify(e)
	if e.Kind == EventStepStarted && e.Step == u.at {
		u.got, u.err = u.session.(InputSetter).SetInputs(u.changes, "pat")
	}
}

const inputsRunbook = `
name: t
shell: sh
inputs:
  - {name: who, default: a}
  - {name: size, default: small, choices: [small, large]}
  - {name: token, secret: true, default: s3cr3t-one}
steps:
  - {name: first, run: 'echo "first {{.who}}"'}
  - {name: middle, run: 'true'}
  - {name: last, run: 'echo "last $who $size"'}
`

// A change made as a step starts takes effect before its command, is recorded,
// and says which steps already ran with the old value.
func TestSetInputs(t *testing.T) {
	rb := mustParse(t, inputsRunbook)
	ui := &inputsUI{at: 1, changes: map[string]string{"who": "b", "size": "large", "token": "s3cr3t-two"}}
	run := Execute(context.Background(), rb, ui, "", Options{Interactive: true, Vars: map[string]string{"who": "a"}})
	if ui.err != nil {
		t.Fatal(ui.err)
	}
	out := strings.Join(ui.output, "|")
	if run.Status != StatusSucceeded || !strings.Contains(out, "first a") || !strings.Contains(out, "last b large") {
		t.Fatalf("%s %q", run.Status, out)
	}
	if strings.Join(ui.got.Changed, ",") != "size,token,who" || len(ui.got.Stale["who"]) != 1 || ui.got.Stale["who"][0] != 1 {
		t.Fatalf("changed %+v", ui.got)
	}
	if run.Inputs["who"] != "b" || len(run.InputChanges) != 3 || run.InputChanges[2].Old != "a" || run.InputChanges[2].By != "pat" || run.InputChanges[2].Step != 1 {
		t.Fatalf("inputs %v changes %+v", run.Inputs, run.InputChanges)
	}
	if strings.Contains(run.Inputs["token"], "two") || strings.Contains(out, "s3cr3t-two") {
		t.Fatalf("secret shown: %v %q", run.Inputs, out)
	}
}

func TestSetInputsChecks(t *testing.T) {
	rb := mustParse(t, inputsRunbook)
	for _, c := range []map[string]string{{"size": "huge"}, {"nope": "x"}} {
		ui := &inputsUI{at: 1, changes: c}
		Execute(context.Background(), rb, ui, "", Options{Interactive: true})
		if ui.err == nil {
			t.Fatalf("%v: no error", c)
		}
	}
}

// A published value of an input's name is replaced by a change.
func TestSetInputsReplacesPublished(t *testing.T) {
	rb := mustParse(t, `
name: t
shell: sh
inputs: [{name: who, default: a}]
steps:
  - {name: publish, run: 'who=published', outputs: [who]}
  - {name: middle, run: 'true'}
  - {name: last, run: 'echo "last $who"'}
`)
	ui := &inputsUI{at: 1, changes: map[string]string{"who": "typed"}}
	run := Execute(context.Background(), rb, ui, "", Options{Interactive: true})
	if strings.Join(ui.got.Replaces, ",") != "who" || !strings.Contains(strings.Join(ui.output, "|"), "last typed") || run.Status != StatusSucceeded {
		t.Fatalf("%+v %q", ui.got, ui.output)
	}
}
