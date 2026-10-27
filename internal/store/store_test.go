package store

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/ver"
)

// stores returns a fresh SQLite store, plus Postgres when
// RUNNR_TEST_POSTGRES is set to a URL of a database we may wipe.
func stores(t *testing.T) map[string]*Store {
	t.Helper()
	out := map[string]*Store{}
	sq, err := Open(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sq.Close() })
	out["sqlite"] = sq
	if dsn := os.Getenv("RUNNR_TEST_POSTGRES"); dsn != "" {
		pg, err := Open("", dsn)
		if err != nil {
			t.Fatal(err)
		}
		for _, tbl := range []string{"runbooks", "runbook_versions", "runs", "collections", "collection_versions", "schedules", "run_aborts", "run_suspends"} {
			if _, err := pg.db.Exec("DELETE FROM " + tbl); err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() { pg.Close() })
		out["postgres"] = pg
	}
	return out
}

const rbA = "name: a\nsteps: [{name: s, run: 'true'}]\n"

func TestRunbooksAndVersions(t *testing.T) {
	two := rbA + "description: two\n"
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			_, v, err := s.Save([]byte(rbA), SaveOpts{})
			if err != nil || v != ver.Of([]byte(rbA)) {
				t.Fatalf("create: %s %v", v, err)
			}
			if _, _, err := s.Save([]byte(rbA), SaveOpts{}); err == nil {
				t.Fatal("duplicate create should fail")
			}
			v1 := v
			_, v, err = s.Save([]byte(two), SaveOpts{OldName: "a", Base: v1})
			if err != nil || v != ver.Of([]byte(two)) {
				t.Fatalf("update: %s %v", v, err)
			}
			if _, _, err := s.Save([]byte(rbA), SaveOpts{OldName: "a", Base: v1}); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale update: %v", err)
			}
			// Saving text it had before makes that version current again: no
			// new version.
			if _, v, err = s.Save([]byte(rbA), SaveOpts{OldName: "a", Base: ver.Of([]byte(two))}); err != nil || v != v1 {
				t.Fatalf("back to the first: %s %v", v, err)
			}
			if vs, err := s.Versions("a"); err != nil || len(vs) != 2 || !slices.ContainsFunc(vs, func(x Version) bool { return x.Version == v1 && x.Current }) {
				t.Fatalf("versions %+v %v", vs, err)
			}

			// Rename keeps the old name's history and frees the name.
			if _, _, err := s.Save([]byte("name: b\nsteps: [{name: s, run: 'true'}]\n"), SaveOpts{OldName: "a"}); err != nil {
				t.Fatal(err)
			}
			if s.Exists("a") || !s.Exists("b") {
				t.Fatal("rename")
			}
			short := ver.Of([]byte(two)).Short()
			if raw, id, err := s.RawVersion("a", short); err != nil || string(raw) != two || id != ver.Of([]byte(two)) {
				t.Fatalf("old version by its short form: %q %v", raw, err)
			}
			if _, _, err := s.RawVersion("a", "ffffffff"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("no such version: %v", err)
			}

			// Re-creating a deleted name with its old text is that version.
			if _, v, _ := s.Save([]byte(rbA), SaveOpts{}); v != v1 {
				t.Fatalf("recreate version %s", v)
			}
			if err := s.Delete("a"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Load("a"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("deleted: %v", err)
			}
		})
	}
}

func TestRuns(t *testing.T) {
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			old := &engine.Run{ID: "1", Runbook: "x", Status: engine.StatusSucceeded, Started: time.Now().Add(-48 * time.Hour)}
			live := &engine.Run{ID: "2", Runbook: "y", Status: engine.StatusRunning, Started: time.Now().Add(-48 * time.Hour)}
			recent := &engine.Run{ID: "3", Runbook: "x", Status: engine.StatusRunning, Started: time.Now(), Steps: []engine.StepResult{{Name: "s"}}}
			for _, r := range []*engine.Run{old, live, recent} {
				if err := s.SaveRun(r); err != nil {
					t.Fatal(err)
				}
			}
			recent.Status = engine.StatusFailed
			if err := s.SaveRun(recent); err != nil {
				t.Fatal(err)
			}
			got, err := s.LoadRun("3")
			if err != nil || got.Status != engine.StatusFailed || len(got.Steps) != 1 {
				t.Fatalf("load %+v %v", got, err)
			}
			list, _ := s.ListRuns("x", 10)
			if len(list) != 2 || list[0].ID != "3" || list[0].Steps != nil {
				t.Fatalf("list %+v", list)
			}
			n, err := s.PruneRuns(time.Now().Add(-24 * time.Hour))
			if err != nil || n != 1 {
				t.Fatalf("prune %d %v (running runs must survive)", n, err)
			}
		})
	}
}

