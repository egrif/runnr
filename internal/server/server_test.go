package server

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/egrif/runnr/internal/claude"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
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
	st, _ := store.Open(t.TempDir(), "")
	ts := httptest.NewServer(New(st, Config{}).Handler())
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

	if rec, err := st.LoadRun(started.ID); err != nil || rec.Status != engine.StatusSucceeded {
		t.Fatalf("history: %v %+v", err, rec)
	}
}

func TestGuardRejectsCrossOrigin(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	ts := httptest.NewServer(New(st, Config{}).Handler())
	defer ts.Close()

	if r := do(t, "POST", ts.URL+"/api/runs", `{}`, map[string]string{"Origin": "https://evil.example"}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin: %d", r.StatusCode)
	}
	req, _ := http.NewRequest("POST", ts.URL+"/api/runs", strings.NewReader(`{}`))
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != http.StatusForbidden {
		t.Fatalf("missing header: %d", r.StatusCode)
	}
}

func TestTokenAuthWaitAndResume(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	ts := httptest.NewServer(New(st, Config{Token: "s3cret", NoUI: true}).Handler())
	defer ts.Close()

	if r := do(t, "GET", ts.URL+"/api/runbooks", "", nil); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", r.StatusCode)
	}
	if r := do(t, "GET", ts.URL+"/api/runbooks", "", map[string]string{"Authorization": "Bearer nope"}); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token: %d", r.StatusCode)
	}
	auth := map[string]string{"Authorization": "Bearer s3cret"}
	rb := "name: flaky\ninputs: [{name: mode, default: fail}]\nsteps:\n  - {name: a, run: 'true'}\n  - {name: b, run: '[ \"$mode\" = ok ]'}\n"
	if r := do(t, "POST", ts.URL+"/api/runbooks", rb, auth); r.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d", r.StatusCode)
	}

	var run engine.Run
	r := do(t, "POST", ts.URL+"/api/runs", `{"runbook":"flaky","unattended":true,"wait":true}`, auth)
	json.NewDecoder(r.Body).Decode(&run)
	if r.StatusCode != http.StatusOK || run.Status != engine.StatusFailed {
		t.Fatalf("wait: %d %s", r.StatusCode, run.Status)
	}

	var resumed engine.Run
	r = do(t, "POST", ts.URL+"/api/runs/"+run.ID+"/resume", `{"vars":{"mode":"ok"},"unattended":true,"wait":true}`, auth)
	json.NewDecoder(r.Body).Decode(&resumed)
	// The run carries on in its own record; step a stands as it went.
	if resumed.Status != engine.StatusSucceeded || resumed.ID != run.ID || len(resumed.Resumptions) != 1 || !resumed.Steps[0].Carried || len(resumed.Steps[1].Before) != 1 {
		t.Fatalf("resume: %d %+v", r.StatusCode, resumed)
	}
	// new_run: a new run, pointing back at it.
	var failed, again engine.Run
	r = do(t, "POST", ts.URL+"/api/runs", `{"runbook":"flaky","unattended":true,"wait":true}`, auth)
	json.NewDecoder(r.Body).Decode(&failed)
	r = do(t, "POST", ts.URL+"/api/runs/"+failed.ID+"/resume", `{"vars":{"mode":"ok"},"unattended":true,"wait":true,"new_run":true}`, auth)
	json.NewDecoder(r.Body).Decode(&again)
	if r.StatusCode != http.StatusOK || again.ID == failed.ID || again.Resumes != failed.ID {
		t.Fatalf("new_run: %d %+v", r.StatusCode, again)
	}

	if r := do(t, "POST", ts.URL+"/api/runs", `{"runbook":"flaky","wait":true}`, auth); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("interactive wait should be rejected: %d", r.StatusCode)
	}
	if r := do(t, "GET", ts.URL+"/", "", auth); r.StatusCode != http.StatusNotFound {
		t.Fatalf("no-ui should not serve GUI: %d", r.StatusCode)
	}
}

