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
	"github.com/egrif/runnr/internal/ver"
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
			if err := os.MkdirAll(home, 0o700); err != nil {
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
	if !s.postgres {
		// Run records hold commands and output: for this user alone.
		for _, f := range []string{s.Location, s.Location + "-wal", s.Location + "-shm"} {
			if st, err := os.Stat(f); err == nil && st.Mode().Perm()&0o077 != 0 {
				_ = os.Chmod(f, 0o600)
			}
		}
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
	if s.postgres {
		// Two runnrs opening a database at once would race to create its
		// tables (and convert them): one waits here for the other.
		conn, err := s.db.Conn(ctx)
		if err != nil {
			return err
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(7172707)`); err != nil {
			return err
		}
		defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(7172707)`)
	}
	for _, q := range schema {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return s.migrateHashes(ctx)
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

var runbookKind = docKind{noun: "runbook", table: "runbooks", versions: "runbook_versions",
	parse: func(raw []byte) (string, error) {
		rb, err := runbook.Parse(raw)
		if err != nil {
			return "", err
		}
		return rb.Name, nil
	}}

// Entry is a listing row; Err is set when the stored YAML fails to parse.
type Entry struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Tags        []string  `json:"tags,omitempty"`
	Steps       int       `json:"steps"`
	Version     ver.ID    `json:"version"`
	UpdatedAt   time.Time `json:"updated_at"`
	Err         string    `json:"error,omitempty"`
}

func (s *Store) List() ([]Entry, error) {
	rows, err := s.listDocs(runbookKind)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, r := range rows {
		e := Entry{Name: r.Name, Version: r.Version, UpdatedAt: r.UpdatedAt}
		if rb, err := runbook.Parse(r.Raw); err != nil {
			e.Err = err.Error()
		} else {
			e.Description, e.Tags, e.Steps = rb.Description, rb.Tags, len(rb.Steps)
		}
		out = append(out, e)
	}
	return out, nil
}

// Doc is a stored runbook. Runbook is nil if Raw doesn't parse (ParseErr).
type Doc struct {
	Name      string
	Raw       []byte
	Version   ver.ID
	UpdatedAt time.Time
	Runbook   *runbook.Runbook
	ParseErr  error
}

func (s *Store) Load(name string) (*Doc, error) {
	r, err := s.loadDoc(runbookKind, name)
	if err != nil {
		return nil, err
	}
	d := &Doc{Name: name, Raw: r.Raw, Version: r.Version, UpdatedAt: r.UpdatedAt}
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
func (s *Store) Get(name string) (*runbook.Runbook, ver.ID, error) {
	d, err := s.Load(name)
	if err != nil {
		return nil, "", err
	}
	return d.Runbook, d.Version, d.ParseErr
}

// Resolve loads a runbook by name, or from a file if ref looks like a path.
// Either way the version is its text's (HasVersion says whether it's one
// runnr has saved).
func (s *Store) Resolve(ref string) (*runbook.Runbook, ver.ID, error) {
	if strings.ContainsAny(ref, `/\`) || strings.HasSuffix(ref, ".yaml") || strings.HasSuffix(ref, ".yml") {
		b, err := os.ReadFile(ref)
		if err != nil {
			return nil, "", err
		}
		rb, err := runbook.Parse(b)
		return rb, ver.Of(b), err
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
	// Base, if set, must be the stored version (else ErrConflict).
	Base ver.ID
	// Overwrite allows creating over an existing runbook.
	Overwrite bool
}

// Save validates raw YAML and makes it the current version of its declared
// name.
func (s *Store) Save(raw []byte, o SaveOpts) (*runbook.Runbook, ver.ID, error) {
	name, v, err := s.saveDoc(runbookKind, raw, o)
	if err != nil {
		return nil, "", err
	}
	if o.OldName != "" && o.OldName != name {
		// Its folders on this machine go with it.
		if err := s.renamePlaceholderScope(RunbookScope(o.OldName), RunbookScope(name)); err != nil {
			return nil, "", err
		}
	}
	rb, err := runbook.Parse(raw)
	return rb, v, err
}

// Delete removes a runbook; its versions are kept.
func (s *Store) Delete(name string) error { return s.deleteDoc(runbookKind, name) }

// Versions lists a runbook's versions, the most recently first saved first.
func (s *Store) Versions(name string) ([]Version, error) { return s.docVersions(runbookKind, name) }

// RawVersion is the text of a runbook's version that ref names (the hash,
// a prefix of it or its short form), and that version.
func (s *Store) RawVersion(name, ref string) ([]byte, ver.ID, error) {
	return s.rawDocVersion(runbookKind, name, ref)
}

// HasVersion says whether v is a saved version of runbook name.
func (s *Store) HasVersion(name string, v ver.ID) bool {
	if v == "" {
		return false
	}
	var one int
	return s.db.QueryRow(s.q(`SELECT 1 FROM runbook_versions WHERE name = ? AND hash = ?`), name, v).Scan(&one) == nil
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
func (s *Store) SaveRunEdits(run *engine.Run) (ver.ID, error) {
	if len(run.Edits) == 0 {
		return "", errors.New("this run has no step edits")
	}
	if !s.HasVersion(run.Runbook, run.RunbookVersion) {
		return "", errors.New("this run's runbook isn't saved in runnr (it ran from a file, or came from another runnr)")
	}
	rb, v, err := s.Get(run.Runbook)
	if err != nil {
		return "", err
	}
	edited, err := engine.ApplyEdits(rb, run.Edits)
	if err != nil {
		return "", err
	}
	raw, err := runbook.Format(edited)
	if err != nil {
		return "", err
	}
	if _, v, err = s.Save(raw, SaveOpts{OldName: rb.Name, Base: v}); err != nil {
		return "", err
	}
	run.EditsSaved = v
	return v, s.SaveRun(run)
}
