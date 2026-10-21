package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/egrif/runnr/internal/store"
)

// Importing a file whose name is taken is refused unless it's renamed or
// saved as the next version.
func TestImport(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	ts := httptest.NewServer(New(st, Config{}).Handler())
	defer ts.Close()
	v1 := "name: w\nsteps:\n  - {name: one, run: 'echo 1'}\n"
	v2 := "name: w\nsteps:\n  - {name: one, run: 'echo 2'}\n"
	post := func(path, body string) (int, map[string]any) {
		res := do(t, "POST", ts.URL+path, body, map[string]string{"Content-Type": "application/yaml"})
		var m map[string]any
		json.NewDecoder(res.Body).Decode(&m)
		return res.StatusCode, m
	}
	if code, m := post("/api/import", v1); code != http.StatusCreated || m["version"] != 1.0 {
		t.Fatalf("first import %d %v", code, m)
	}
	if _, m := post("/api/import/check", v2); m["kind"] != "runbook" || m["exists"] != true || m["version"] != 1.0 || m["same"] == true {
		t.Fatalf("check %v", m)
	}
	if _, m := post("/api/import/check", v1); m["same"] != true {
		t.Fatalf("check same %v", m)
	}
	if code, _ := post("/api/import", v2); code != http.StatusConflict {
		t.Fatalf("taken name: %d", code)
	}
	if code, m := post("/api/import?as=w-2", v2); code != http.StatusCreated || m["name"] != "w-2" {
		t.Fatalf("renamed %d %v", code, m)
	}
	if code, m := post("/api/import?replace=1", v2); code != http.StatusCreated || m["version"] != 2.0 || m["replaced"] != 1.0 {
		t.Fatalf("replace %d %v", code, m)
	}
	lib := "name: helpers\nfunctions:\n  hi: {body: 'echo hi'}\n"
	if code, m := post("/api/import", lib); code != http.StatusCreated || m["kind"] != "library" {
		t.Fatalf("library %d %v", code, m)
	}
	if _, m := post("/api/import/check", "name: x\n"); m["error"] == nil {
		t.Fatalf("neither: %v", m)
	}
}
