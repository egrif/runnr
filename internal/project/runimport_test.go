package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/store"
)

func TestImportRuns(t *testing.T) {
	at := time.Date(2026, 10, 20, 16, 0, 0, 0, time.UTC)
	done := &engine.Run{ID: "20261020-160000-aaaaaa", Runbook: "orders-db-cutover", Mode: "interactive", Status: engine.StatusSucceeded, Started: at, Ended: at.Add(time.Hour),
		Inputs: map[string]string{"target": "replica-b"}, Notes: []engine.Note{{Time: at.Add(time.Minute), Author: "pat", Text: "lag at zero"}},
		Steps: []engine.StepResult{{Name: "promote", Kind: "run", Status: engine.StatusSucceeded, Started: at, Ended: at.Add(time.Hour)}}}
	going := &engine.Run{ID: "20261021-090000-bbbbbb", Runbook: "orders-db-cutover", Mode: "interactive", Status: engine.StatusRunning, Started: at.Add(17 * time.Hour),
		Owner: &engine.Owner{Host: "elsewhere", PID: 4242},
		Steps: []engine.StepResult{{Name: "promote", Kind: "run", Status: engine.StatusRunning, Started: at.Add(17 * time.Hour)}}}

	// An export: a record per run, zipped (as the GUI downloads it), and
	// the same runs in one file.
	per, _ := LogFiles([]*engine.Run{done, going}, "both", false, "")
	zipped, _ := Zip(per)
	one, _ := LogFiles([]*engine.Run{done, going}, "json", true, "")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "runs.zip"), zipped, 0o644)

	files, bad := ReadRunPaths([]string{dir})
	if len(files) != 2 || len(bad) != 0 {
		t.Fatalf("zip: %d runs, %+v", len(files), bad)
	}
	if fs, bad := ReadRuns("list.json", one[0].Data); len(fs) != 2 || len(bad) != 0 {
		t.Fatalf("list: %d runs, %+v", len(fs), bad)
	}
	for name, data := range map[string]string{"run.log": engine.RawLog(done), "x.json": `{"id": "../x", "runbook": "r", "started": "2026-10-20T16:00:00Z"}`, "y.txt": "hello"} {
		if fs, bad := ReadRuns(name, []byte(data)); len(fs) != 0 || len(bad) != 1 {
			t.Errorf("%s: %d runs, %+v", name, len(fs), bad)
		}
	}
	if _, bad := ReadRuns("run.log", []byte(engine.RawLog(done))); !strings.Contains(bad[0].Error, "raw log") {
		t.Errorf("raw log: %s", bad[0].Error)
	}

	st, _ := store.Open(t.TempDir(), "")
	check, err := ImportRuns(st, files, nil, true)
	if err != nil || len(check) != 2 || check[0].Imported || !check[1].Unfinished {
		t.Fatalf("check: %v %+v", err, check)
	}
	if _, err := st.LoadRun(done.ID); err == nil {
		t.Fatal("a check imported")
	}
	got, err := ImportRuns(st, files, nil, false)
	if err != nil || len(got) != 2 || !got[0].Imported || !got[1].Imported {
		t.Fatalf("import: %v %+v", err, got)
	}
	r, _ := st.LoadRun(done.ID)
	if r.Status != engine.StatusSucceeded || r.Inputs["target"] != "replica-b" || len(r.Notes) != 1 || r.Imported.IsZero() {
		t.Fatalf("done: %+v", r)
	}
	g, _ := st.LoadRun(going.ID)
	if g.Status != engine.StatusAborted || g.Steps[0].Status != engine.StatusAborted || g.Owner != nil || g.Error == "" {
		t.Fatalf("going: %+v", g)
	}
	if runs, _ := st.FindRuns("orders-db-cutover", "", 10); len(runs) != 2 {
		t.Fatalf("history has %d", len(runs))
	}
	// Again: they're here already, and left as they are.
	again, _ := ImportRuns(st, files, []string{done.ID}, false)
	if len(again) != 1 || !again[0].Exists || again[0].Imported {
		t.Fatalf("again: %+v", again)
	}
}
