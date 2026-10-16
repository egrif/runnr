package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/egrif/runnr/internal/runbook"
)

func init() {
	schema = append(schema,
		`CREATE TABLE IF NOT EXISTS libraries (
			name       TEXT PRIMARY KEY,
			raw        TEXT NOT NULL,
			version    INTEGER NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS library_versions (
			name       TEXT NOT NULL,
			version    INTEGER NOT NULL,
			raw        TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY (name, version)
		)`,
	)
}

// LibraryEntry is a library listing row.
type LibraryEntry struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Functions   []string  `json:"functions"`
	Version     int       `json:"version"`
	UpdatedAt   time.Time `json:"updated_at"`
	Err         string    `json:"error,omitempty"`
}

func (s *Store) ListLibraries() ([]LibraryEntry, error) {
	rows, err := s.db.Query(`SELECT name, raw, version, updated_at FROM libraries ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LibraryEntry{}
	for rows.Next() {
		var name, raw, updated string
		var version int
		if err := rows.Scan(&name, &raw, &version, &updated); err != nil {
			return nil, err
		}
		e := LibraryEntry{Name: name, Version: version, UpdatedAt: parseTS(updated), Functions: []string{}}
		if lib, err := runbook.ParseLibrary([]byte(raw)); err != nil {
			e.Err = err.Error()
		} else {
			e.Description = lib.Description
			for f := range lib.Functions {
				e.Functions = append(e.Functions, f)
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LibraryDoc is a stored library; Library is nil if Raw doesn't parse.
type LibraryDoc struct {
	Name      string
	Raw       []byte
	Version   int
	UpdatedAt time.Time
	Library   *runbook.Library
	ParseErr  error
}

func (s *Store) LoadLibrary(name string) (*LibraryDoc, error) {
	var raw, updated string
	var version int
	err := s.db.QueryRow(s.q(`SELECT raw, version, updated_at FROM libraries WHERE name = ?`), name).Scan(&raw, &version, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("library %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	d := &LibraryDoc{Name: name, Raw: []byte(raw), Version: version, UpdatedAt: parseTS(updated)}
	d.Library, d.ParseErr = runbook.ParseLibrary(d.Raw)
	return d, nil
}

// Libraries loads the named libraries, in order, failing on any that are
// missing or invalid.
func (s *Store) Libraries(names []string) ([]*runbook.Library, error) {
	var out []*runbook.Library
	for _, n := range names {
		d, err := s.LoadLibrary(n)
		if err != nil {
			return nil, err
		}
		if d.ParseErr != nil {
			return nil, fmt.Errorf("library %q: %w", n, d.ParseErr)
		}
		out = append(out, d.Library)
	}
	return out, nil
}

// SaveLibrary validates raw YAML and stores it as a new version.
func (s *Store) SaveLibrary(raw []byte, o SaveOpts) (*runbook.Library, int, error) {
	lib, err := runbook.ParseLibrary(raw)
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
		err := tx.QueryRow(s.q(`SELECT version FROM libraries WHERE name = ?`+s.forUpdate()), name).Scan(&v)
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
			return nil, 0, fmt.Errorf("library %q: %w", o.OldName, ErrNotFound)
		}
		if o.BaseVersion != 0 && v != o.BaseVersion {
			return nil, 0, fmt.Errorf("library %q is at version %d, you edited version %d: %w", o.OldName, v, o.BaseVersion, ErrConflict)
		}
	}
	if o.OldName != lib.Name {
		if _, exists, err := current(lib.Name); err != nil {
			return nil, 0, err
		} else if exists && !o.Overwrite {
			return nil, 0, fmt.Errorf("library %q already exists", lib.Name)
		}
	}
	var next int
	if err := tx.QueryRow(s.q(`SELECT COALESCE(MAX(version), 0) + 1 FROM library_versions WHERE name = ?`), lib.Name).Scan(&next); err != nil {
		return nil, 0, err
	}
	now := ts(time.Now())
	if _, err := tx.Exec(s.q(`INSERT INTO library_versions (name, version, raw, created_at) VALUES (?, ?, ?, ?)`), lib.Name, next, string(raw), now); err != nil {
		return nil, 0, err
	}
	if _, err := tx.Exec(s.q(`INSERT INTO libraries (name, raw, version, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET raw = excluded.raw, version = excluded.version, updated_at = excluded.updated_at`),
		lib.Name, string(raw), next, now); err != nil {
		return nil, 0, err
	}
	if o.OldName != "" && o.OldName != lib.Name {
		if _, err := tx.Exec(s.q(`DELETE FROM libraries WHERE name = ?`), o.OldName); err != nil {
			return nil, 0, err
		}
	}
	return lib, next, tx.Commit()
}

func (s *Store) DeleteLibrary(name string) error {
	res, err := s.db.Exec(s.q(`DELETE FROM libraries WHERE name = ?`), name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("library %q: %w", name, ErrNotFound)
	}
	return nil
}

func (s *Store) LibraryVersions(name string) ([]Version, error) {
	rows, err := s.db.Query(s.q(`SELECT version, created_at, LENGTH(raw) FROM library_versions WHERE name = ? ORDER BY version DESC`), name)
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
	if len(out) == 0 && rows.Err() == nil {
		return nil, fmt.Errorf("library %q: %w", name, ErrNotFound)
	}
	return out, rows.Err()
}

func (s *Store) RawLibraryVersion(name string, version int) ([]byte, error) {
	var raw string
	err := s.db.QueryRow(s.q(`SELECT raw FROM library_versions WHERE name = ? AND version = ?`), name, version).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("library %q version %d: %w", name, version, ErrNotFound)
	}
	return []byte(raw), err
}
