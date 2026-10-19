package engine

import (
	"strings"
	"testing"

	"github.com/egrif/runnr/internal/runbook"
)

func TestInvocation(t *testing.T) {
	rb, err := runbook.Parse([]byte(`name: inv
inputs: [{name: env, default: dev}]
steps:
  - {name: a, run: 'echo {{.env}}', outputs: [x], dir: '/srv/{{.env}}', env: {MODE: '{{.env}}'}}
  - {name: b, run: 'echo $x'}
`))
	if err != nil {
		t.Fatal(err)
	}
	p := NewPreviewer(rb, nil, nil, false)
	inv := p.Invocation(0, rb.Steps[0], p.Render(rb.Steps[0].Run))
	// No functions or profile: the shell gets the step itself, with the
	// trap that publishes its outputs (the same text a run uses).
	if want := withOutputs(rb.Steps[0], "echo dev"); inv.Script != want || inv.StepFile != "" {
		t.Errorf("script = %q, step file %q", inv.Script, inv.StepFile)
	}
	if inv.Shell != "sh" || inv.Dir != "/srv/dev" || !strings.Contains(strings.Join(inv.Env, " "), "MODE=dev") {
		t.Errorf("invocation = %+v", inv)
	}
	// Step b also sees what a publishes.
	if got := strings.Join(p.Invocation(1, rb.Steps[1], "echo $x").Values, ","); got != "env,x" {
		t.Errorf("values = %s", got)
	}

	rb.Profile = []string{"~/.zshrc"}
	p = NewPreviewer(rb, nil, nil, true)
	inv = p.Invocation(1, rb.Steps[1], "echo $x")
	for _, want := range []string{"[ -r ~/'.zshrc' ] && . ~/'.zshrc' >/dev/null 2>&1", ". '/tmp/runnr-functions-XXXXXX.sh'", ". '/tmp/runnr-step-XXXXXX/step.sh'"} {
		if !strings.Contains(inv.Script, want) {
			t.Errorf("script lacks %q:\n%s", want, inv.Script)
		}
	}
	if inv.StepFile != "echo $x" {
		t.Errorf("step file = %q", inv.StepFile)
	}
}
