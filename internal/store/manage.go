package store

import (
	"fmt"
	"slices"
	"time"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/ver"
)

// Deleting keeps a document's versions, so a deleted runbook, library,
// fake library, test suite or collection can be restored: its last
// version is saved again.

// Kinds of stored document, by the names the import and manage pages use.
const (
	KindRunbook    = "runbook"
	KindLibrary    = "library"
	KindFakes      = "fakes"
	KindSuite      = "suite"
	KindCollection = "collection"
)

// Deleted is a document that was deleted: its last version is kept.
type Deleted struct {
	Kind    string    `json:"kind"`
	Name    string    `json:"name"`
	Version ver.ID    `json:"version"`
	SavedAt time.Time `json:"saved_at"`
}

func tables(kind string) (table, versions string, err error) {
	switch kind {
	case KindRunbook:
		return runbookKind.table, runbookKind.versions, nil
	case KindLibrary:
		return libraryKind.table, libraryKind.versions, nil
	case KindFakes:
		return fakesKind.table, fakesKind.versions, nil
	case KindSuite:
		return suiteKind.table, suiteKind.versions, nil
	case KindCollection:
		return collectionKind.table, collectionKind.versions, nil
	}
	return "", "", fmt.Errorf("unknown kind %q", kind)
}

// ListDeleted lists the deleted documents of a kind, each with its last
// saved version.
func (s *Store) ListDeleted(kind string) ([]Deleted, error) {
	table, versions, err := tables(kind)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT v.name, v.hash, v.created_at FROM ` + versions + ` v
		WHERE v.name NOT IN (SELECT name FROM ` + table + `) ORDER BY v.name, v.created_at DESC, v.hash`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Deleted{}
	for rows.Next() {
		d := Deleted{Kind: kind}
		var at string
		if err := rows.Scan(&d.Name, &d.Version, &at); err != nil {
			return nil, err
		}
		if len(out) > 0 && out[len(out)-1].Name == d.Name {
			continue
		}
		d.SavedAt = parseTS(at)
		out = append(out, d)
	}
	return out, rows.Err()
}

// Restore makes a deleted document's last saved version current again,
// returning it.
func (s *Store) Restore(kind, name string) (ver.ID, error) {
	table, versions, err := tables(kind)
	if err != nil {
		return "", err
	}
	var n int
	if err := s.db.QueryRow(s.q(`SELECT COUNT(*) FROM `+table+` WHERE name = ?`), name).Scan(&n); err != nil {
		return "", err
	}
	if n > 0 {
		return "", fmt.Errorf("%s %q isn't deleted", kind, name)
	}
	var raw string
	err = s.db.QueryRow(s.q(`SELECT raw FROM `+versions+` WHERE name = ? ORDER BY created_at DESC, hash LIMIT 1`), name).Scan(&raw)
	if err != nil {
		return "", fmt.Errorf("%s %q: no saved version: %w", kind, name, ErrNotFound)
	}
	var v ver.ID
	switch kind {
	case KindRunbook:
		_, v, err = s.Save([]byte(raw), SaveOpts{})
	case KindLibrary:
		_, v, err = s.SaveLibrary([]byte(raw), SaveOpts{})
	case KindFakes:
		_, v, err = s.SaveFakes([]byte(raw), SaveOpts{})
	case KindSuite:
		_, v, err = s.SaveSuite([]byte(raw), SaveOpts{})
	case KindCollection:
		_, v, err = s.SaveCollection([]byte(raw), SaveOpts{})
	}
	return v, err
}

// DeleteDoc deletes a document of a kind (its versions are kept).
func (s *Store) DeleteDoc(kind, name string) error {
	switch kind {
	case KindRunbook:
		return s.Delete(name)
	case KindLibrary:
		return s.DeleteLibrary(name)
	case KindFakes:
		return s.DeleteFakes(name)
	case KindSuite:
		return s.DeleteSuite(name)
	case KindCollection:
		return s.DeleteCollection(name)
	}
	return fmt.Errorf("unknown kind %q", kind)
}

// DeleteRun deletes a run's record and notes. A running one can't be.
func (s *Store) DeleteRun(id string) error {
	r, err := s.LoadRun(id)
	if err != nil {
		return err
	}
	if r.Status == engine.StatusRunning {
		return fmt.Errorf("run %s is running: abort it first", id)
	}
	if _, err := s.db.Exec(s.q(`DELETE FROM runs WHERE id = ?`), id); err != nil {
		return err
	}
	_, err = s.db.Exec(s.q(`DELETE FROM run_notes WHERE run_id = ?`), id)
	return err
}

// DeleteRunbook deletes a runbook (its versions are kept), takes it out of
// the collections it's in and removes its scheduled runs (they couldn't
// run). It says what else it did.
func (s *Store) DeleteRunbook(name string) ([]string, error) {
	if err := s.Delete(name); err != nil {
		return nil, err
	}
	var also []string
	cols, _ := s.ListCollections()
	for _, c := range cols {
		if c.Collection != nil && slices.Contains(c.Collection.Runbooks, name) {
			if _, err := s.RemoveFromCollection(c.Name, name); err == nil {
				also = append(also, "taken out of collection "+c.Name)
			}
		}
	}
	scheds, _ := s.ListSchedules(time.Now())
	for _, sc := range scheds {
		if sc.Runbook == name {
			if err := s.DeleteSchedule(sc.ID); err == nil {
				also = append(also, "scheduled run "+sc.At.Local().Format("Jan 2 15:04")+" removed")
			}
		}
	}
	return also, nil
}

// MoveRunNotes follows a run's steps to their new places (moved: old index
// → new, -1 for a step no longer in the runbook: its notes go to the run,
// saying which step they were on).
func (s *Store) MoveRunNotes(runID string, moved map[int]int, names map[int]string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Out of the way first, so moves can't collide.
	if _, err := tx.Exec(s.q(`UPDATE run_notes SET step = -1000 - step WHERE run_id = ? AND step >= 0`), runID); err != nil {
		return err
	}
	for old, to := range moved {
		if to >= 0 {
			_, err = tx.Exec(s.q(`UPDATE run_notes SET step = ? WHERE run_id = ? AND step = ?`), to, runID, -1000-old)
		} else {
			_, err = tx.Exec(s.q(`UPDATE run_notes SET step = -1, text = ? || text WHERE run_id = ? AND step = ?`),
				fmt.Sprintf("(on step %d, %s, no longer in the runbook) ", old+1, names[old]), runID, -1000-old)
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