func TestLegacyImport(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "runbooks"), 0o755)
	os.MkdirAll(filepath.Join(home, "runs"), 0o755)
	os.WriteFile(filepath.Join(home, "runbooks", "a.yaml"), []byte(rbA), 0o644)
	os.WriteFile(filepath.Join(home, "runs", "r1.json"), []byte(`{"id":"r1","runbook":"a","status":"succeeded","started":"2026-01-01T00:00:00Z"}`), 0o644)

	s, err := Open(home, "")
	if err != nil {
		t.Fatal(err)
	}
	if !s.Exists("a") {
		t.Fatal("runbook not imported")
	}
	if _, err := s.LoadRun("r1"); err != nil {
		t.Fatal(err)
	}
	s.Delete("a")
	s.Close()
	// Only once: a deleted runbook must not come back on the next open.
	s, _ = Open(home, "")
	defer s.Close()
	if s.Exists("a") {
		t.Fatal("imported twice")
	}
}

func TestRedact(t *testing.T) {
	if got := redact("postgres://u:pw@h:5432/db"); got != "postgres://u:****@h:5432/db" {
		t.Fatal(got)
	}
}

func TestNotes(t *testing.T) {
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			s.db.Exec("DELETE FROM run_notes")
			r := &engine.Run{ID: "n1", Runbook: "x", Status: engine.StatusRunning, Started: time.Now(), Steps: []engine.StepResult{{Name: "a"}, {Name: "b"}}}
			s.SaveRun(r)
			if _, err := s.AddNote("n1", 1, "eric", "checked the dashboard"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AddNote("n1", -1, "eric", "window started late"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AddNote("n1", 2, "eric", "x"); err == nil {
				t.Fatal("out-of-range step should fail")
			}
			// The engine keeps rewriting the record; notes must survive that.
			r.Status = engine.StatusSucceeded
			s.SaveRun(r)
			got, err := s.LoadRun("n1")
			if err != nil || len(got.Steps[1].Notes) != 1 || got.Steps[1].Notes[0].Text != "checked the dashboard" || len(got.Notes) != 1 {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}

func TestLibraries(t *testing.T) {
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			s.db.Exec("DELETE FROM libraries")
			s.db.Exec("DELETE FROM library_versions")
			lib := "name: helpers\nfunctions:\n  greet: {body: 'echo hi'}\n"
			if _, v, err := s.SaveLibrary([]byte(lib), SaveOpts{}); err != nil || v != ver.Of([]byte(lib)) {
				t.Fatalf("%s %v", v, err)
			}
			if _, _, err := s.SaveLibrary([]byte(lib), SaveOpts{OldName: "helpers", Base: "0000"}); !errors.Is(err, ErrConflict) {
				t.Fatalf("conflict: %v", err)
			}
			libs, err := s.Libraries([]string{"helpers"})
			if err != nil || libs[0].Functions["greet"].Body != "echo hi" {
				t.Fatalf("%v %v", libs, err)
			}
			if _, err := s.Libraries([]string{"missing"}); !errors.Is(err, ErrNotFound) {
				t.Fatalf("missing: %v", err)
			}
			if l, _ := s.ListLibraries(); len(l) != 2 || l[0].Functions[0] != "greet" || l[1].Name != "std" || !l[1].Builtin || len(l[1].Functions) == 0 {
				t.Fatalf("list %+v", l)
			}
			if libs, err := s.Libraries([]string{"std"}); err != nil || libs[0].Functions["tf_plan"].Body == "" {
				t.Fatalf("std: %v", err)
			}
		})
	}
}

