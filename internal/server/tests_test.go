package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/egrif/runnr/internal/runtest"
	"github.com/egrif/runnr/internal/store"
)

// The test binary stands in for runnr when a test's fake commands call back.
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "__fake" {
		os.Exit(runtest.Dispatch(os.Args[2], os.Args[3:]))
	}
	os.Exit(m.Run())
}

func TestRunbookTestsOverHTTP(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	ts := httptest.NewServer(New(st, Config{}).Handler())
	defer ts.Close()
	rb := "name: tt\nsteps:\n  - {name: ask, run: 'aws sts get-caller-identity --query Arn --output text'}\n"
	if res := do(t, "POST", ts.URL+"/api/runbooks", rb, nil); res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d", res.StatusCode)
	}
	// No suite yet.
	res := do(t, "GET", ts.URL+"/api/runbooks/tt/tests", "", nil)
	var got struct {
		Suite any `json:"suite"`
	}
	json.NewDecoder(res.Body).Decode(&got)
	if got.Suite != nil {
		t.Fatalf("suite %v", got.Suite)
	}
	if _, _, err := st.SaveSuite([]byte(`runbook: tt
isolate: [aws]
fakes: [{command: aws sts get-caller-identity, json: {Arn: "arn:aws:sts::1:assumed-role/x/me"}}]
scenarios:
  - {name: ok, expect: {status: succeeded, output: {1: "assumed-role/x/me"}}}
`), store.SaveOpts{}); err != nil {
		t.Fatal(err)
	}
	run := func(body string) testRun {
		res := do(t, "POST", ts.URL+"/api/runbooks/tt/tests/run", body, map[string]string{"Content-Type": "application/json"})
		if res.StatusCode != http.StatusOK {
			t.Fatalf("run: %d", res.StatusCode)
		}
		var tr testRun
		json.NewDecoder(res.Body).Decode(&tr)
		return tr
	}
	if tr := run(`{}`); len(tr.Results) != 1 || !tr.Results[0].Passed || tr.Draft {
		t.Fatalf("saved: %+v", tr)
	}
	// Against a draft whose step asks for something no fake answers.
	draft := strings.Replace(rb, "sts get-caller-identity --query Arn", "sts get-session-token", 1)
	body, _ := json.Marshal(map[string]string{"raw": draft})
	tr := run(string(body))
	if !tr.Draft || tr.Results[0].Passed || !strings.Contains(strings.Join(tr.Results[0].Problems, "\n"), "no fake answered") {
		t.Fatalf("draft: %+v", tr)
	}
	// The latest results are the saved runbook's, not the draft's.
	res = do(t, "GET", ts.URL+"/api/runbooks/tt/tests", "", nil)
	var last struct {
		Last *testRun `json:"last"`
	}
	json.NewDecoder(res.Body).Decode(&last)
	if last.Last == nil || last.Last.Draft || !last.Last.Results[0].Passed {
		t.Fatalf("last %+v", last.Last)
	}
}
