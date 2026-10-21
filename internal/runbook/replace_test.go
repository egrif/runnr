package runbook

import (
	"strings"
	"testing"
)

func TestReplaceStep(t *testing.T) {
	raw := `name: t
# the steps
steps:
  # first
  - name: a
    section: One
    run: echo a   # says a
  # second
  - name: b
    run: echo b
  - {name: c, section: Two, run: echo c}
`
	rb, _ := Parse([]byte(raw))
	s := rb.Steps[1] // section One, inherited
	s.Run = "echo B"
	out, err := ReplaceStep([]byte(raw), 1, s)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, want := range []string{"# the steps", "# first", "echo a # says a", "# second", "run: echo B", "{name: c, section: Two, run: echo c}"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Count(got, "section: One") != 1 {
		t.Errorf("the inherited section was written out:\n%s", got)
	}
	nrb, _ := Parse(out)
	if nrb.Steps[1].Section != "One" || nrb.Steps[1].Run != "echo B" {
		t.Errorf("%+v", nrb.Steps[1])
	}
	bad := s
	bad.Run, bad.Manual = "", ""
	if _, err := ReplaceStep([]byte(raw), 1, bad); err == nil {
		t.Error("an invalid step was accepted")
	}
}