func TestRunbookConflict(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	ts := httptest.NewServer(New(st, Config{}).Handler())
	defer ts.Close()

	v1 := "name: c\nsteps: [{name: a, run: 'true'}]\n"
	do(t, "POST", ts.URL+"/api/runbooks", v1, nil)
	if r := do(t, "PUT", ts.URL+"/api/runbooks/c", strings.Replace(v1, "true", "echo 2", 1), map[string]string{"If-Match": "1"}); r.StatusCode != http.StatusOK {
		t.Fatalf("first edit: %d", r.StatusCode)
	}
	// A second editor still on version 1 must not clobber version 2.
	if r := do(t, "PUT", ts.URL+"/api/runbooks/c", strings.Replace(v1, "true", "echo 3", 1), map[string]string{"If-Match": "1"}); r.StatusCode != http.StatusConflict {
		t.Fatalf("stale edit: %d", r.StatusCode)
	}
	r := do(t, "GET", ts.URL+"/api/runbooks/c/versions", "", nil)
	var vs []store.Version
	json.NewDecoder(r.Body).Decode(&vs)
	if len(vs) != 2 || vs[0].Version != 2 {
		t.Fatalf("versions %+v", vs)
	}
}

func TestForceAndNotesOverHTTP(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	ts := httptest.NewServer(New(st, Config{}).Handler())
	defer ts.Close()

	do(t, "POST", ts.URL+"/api/runbooks", "name: f\nsteps:\n  - {name: bad, run: 'exit 1'}\n  - {name: next, run: 'true'}\n", nil)
	res := do(t, "POST", ts.URL+"/api/runs", `{"runbook":"f"}`, nil)
	var started struct{ ID string }
	json.NewDecoder(res.Body).Decode(&started)

	evs, err := http.Get(ts.URL + "/api/runs/" + started.ID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer evs.Body.Close()
	sc := bufio.NewScanner(evs.Body)
	deadline := time.AfterFunc(10*time.Second, func() { evs.Body.Close() })
	defer deadline.Stop()
	asked := 0
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var e WireEvent
		json.Unmarshal([]byte(line), &e)
		if e.Kind == "question" {
			asked++
			// First a force without a reason (re-asked), then with one.
			note := ""
			if asked == 2 {
				note = "accepted: expected on staging"
				do(t, "POST", ts.URL+"/api/runs/"+started.ID+"/notes", `{"step":1,"author":"t","text":"investigating"}`, nil)
			}
			body, _ := json.Marshal(Answer{QuestionID: e.Question.ID, Value: "force", Note: note})
			do(t, "POST", ts.URL+"/api/runs/"+started.ID+"/answer", string(body), nil)
		}
		if e.Kind == engine.EventRunFinished {
			break
		}
	}
	run, err := st.LoadRun(started.ID)
	if err != nil || run.Status != engine.StatusSucceeded || run.Steps[0].Status != engine.StatusForced || asked != 2 {
		t.Fatalf("%v %+v asked=%d", err, run, asked)
	}
	if len(run.Steps[0].Notes) != 1 || run.Steps[0].Notes[0].Text != "investigating" {
		t.Fatalf("notes %+v", run.Steps[0].Notes)
	}
}

