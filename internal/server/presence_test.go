package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/store"
)

func as(name, session string, override bool) map[string]string {
	h := map[string]string{hdrUser: name, hdrSession: session}
	if override {
		h[hdrOverride] = "1"
	}
	return h
}

// Only the person who started it drives it; someone else is turned away
// unless they override, which everyone hears about and the run notes.
func TestReadOnlyAndOverride(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	ts := httptest.NewServer(New(st, Config{}).Handler())
	defer ts.Close()
	do(t, "POST", ts.URL+"/api/runbooks", "name: ro\nshell: sh\nsteps:\n  - {name: a, run: 'sleep 3'}\n  - {name: b, run: 'true'}\n", nil)
	var run engine.Run
	json.NewDecoder(do(t, "POST", ts.URL+"/api/runs", `{"runbook":"ro","unattended":true,"access":{"mode":"only"}}`, as("Eric", "s1", false)).Body).Decode(&run)

	ctl := `{"step_mode":false,"skip":[2],"pause_before":[],"pause_after":[]}`
	if r := do(t, "PUT", ts.URL+"/api/runs/"+run.ID+"/controls", ctl, as("eric", "s1", false)); r.StatusCode != http.StatusOK {
		t.Fatalf("the driver: %d", r.StatusCode)
	}
	r := do(t, "PUT", ts.URL+"/api/runs/"+run.ID+"/controls", ctl, as("pat", "s2", false))
	var refused struct{ Code, Error string }
	json.NewDecoder(r.Body).Decode(&refused)
	if r.StatusCode != http.StatusForbidden || refused.Code != "read_only" || !strings.Contains(refused.Error, "only Eric can drive") {
		t.Fatalf("watcher: %d %+v", r.StatusCode, refused)
	}
	for i := 0; i < 2; i++ { // noted once
		if r := do(t, "PUT", ts.URL+"/api/runs/"+run.ID+"/controls", ctl, as("pat", "s2", true)); r.StatusCode != http.StatusOK {
			t.Fatalf("override: %d", r.StatusCode)
		}
	}
	saved, _ := st.LoadRun(run.ID)
	if len(saved.Notes) != 1 || saved.Notes[0].Author != "pat" || !strings.Contains(saved.Notes[0].Text, "overrode read-only") {
		t.Fatalf("notes %+v", saved.Notes)
	}
	// Everyone except pat: pat can't change it back without overriding.
	if r := do(t, "PUT", ts.URL+"/api/runs/"+run.ID+"/access", `{"mode":"except","names":["pat"]}`, as("eric", "s1", false)); r.StatusCode != http.StatusOK {
		t.Fatalf("set access: %d", r.StatusCode)
	}
	if r := do(t, "PUT", ts.URL+"/api/runs/"+run.ID+"/access", `{"mode":"everyone"}`, as("Pat", "s3", false)); r.StatusCode != http.StatusForbidden {
		t.Fatalf("pat set access: %d", r.StatusCode)
	}
	if r := do(t, "POST", ts.URL+"/api/runs/"+run.ID+"/notes", `{"step":0,"text":"watching","author":"pat"}`, as("pat", "s3", false)); r.StatusCode >= 300 {
		t.Fatalf("notes stay open to watchers: %d", r.StatusCode)
	}
}

// Who's watching: a viewer hears who's there as it joins, and the others
// hear about it.
func TestPresence(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	ts := httptest.NewServer(New(st, Config{}).Handler())
	defer ts.Close()
	do(t, "POST", ts.URL+"/api/runbooks", "name: pr\nshell: sh\nsteps: [{name: a, run: 'sleep 3'}]\n", nil)
	var run engine.Run
	json.NewDecoder(do(t, "POST", ts.URL+"/api/runs", `{"runbook":"pr","unattended":true}`, as("eric", "s1", false)).Body).Decode(&run)
	watch := func(name, session string) (<-chan Presence, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/runs/"+run.ID+"/events?name="+name+"&session="+session, nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		out := make(chan Presence, 16)
		go func() {
			sc := bufio.NewScanner(res.Body)
			sc.Buffer(nil, 1<<20)
			for sc.Scan() {
				var e WireEvent
				if b, ok := strings.CutPrefix(sc.Text(), "data: "); ok && json.Unmarshal([]byte(b), &e) == nil && e.Presence != nil {
					out <- *e.Presence
				}
			}
		}()
		return out, cancel
	}
	next := func(ch <-chan Presence) Presence {
		select {
		case p := <-ch:
			return p
		case <-time.After(3 * time.Second):
			t.Fatal("no presence event")
		}
		return Presence{}
	}
	names := func(p Presence) string {
		var n []string
		for _, v := range p.Viewers {
			n = append(n, v.Name)
		}
		return strings.Join(n, ",")
	}
	eric, stop := watch("eric", "s1")
	defer stop()
	if p := next(eric); names(p) != "eric" || p.StartedBy != "eric" || p.Access.Mode != "everyone" {
		t.Fatalf("%+v", p)
	}
	_, stopPat := watch("pat", "s2")
	if p := next(eric); names(p) != "eric,pat" {
		t.Fatalf("%+v", p)
	}
	stopPat()
	if p := next(eric); names(p) != "eric" {
		t.Fatalf("after pat left: %+v", p)
	}
}

// Whoever typed last sets a terminal's size.
func TestSizeFollowsTheTypist(t *testing.T) {
	l := newLiveRun("x", nil)
	check := func(what, session string, typed bool, rows, cols uint16, want [2]uint16, apply bool) {
		t.Helper()
		r, c, ok := l.sizeFor("t1", session, typed, rows, cols)
		if ok != apply || ok && [2]uint16{r, c} != want {
			t.Fatalf("%s: %d×%d %v", what, r, c, ok)
		}
	}
	check("a resizes, nobody typed yet", "a", false, 40, 120, [2]uint16{40, 120}, true)
	check("b resizes, nobody typed yet", "b", false, 30, 80, [2]uint16{30, 80}, true)
	check("a types: a's size", "a", true, 0, 0, [2]uint16{40, 120}, true)
	check("b resizes: kept, not applied", "b", false, 25, 70, [2]uint16{}, false)
	check("a types again: nothing new", "a", true, 0, 0, [2]uint16{}, false)
	check("b types: b's latest size", "b", true, 0, 0, [2]uint16{25, 70}, true)
}
