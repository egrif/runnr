package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

// A project folder: placeholders filled, a library from elsewhere in the
// repository, and a file whose name is taken left alone unless replaced.
func TestImportProject(t *testing.T) {
	root := t.TempDir()
	for p, body := range map[string]string{
		".git/HEAD":         "x",
		"lib/shared.yaml":   "name: shared\nfunctions:\n  hi: {body: 'echo hi'}\n",
		"proj/rb.yaml":      "name: rb\nlibraries: [shared]\nsteps:\n  - {name: one, run: 'echo @PROJECT_DIR@ @TEAM@'}\n",
		"proj/rb.test.yaml": "runbook: rb\nscenarios:\n  - {name: works}\n",
	} {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(body), 0o644)
	}
	st, _ := store.Open(t.TempDir(), "")
	ts := httptest.NewServer(New(st, Config{}).Handler())
	defer ts.Close()
	post := func(path string, body any) map[string]any {
		b, _ := json.Marshal(body)
		res := do(t, "POST", ts.URL+path, string(b), nil)
		var m map[string]any
		json.NewDecoder(res.Body).Decode(&m)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %v", path, res.StatusCode, m)
		}
		return m
	}
	dir := filepath.Join(root, "proj")
	plan := post("/api/import/scan", map[string]any{"dir": dir})
	if fmt.Sprint(plan["unset"]) != "[TEAM]" {
		t.Fatalf("unset %v", plan["unset"])
	}
	req := map[string]any{"dir": dir, "values": map[string]string{"TEAM": "x"}, "include": []string{filepath.Join(root, "lib/shared.yaml")}}
	out := post("/api/import/project", req)
	if got := statusList(out); got != "imported,imported,imported" {
		t.Fatalf("first %s", got)
	}
	if d, err := st.Load("rb"); err != nil || !strings.Contains(string(d.Raw), "echo "+dir+" x") {
		t.Fatalf("%v %s", err, d.Raw)
	}
	os.WriteFile(filepath.Join(root, "lib/shared.yaml"), []byte("name: shared\nfunctions:\n  hi: {body: 'echo HI'}\n"), 0o644)
	if got := statusList(post("/api/import/project", req)); got != "exists,unchanged,unchanged" {
		t.Fatalf("taken %s", got)
	}
	req["replace"] = []string{filepath.Join(root, "lib/shared.yaml")}
	if got := statusList(post("/api/import/project", req)); got != "replaced,unchanged,unchanged" {
		t.Fatalf("replaced %s", got)
	}
}

func statusList(out map[string]any) string {
	var s []string
	for _, r := range out["results"].([]any) {
		s = append(s, r.(map[string]any)["status"].(string))
	}
	return strings.Join(s, ",")
}

// A file with a placeholder isn't imported without a value for it.
func TestImportPlaceholders(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	ts := httptest.NewServer(New(st, Config{}).Handler())
	defer ts.Close()
	rb := "name: p\nsteps:\n  - {name: one, run: 'echo @TEAM@'}\n"
	post := func(path string) (int, map[string]any) {
		res := do(t, "POST", ts.URL+path, rb, map[string]string{"Content-Type": "application/yaml"})
		var m map[string]any
		json.NewDecoder(res.Body).Decode(&m)
		return res.StatusCode, m
	}
	if _, m := post("/api/import/check"); fmt.Sprint(m["unset"]) != "[TEAM]" {
		t.Fatalf("check %v", m)
	}
	if code, _ := post("/api/import"); code != http.StatusBadRequest {
		t.Fatalf("unset: %d", code)
	}
	if code, m := post("/api/import?set=TEAM=dbre"); code != http.StatusCreated {
		t.Fatalf("set: %d %v", code, m)
	}
	if d, _ := st.Load("p"); !strings.Contains(string(d.Raw), "echo dbre") {
		t.Fatalf("%s", d.Raw)
	}
}