func TestStepModeSkipAndBrowserTerminal(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	ts := httptest.NewServer(New(st, Config{}).Handler())
	defer ts.Close()

	do(t, "POST", ts.URL+"/api/runbooks", `name: w
steps:
  - {name: tty, run: 'read x; echo "got $x"; answer=42', tty: true, outputs: [answer]}
  - {name: left-out, run: 'exit 1'}
  - {name: skipped-live, run: 'exit 1'}
  - {name: last, run: 'echo "answer=$answer"', expect: 'prints 42'}
`, nil)
	res := do(t, "POST", ts.URL+"/api/runs", `{"runbook":"w","step_mode":true,"skip":[2]}`, nil)
	var started struct{ ID string }
	json.NewDecoder(res.Body).Decode(&started)

	evs, err := http.Get(ts.URL + "/api/runs/" + started.ID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer evs.Body.Close()
	sc := bufio.NewScanner(evs.Body)
	deadline := time.AfterFunc(15*time.Second, func() { evs.Body.Close() })
	defer deadline.Stop()
	var output []string
	var sawExpect bool
	var termOut strings.Builder
	termEnded := false
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var e WireEvent
		json.Unmarshal([]byte(line), &e)
		switch e.Kind {
		case "question":
			if e.Question.Type != "before" || !slices.Contains(e.Question.Options, "shell") {
				t.Fatalf("question %+v", e.Question)
			}
			answer := "run"
			if e.Question.Title == "Next: skipped-live" {
				answer = "skip"
			}
			sawExpect = sawExpect || e.Question.Expect == "prints 42"
			body, _ := json.Marshal(Answer{QuestionID: e.Question.ID, Value: answer})
			do(t, "POST", ts.URL+"/api/runs/"+started.ID+"/answer", string(body), nil)
		case engine.EventOutput:
			output = append(output, e.Text)
		case "term_start":
			// Type into the browser terminal.
			do(t, "POST", ts.URL+"/api/runs/"+started.ID+"/term", `{"id":"`+e.Term.ID+`","data":"hello\r","rows":40,"cols":100}`, nil)
		case "term":
			b, _ := base64.StdEncoding.DecodeString(e.Term.Data)
			termOut.Write(b)
		case "term_end":
			termEnded = true
		}
		if e.Kind == engine.EventRunFinished {
			break
		}
	}
	run, err := st.LoadRun(started.ID)
	if err != nil || run.Status != engine.StatusSucceeded {
		t.Fatalf("%v %+v", err, run)
	}
	got := []string{}
	for _, s := range run.Steps {
		got = append(got, string(s.Status)+":"+s.Note)
	}
	if strings.Join(got, ",") != "succeeded:,skipped:deselected,skipped:skipped by operator,succeeded:" {
		t.Fatalf("steps %v", got)
	}
	if !slices.Contains(output, "answer=42") || run.State["answer"] != "42" || !sawExpect {
		t.Fatalf("output %q state %v expect %v", output, run.State, sawExpect)
	}
	if !strings.Contains(termOut.String(), "got hello") || !termEnded || !strings.Contains(run.Steps[0].Output, "got hello") {
		t.Fatalf("terminal %q ended %v recorded %q", termOut.String(), termEnded, run.Steps[0].Output)
	}
}

func TestRunThis(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	ts := httptest.NewServer(New(st, Config{}).Handler())
	defer ts.Close()

	raw := `name: edited
inputs: [{name: env, required: true}]
steps:
  - {name: setup, always: true, run: 'region=use1', outputs: [region]}
  - {name: other, run: 'exit 1'}
  - {name: target, run: 'echo "env=$env region=$region extra=$extra"'}
  - {name: later, always: true, run: 'echo later-ran'}
`
	body, _ := json.Marshal(map[string]any{"raw": raw, "step": 3, "vars": map[string]string{"env": "staging"}, "extra": map[string]string{"extra": "x1"}, "setup": true, "unattended": true})
	res := do(t, "POST", ts.URL+"/api/test-run", string(body), nil)
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
	var output []string
	var final *engine.Run
	for final == nil && sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var e WireEvent
		json.Unmarshal([]byte(line), &e)
		if e.Kind == engine.EventOutput {
			output = append(output, e.Text)
		}
		if e.Kind == engine.EventRunFinished {
			final = e.Run
		}
	}
	if final == nil || final.Status != engine.StatusSucceeded || !slices.Contains(output, "env=staging region=use1 extra=x1") || slices.Contains(output, "later-ran") {
		t.Fatalf("%+v %q", final, output)
	}
	if runs, _ := st.ListRuns("", 10); len(runs) != 0 {
		t.Fatalf("test runs must not be recorded: %d", len(runs))
	}
}

