package project

import (
	"testing"
	"time"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/store"
)

// Starting a run asks first when the latest for the same thing (its
// identifier inputs) is still running or stopped partway, and recent.
func TestUnfinished(t *testing.T) {
	st, err := store.Open(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rb, _, err := st.Save([]byte("name: rb\ninputs:\n  - {name: target, identifier: true, default: a}\n  - {name: note}\nsteps:\n  - {name: one, run: 'true'}\n  - {name: two, run: 'true'}\n"), store.SaveOpts{})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	save := func(target string, status engine.Status, ago time.Duration, steps ...engine.Status) *engine.Run {
		t.Helper()
		n++
		at := time.Now().Add(-ago)
		r := &engine.Run{ID: "r" + string(rune('0'+n)), Runbook: "rb", Status: status, Started: at, Begun: at,
			Identifiers: []string{"target"}, Inputs: map[string]string{"target": target, "note": "x"}}
		for i, s := range steps {
			r.Steps = append(r.Steps, engine.StepResult{Index: i, Name: []string{"one", "two"}[i], Status: s})
		}
		if err := st.SaveRun(r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	ok, failed, pending := engine.StatusSucceeded, engine.StatusFailed, engine.StatusPending
	a, b := IdentifiersOf(rb, map[string]string{}), IdentifiersOf(rb, map[string]string{"target": "b"})
	if e := IdentifiersOf(rb, map[string]string{"target": ""}); len(e) != 1 || e[0] != "target=a" {
		t.Fatalf("empty takes the default: %v", e)
	}
	if len(a) != 1 || a[0] != "target=a" {
		t.Fatalf("default identifier: %v", a)
	}

	if u := Unfinished(st, rb, a, ""); u != nil {
		t.Fatalf("no runs: %s", u.ID)
	}
	save("a", failed, 8*24*time.Hour, ok, failed)
	if u := Unfinished(st, rb, a, ""); u != nil {
		t.Fatalf("too old: %s", u.ID)
	}
	f := save("a", failed, time.Hour, ok, failed)
	if u := Unfinished(st, rb, a, ""); u == nil || u.ID != f.ID {
		t.Fatalf("failed an hour ago: %v", u)
	}
	if u := Unfinished(st, rb, b, ""); u != nil {
		t.Fatalf("another target: %s", u.ID)
	}
	// One that stopped before its first step doesn't count.
	nb := save("a", engine.StatusAborted, time.Minute, pending, pending)
	nb.Begun = time.Time{}
	st.SaveRun(nb)
	if u := Unfinished(st, rb, a, ""); u == nil || u.ID != f.ID {
		t.Fatalf("past one that never began: %v", u)
	}
	// Nor the run being started.
	r := save("b", engine.StatusRunning, 0, pending, pending)
	if u := Unfinished(st, rb, b, r.ID); u != nil {
		t.Fatalf("itself: %s", u.ID)
	}
	if u := Unfinished(st, rb, b, ""); u == nil || u.ID != r.ID {
		t.Fatalf("still running: %v", u)
	}
	// Done since: nothing to ask.
	save("a", ok, 0, ok, ok)
	if u := Unfinished(st, rb, a, ""); u != nil {
		t.Fatalf("succeeded since: %s", u.ID)
	}
}
