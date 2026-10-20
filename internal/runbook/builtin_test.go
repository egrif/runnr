package runbook

import "testing"

func TestBuiltinLibrariesParse(t *testing.T) {
	for _, n := range BuiltinLibraries() {
		if _, err := ParseLibrary(BuiltinLibrary(n)); err != nil {
			t.Errorf("%s: %v", n, err)
		}
	}
	if len(BuiltinLibraries()) == 0 {
		t.Fatal("no builtin libraries")
	}
}
