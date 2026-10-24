package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egrif/runnr/internal/store"
)

func TestDiff(t *testing.T) {
	a := "name: x\ninputs:\n  - {name: silo}\nsteps:\n  - {name: one, run: 'echo 1'}\n  - {name: two, run: 'echo 2'}\n  - {name: three, run: 'echo 3'}\n  - {name: four, run: 'echo 4'}\n  - {name: five, run: 'echo 5'}\n  - {name: six, run: 'echo 6'}\n  - {name: seven, run: 'echo 7'}\n  - {name: eight, run: 'echo 8'}\n  - {name: nine, run: 'echo 9'}\n"
	b := strings.Replace(strings.Replace(a, "echo 2", "echo two", 1), "  - {name: nine, run: 'echo 9'}\n", "  - {name: nine, run: 'echo 9'}\n  - {name: ten, run: 'echo 10'}\n", 1)
	b = strings.Replace(b, "inputs:\n  - {name: silo}\n", "", 1)
	got := Diff(a, b, "stored", "file", 3)
	if Diff(a, a, "x", "y", 3) != "" {
		t.Fatal("same files differ")
	}
	if add, rm := DiffStat(got); add != 2 || rm != 3 {
		t.Fatalf("stat +%d -%d\n%s", add, rm, got)
	}
	// As diff -u writes it.
	want := `--- stored
+++ file
@@ -1,9 +1,7 @@
 name: x
-inputs:
-  - {name: silo}
 steps:
   - {name: one, run: 'echo 1'}
-  - {name: two, run: 'echo 2'}
+  - {name: two, run: 'echo two'}
   - {name: three, run: 'echo 3'}
   - {name: four, run: 'echo 4'}
   - {name: five, run: 'echo 5'}
@@ -11,3 +9,4 @@
   - {name: seven, run: 'echo 7'}
   - {name: eight, run: 'echo 8'}
   - {name: nine, run: 'echo 9'}
+  - {name: ten, run: 'echo 10'}
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// A scan says what replacing a stored runbook would change.
func TestScanDiff(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	st.Save([]byte("name: d\nsteps: [{name: a, run: 'echo 1'}]\n"), store.SaveOpts{})
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "d.yaml"), []byte("name: d\nsteps: [{name: a, run: 'echo 2'}]\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "same.yaml"), []byte("name: e\nsteps: [{name: a, run: 'true'}]\n"), 0o644)
	p, err := Scan(Options{Dir: dir, Lookup: StoreLookup(st)})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range p.Docs {
		switch d.Name {
		case "d":
			if add, rm := DiffStat(d.Diff); add != 1 || rm != 1 || !strings.Contains(d.Diff, "--- d (version 1, stored)\n+++ d.yaml\n") {
				t.Fatalf("%q", d.Diff)
			}
		case "e":
			if d.Diff != "" {
				t.Fatalf("new one has a diff: %q", d.Diff)
			}
		}
	}
}
