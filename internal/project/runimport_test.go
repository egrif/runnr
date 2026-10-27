package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/store"
	"github.com/egrif/runnr/internal/ver"
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
	if files[0].Run.FromLog || files[1].Run.FromLog {
		t.Fatal("used the raw log where the record was there too")
	}
	for name, data := range map[string]string{"x.json": `{"id": "../x", "runbook": "r", "started": "2026-10-20T16:00:00Z"}`, "y.txt": "hello", "z.log": "run x · y\n"} {
		if fs, bad := ReadRuns(name, []byte(data)); len(fs) != 0 || len(bad) != 1 {
			t.Errorf("%s: %d runs, %+v", name, len(fs), bad)
		}
	}
	// A raw log alone is rebuilt.
	logs, _ := LogFiles([]*engine.Run{done, going}, "raw", true, "")
	fromLog, bad := ReadRuns("both.log", logs[0].Data)
	if len(fromLog) != 2 || len(bad) != 0 || !fromLog[0].Run.FromLog || fromLog[0].Run.Inputs["target"] != "replica-b" || fromLog[0].Run.Steps[0].Name != "promote" {
		t.Fatalf("raw log: %+v %+v", fromLog, bad)
	}
	if st2, _ := store.Open(t.TempDir(), ""); true {
		got, err := ImportRuns(st2, fromLog, nil, false)
		if err != nil || len(got) != 2 || !got[0].FromLog || !got[0].Imported {
			t.Fatalf("raw import: %v %+v", err, got)
		}
		if r, _ := st2.LoadRun(done.ID); r == nil || !r.FromLog || len(r.Notes) != 1 {
			t.Fatalf("raw run: %+v", r)
		}
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

// An exported run carries the runbook and library it ran; importing it
// into another runnr keeps them, so that runnr knows exactly what ran.
func TestRunBringsItsDefinitions(t *testing.T) {
	from, _ := store.Open(t.TempDir(), "")
	to, _ := store.Open(t.TempDir(), "")
	lib := "name: helpers\nfunctions:\n  hi: {body: 'echo hi'}\n"
	rb := "name: deploy\nlibraries: [helpers, std]\nsteps: [{name: a, run: hi}]\n"
	from.SaveLibrary([]byte(lib), store.SaveOpts{})
	_, v, _ := from.Save([]byte(rb), store.SaveOpts{})
	run := &engine.Run{ID: "r1", Runbook: "deploy", Status: engine.StatusSucceeded, Started: time.Now(), RunbookVersion: v,
		Libraries: from.LibrariesUsed([]string{"helpers", "std"}), Inputs: map[string]string{}}
	// The runbook moved on after the run: the export still has what ran.
	from.Save([]byte(rb+"description: later\n"), store.SaveOpts{OldName: "deploy"})

	b, err := json.Marshal(WithDefinitions(from, []*engine.Run{run})[0])
	if err != nil {
		t.Fatal(err)
	}
	files, bad := ReadRuns("r1.json", b)
	if len(bad) > 0 {
		t.Fatal(bad)
	}
	if _, err := ImportRuns(to, files, nil, false); err != nil {
		t.Fatal(err)
	}
	if d, err := to.Load("deploy"); err != nil || d.Version != v {
		t.Fatalf("runbook: %v %v", d, err)
	}
	if d, err := to.LoadLibrary("helpers"); err != nil || d.Version != ver.Of([]byte(lib)) {
		t.Fatalf("library: %v %v", d, err)
	}
	if r, _ := to.LoadRun("r1"); r.Definitions != nil || r.RunbookVersion != v || !to.HasVersion("deploy", r.RunbookVersion) {
		t.Fatalf("record: %+v", r)
	}
}
