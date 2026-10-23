package runbook

import (
	"strings"
	"testing"
)

func TestIdentifierInputs(t *testing.T) {
	rb, err := Parse([]byte("name: x\ninputs:\n  - {name: silo, identifier: true}\n  - {name: note}\n  - {name: db, identifier: true}\nsteps: [{name: a, run: 'true'}]\n"))
	if err != nil || strings.Join(rb.IdentifierInputs(), ",") != "silo,db" {
		t.Fatalf("%v %v", err, rb)
	}
	if _, err := Parse([]byte("name: x\ninputs: [{name: tok, secret: true, identifier: true}]\nsteps: [{name: a, run: 'true'}]\n")); err == nil || !strings.Contains(err.Error(), "can't be an identifier") {
		t.Fatalf("secret identifier: %v", err)
	}
}
