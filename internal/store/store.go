// Package store persists runbooks (with version history) and run records in
// SQLite (default, a file under the runnr home) or Postgres (shared).
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/runbook"
)

var (
	ErrNotFound = errors.New("not found")
	// ErrConflict means the runbook changed since the caller loaded it.
	ErrConflict = errors.New("changed since it was loaded")
)

type Store struct {
	db       *sql.DB
	postgres bool
	// Location describes where data lives, for humans (password removed).
	Location string
}

// DefaultHome is $RUNNR_HOME, else ~/.runnr.
func DefaultHome() string {
	if h := os.Getenv("RUNNR_HOME"); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".runnr")
	}
	return ".runnr"
}

// Open connects to dsn: a postgres:// URL, or a SQLite file path. An empty
// dsn means home/runnr.db; on first use that database imports any runbooks
// and runs that older runnr versions left as files under home.
func Open(home, dsn string) (*Store, error) {
	s := &Store{}
	var err error
	switch {
	case strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://"):
		s.postgres = true
		s.Location = redact(dsn)
		s.db, err = sql.Open("pgx", dsn)
	default:
		path := strings.TrimPrefix(dsn, "sqlite:")
		if path == "" {
			if err := os.MkdirAll(home, 0o755); err != nil {
				return nil, err
			}
			path = filepath.Join(home, "runnr.db")
		}
		s.Location = path
		s.db, err = sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate")
		if err == nil {
			s.db.SetMaxOpenConns(1)
		}
	}
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.migrate(ctx); err != nil {
		s.db.Close()
		return nil, fmt.Errorf("database %s: %w", s.Location, err)
	}
	if dsn == "" {
		if err := s.importLegacy(ctx, home); err != nil {
			fmt.Fprintln(os.Stderr, "warning: importing old runnr files:", err)
		}
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func redact(dsn string) string {
	if i, j := strings.Index(dsn, "://"), strings.LastIndex(dsn, "@"); i >= 0 && j > i {
		if k := strings.Index(dsn[i+3:j], ":"); k >= 0 {
			return dsn[:i+3+k] + ":****" + dsn[j:]
		}
	}
	return dsn
}

var schema = []string{
	`CREATE TABLE IF NOT EXISTS runbooks (
		name       TEXT PRIMARY KEY,
		raw        TEXT NOT NULL,
		version    INTEGER NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS runbook_versions (
		name       TEXT NOT NULL,
		version    INTEGER NOT NULL,
		raw        TEXT NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY (name, version)
	)`,
	`CREATE TABLE IF NOT EXISTS runs (
		id         TEXT PRIMARY KEY,
		runbook    TEXT NOT NULL,
		status     TEXT NOT NULL,
		started_at TEXT NOT NULL,
		record     TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS runs_by_runbook ON runs (runbook, id)`,
	`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	// Notes live outside the run record so they can be added while another
	// process is still writing the run.
	`CREATE TABLE IF NOT EXISTS run_notes (
		run_id     TEXT NOT NULL,
		step       INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		author     TEXT NOT NULL,
		text       TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS run_notes_by_run ON run_notes (run_id, created_at)`,
}

func (s *Store) migrate(ctx context.Context) error {
	for _, q := range schema {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// q rewrites ? placeholders to $n for Postgres.
func (s *Store) q(query string) string {
	if !s.postgres {
		return query
	}
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			b.WriteString("$" + strconv.Itoa(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Timestamps are fixed-width UTC strings so they sort and compare as text
// in both databases.
const tsFormat = "2006-01-02T15:04:05.000000Z"

func ts(t time.Time) string { return t.UTC().Format(tsFormat) }

func parseTS(s string) time.Time {
	t, _ := time.Parse(tsFormat, s)
	return t
}

// ---------- runbooks ----------

// Entry is a listing row; Err is set when the stored YAML fails to parse.
type Entry struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Tags        []string  `json:"tags,omitempty"`
	Steps       int       `json:"steps"`
	Version     int       `json:"version"`
	UpdatedAt   time.Time `json:"updated_at"`
	Err         string    `json:"error,omitempty"`
}

func (s *Store) List() ([]Entry, error) {
	rows, err := s.db.Query(`SELECT name, raw, version, updated_at FROM runbooks ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var name, raw, updated string
		var version int
		if err := rows.Scan(&name, &raw, &version, &updated); err != nil {
			return nil, err
		}
		e := Entry{Name: name, Version: version, UpdatedAt: parseTS(updated)}
		if rb, err := runbook.Parse([]byte(raw)); err != nil {
			e.Err = err.Error()
		} else {
			e.Description, e.Tags, e.Steps = rb.Description, rb.Tags, len(rb.Steps)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Doc is a stored runbook. Runbook is nil if Raw doesn't parse (ParseErr).
type Doc struct {
	Name      string
	Raw       []byte
	Version   int
	UpdatedAt time.Time
	Runbook   *runbook.Runbook
	ParseErr  error
}

func (s *Store) Load(name string) (*Doc, error) {
	var raw, updated string
	var version int
	err := s.db.QueryRow(s.q(`SELECT raw, version, updated_at FROM runbooks WHERE name = ?`), name).Scan(&raw, &version, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("runbook %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	d := &Doc{Name: name, Raw: []byte(raw), Version: version, UpdatedAt: parseTS(updated)}
	d.Runbook, d.ParseErr = runbook.Parse(d.Raw)
	return d, nil
}

func (s *Store) Raw(name string) ([]byte, error) {
	d, err := s.Load(name)
	if err != nil {
		return nil, err
	}
	return d.Raw, nil
}

// Get returns a parsed runbook and its version.
func (s *Store) Get(name string) (*runbook.Runbook, int, error) {
	d, err := s.Load(name)
	if err != nil {
		return nil, 0, err
	}
	return d.Runbook, d.Version, d.ParseErr
}

// Resolve loads a runbook by name, or from a file if ref looks like a path
// (version 0).
func (s *Store) Resolve(ref string) (*runbook.Runbook, int, error) {
	if strings.ContainsAny(ref, `/\`) || strings.HasSuffix(ref, ".yaml") || strings.HasSuffix(ref, ".yml") {
		b, err := os.ReadFile(ref)
		if err != nil {
			return nil, 0, err
		}
		rb, err := runbook.Parse(b)
		return rb, 0, err
	}
	return s.Get(ref)
}

func (s *Store) Exists(name string) bool {
	var one int
	return s.db.QueryRow(s.q(`SELECT 1 FROM runbooks WHERE name = ?`), name).Scan(&one) == nil
}

// SaveOpts controls Save.
type SaveOpts struct {
	// OldName is the name being edited; if the YAML declares a different
	// name, the runbook is renamed. Empty means create.
	OldName string
	// BaseVersion, if set, must match the stored version (else ErrConflict).
	BaseVersion int
	// Overwrite allows creating over an existing runbook.
	Overwrite bool
}

// Save validates raw YAML and stores it as a new version of its declared name.
func (s *Store) Save(raw []byte, o SaveOpts) (*runbook.Runbook, int, error) {
	rb, err := runbook.Parse(raw)
	if err != nil {
		return nil, 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()

	current := func(name string) (int, bool, error) {
		var v int
		err := tx.QueryRow(s.q(`SELECT version FROM runbooks WHERE name = ?`+s.forUpdate()), name).Scan(&v)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return v, err == nil, err
	}

	if o.OldName != "" {
		v, ok, err := current(o.OldName)
		if err != nil {
			return nil, 0, err
		}
		if !ok {
			return nil, 0, fmt.Errorf("runbook %q: %w", o.OldName, ErrNotFound)
		}
		if o.BaseVersion != 0 && v != o.BaseVersion {
			return nil, 0, fmt.Errorf("runbook %q is at version %d, you edited version %d: %w", o.OldName, v, o.BaseVersion, ErrConflict)
		}
	}
	if o.OldName != rb.Name {
		if _, exists, err := current(rb.Name); err != nil {
			return nil, 0, err
		} else if exists && !o.Overwrite {
			return nil, 0, fmt.Errorf("runbook %q already exists", rb.Name)
		}
	}

	// Saving it unchanged keeps the version it has.
	if o.OldName == "" || o.OldName == rb.Name {
		var v int
		var cur string
		if err := tx.QueryRow(s.q(`SELECT version, raw FROM runbooks WHERE name = ?`), rb.Name).Scan(&v, &cur); err == nil && cur == string(raw) {
			return rb, v, nil
		}
	}
	// Versions continue across deletes so history stays unambiguous.
	var next int
	if err := tx.QueryRow(s.q(`SELECT COALESCE(MAX(version), 0) + 1 FROM runbook_versions WHERE name = ?`), rb.Name).Scan(&next); err != nil {
		return nil, 0, err
	}
	now := ts(time.Now())
	if _, err := tx.Exec(s.q(`INSERT INTO runbook_versions (name, version, raw, created_at) VALUES (?, ?, ?, ?)`), rb.Name, next, string(raw), now); err != nil {
		return nil, 0, err
	}
	if _, err := tx.Exec(s.q(`INSERT INTO runbooks (name, raw, version, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET raw = excluded.raw, version = excluded.version, updated_at = excluded.updated_at`),
		rb.Name, string(raw), next, now); err != nil {
		return nil, 0, err
	}
	if o.OldName != "" && o.OldName != rb.Name {
		if _, err := tx.Exec(s.q(`DELETE FROM runbooks WHERE name = ?`), o.OldName); err != nil {
			return nil, 0, err
		}
	}
	return rb, next, tx.Commit()
}

func (s *Store) forUpdate() string {
	if s.postgres {
		return " FOR UPDATE"
	}
	return "" // SQLite transactions here are IMMEDIATE, i.e. already exclusive.
}

// Delete removes a runbook; its version history is kept.
func (s *Store) Delete(name string) error {
	res, err := s.db.Exec(s.q(`DELETE FROM runbooks WHERE name = ?`), name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("runbook %q: %w", name, ErrNotFound)
	}
	return nil
}

type Version struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	Bytes     int       `json:"bytes"`
}

// Versions lists a runbook's saved versions, newest first.
func (s *Store) Versions(name string) ([]Version, error) {
	rows, err := s.db.Query(s.q(`SELECT version, created_at, LENGTH(raw) FROM runbook_versions WHERE name = ? ORDER BY version DESC`), name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Version
	for rows.Next() {
		var v Version
		var created string
		if err := rows.Scan(&v.Version, &created, &v.Bytes); err != nil {
			return nil, err
		}
		v.CreatedAt = parseTS(created)
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("runbook %q: %w", name, ErrNotFound)
	}
	return out, nil
}

func (s *Store) RawVersion(name string, version int) ([]byte, error) {
	var raw string
	err := s.db.QueryRow(s.q(`SELECT raw FROM runbook_versions WHERE name = ? AND version = ?`), name, version).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("runbook %q version %d: %w", name, version, ErrNotFound)
	}
	return []byte(raw), err
}

// ---------- runs ----------

func (s *Store) SaveRun(r *engine.Run) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(s.q(`INSERT INTO runs (id, runbook, status, started_at, record) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET status = excluded.status, record = excluded.record`),
		r.ID, r.Runbook, string(r.Status), ts(r.Started), string(b))
	return err
}

func (s *Store) LoadRun(id string) (*engine.Run, error) {
	r, err := s.loadRunRecord(id)
	if err != nil {
		return nil, err
	}
	return r, s.attachNotes(r)
}

// loadRunRecord is the run as its process saved it, without the notes
// kept apart from it.
func (s *Store) loadRunRecord(id string) (*engine.Run, error) {
	var rec string
	err := s.db.QueryRow(s.q(`SELECT record FROM runs WHERE id = ?`), id).Scan(&rec)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("run %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	var r engine.Run
	if err := json.Unmarshal([]byte(rec), &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// AddNote records an operator note on a step (0-based), or on the run
// itself when step is -1.
func (s *Store) AddNote(runID string, step int, author, text string) (engine.Note, error) {
	n := engine.Note{Time: time.Now().UTC().Truncate(time.Microsecond), Author: author, Text: strings.TrimSpace(text)}
	if n.Text == "" {
		return n, errors.New("note is empty")
	}
	r, err := s.LoadRun(runID)
	if err != nil {
		return n, err
	}
	if step < -1 || step >= len(r.Steps) {
		return n, fmt.Errorf("run %s has no step %d", runID, step+1)
	}
	_, err = s.db.Exec(s.q(`INSERT INTO run_notes (run_id, step, created_at, author, text) VALUES (?, ?, ?, ?, ?)`),
		runID, step, ts(n.Time), author, n.Text)
	return n, err
}

func (s *Store) attachNotes(r *engine.Run) error {
	rows, err := s.db.Query(s.q(`SELECT step, created_at, author, text FROM run_notes WHERE run_id = ? ORDER BY created_at`), r.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var step int
		var created string
		var n engine.Note
		if err := rows.Scan(&step, &created, &n.Author, &n.Text); err != nil {
			return err
		}
		n.Time = parseTS(created)
		// A record can carry notes already (a resumed run keeps its steps
		// whole; an imported one brings its own): not twice.
		has := func(ns []engine.Note) bool {
			return slices.ContainsFunc(ns, func(m engine.Note) bool { return m.Author == n.Author && m.Text == n.Text && m.Time.Equal(n.Time) })
		}
		switch {
		case step == -1 && !has(r.Notes):
			r.Notes = append(r.Notes, n)
		case step >= 0 && step < len(r.Steps) && !has(r.Steps[step].Notes):
			r.Steps[step].Notes = append(r.Steps[step].Notes, n)
		}
	}
	return rows.Err()
}

// ListRuns returns runs newest first, without step detail.
func (s *Store) ListRuns(runbookName string, limit int) ([]engine.Run, error) {
	query, args := `SELECT record FROM runs`, []any{}
	if runbookName != "" {
		query += ` WHERE runbook = ?`
		args = append(args, runbookName)
	}
	query += ` ORDER BY started_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []engine.Run{}
	for rows.Next() {
		var rec string
		if err := rows.Scan(&rec); err != nil {
			return nil, err
		}
		var r engine.Run
		if json.Unmarshal([]byte(rec), &r) == nil {
			r.Steps = nil
			out = append(out, r)
		}
	}
	return out, rows.Err()
}

// FindRuns lists runs (most recent first, of runbookName if given, up to
// limit) with their identifiers filled in (engine.RunIdentifiers: runs
// from before the runbook marked them get its marks now), keeping those
// whose identifier values or ID contain find (any case; "" keeps all).
func (s *Store) FindRuns(runbookName, find string, limit int) ([]engine.Run, error) {
	find = strings.ToLower(strings.TrimSpace(find))
	// Read before the runs' rows are open (SQLite has one connection).
	marks, err := s.identifierMarks(runbookName)
	if err != nil {
		return nil, err
	}
	query, args := `SELECT record FROM runs WHERE 1 = 1`, []any{}
	if runbookName != "" {
		query += ` AND runbook = ?`
		args = append(args, runbookName)
	}
	if plainText.MatchString(find) && find != "" {
		// A coarse cut in the database (the record is JSON, which leaves
		// plain text as it is); the identifiers decide below.
		query += ` AND LOWER(record) LIKE ?`
		args = append(args, "%"+strings.ReplaceAll(find, "%", "_")+"%")
	}
	query += ` ORDER BY started_at DESC, id DESC`
	rows, err := s.db.Query(s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []engine.Run{}
	for rows.Next() && len(out) < limit {
		var rec string
		if err := rows.Scan(&rec); err != nil {
			return nil, err
		}
		var r engine.Run
		if json.Unmarshal([]byte(rec), &r) != nil {
			continue
		}
		r.Steps = nil
		if len(r.Identifiers) == 0 {
			r.Identifiers = marks[r.Runbook]
		}
		if find != "" && !strings.Contains(strings.ToLower(r.ID), find) &&
			!slices.ContainsFunc(engine.RunIdentifiers(&r, nil), func(id string) bool {
				_, v, _ := strings.Cut(id, "=")
				return strings.Contains(strings.ToLower(v), find)
			}) {
			continue
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// identifierMarks are the identifier inputs each runbook (or just name)
// marks now.
func (s *Store) identifierMarks(name string) (map[string][]string, error) {
	query, args := `SELECT name, raw FROM runbooks`, []any{}
	if name != "" {
		query += ` WHERE name = ?`
		args = append(args, name)
	}
	rows, err := s.db.Query(s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var n, raw string
		if err := rows.Scan(&n, &raw); err != nil {
			return nil, err
		}
		if rb, err := runbook.Parse([]byte(raw)); err == nil {
			if ids := rb.IdentifierInputs(); len(ids) > 0 {
				out[n] = ids
			}
		}
	}
	return out, rows.Err()
}

// plainText is text JSON writes as it is ("_" in a LIKE pattern matches
// any one character, itself included).
var plainText = regexp.MustCompile(`^[a-z0-9 ._:/@=+,%-]*$`)

// PruneRuns deletes finished runs that started before cutoff.
func (s *Store) PruneRuns(cutoff time.Time) (int64, error) {
	res, err := s.db.Exec(s.q(`DELETE FROM runs WHERE started_at < ? AND status <> ?`), ts(cutoff), string(engine.StatusRunning))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	_, err = s.db.Exec(`DELETE FROM run_notes WHERE run_id NOT IN (SELECT id FROM runs)`)
	return n, err
}

// ---------- stats & import ----------

type Stats struct {
	Location string `json:"location"`
	Driver   string `json:"driver"`
	Runbooks int    `json:"runbooks"`
	Versions int    `json:"versions"`
	Runs     int    `json:"runs"`
}

func (s *Store) Stats() (Stats, error) {
	st := Stats{Location: s.Location, Driver: "sqlite"}
	if s.postgres {
		st.Driver = "postgres"
	}
	for q, dst := range map[string]*int{
		`SELECT COUNT(*) FROM runbooks`:         &st.Runbooks,
		`SELECT COUNT(*) FROM runbook_versions`: &st.Versions,
		`SELECT COUNT(*) FROM runs`:             &st.Runs,
	} {
		if err := s.db.QueryRow(q).Scan(dst); err != nil {
			return st, err
		}
	}
	return st, nil
}

// ImportFiles loads <dir>/runbooks/*.yaml and <dir>/runs/*.json (the
// pre-database layout). Existing runbooks and runs are left alone.
func (s *Store) ImportFiles(dir string) (runbooks, runs int, err error) {
	files, _ := filepath.Glob(filepath.Join(dir, "runbooks", "*.yaml"))
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return runbooks, runs, err
		}
		rb, perr := runbook.Parse(raw)
		if perr != nil {
			fmt.Fprintf(os.Stderr, "skipping %s: %v\n", f, perr)
			continue
		}
		if s.Exists(rb.Name) {
			continue
		}
		if _, _, err := s.Save(raw, SaveOpts{}); err != nil {
			return runbooks, runs, err
		}
		runbooks++
	}
	files, _ = filepath.Glob(filepath.Join(dir, "runs", "*.json"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return runbooks, runs, err
		}
		var r engine.Run
		if json.Unmarshal(b, &r) != nil || r.ID == "" {
			continue
		}
		if _, err := s.LoadRun(r.ID); err == nil {
			continue
		}
		if err := s.SaveRun(&r); err != nil {
			return runbooks, runs, err
		}
		runs++
	}
	return runbooks, runs, nil
}

func (s *Store) importLegacy(ctx context.Context, home string) error {
	var done string
	if s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'legacy_import'`).Scan(&done) == nil {
		return nil
	}
	rbs, runs, err := s.ImportFiles(home)
	if err != nil {
		return err
	}
	if rbs+runs > 0 {
		fmt.Fprintf(os.Stderr, "runnr: imported %d runbook(s) and %d run(s) from %s into %s (files left in place)\n", rbs, runs, home, s.Location)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES ('legacy_import', 'done')`)
	return err
}

// SaveRunEdits saves the step edits made during a run as a new version of
// its runbook, and records that on the run. A step that has changed in the
// runbook since the run started is a conflict.
func (s *Store) SaveRunEdits(run *engine.Run) (int, error) {
	if len(run.Edits) == 0 {
		return 0, errors.New("this run has no step edits")
	}
	if run.RunbookVersion == 0 {
		return 0, errors.New("this run's runbook isn't saved in runnr (it ran from a file)")
	}
	rb, ver, err := s.Get(run.Runbook)
	if err != nil {
		return 0, err
	}
	edited, err := engine.ApplyEdits(rb, run.Edits)
	if err != nil {
		return 0, err
	}
	raw, err := runbook.Format(edited)
	if err != nil {
		return 0, err
	}
	if _, ver, err = s.Save(raw, SaveOpts{OldName: rb.Name, BaseVersion: ver}); err != nil {
		return 0, err
	}
	run.EditsSaved = ver
	return ver, s.SaveRun(run)
}
