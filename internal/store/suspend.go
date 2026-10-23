package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/egrif/runnr/internal/engine"
)

func init() {
	// Suspend requests, like abort requests, live outside the run record so
	// any runnr can make one while another process runs it.
	schema = append(schema, `CREATE TABLE IF NOT EXISTS run_suspends (
		run_id       TEXT PRIMARY KEY,
		requested_at TEXT NOT NULL,
		author       TEXT NOT NULL,
		reason       TEXT NOT NULL,
		immediate    INTEGER NOT NULL DEFAULT 0
	)`)
}

// RequestSuspend asks whichever runnr is running a run to suspend it.
func (s *Store) RequestSuspend(runID string, e *engine.SuspendError) error {
	now := 0
	if e.Now {
		now = 1
	}
	_, err := s.db.Exec(s.q(`INSERT INTO run_suspends (run_id, requested_at, author, reason, immediate) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (run_id) DO UPDATE SET requested_at = excluded.requested_at, author = excluded.author, reason = excluded.reason, immediate = excluded.immediate`),
		runID, ts(time.Now()), e.By, e.Reason, now)
	return err
}

// takeSuspend returns (and clears) the request to suspend a run, if any.
func (s *Store) takeSuspend(runID string) (*engine.SuspendError, error) {
	var e engine.SuspendError
	var at string
	var now int
	err := s.db.QueryRow(s.q(`SELECT requested_at, author, reason, immediate FROM run_suspends WHERE run_id = ?`), runID).Scan(&at, &e.By, &e.Reason, &now)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e.At, e.Now = parseTS(at), now != 0
	_, err = s.db.Exec(s.q(`DELETE FROM run_suspends WHERE run_id = ?`), runID)
	return &e, err
}

// SuspendResult says what suspending a run did.
type SuspendResult struct {
	Suspended bool   `json:"suspended"` // the run is now suspended
	Message   string `json:"message"`
}

// SuspendRun suspends a run that isn't running in this process: it asks
// the runnr running it to, and waits up to wait for it. A run whose
// process is gone (it ran on this host and has exited) is marked
// suspended straight away.
func (s *Store) SuspendRun(runID string, e *engine.SuspendError, wait time.Duration) (SuspendResult, error) {
	r, err := s.LoadRun(runID)
	if err != nil {
		return SuspendResult{}, err
	}
	if r.Status != engine.StatusRunning {
		return SuspendResult{}, fmt.Errorf("run %s isn't running (it %s)", runID, r.Status)
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	host, _ := os.Hostname()
	if o := r.Owner; o != nil && o.Host == host && !processAlive(o.PID) {
		return s.markSuspended(r, e, fmt.Sprintf("its runnr process (pid %d) is gone, so it's marked suspended", o.PID))
	}
	if err := s.RequestSuspend(runID, e); err != nil {
		return SuspendResult{}, err
	}
	for deadline := time.Now().Add(wait); time.Now().Before(deadline); {
		time.Sleep(250 * time.Millisecond)
		if r, err = s.LoadRun(runID); err != nil {
			return SuspendResult{}, err
		}
		if r.Status != engine.StatusRunning {
			if r.Status == engine.StatusSuspended {
				return SuspendResult{Suspended: true, Message: "it stopped"}, nil
			}
			return SuspendResult{Message: "it ended first: " + string(r.Status)}, nil
		}
	}
	msg := "asked to suspend it: it will once the step running now finishes"
	for i, st := range r.Steps {
		if st.Status == engine.StatusRunning {
			msg = fmt.Sprintf("asked to suspend it: it will once step %d (%s) finishes", i+1, st.Name)
			break
		}
	}
	return SuspendResult{Message: msg}, nil
}

// markSuspended records a run as suspended without the process that ran it.
func (s *Store) markSuspended(loaded *engine.Run, why *engine.SuspendError, msg string) (SuspendResult, error) {
	r, err := s.loadRunRecord(loaded.ID)
	if err != nil {
		return SuspendResult{}, err
	}
	r.Status, r.Error, r.Ended = engine.StatusSuspended, why.Error(), time.Now()
	for i := range r.Steps {
		if r.Steps[i].Status == engine.StatusRunning {
			r.Steps[i].Status, r.Steps[i].Error = engine.StatusSuspended, r.Error
			if r.Steps[i].Ended.IsZero() {
				r.Steps[i].Ended = r.Ended
			}
		}
	}
	return SuspendResult{Suspended: true, Message: msg}, s.SaveRun(r)
}
