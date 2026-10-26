package store

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/egrif/runnr/internal/engine"
)

// Scrubbing: rewriting stored runs with credentials masked (what was
// recorded before runnr knew to hide it).

// ErrRunning is a run that's still going: its process is writing it.
var ErrRunning = errors.New("still running")

// RunIDs is every run's id, oldest first.
func (s *Store) RunIDs() ([]string, error) {
	rows, err := s.db.Query(`SELECT id FROM runs ORDER BY started_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ScrubResult is what scrubbing a run changed (or would).
type ScrubResult struct {
	Record bool // the run's record
	Notes  int  // notes kept beside it
	Asks   int  // abort and suspend requests' reasons
}

// Changed says whether anything was (or would be) masked.
func (r ScrubResult) Changed() bool { return r.Record || r.Notes+r.Asks > 0 }

// ScrubRun rewrites run id's record and the text kept beside it (its
// notes, and the reasons of abort and suspend requests) with what scrub
// gives for the record: the record to keep, and how to mask text; with
// dry, it only says what that would change. A run that's still going is
// left alone (ErrRunning).
func (s *Store) ScrubRun(id string, scrub func(*engine.Run) (*engine.Run, func(string) string), dry bool) (ScrubResult, error) {
	var res ScrubResult
	r, err := s.loadRunRecord(id)
	if err != nil {
		return res, err
	}
	if r.Status == engine.StatusRunning {
		return res, fmt.Errorf("run %s: %w", id, ErrRunning)
	}
	before, _ := json.Marshal(r)
	x, mask := scrub(r)
	after, _ := json.Marshal(x)
	if string(before) != string(after) {
		res.Record = true
		if !dry {
			if err := s.SaveRun(x); err != nil {
				return res, err
			}
		}
	}

	// Notes: read first (SQLite has one connection), then rewrite.
	type note struct{ created, author, text string }
	var notes []note
	rows, err := s.db.Query(s.q(`SELECT created_at, author, text FROM run_notes WHERE run_id = ?`), id)
	if err != nil {
		return res, err
	}
	for rows.Next() {
		var n note
		if err := rows.Scan(&n.created, &n.author, &n.text); err != nil {
			rows.Close()
			return res, err
		}
		notes = append(notes, n)
	}
	rows.Close()
	for _, n := range notes {
		if m := mask(n.text); m != n.text {
			res.Notes++
			if !dry {
				if _, err := s.db.Exec(s.q(`UPDATE run_notes SET text = ? WHERE run_id = ? AND created_at = ? AND author = ? AND text = ?`), m, id, n.created, n.author, n.text); err != nil {
					return res, err
				}
			}
		}
	}

	for _, table := range []string{"run_aborts", "run_suspends"} {
		var reason string
		err := s.db.QueryRow(s.q(`SELECT reason FROM `+table+` WHERE run_id = ?`), id).Scan(&reason)
		if err != nil {
			continue // none
		}
		if m := mask(reason); m != reason {
			res.Asks++
			if !dry {
				if _, err := s.db.Exec(s.q(`UPDATE `+table+` SET reason = ? WHERE run_id = ?`), m, id); err != nil {
					return res, err
				}
			}
		}
	}
	return res, nil
}

// Compact rewrites the database so text that was replaced isn't left in
// its free space: SQLite's file is rebuilt (VACUUM, after emptying the
// write-ahead log); PostgreSQL's tables are vacuumed (which frees the old
// rows for reuse, though their bytes stay on disk until overwritten).
func (s *Store) Compact() error {
	if s.postgres {
		_, err := s.db.Exec(`VACUUM runs, run_notes, run_aborts, run_suspends`)
		return err
	}
	if _, err := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`VACUUM`); err != nil {
		return err
	}
	_, err := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	return err
}
