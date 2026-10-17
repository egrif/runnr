package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egrif/runnr/internal/runbook"
)

func init() {
	schema = append(schema, `CREATE TABLE IF NOT EXISTS schedules (
		id     TEXT PRIMARY KEY,
		at     TEXT NOT NULL,
		record TEXT NOT NULL
	)`)
}

// Schedule is a run planned for a time: for a runbook with a maintenance
// window, At is when the window is to open (T+0); otherwise when the run
// is to start. Nothing starts it yet; it's for planning.
type Schedule struct {
	ID        string            `json:"id"`
	Runbook   string            `json:"runbook"`
	At        time.Time         `json:"at"`
	Title     string            `json:"title,omitempty"`
	Notes     string            `json:"notes,omitempty"`
	Inputs    map[string]string `json:"inputs,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	CreatedBy string            `json:"created_by,omitempty"`
}

// SaveSchedule adds a schedule (empty ID) or replaces one. Secret inputs
// aren't kept: they're asked for when the run starts.
func (s *Store) SaveSchedule(sc *Schedule) error {
	rb, _, err := s.Get(sc.Runbook)
	if err != nil {
		return err
	}
	if sc.At.IsZero() {
		return errors.New("a schedule needs a time")
	}
	known := map[string]bool{}
	for _, in := range rb.Inputs {
		known[in.Name] = true
		if in.Secret {
			delete(sc.Inputs, in.Name)
		}
		if v := sc.Inputs[in.Name]; v != "" && len(in.Choices) > 0 && !slices.Contains(in.Choices, v) {
			return fmt.Errorf("input %s: %q isn't one of %s", in.Name, v, strings.Join(in.Choices, ", "))
		}
	}
	for k := range sc.Inputs {
		if !known[k] {
			return fmt.Errorf("runbook %s has no input %q", sc.Runbook, k)
		}
	}
	if err := rb.CheckWindowInputs(sc.Inputs); err != nil {
		return err
	}
	if sc.ID == "" {
		b := make([]byte, 4)
		rand.Read(b)
		sc.ID = "s" + time.Now().UTC().Format("20060102") + "-" + hex.EncodeToString(b)
		sc.CreatedAt = time.Now().UTC()
	}
	rec, err := json.Marshal(sc)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(s.q(`INSERT INTO schedules (id, at, record) VALUES (?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET at = excluded.at, record = excluded.record`), sc.ID, ts(sc.At), string(rec))
	return err
}

func (s *Store) GetSchedule(id string) (*Schedule, error) {
	var rec string
	err := s.db.QueryRow(s.q(`SELECT record FROM schedules WHERE id = ?`), id).Scan(&rec)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("schedule %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	var sc Schedule
	return &sc, json.Unmarshal([]byte(rec), &sc)
}

// ListSchedules returns schedules at or after from (zero: all), soonest first.
func (s *Store) ListSchedules(from time.Time) ([]Schedule, error) {
	rows, err := s.db.Query(s.q(`SELECT record FROM schedules WHERE at >= ? ORDER BY at`), ts(from))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Schedule{}
	for rows.Next() {
		var rec string
		if err := rows.Scan(&rec); err != nil {
			return nil, err
		}
		var sc Schedule
		if err := json.Unmarshal([]byte(rec), &sc); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *Store) DeleteSchedule(id string) error {
	res, err := s.db.Exec(s.q(`DELETE FROM schedules WHERE id = ?`), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("schedule %q: %w", id, ErrNotFound)
	}
	return nil
}

// AgendaEntry is one dated point of a scheduled run: its maintenance
// window (with End), a section or step's planned start, or for a runbook
// without a window, the run's start.
type AgendaEntry struct {
	Kind   string     `json:"kind"` // window | section | step | start
	Name   string     `json:"name"`
	Step   int        `json:"step"`
	At     time.Time  `json:"at"`
	End    *time.Time `json:"end,omitempty"`
	Offset string     `json:"offset,omitempty"` // e.g. -30m, +12m
}

// PlannedRun is a schedule with its runbook's timeline laid out in time.
type PlannedRun struct {
	Schedule
	Description string        `json:"description,omitempty"`
	Window      bool          `json:"window"`
	Entries     []AgendaEntry `json:"entries"`
	Error       string        `json:"error,omitempty"`
}

// Agenda lays out the schedules at or after from (zero: all). Planned
// times come from each runbook as saved now, with the schedule's inputs.
func (s *Store) Agenda(from time.Time) ([]PlannedRun, error) {
	scs, err := s.ListSchedules(time.Time{})
	if err != nil {
		return nil, err
	}
	out := []PlannedRun{}
	for _, sc := range scs {
		pr := PlannedRun{Schedule: sc}
		rb, _, err := s.Get(sc.Runbook)
		if err != nil {
			pr.Error = err.Error()
			pr.Entries = []AgendaEntry{{Kind: "start", Name: sc.Runbook, At: sc.At}}
		} else {
			pr.Description = firstLine(rb.Description)
			tl := rb.Timeline(sc.Inputs)
			pr.Window = len(tl) > 0
			if !pr.Window {
				pr.Entries = []AgendaEntry{{Kind: "start", Name: sc.Runbook, At: sc.At}}
			}
			for _, e := range tl {
				ae := AgendaEntry{Kind: e.Kind, Name: e.Name, Step: e.Step, At: sc.At.Add(e.Offset), Offset: offsetString(e.Offset)}
				if e.Kind == "window" && e.Length > 0 {
					end := sc.At.Add(e.Length)
					ae.End = &end
				}
				pr.Entries = append(pr.Entries, ae)
			}
		}
		slices.SortStableFunc(pr.Entries, func(a, b AgendaEntry) int { return a.At.Compare(b.At) })
		// Keep runs any part of which is at or after from.
		last := sc.At
		for _, e := range pr.Entries {
			if e.At.After(last) {
				last = e.At
			}
			if e.End != nil && e.End.After(last) {
				last = *e.End
			}
		}
		if from.IsZero() || !last.Before(from) {
			out = append(out, pr)
		}
	}
	return out, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func offsetString(d time.Duration) string {
	if d < 0 {
		return runbook.FormatSpan(d)
	}
	return "+" + runbook.FormatSpan(d)
}
