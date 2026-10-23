package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/egrif/runnr/internal/engine"
)

func init() {
	// Abort requests live outside the run record, like notes, so any runnr
	// can make one while another process is writing the run.
	schema = append(schema, `CREATE TABLE IF NOT EXISTS run_aborts (
		run_id       TEXT PRIMARY KEY,
		requested_at TEXT NOT NULL,
		author       TEXT NOT NULL,
		reason       TEXT NOT NULL
	)`)
}

// RequestAbort asks whichever runnr is running a run to abort it.
func (s *Store) RequestAbort(runID, by, reason string) error {
	_, err := s.db.Exec(s.q(`INSERT INTO run_aborts (run_id, requested_at, author, reason) VALUES (?, ?, ?, ?)
		ON CONFLICT (run_id) DO UPDATE SET requested_at = excluded.requested_at, author = excluded.author, reason = excluded.reason`),
		runID, ts(time.Now()), by, reason)
	return err
}

// AbortRequested returns the request to abort a run, if there is one.
func (s *Store) AbortRequested(runID string) (*engine.AbortError, error) {
	var e engine.AbortError
	err := s.db.QueryRow(s.q(`SELECT author, reason FROM run_aborts WHERE run_id = ?`), runID).Scan(&e.By, &e.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// WatchAbort cancels a run's context (with the request as the cause) when
// another runnr asks to abort it, and passes on requests to suspend it to
// ctl, checking every few seconds until ctx is done. Run it in its own
// goroutine for each run this process runs.
func (s *Store) WatchAbort(ctx context.Context, runID string, cancel context.CancelCauseFunc, ctl *engine.Controls) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if e, err := s.AbortRequested(runID); err == nil && e != nil {
				cancel(e)
				return
			}
			if ctl != nil {
				if e, err := s.takeSuspend(runID); err == nil && e != nil {
					ctl.Suspend(e)
				}
			}
		}
	}
}

// AbortResult says what aborting a run did.
type AbortResult struct {
	Aborted bool   `json:"aborted"` // the run is now aborted
	Message string `json:"message"`
}

// AbortRun aborts a run that isn't running in this process: it asks the
// runnr running it to stop, and waits up to wait for it to. When that
// process is gone (it ran on this host and has exited), or with force, it
// marks the run aborted itself.
func (s *Store) AbortRun(runID, by, reason string, force bool, wait time.Duration) (AbortResult, error) {
	r, err := s.LoadRun(runID)
	if err != nil {
		return AbortResult{}, err
	}
	if r.Status != engine.StatusRunning {
		return AbortResult{}, fmt.Errorf("run %s isn't running (it %s)", runID, r.Status)
	}
	why := &engine.AbortError{By: by, Reason: reason}
	if force {
		return s.markAborted(r, why, "marked aborted; any runnr still running it will stop at its next check")
	}
	if err := s.RequestAbort(runID, by, reason); err != nil {
		return AbortResult{}, err
	}
	owner := r.Owner
	host, _ := os.Hostname()
	local := owner != nil && owner.Host == host
	if local && !processAlive(owner.PID) {
		return s.markAborted(r, why, fmt.Sprintf("its runnr process (pid %d) is gone, so it's marked aborted", owner.PID))
	}
	for deadline := time.Now().Add(wait); time.Now().Before(deadline); {
		time.Sleep(250 * time.Millisecond)
		if r, err = s.LoadRun(runID); err != nil {
			return AbortResult{}, err
		}
		if r.Status != engine.StatusRunning {
			msg := "it stopped"
			if r.Status != engine.StatusAborted {
				msg = "it ended first: " + string(r.Status)
			}
			return AbortResult{Aborted: r.Status == engine.StatusAborted, Message: msg}, nil
		}
	}
	switch {
	case local:
		return AbortResult{Message: fmt.Sprintf("asked runnr (pid %d) to abort it; it hasn't stopped yet (a step may be ending). If that process is stuck, stop it or use --force", owner.PID)}, nil
	case owner != nil:
		return AbortResult{Message: fmt.Sprintf("asked the runnr running it (%s on %s, pid %d) to abort it; it hasn't stopped. If that process is gone, use --force to mark the run aborted", owner.Via, owner.Host, owner.PID)}, nil
	}
	return AbortResult{Message: "asked to abort it, but no runnr has stopped it, and the run doesn't say which process ran it. If none is running it, use --force to mark it aborted"}, nil
}

// markAborted records a run as aborted without the process that ran it.
func (s *Store) markAborted(loaded *engine.Run, why *engine.AbortError, msg string) (AbortResult, error) {
	r, err := s.loadRunRecord(loaded.ID)
	if err != nil {
		return AbortResult{}, err
	}
	r.Status, r.Error, r.Ended = engine.StatusAborted, why.Error(), time.Now()
	for i := range r.Steps {
		if r.Steps[i].Status == engine.StatusRunning {
			r.Steps[i].Status = engine.StatusAborted
			if r.Steps[i].Ended.IsZero() {
				r.Steps[i].Ended = r.Ended
			}
		}
	}
	return AbortResult{Aborted: true, Message: msg}, s.SaveRun(r)
}
