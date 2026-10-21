package server

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/store"
)

// The console sees the runbook saved as a newer version mid-run, and moves
// the run onto it.
func TestRunPlanAndAdoptVersion(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	ts := httptest.NewServer(New(st, Config{}).Handler())
	defer ts.Close()
	v1 := "name: demo\nsteps:\n  - {name: a, run: 'echo a', confirm: true}\n  - {name: b, run: 'echo b'}\n  - {name: c, run: 'echo old c'}\n"
	if res := do(t, "POST", ts.URL+"/api/runbooks", v1, nil); res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d", res.StatusCode)
	}
	res := do(t, "POST", ts.URL+"/api/runs", `{"runbook":"demo"}`, nil)
	var started struct{ ID string }
	json.NewDecoder(res.Body).Decode(&started)
	id := started.ID

	evs, err := http.Get(ts.URL + "/api/runs/" + id + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer evs.Body.Close()
	deadline := time.AfterFunc(10*time.Second, func() { evs.Body.Close() })
	defer deadline.Stop()
	sc := bufio.NewScanner(evs.Body)
	next := func(kind engine.EventKind) WireEvent {
		for sc.Scan() {
			line, ok := strings.CutPrefix(sc.Text(), "data: ")
			if !ok {
				continue
			}
			var e WireEvent
			json.Unmarshal([]byte(line), &e)
			if e.Kind == kind {
				return e
			}
		}
		t.Fatalf("no %s event", kind)
		return WireEvent{}
	}
	q := next("question")

	// Someone saves version 2 while the run waits at step 1.
	if _, _, err := st.Save([]byte(strings.Replace(v1, "echo old c", "echo new c", 1)), store.SaveOpts{Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	var p runPlan
	json.NewDecoder(do(t, "GET", ts.URL+"/api/runs/"+id+"/plan", "", nil).Body).Decode(&p)
	if !p.Live || p.Version != 1 || p.Latest != 2 || len(p.Changed) != 1 || p.Changed[0] != 3 || len(p.Steps) != 3 {
		t.Fatalf("plan: %+v", p)
	}
	if r := do(t, "POST", ts.URL+"/api/runs/"+id+"/adopt-version", `{"version":2}`, nil); r.StatusCode != http.StatusOK {
		t.Fatalf("adopt: %d", r.StatusCode)
	}
	body, _ := json.Marshal(Answer{QuestionID: q.Question.ID, Value: "run"})
	do(t, "POST", ts.URL+"/api/runs/"+id+"/answer", string(body), nil)
	final := next(engine.EventRunFinished).Run
	if final.Status != engine.StatusSucceeded || final.Steps[2].Output != "new c\n" || final.RunbookVersion != 2 {
		t.Fatalf("%s %q v%d", final.Status, final.Steps[2].Output, final.RunbookVersion)
	}

	// A finished run's plan: the steps as it ran them, and nothing to adopt.
	p = runPlan{}
	json.NewDecoder(do(t, "GET", ts.URL+"/api/runs/"+id+"/plan", "", nil).Body).Decode(&p)
	if p.Live || len(p.Changed) != 0 || !strings.Contains(p.Steps[2].Command, "new c") {
		t.Fatalf("finished plan: %+v", p)
	}
}
