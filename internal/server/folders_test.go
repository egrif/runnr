package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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
