package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egrif/runnr/internal/engine"
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
		for _, tbl := range []string{"runbooks", "runbook_versions", "runs"} {
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
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			_, v, err := s.Save([]byte(rbA), SaveOpts{})
			if err != nil || v != 1 {
				t.Fatalf("create: v%d %v", v, err)
			}
			if _, _, err := s.Save([]byte(rbA), SaveOpts{}); err == nil {
				t.Fatal("duplicate create should fail")
			}
			_, v, err = s.Save([]byte(rbA+"description: two\n"), SaveOpts{OldName: "a", BaseVersion: 1})
			if err != nil || v != 2 {
				t.Fatalf("update: v%d %v", v, err)
			}
			if _, _, err := s.Save([]byte(rbA), SaveOpts{OldName: "a", BaseVersion: 1}); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale update: %v", err)
			}

			// Rename keeps the old name's history and frees the name.
			if _, _, err := s.Save([]byte("name: b\nsteps: [{name: s, run: 'true'}]\n"), SaveOpts{OldName: "a"}); err != nil {
				t.Fatal(err)
			}
			if s.Exists("a") || !s.Exists("b") {
				t.Fatal("rename")
			}
			if raw, err := s.RawVersion("a", 2); err != nil || string(raw) != rbA+"description: two\n" {
				t.Fatalf("old version: %q %v", raw, err)
			}

			// Re-creating a deleted name continues its version numbers.
			if _, v, _ := s.Save([]byte(rbA), SaveOpts{}); v != 3 {
				t.Fatalf("recreate version %d", v)
			}
			if err := s.Delete("a"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Load("a"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("deleted: %v", err)
			}
			entries, _ := s.List()
			if len(entries) != 1 || entries[0].Name != "b" || entries[0].Steps != 1 {
				t.Fatalf("list %+v", entries)
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
