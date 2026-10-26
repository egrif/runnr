package project

import (
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