// A step fails; the operator edits it from the failure question, it runs
// again with the fix, and the edit is saved back to the runbook.
func TestEditStepDuringRunOverHTTP(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	ts := httptest.NewServer(New(st, Config{}).Handler())
	defer ts.Close()

	rb := "name: fixme\nsteps:\n  - {name: a, run: 'echo a'}\n  - {name: b, run: 'exit 3'}\n"
	if res := do(t, "POST", ts.URL+"/api/runbooks", rb, nil); res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d", res.StatusCode)
	}
	res := do(t, "POST", ts.URL+"/api/runs", `{"runbook":"fixme"}`, nil)
	var started struct{ ID string }
	json.NewDecoder(res.Body).Decode(&started)
	base := ts.URL + "/api/runs/" + started.ID

	evs, err := http.Get(base + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer evs.Body.Close()
	deadline := time.AfterFunc(10*time.Second, func() { evs.Body.Close() })
	defer deadline.Stop()
	sc := bufio.NewScanner(evs.Body)
	var final *engine.Run
	var output []string
	for final == nil && sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var e WireEvent
		json.Unmarshal([]byte(line), &e)
		switch e.Kind {
		case "question":
			if e.Question.Type != "failure" {
				t.Fatalf("unexpected question %+v", e.Question)
			}
			if r := do(t, "PUT", base+"/steps/1", "name: a\nrun: echo nope\n", nil); r.StatusCode == http.StatusOK {
				t.Fatal("editing a finished step should fail")
			}
			r := do(t, "PUT", base+"/steps/2", "name: b\nrun: echo fixed\n", nil)
			var body struct{ Restarted bool }
			json.NewDecoder(r.Body).Decode(&body)
			if r.StatusCode != http.StatusOK || !body.Restarted {
				t.Fatalf("edit: %d %+v", r.StatusCode, body)
			}
		case engine.EventOutput:
			output = append(output, e.Text)
		case engine.EventRunFinished:
			final = e.Run
		}
	}
	if final == nil || final.Status != engine.StatusSucceeded || !final.Steps[1].Edited || !strings.Contains(strings.Join(output, "|"), "fixed") {
		t.Fatalf("%+v %q", final, output)
	}
	r := do(t, "POST", base+"/save-edits", "", nil)
	var saved struct{ Version int }
	json.NewDecoder(r.Body).Decode(&saved)
	if r.StatusCode != http.StatusOK || saved.Version != 2 {
		t.Fatalf("save: %d %+v", r.StatusCode, saved)
	}
	if got, _, _ := st.Get("fixme"); got.Steps[1].Run != "echo fixed" {
		t.Fatalf("runbook not updated: %q", got.Steps[1].Run)
	}
	if rec, _ := st.LoadRun(started.ID); rec.EditsSaved != 2 {
		t.Fatalf("run record: edits_saved %d", rec.EditsSaved)
	}
}

func TestCollectionMembershipOverHTTP(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	ts := httptest.NewServer(New(st, Config{}).Handler())
	defer ts.Close()

	if r := do(t, "POST", ts.URL+"/api/runbooks?collection=ops", "name: a\nsteps: [{name: s, run: 'true'}]\n", nil); r.StatusCode != http.StatusCreated {
		t.Fatalf("create into collection: %d", r.StatusCode)
	}
	if r := do(t, "POST", ts.URL+"/api/runbooks/a/move", `{"to":"infra"}`, nil); r.StatusCode != http.StatusOK {
		t.Fatalf("move: %d", r.StatusCode)
	}
	if in, _ := st.CollectionsOf("a"); len(in) != 1 || in[0] != "infra" {
		t.Fatalf("after move: %v", in)
	}
	if r := do(t, "POST", ts.URL+"/api/collections/ops/runbooks", `{"runbooks":["a"]}`, nil); r.StatusCode != http.StatusOK {
		t.Fatalf("add: %d", r.StatusCode)
	}
	if r := do(t, "DELETE", ts.URL+"/api/collections/infra/runbooks/a", "", nil); r.StatusCode != http.StatusOK {
		t.Fatalf("remove: %d", r.StatusCode)
	}
	if in, _ := st.CollectionsOf("a"); len(in) != 1 || in[0] != "ops" {
		t.Fatalf("after add/remove: %v", in)
	}
}

