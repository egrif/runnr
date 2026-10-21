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

// Changing inputs while a step waits for its confirmation starts it again
// with the new values; the change is recorded with the run.
func TestSetRunInputs(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	ts := httptest.NewServer(New(st, Config{}).Handler())
	defer ts.Close()
	do(t, "POST", ts.URL+"/api/runbooks", `name: w
inputs: [{name: who, default: a}, {name: size, default: s, choices: [s, l]}]
steps:
  - {name: greet, run: 'echo "hi $who"', confirm: true}
`, nil)
	res := do(t, "POST", ts.URL+"/api/runs", `{"runbook":"w","vars":{"who":"a","size":"s"}}`, nil)
	var started struct{ ID string }
	json.NewDecoder(res.Body).Decode(&started)
	evs, err := http.Get(ts.URL + "/api/runs/" + started.ID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer evs.Body.Close()
	deadline := time.AfterFunc(10*time.Second, func() { evs.Body.Close() })
	defer deadline.Stop()
	sc := bufio.NewScanner(evs.Body)
	asked := 0
	var output []string
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var e WireEvent
		json.Unmarshal([]byte(line), &e)
		if e.Kind == engine.EventOutput {
			output = append(output, e.Text)
		}
		if e.Kind == "question" && e.Question.Type == "confirm" {
			asked++
			if asked == 1 {
				bad := do(t, "PUT", ts.URL+"/api/runs/"+started.ID+"/inputs", `{"values":{"size":"xl"}}`, nil)
				if bad.StatusCode != http.StatusBadRequest {
					t.Fatalf("bad choice: %d", bad.StatusCode)
				}
				r := do(t, "PUT", ts.URL+"/api/runs/"+started.ID+"/inputs", `{"values":{"who":"b"},"by":"pat"}`, nil)
				var got struct {
					Changed   []string
					Restarted bool
				}
				json.NewDecoder(r.Body).Decode(&got)
				if r.StatusCode != http.StatusOK || !got.Restarted || len(got.Changed) != 1 {
					t.Fatalf("%d %+v", r.StatusCode, got)
				}
				continue
			}
			body, _ := json.Marshal(Answer{QuestionID: e.Question.ID, Value: "run"})
			do(t, "POST", ts.URL+"/api/runs/"+started.ID+"/answer", string(body), nil)
		}
		if e.Kind == engine.EventRunFinished {
			break
		}
	}
	run, err := st.LoadRun(started.ID)
	if err != nil || run.Status != engine.StatusSucceeded || asked != 2 {
		t.Fatalf("%v %+v asked=%d", err, run, asked)
	}
	if run.Inputs["who"] != "b" || len(run.InputChanges) != 1 || run.InputChanges[0].By != "pat" || !strings.Contains(run.Steps[0].Output, "hi b") {
		t.Fatalf("inputs %v changes %+v output %q", run.Inputs, run.InputChanges, run.Steps[0].Output)
	}
}
