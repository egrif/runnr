package server

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/store"
)

// A shell before any run: the runbook's environment (inputs given, else
// their defaults), its own event stream, and keystrokes in.
func TestShellBeforeTheRun(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	ts := httptest.NewServer(New(st, Config{}).Handler())
	defer ts.Close()
	do(t, "POST", ts.URL+"/api/runbooks", `name: w
shell: sh
inputs:
  - {name: who, default: world}
  - {name: where}
steps:
  - {name: one, run: 'true'}
`, nil)
	res := do(t, "POST", ts.URL+"/api/shells", `{"runbook":"w","step":1,"vars":{"where":"here"}}`, nil)
	var sh struct{ ID, Title, Phase string }
	json.NewDecoder(res.Body).Decode(&sh)
	if res.StatusCode != http.StatusCreated || sh.Phase != "before" || sh.Title != "shell before the run · step 1: one" {
		t.Fatalf("%d %+v", res.StatusCode, sh)
	}
	evs, err := http.Get(ts.URL + "/api/shells/" + sh.ID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer evs.Body.Close()
	deadline := time.AfterFunc(10*time.Second, func() { evs.Body.Close() })
	defer deadline.Stop()
	sc := bufio.NewScanner(evs.Body)
	var out strings.Builder
	ended := false
	for !ended && sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var e WireEvent
		json.Unmarshal([]byte(line), &e)
		switch e.Kind {
		case "term_start":
			do(t, "POST", ts.URL+"/api/shells/"+sh.ID+"/term", `{"id":"`+e.Term.ID+`","data":"echo \"[$who $where]\"; exit\r","rows":24,"cols":80}`, nil)
		case "term":
			b, _ := base64.StdEncoding.DecodeString(e.Term.Data)
			out.Write(b)
		case "term_end":
			ended = true
		}
	}
	if !ended || !strings.Contains(out.String(), "[world here]") {
		t.Fatalf("ended %v output %q", ended, out.String())
	}
}

// An unattended run started here can still be driven from the console: a
// shell opens alongside it, and its background steps get a terminal.
func TestUnattendedRunIsLive(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	srv := New(st, Config{})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	do(t, "POST", ts.URL+"/api/runbooks", "name: u\nshell: sh\nsteps:\n  - {name: bg, run: 'echo bg; sleep 0.3', background: true}\n  - {name: slow, run: 'sleep 1.5'}\n", nil)
	var run engine.Run
	json.NewDecoder(do(t, "POST", ts.URL+"/api/runs", `{"runbook":"u","unattended":true}`, nil).Body).Decode(&run)
	time.Sleep(400 * time.Millisecond)
	var sh struct{ Phase string }
	json.NewDecoder(do(t, "POST", ts.URL+"/api/shells", `{"run":"`+run.ID+`","step":0}`, nil).Body).Decode(&sh)
	if sh.Phase != "alongside" {
		t.Fatalf("shell phase %q", sh.Phase)
	}
	l := srv.getLive(run.ID)
	if l == nil {
		t.Fatal("not live")
	}
	l.mu.Lock()
	var bg bool
	for _, e := range l.events {
		bg = bg || (e.Kind == "term_start" && e.Term != nil && e.Term.Background)
	}
	l.mu.Unlock()
	if !bg {
		t.Fatal("the background step got no terminal")
	}
}
