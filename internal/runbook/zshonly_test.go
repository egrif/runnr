package runbook

import (
	"strings"
	"testing"
)

func TestZshOnlySyntax(t *testing.T) {
	bad := "name: t\nfunctions:\n  ask: {body: 'read -r \"a?$1 [y/N] \"; [[ $a == [yY]* ]]'}\nsteps:\n  - {name: a, run: ask hi}\n"
	if _, err := Parse([]byte(bad)); err == nil || !strings.Contains(err.Error(), `function ask uses read "var?prompt"`) || !strings.Contains(err.Error(), "add shell: zsh") {
		t.Fatalf("%v", err)
	}
	for _, ok := range []string{
		strings.Replace(bad, "name: t\n", "name: t\nshell: zsh\n", 1),
		"name: t\nsteps:\n  - {name: a, run: 'read -r -p \"ready? \" a; echo \"$a?\"'}\n",
		"name: t\nsteps:\n  - {name: a, run: 'curl \"https://x/?a=1\"; ls *.txt?'}\n",
	} {
		if _, err := Parse([]byte(ok)); err != nil {
			t.Errorf("%v\n%s", err, ok)
		}
	}
}

func TestCloseOnExitNeedsBackground(t *testing.T) {
	if _, err := Parse([]byte("name: t\nsteps:\n  - {name: a, run: 'true', close_on_exit: true}\n")); err == nil || !strings.Contains(err.Error(), "close_on_exit applies to background steps") {
		t.Fatalf("%v", err)
	}
	if _, err := Parse([]byte("name: t\nsteps:\n  - {name: a, run: 'true', background: true, close_on_exit: true}\n")); err != nil {
		t.Fatal(err)
	}
}
