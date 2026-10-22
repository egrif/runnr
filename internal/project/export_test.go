package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/store"
)

func TestExport(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	st.SaveLibrary([]byte("name: helpers\nfunctions:\n  hi: {body: 'echo hi'}\n"), store.SaveOpts{})
	raw := "name: rb   # kept\nlibraries: [helpers, std]\nsteps:\n  - {name: one, run: 'hi'}\n"
	st.Save([]byte(raw), store.SaveOpts{})
	dir := t.TempDir()
	plan, err := ExportPlan(st, "rb", dir)
	if err != nil || len(plan) != 3 || plan[0].Path != filepath.Join(dir, "rb.yaml") || !plan[2].Builtin {
		t.Fatalf("%v %+v", err, plan)
	}
	if _, err := Export(st, "rb", dir, []string{"helpers"}, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "rb.yaml")); string(b) != raw {
		t.Fatalf("runbook %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "std.yaml")); err == nil {
		t.Fatal("wrote a library left out")
	}
	if _, err := Export(st, "rb", dir, []string{"helpers"}, false); err == nil || !strings.Contains(err.Error(), "already there") {
		t.Fatalf("overwrote: %v", err)
	}
	if _, err := Export(st, "rb", dir, nil, true); err != nil {
		t.Fatal(err)
	}
	// The folder imports again.
	st2, _ := store.Open(t.TempDir(), "")
	p, _ := Scan(Options{Dir: dir, Lookup: StoreLookup(st2)})
	if res := Apply(p, func(Doc) bool { return false }, StoreSaver(st2)); statuses(res) != "imported,imported" {
		t.Fatalf("%v", res)
	}
}

// A run's chain (what it resumed, what resumed it), exported and imported
// elsewhere.
func TestLogsChainAndImport(t *testing.T) {
	st, _ := store.Open(t.TempDir(), "")
	at := time.Now()
	for i, r := range []engine.Run{{ID: "r1", Status: engine.StatusFailed}, {ID: "r2", Resumes: "r1", Status: engine.StatusFailed}, {ID: "r3", Resumes: "r2", Status: engine.StatusSucceeded}, {ID: "other", Status: engine.StatusSucceeded}} {
		r.Runbook, r.Started = "rb", at.Add(time.Duration(i)*time.Minute)
		r.Steps = []engine.StepResult{{Name: "one", Status: r.Status}}
		st.SaveRun(&r)
	}
	chain, err := Chain(st, "r2")
	if err != nil || len(chain) != 3 || chain[0].ID != "r1" || chain[2].ID != "r3" {
		t.Fatalf("%v %d", err, len(chain))
	}
	files, _ := LogFiles(chain, "both", true, "x")
	if len(files) != 2 || !strings.Contains(string(files[0].Data), "3 of 3: run r3") {
		t.Fatalf("%v", files)
	}
	var list []engine.Run
	if json.Unmarshal(files[1].Data, &list) != nil || len(list) != 3 {
		t.Fatal("records")
	}
	dir := t.TempDir()
	if _, err := WriteLogFiles(files, dir, false); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteLogFiles(files, dir, false); err == nil {
		t.Fatal("wrote over files")
	}
	st2, _ := store.Open(t.TempDir(), "")
	p, _ := Scan(Options{Dir: dir, Lookup: StoreLookup(st2)})
	if res := Apply(p, func(Doc) bool { return false }, StoreSaver(st2)); statuses(res) != "imported,imported,imported" {
		t.Fatalf("%v", res)
	}
	p, _ = Scan(Options{Dir: dir, Lookup: StoreLookup(st2)})
	if res := Apply(p, func(Doc) bool { return false }, StoreSaver(st2)); statuses(res) != "unchanged,unchanged,unchanged" {
		t.Fatalf("again %v", res)
	}
}

// Timelines export as a page per run, beside the other formats; a folder
// with them still imports.
func TestLogsWithTimeline(t *testing.T) {
	at := time.Now()
	var runs []*engine.Run
	for i, id := range []string{"r1", "r2"} {
		runs = append(runs, &engine.Run{ID: id, Runbook: "rb", Status: engine.StatusSucceeded, Started: at.Add(time.Duration(i) * time.Minute), Ended: at.Add(time.Duration(i)*time.Minute + time.Second),
			Steps: []engine.StepResult{{Name: "one", Status: engine.StatusSucceeded, Started: at, Ended: at.Add(time.Second)}}})
	}
	files, err := LogFiles(runs, "json,timeline", true, "x")
	if err != nil || len(files) != 3 || files[0].Name != "run-r1.timeline.html" || !strings.Contains(string(files[1].Data), "<h2>The whole run</h2>") || files[2].Name != "x.json" {
		t.Fatalf("%v %v", err, files)
	}
	if _, err := LogFiles(runs, "raw,gantt", false, ""); err == nil {
		t.Fatal("unknown format taken")
	}
	dir := t.TempDir()
	WriteLogFiles(files, dir, false)
	st, _ := store.Open(t.TempDir(), "")
	p, _ := Scan(Options{Dir: dir, Lookup: StoreLookup(st)})
	if res := Apply(p, func(Doc) bool { return false }, StoreSaver(st)); statuses(res) != "imported,imported" {
		t.Fatalf("%v", res)
	}
}
