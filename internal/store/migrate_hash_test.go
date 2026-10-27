package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egrif/runnr/internal/ver"
)

// A database from when versions were numbers opens converted: versions are
// hashes, text saved twice is one version, and runs name the hash of what
// they ran.
func TestMigrateNumberedVersions(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "runnr.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	one, two := rbA, rbA+"description: two\n"
	for _, q := range []string{
		`CREATE TABLE runbooks (name TEXT PRIMARY KEY, raw TEXT NOT NULL, version INTEGER NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE runbook_versions (name TEXT NOT NULL, version INTEGER NOT NULL, raw TEXT NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY (name, version))`,
		`CREATE TABLE libraries (name TEXT PRIMARY KEY, raw TEXT NOT NULL, version INTEGER NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE library_versions (name TEXT NOT NULL, version INTEGER NOT NULL, raw TEXT NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY (name, version))`,
		`CREATE TABLE runs (id TEXT PRIMARY KEY, runbook TEXT NOT NULL, status TEXT NOT NULL, started_at TEXT NOT NULL, record TEXT NOT NULL)`,
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`INSERT INTO meta VALUES ('legacy_import', 'done')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for v, raw := range []string{one, two, one} { // back to the first text at version 3
		if _, err := db.Exec(`INSERT INTO runbook_versions VALUES ('a', ?, ?, ?)`, v+1, raw, "2026-01-0"+string(rune('1'+v))+"T00:00:00.000000Z"); err != nil {
			t.Fatal(err)
		}
	}
	db.Exec(`INSERT INTO runbooks VALUES ('a', ?, 3, '2026-01-03T00:00:00.000000Z')`, one)
	db.Exec(`INSERT INTO runs VALUES ('r1', 'a', 'failed', '2026-01-02T00:00:00.000000Z', ?)`,
		`{"id":"r1","runbook":"a","status":"failed","started":"2026-01-02T00:00:00Z","runbook_version":2,"edits_saved":3,"resumptions":[{"at":"2026-01-03T00:00:00Z","from":0,"status":"failed","runbook_version":3,"was_version":2}],"steps":[]}`)
	db.Exec(`INSERT INTO runs VALUES ('r2', 'a', 'failed', '2026-01-02T00:00:00.000000Z', ?)`,
		`{"id":"r2","runbook":"a","status":"failed","started":"2026-01-02T00:00:00Z","runbook_version":9,"steps":[]}`)
	db.Close()

	s, err := Open(home, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if backups, _ := filepath.Glob(path + ".before-hashes-*"); len(backups) != 1 {
		t.Fatalf("backup: %v", backups)
	}
	vs, err := s.Versions("a")
	if err != nil || len(vs) != 2 {
		t.Fatalf("versions %+v %v", vs, err)
	}
	d, _ := s.Load("a")
	if d.Version != ver.Of([]byte(one)) {
		t.Fatalf("current %s", d.Version)
	}
	r1, err := s.LoadRun("r1")
	if err != nil {
		t.Fatal(err)
	}
	if r1.RunbookVersion != ver.Of([]byte(two)) || r1.EditsSaved != ver.Of([]byte(one)) ||
		r1.Resumptions[0].RunbookVersion != ver.Of([]byte(one)) || r1.Resumptions[0].Was != ver.Of([]byte(two)) {
		t.Fatalf("run r1: %+v", r1)
	}
	// Version 9 was never saved here (an imported run): not known.
	if r2, _ := s.LoadRun("r2"); r2.RunbookVersion != "" {
		t.Fatalf("run r2: %q", r2.RunbookVersion)
	}
	// Saving works on the converted tables, and opening again changes nothing.
	if _, v, err := s.Save([]byte(two), SaveOpts{OldName: "a"}); err != nil || v != ver.Of([]byte(two)) {
		t.Fatalf("save: %s %v", v, err)
	}
	s.Close()
	s2, err := Open(home, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if backups, _ := filepath.Glob(path + ".before-hashes-*"); len(backups) != 1 {
		t.Fatalf("converted twice: %v", backups)
	}
	if raw, _ := s2.Raw("a"); !strings.Contains(string(raw), "two") {
		t.Fatalf("raw %q", raw)
	}
}
