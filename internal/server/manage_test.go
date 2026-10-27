package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/egrif/runnr/internal/store"
	"github.com/egrif/runnr/internal/ver"
)

// Deleting a runbook takes it out of its collections and removes its
// scheduled runs; restoring brings back its last version.
func TestManageDeleteRestore(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	ts := httptest.NewServer(New(st, Config{}).Handler())
	defer ts.Close()
	st.SaveLibrary([]byte("name: helpers\nfunctions:\n  hi: {body: 'echo hi'}\n"), store.SaveOpts{})
	st.Save([]byte("name: rb\nlibraries: [helpers]\nsteps:\n  - {name: one, run: 'hi'}\n"), store.SaveOpts{})
	st.AddToCollection("ops", true, "rb")
	st.SaveSuite([]byte("runbook: rb\nscenarios:\n  - {name: works}\n"), store.SaveOpts{})
	if err := st.SaveSchedule(&store.Schedule{Runbook: "rb", At: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	get := func() map[string]any {
		res := do(t, "GET", ts.URL+"/api/manage", "", nil)
		var m map[string]any
		json.NewDecoder(res.Body).Decode(&m)
		return m
	}
	find := func(m map[string]any, list, kind, name string) map[string]any {
		for _, x := range m[list].([]any) {
			it := x.(map[string]any)
			if it["kind"] == kind && it["name"] == name {
				return it
			}
		}
		return nil
	}
	m := get()
	lib, rb := find(m, "items", "library", "helpers"), find(m, "items", "runbook", "rb")
	if lib == nil || len(lib["used_by"].([]any)) != 1 || rb == nil || len(rb["used_by"].([]any)) != 2 || rb["schedules"] != 1.0 {
		t.Fatalf("lib %v rb %v", lib, rb)
	}
	res := do(t, "POST", ts.URL+"/api/manage/delete", `{"items":[{"kind":"runbook","name":"rb"}]}`, nil)
	var out struct{ Results []manageResult }
	json.NewDecoder(res.Body).Decode(&out)
	if len(out.Results) != 1 || !out.Results[0].OK || len(out.Results[0].Also) != 2 {
		t.Fatalf("delete %+v", out)
	}
	m = get()
	if find(m, "items", "runbook", "rb") != nil || find(m, "deleted", "runbook", "rb") == nil {
		t.Fatalf("after delete %v", m)
	}
	if c, _ := st.LoadCollection("ops"); len(c.Collection.Runbooks) != 0 {
		t.Fatalf("collection %+v", c.Collection)
	}
	res = do(t, "POST", ts.URL+"/api/manage/restore", `{"items":[{"kind":"runbook","name":"rb"}]}`, nil)
	json.NewDecoder(res.Body).Decode(&out)
	// Restoring makes its last version current again.
	if !out.Results[0].OK || out.Results[0].Version != ver.Of([]byte("name: rb\nlibraries: [helpers]\nsteps:\n  - {name: one, run: 'hi'}\n")) {
		t.Fatalf("restore %+v", out)
	}
	if res := do(t, "DELETE", ts.URL+"/api/runs/nope", "", nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("delete missing run: %d", res.StatusCode)
	}
}
