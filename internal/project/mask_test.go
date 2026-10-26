package project

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/store"
)

// An export hides credentials a run recorded in plain text (as an older
// runnr would have): by pattern, by the runbook's own sensitive patterns,
// and the values of inputs named as credentials, wherever they appear.
func TestMaskedExport(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	st.Save([]byte("name: rb\nsensitive:\n  ticket: 'ticket {{SENSITIVE}}'\nsteps: [{name: a, run: x}]\n"), store.SaveOpts{})
	at := time.Date(2026, 10, 20, 16, 0, 0, 0, time.UTC)
	r := &engine.Run{ID: "20261020-160000-aaaaaa", Runbook: "rb", Mode: "interactive", Status: engine.StatusSucceeded, Started: at, Ended: at.Add(time.Minute),
		Inputs: map[string]string{"db_password": "Hunt3r22pw", "region": "use1"},
		Notes:  []engine.Note{{Time: at, Author: "pat", Text: "PGPASSWORD=N0tePw11 worked"}},
		Steps: []engine.StepResult{{Name: "a", Kind: "run", Status: engine.StatusSucceeded, Started: at, Ended: at.Add(time.Minute),
			Command: "psql \"postgres://app:Ur1Pw33@db/x\"\n", Output: "password=L1tPw44\nusing Hunt3r22pw in use1\nopened ticket T-42\n"}}}
	files, err := LogFiles(Masked(st, []*engine.Run{r}), "both,timeline", false, "")
	if err != nil || len(files) != 3 {
		t.Fatalf("%v %d", err, len(files))
	}
	for _, f := range files {
		for _, secret := range []string{"Hunt3r22pw", "N0tePw11", "Ur1Pw33", "L1tPw44", "T-42"} {
			if strings.Contains(string(f.Data), secret) {
				t.Errorf("%s has %s", f.Name, secret)
			}
		}
		if !strings.HasSuffix(f.Name, ".html") && !strings.Contains(string(f.Data), "use1") {
			t.Errorf("%s lost what isn't a secret", f.Name)
		}
	}
	if r.Steps[0].Output == "" || !strings.Contains(r.Steps[0].Output, "L1tPw44") {
		t.Fatal("masking changed the run it was given")
	}
}

// Scrub masks the stored history: records and the notes kept beside them;
// a check changes nothing, a run still going is left alone, and a second
// scrub finds nothing.
func TestScrub(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	at := time.Date(2026, 10, 20, 16, 0, 0, 0, time.UTC)
	leaky := &engine.Run{ID: "20261020-160000-aaaaaa", Runbook: "rb", Mode: "interactive", Status: engine.StatusSucceeded, Started: at,
		Steps: []engine.StepResult{{Name: "a", Kind: "run", Status: engine.StatusSucceeded, Output: "password=L1tPw44\n"}}}
	clean := &engine.Run{ID: "20261020-170000-bbbbbb", Runbook: "rb", Mode: "interactive", Status: engine.StatusSucceeded, Started: at.Add(time.Hour),
		Steps: []engine.StepResult{{Name: "a", Kind: "run", Status: engine.StatusSucceeded, Output: "region=use1\n"}}}
	going := &engine.Run{ID: "20261020-180000-cccccc", Runbook: "rb", Mode: "interactive", Status: engine.StatusRunning, Started: at.Add(2 * time.Hour),
		Steps: []engine.StepResult{{Name: "a", Kind: "run", Status: engine.StatusRunning, Output: "password=G01ngPw55\n"}}}
	for _, r := range []*engine.Run{leaky, clean, going} {
		st.SaveRun(r)
	}
	st.AddNote(clean.ID, -1, "pat", "PGPASSWORD=N0tePw11 worked")

	check, err := Scrub(st, nil, true)
	if err != nil || len(check) != 3 || !check[0].Record || check[1].Record || check[1].Notes != 1 || !errors.Is(check[2].Err, store.ErrRunning) {
		t.Fatalf("check: %v %+v", err, check)
	}
	if r, _ := st.LoadRun(leaky.ID); !strings.Contains(r.Steps[0].Output, "L1tPw44") {
		t.Fatal("a check changed the run")
	}
	if _, err := Scrub(st, nil, false); err != nil {
		t.Fatal(err)
	}
	l, _ := st.LoadRun(leaky.ID)
	c, _ := st.LoadRun(clean.ID)
	g, _ := st.LoadRun(going.ID)
	if l.Steps[0].Output != "password=****\n" || c.Steps[0].Output != "region=use1\n" || len(c.Notes) != 1 || c.Notes[0].Text != "PGPASSWORD=**** worked" ||
		!strings.Contains(g.Steps[0].Output, "G01ngPw55") {
		t.Fatalf("after: %q %q %+v %q", l.Steps[0].Output, c.Steps[0].Output, c.Notes, g.Steps[0].Output)
	}
	again, _ := Scrub(st, []string{leaky.ID, clean.ID}, false)
	if len(again) != 2 || again[0].Changed() || again[1].Changed() {
		t.Fatalf("again: %+v", again)
	}
}
