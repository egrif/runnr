package htmlview

import (
	"strings"
	"testing"

	"github.com/egrif/runnr/internal/runbook"
)

func TestRender(t *testing.T) {
	rb, err := runbook.Parse([]byte(`name: demo
shell: zsh
inputs:
  - {name: env, default: staging}
  - {name: token, secret: true}
sections: [{name: Cut, description: Switch it over.}]
functions:
  greet: {body: 'echo "hi $1"'}
steps:
  - {name: setup, run: 'db=orders-{{.env}}', outputs: [db], always: true}
  - name: switch
    section: Cut
    dangerous: true
    ways:
      - {name: Scripted, run: 'greet {{.db}} {{.token}}', preferred: true}
      - {name: By hand, manual: 'Flip it in the console for {{.env}}.'}
  - name: verify
    checks: [{name: up, run: 'test -n "$db"'}]
`))
	if err != nil {
		t.Fatal(err)
	}
	page, err := Render(rb, Options{Vars: map[string]string{"env": "prod", "token": "s3cret"}})
	if err != nil {
		t.Fatal(err)
	}
	out := string(page)
	for _, want := range []string{
		`id="step-2"`, `id="section-cut"`, "Switch it over.",
		// As written: the template; as runnr runs it: filled in, with the
		// outputs trap and the functions file.
		"db=orders-{{.env}}", "db=orders-prod", "trap __runnr_outputs EXIT",
		". &#39;/tmp/runnr-functions-XXXXXX.sh&#39;",
		// Ways as tabs, preferred first; the manual way rendered.
		`data-tab="1"`, "preferred", "Flip it in the console for prod.",
		// A value a step publishes isn't known yet; the secret is masked.
		"greet &lt;db&gt; ****", "dangerous", "✓",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(out, "s3cret") {
		t.Error("secret input leaked into the page")
	}
}