func TestSchedulesOverHTTP(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	ts := httptest.NewServer(New(st, Config{}).Handler())
	defer ts.Close()

	rb := "name: mw\ninputs: [{name: t_cut}]\nmaintenance_window: {target: 20m}\nsteps:\n  - {name: down, run: x, window: open}\n  - {name: cut, run: x, window_offset: +5m, offset_input: t_cut}\n  - {name: up, run: x, window: close}\n"
	if r := do(t, "POST", ts.URL+"/api/runbooks", rb, nil); r.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d", r.StatusCode)
	}
	r := do(t, "POST", ts.URL+"/api/schedules", `{"runbook":"mw","at":"2030-01-02T22:00:00Z","title":"cutover","inputs":{"t_cut":"+9m"}}`, nil)
	var sc store.Schedule
	json.NewDecoder(r.Body).Decode(&sc)
	if r.StatusCode != http.StatusCreated || sc.ID == "" {
		t.Fatalf("schedule: %d", r.StatusCode)
	}
	if r := do(t, "POST", ts.URL+"/api/schedules", `{"runbook":"mw","at":"2030-01-02T22:00:00Z","inputs":{"t_cut":"soon"}}`, nil); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad override: %d", r.StatusCode)
	}
	r = do(t, "GET", ts.URL+"/api/schedules?from=2030-01-01T00:00:00Z", "", nil)
	var ag []store.PlannedRun
	json.NewDecoder(r.Body).Decode(&ag)
	if len(ag) != 1 || len(ag[0].Entries) != 2 || ag[0].Entries[1].Offset != "+9m" || ag[0].Entries[0].End == nil {
		t.Fatalf("agenda %+v", ag)
	}
	if r := do(t, "PUT", ts.URL+"/api/schedules/"+sc.ID, `{"runbook":"mw","at":"2030-01-03T22:00:00Z","title":"moved"}`, nil); r.StatusCode != http.StatusOK {
		t.Fatalf("update: %d", r.StatusCode)
	}
	if got, _ := st.GetSchedule(sc.ID); got.Title != "moved" || got.At.Day() != 3 {
		t.Fatalf("after update %+v", got)
	}
	if r := do(t, "DELETE", ts.URL+"/api/schedules/"+sc.ID, "", nil); r.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", r.StatusCode)
	}
}

func TestExplainOverHTTP(t *testing.T) {
	var asked map[string]any
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&asked)
		calls++
		fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5,\"cache_read_input_tokens\":2000}}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Sets the region.\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":4}}\n\n")
	}))
	defer api.Close()
	st, _ := store.Open(t.TempDir(), "")
	ts := httptest.NewServer(New(st, Config{Claude: &claude.Client{Key: "k", Model: "m", BaseURL: api.URL}}).Handler())
	defer ts.Close()

	if r := do(t, "GET", ts.URL+"/api/claude", "", nil); r.StatusCode != 200 {
		t.Fatalf("status %d", r.StatusCode)
	}
	defer func() {
		// Asked twice: the second time comes from runnr's own store.
		if calls != 1 {
			t.Errorf("the API was called %d times, want 1", calls)
		}
	}()
	// An unsaved runbook from the editor.
	body := `{"raw":"name: t\nsteps:\n  - {name: setup, always: true, run: 'region=use1', outputs: [region]}\n","step":1,"question":"why?"}`
	r := do(t, "POST", ts.URL+"/api/explain", body, nil)
	b, _ := io.ReadAll(r.Body)
	out := string(b)
	if !strings.Contains(out, `{"text":"Sets the region."}`) || !strings.Contains(out, `"cache_read_input_tokens":2000`) {
		t.Fatalf("%s", out)
	}
	if q := asked["messages"].([]any)[0].(map[string]any)["content"]; q != `About step 1 ("setup"): why?` {
		t.Fatalf("question %q", q)
	}
	r = do(t, "POST", ts.URL+"/api/explain", body, nil)
	b, _ = io.ReadAll(r.Body)
	if !strings.Contains(string(b), `"local":true`) || !strings.Contains(string(b), "Sets the region.") {
		t.Fatalf("second time: %s", b)
	}
	// Without Claude set up, it says how.
	ts2 := httptest.NewServer(New(st, Config{NoClaude: true}).Handler())
	defer ts2.Close()
	if r := do(t, "POST", ts2.URL+"/api/explain", body, nil); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("without a key: %d", r.StatusCode)
	}
}

