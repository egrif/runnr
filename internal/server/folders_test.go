package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/store"
)

// The Settings page's folders: set for everything, a collection or a
// runbook, with what each runbook's runs would have.
func TestFoldersAPI(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	ts := httptest.NewServer(New(st, Config{}).Handler())
	defer ts.Close()
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "ops"), 0o755)
	os.MkdirAll(filepath.Join(src, "mine"), 0o755)
	st.Save([]byte("name: rb\nsteps:\n  - {name: one, run: 'ls @REPO_ROOT@'}\n"), store.SaveOpts{})
	st.Save([]byte("name: other\nsteps:\n  - {name: one, run: 'ls @REPO_ROOT@'}\n"), store.SaveOpts{})
	st.AddToCollection("ops", true, "rb", "other")

	put := func(body string) int {
		t.Helper()
		res := do(t, "PUT", ts.URL+"/api/folders", body, nil)
		res.Body.Close()
		return res.StatusCode
	}
	for _, b := range []string{
		`{"name": "REPOS_ROOT", "value": "` + src + `"}`,
		`{"collection": "ops", "name": "REPO_ROOT", "value": "@REPOS_ROOT@/ops"}`,
		`{"runbook": "rb", "name": "REPO_ROOT", "value": "@REPOS_ROOT@/mine"}`,
	} {
		if c := put(b); c != 200 {
			t.Fatalf("%s: %d", b, c)
		}
	}
	if c := put(`{"runbook": "nope", "name": "REPO_ROOT", "value": "/x"}`); c != 404 {
		t.Fatalf("unknown runbook: %d", c)
	}

	var got struct {
		Scopes   map[string]map[string]folderValue `json:"scopes"`
		Runbooks map[string]struct {
			Values map[string]string `json:"values"`
			From   map[string]string `json:"from"`
		} `json:"runbooks"`
	}
	res := do(t, "GET", ts.URL+"/api/folders", "", nil)
	json.NewDecoder(res.Body).Decode(&got)
	res.Body.Close()
	if v := got.Scopes["runbook:rb"]["REPO_ROOT"]; v.Raw != "@REPOS_ROOT@/mine" || v.Value != filepath.Join(src, "mine") || v.Why != "" {
		t.Fatalf("runbook's own: %+v", v)
	}
	if r := got.Runbooks["rb"]; r.From["REPO_ROOT"] != "runbook" || r.Values["REPO_ROOT"] != filepath.Join(src, "mine") {
		t.Fatalf("rb: %+v", r)
	}
	if r := got.Runbooks["other"]; r.From["REPO_ROOT"] != "collection" || r.Values["REPO_ROOT"] != filepath.Join(src, "ops") {
		t.Fatalf("other: %+v", r)
	}
}

// About says which runnr this is and where its data lives.
func TestAbout(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	ts := httptest.NewServer(New(st, Config{Build: Build{Version: "v9.9.9", Commit: "abc1234def"}}).Handler())
	defer ts.Close()
	var a struct {
		Build    Build             `json:"build"`
		Database map[string]string `json:"database"`
		Started  string            `json:"started"`
	}
	res := do(t, "GET", ts.URL+"/api/about", "", nil)
	json.NewDecoder(res.Body).Decode(&a)
	res.Body.Close()
	if a.Build.Version != "v9.9.9" || a.Build.Commit != "abc1234def" || a.Database["kind"] != "SQLite" || a.Database["location"] != st.Location || a.Started == "" {
		t.Fatalf("%+v", a)
	}
}

// Before starting a run, the GUI asks about an unfinished one for the
// same thing; a new run isn't compared with an earlier one unless asked.
func TestUnfinishedAPI(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	ts := httptest.NewServer(New(st, Config{}).Handler())
	defer ts.Close()
	st.Save([]byte("name: rb\ninputs:\n  - {name: target, identifier: true}\nsteps:\n  - {name: one, run: 'true'}\n  - {name: two, run: 'false'}\n"), store.SaveOpts{})
	rb, _, _ := st.Get("rb")
	failed := engine.Execute(context.Background(), rb, engine.Unattended{}, "", engine.Options{Vars: map[string]string{"target": "a"}})
	if err := st.SaveRun(failed); err != nil {
		t.Fatal(err)
	}
	ask := func(target string) (got struct {
		Run *struct {
			ID       string `json:"id"`
			Step     int    `json:"step"`
			StepName string `json:"step_name"`
		} `json:"run"`
	}) {
		t.Helper()
		res := do(t, "POST", ts.URL+"/api/runbooks/rb/unfinished", `{"vars": {"target": "`+target+`"}}`, nil)
		json.NewDecoder(res.Body).Decode(&got)
		res.Body.Close()
		return got
	}
	if u := ask("a").Run; u == nil || u.ID != failed.ID || u.Step != 1 || u.StepName != "two" {
		t.Fatalf("target a: %+v", u)
	}
	if u := ask("b").Run; u != nil {
		t.Fatalf("target b: %+v", u)
	}
	if p, _ := (&Server{st: st}).previousRun(""); p != nil {
		t.Fatalf("compared with %s without asking", p.ID)
	}
}
