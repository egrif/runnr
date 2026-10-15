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

func do(t *testing.T, method, url, body string, hdr map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("X-Runnr", "1")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestInteractiveRunOverHTTP(t *testing.T) {
	st, _ := store.New(t.TempDir())
	ts := httptest.NewServer(New(st).Handler())
	defer ts.Close()

	rb := "name: demo\nsteps:\n  - {name: hi, run: 'echo hello'}\n  - {name: check, manual: look around}\n  - {name: go, run: 'echo going', confirm: true}\n"
	if res := do(t, "POST", ts.URL+"/api/runbooks", rb, nil); res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d", res.StatusCode)
	}
	res := do(t, "POST", ts.URL+"/api/runs", `{"runbook":"demo"}`, nil)
	var started struct{ ID string }
	json.NewDecoder(res.Body).Decode(&started)
	if started.ID == "" {
		t.Fatalf("start: %d", res.StatusCode)
	}

	evs, err := http.Get(ts.URL + "/api/runs/" + started.ID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer evs.Body.Close()
	sc := bufio.NewScanner(evs.Body)
	deadline := time.AfterFunc(10*time.Second, func() { evs.Body.Close() })
	defer deadline.Stop()

	var final *engine.Run
	var output []string
	for final == nil && sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var e WireEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		switch e.Kind {
		case "question":
			answer := map[string]string{"manual": "done", "confirm": "run"}[e.Question.Type]
			body, _ := json.Marshal(Answer{QuestionID: e.Question.ID, Value: answer})
			if r := do(t, "POST", ts.URL+"/api/runs/"+started.ID+"/answer", string(body), nil); r.StatusCode != http.StatusNoContent {
				t.Fatalf("answer: %d", r.StatusCode)
			}
		case engine.EventOutput:
			output = append(output, e.Text)
		case engine.EventRunFinished:
			final = e.Run
		}
	}
	if final == nil || final.Status != engine.StatusSucceeded {
		t.Fatalf("run did not succeed: %+v", final)
	}
	if strings.Join(output, "|") != "hello|going" {
		t.Fatalf("output %q", output)
	}

	var rec engine.Run
	if err := st.LoadRun(started.ID, &rec); err != nil || rec.Status != engine.StatusSucceeded {
		t.Fatalf("history: %v %s", err, rec.Status)
	}
}

func TestGuardRejectsCrossOrigin(t *testing.T) {
	st, _ := store.New(t.TempDir())
	ts := httptest.NewServer(New(st).Handler())
	defer ts.Close()

	if r := do(t, "POST", ts.URL+"/api/runs", `{}`, map[string]string{"Origin": "https://evil.example"}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin: %d", r.StatusCode)
	}
	req, _ := http.NewRequest("POST", ts.URL+"/api/runs", strings.NewReader(`{}`))
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != http.StatusForbidden {
		t.Fatalf("missing header: %d", r.StatusCode)
	}
}