func TestResumeWithChangedRunbook(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	ts := httptest.NewServer(New(st, Config{}).Handler())
	defer ts.Close()

	v1 := "name: ch\ninputs: [{name: mode, default: fail}]\nsteps:\n  - {name: a, run: 'true'}\n  - {name: gone, run: 'true'}\n  - {name: b, run: '[ \"$mode\" = ok ]'}\n"
	do(t, "POST", ts.URL+"/api/runbooks", v1, nil)
	var run engine.Run
	json.NewDecoder(do(t, "POST", ts.URL+"/api/runs", `{"runbook":"ch","unattended":true,"wait":true}`, nil).Body).Decode(&run)
	if run.Status != engine.StatusFailed {
		t.Fatalf("run %s", run.Status)
	}
	if r := do(t, "POST", ts.URL+"/api/runs/"+run.ID+"/notes", `{"step":2,"text":"saw it","author":"pat"}`, nil); r.StatusCode >= 300 {
		b, _ := io.ReadAll(r.Body)
		t.Fatalf("note: %d %s", r.StatusCode, b)
	}
	v2 := strings.Replace(v1, "  - {name: gone, run: 'true'}\n", "", 1) + "  - {name: c, run: 'true'}\n"
	if r := do(t, "PUT", ts.URL+"/api/runbooks/ch", v2, nil); r.StatusCode != http.StatusOK {
		t.Fatalf("edit: %d", r.StatusCode)
	}
	// A new run won't start from a changed runbook's steps.
	if r := do(t, "POST", ts.URL+"/api/runs/"+run.ID+"/resume", `{"vars":{"mode":"ok"},"unattended":true,"wait":true,"new_run":true}`, nil); r.StatusCode == http.StatusOK {
		t.Fatalf("new_run on a changed runbook: %d", r.StatusCode)
	}
	// Carrying on: the same run, with version 2 noted.
	var got engine.Run
	r := do(t, "POST", ts.URL+"/api/runs/"+run.ID+"/resume", `{"vars":{"mode":"ok"},"unattended":true,"wait":true}`, nil)
	json.NewDecoder(r.Body).Decode(&got)
	if r.StatusCode != http.StatusOK || got.ID != run.ID || got.Status != engine.StatusSucceeded || len(got.Steps) != 3 ||
		len(got.Resumptions) != 1 || got.Resumptions[0].RunbookVersion != 2 || len(got.Removed) != 1 || got.Removed[0].Name != "gone" {
		t.Fatalf("resume: %d %+v", r.StatusCode, got)
	}
	saved, _ := st.LoadRun(run.ID)
	if len(saved.Notes) != 1 || !strings.Contains(saved.Notes[0].Text, "gone, no longer in the runbook") {
		t.Fatalf("notes %+v", saved.Notes)
	}
}

func TestRunTimeline(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	ts := httptest.NewServer(New(st, Config{}).Handler())
	defer ts.Close()
	do(t, "POST", ts.URL+"/api/runbooks", "name: tl\nsteps: [{name: a, run: 'true'}]\n", nil)
	var run engine.Run
	json.NewDecoder(do(t, "POST", ts.URL+"/api/runs", `{"runbook":"tl","unattended":true,"wait":true}`, nil).Body).Decode(&run)
	r := do(t, "GET", ts.URL+"/api/runs/"+run.ID+"/timeline?download=1", "", nil)
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != http.StatusOK || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/html") || !strings.Contains(r.Header.Get("Content-Disposition"), ".timeline.html") || !strings.Contains(string(b), ">a<") {
		t.Fatalf("%d %v %s", r.StatusCode, r.Header, b)
	}
}