func TestCollectionsAndTestValues(t *testing.T) {
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			s.db.Exec("DELETE FROM collections")
			s.db.Exec("DELETE FROM collection_versions")
			s.db.Exec("DELETE FROM runbook_test_values")
			if _, v, err := s.SaveCollection([]byte("name: mig\ndescription: d\nrunbooks: [a, b]\n"), SaveOpts{}); err != nil || v != ver.Of([]byte("name: mig\ndescription: d\nrunbooks: [a, b]\n")) {
				t.Fatalf("%s %v", v, err)
			}
			if _, _, err := s.SaveCollection([]byte("name: mig\nrunbooks: [a, a]\n"), SaveOpts{OldName: "mig"}); err == nil {
				t.Fatal("duplicate runbook should be rejected")
			}
			cs, _ := s.ListCollections()
			if len(cs) != 1 || cs[0].Collection == nil || len(cs[0].Collection.Runbooks) != 2 {
				t.Fatalf("%+v", cs)
			}
			tv, _ := s.GetTestValues("x")
			if !tv.Setup {
				t.Fatal("setup should default to on")
			}
			tv.Vars["env"] = "staging"
			tv.Setup = false
			if err := s.SetTestValues("x", tv); err != nil {
				t.Fatal(err)
			}
			if got, _ := s.GetTestValues("x"); got.Vars["env"] != "staging" || got.Setup {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestMoveRunNotes(t *testing.T) {
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			s.db.Exec("DELETE FROM run_notes")
			r := &engine.Run{ID: "m1", Runbook: "x", Status: engine.StatusFailed, Started: time.Now(), Steps: []engine.StepResult{{Name: "a"}, {Name: "b"}, {Name: "c"}}}
			s.SaveRun(r)
			var first engine.Note
			for i, txt := range []string{"on a", "on b", "on c"} {
				n, err := s.AddNote("m1", i, "pat", txt)
				if err != nil {
					t.Fatal(err)
				}
				if i == 0 {
					first = n
				}
			}
			// The runbook changed: a stays, b is gone, c moves to 3 (after
			// a new step at 2).
			if err := s.MoveRunNotes("m1", map[int]int{0: 0, 1: -1, 2: 3}, map[int]string{1: "b"}); err != nil {
				t.Fatal(err)
			}
			r.Steps = []engine.StepResult{{Name: "a"}, {Name: "new"}, {Name: "x"}, {Name: "c"}}
			// A copy of a note already in the record isn't shown twice.
			r.Steps[0].Notes = []engine.Note{first}
			s.SaveRun(r)
			got, err := s.LoadRun("m1")
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Steps[0].Notes) != 1 || len(got.Steps[3].Notes) != 1 || got.Steps[3].Notes[0].Text != "on c" ||
				len(got.Notes) != 1 || got.Notes[0].Text != "(on step 2, b, no longer in the runbook) on b" {
				t.Fatalf("steps %+v, run notes %+v", got.Steps, got.Notes)
			}
		})
	}
}

func TestFindRuns(t *testing.T) {
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			s.db.Exec("DELETE FROM runs")
			s.db.Exec("DELETE FROM runbooks")
			s.db.Exec("DELETE FROM runbook_versions")
			if _, _, err := s.Save([]byte("name: cut\ninputs:\n  - {name: silo, identifier: true}\n  - {name: why}\nsteps: [{name: a, run: 'true'}]\n"), SaveOpts{}); err != nil {
				t.Fatal(err)
			}
			at := time.Now()
			for i, x := range []struct {
				id, silo, why string
				marked        bool
			}{
				{"r1", "silo_3", "routine", true}, {"r2", "silo-12", "silo_3 was slow", true}, {"r3", "SILO_3", "", false}, // from before it was marked
			} {
				r := &engine.Run{ID: x.id, Runbook: "cut", Status: engine.StatusSucceeded, Started: at.Add(time.Duration(i) * time.Minute), Inputs: map[string]string{"silo": x.silo, "why": x.why}}
				if x.marked {
					r.Identifiers = []string{"silo"}
				}
				s.SaveRun(r)
			}
			s.SaveRun(&engine.Run{ID: "other", Runbook: "else", Status: engine.StatusSucceeded, Started: at, Inputs: map[string]string{"silo": "silo_3"}})
			ids := func(runs []engine.Run) string {
				var out []string
				for _, r := range runs {
					out = append(out, r.ID+":"+strings.Join(engine.RunIdentifiers(&r, nil), ","))
				}
				return strings.Join(out, " ")
			}
			// Identifier values only (not other inputs, not runbooks that don't mark them), any case.
			if runs, err := s.FindRuns("", "silo_3", 10); err != nil || ids(runs) != "r3:silo=SILO_3 r1:silo=silo_3" {
				t.Fatalf("%v %s", err, ids(runs))
			}
			if runs, _ := s.FindRuns("cut", "", 2); ids(runs) != "r3:silo=SILO_3 r2:silo=silo-12" {
				t.Fatalf("%s", ids(runs))
			}
			if runs, _ := s.FindRuns("", "r2", 10); ids(runs) != "r2:silo=silo-12" {
				t.Fatalf("by id: %s", ids(runs))
			}
		})
	}
}