// Suspend a run this server runs: it stops after the running step, a
// request can be taken back, and a resume carries the run on.
func TestSuspendOverHTTP(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	ts := httptest.NewServer(New(st, Config{}).Handler())
	defer ts.Close()
	do(t, "POST", ts.URL+"/api/runbooks", "name: sus\nshell: sh\nsteps:\n  - {name: a, run: 'sleep 2'}\n  - {name: b, run: 'sleep 1.5'}\n  - {name: c, run: 'echo C'}\n", nil)
	var run engine.Run
	json.NewDecoder(do(t, "POST", ts.URL+"/api/runs", `{"runbook":"sus","unattended":true}`, nil).Body).Decode(&run)
	time.Sleep(200 * time.Millisecond)

	// Asked, then taken back: it carries on past step a.
	do(t, "POST", ts.URL+"/api/runs/"+run.ID+"/suspend", `{"author":"pat"}`, nil)
	r := do(t, "POST", ts.URL+"/api/runs/"+run.ID+"/suspend", `{"cancel":true}`, nil)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("cancel: %d", r.StatusCode)
	}
	time.Sleep(2200 * time.Millisecond) // into step b
	var res store.SuspendResult
	json.NewDecoder(do(t, "POST", ts.URL+"/api/runs/"+run.ID+"/suspend", `{"author":"pat","reason":"back tomorrow"}`, nil).Body).Decode(&res)
	if res.Suspended || !strings.Contains(res.Message, "once the step running now finishes") {
		t.Fatalf("%+v", res)
	}
	var got *engine.Run
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if got, _ = st.LoadRun(run.ID); got.Status != engine.StatusRunning || time.Now().After(deadline) {
			break
		}
	}
	if got.Status != engine.StatusSuspended || got.Error != "suspended by pat: back tomorrow" || got.Steps[1].Status != engine.StatusSucceeded || got.Steps[2].Status != engine.StatusPending {
		t.Fatalf("%s %q %s %s", got.Status, got.Error, got.Steps[1].Status, got.Steps[2].Status)
	}
	if r := do(t, "POST", ts.URL+"/api/runs/"+run.ID+"/suspend", `{}`, nil); r.StatusCode == http.StatusOK {
		t.Fatal("suspended a run that isn't running")
	}
	var again engine.Run
	json.NewDecoder(do(t, "POST", ts.URL+"/api/runs/"+run.ID+"/resume", `{"unattended":true,"wait":true}`, nil).Body).Decode(&again)
	if again.Status != engine.StatusSucceeded || again.ID != run.ID || len(again.Resumptions) != 1 || again.Resumptions[0].From != 2 {
		t.Fatalf("resume: %s %+v", again.Status, again.Resumptions)
	}
}

func TestRunsByIdentifier(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	ts := httptest.NewServer(New(st, Config{}).Handler())
	defer ts.Close()
	do(t, "POST", ts.URL+"/api/runbooks", "name: silo-op\ninputs: [{name: silo, identifier: true}, {name: why}]\nsteps: [{name: a, run: 'true'}]\n", nil)
	for _, silo := range []string{"silo-3", "silo-12"} {
		do(t, "POST", ts.URL+"/api/runs", `{"runbook":"silo-op","unattended":true,"wait":true,"vars":{"silo":"`+silo+`","why":"silo-3 lag"}}`, nil)
	}
	var runs []engine.Run
	json.NewDecoder(do(t, "GET", ts.URL+"/api/runs?runbook=silo-op&q=SILO-3", "", nil).Body).Decode(&runs)
	if len(runs) != 1 || runs[0].Inputs["silo"] != "silo-3" || strings.Join(runs[0].Identifiers, ",") != "silo" {
		t.Fatalf("%+v", runs)
	}
}
