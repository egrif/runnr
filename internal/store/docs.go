package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// docKind is a kind of named, versioned YAML document stored like
// runbooks (libraries, collections).
type docKind struct {
	noun     string // for messages
	table    string
	versions string
	// parse validates raw and returns the document's name.
	parse func([]byte) (string, error)
}

func init() {
	for _, k := range []docKind{libraryKind, collectionKind, fakesKind, suiteKind} {
		schema = append(schema,
			`CREATE TABLE IF NOT EXISTS `+k.table+` (
				name       TEXT PRIMARY KEY,
				raw        TEXT NOT NULL,
				version    INTEGER NOT NULL,
				updated_at TEXT NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS `+k.versions+` (
				name       TEXT NOT NULL,
				version    INTEGER NOT NULL,
				raw        TEXT NOT NULL,
				created_at TEXT NOT NULL,
				PRIMARY KEY (name, version)
			)`)
	}
}

type docRow struct {
	Name      string
	Raw       []byte
	Version   int
	UpdatedAt time.Time
}

func (s *Store) listDocs(k docKind) ([]docRow, error) {
	rows, err := s.db.Query(`SELECT name, raw, version, updated_at FROM ` + k.table + ` ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []docRow
	for rows.Next() {
		var r docRow
		var raw, updated string
		if err := rows.Scan(&r.Name, &raw, &r.Version, &updated); err != nil {
			return nil, err
		}
		r.Raw, r.UpdatedAt = []byte(raw), parseTS(updated)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) loadDoc(k docKind, name string) (*docRow, error) {
	var raw, updated string
	r := &docRow{Name: name}
	err := s.db.QueryRow(s.q(`SELECT raw, version, updated_at FROM `+k.table+` WHERE name = ?`), name).Scan(&raw, &r.Version, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%s %q: %w", k.noun, name, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	r.Raw, r.UpdatedAt = []byte(raw), parseTS(updated)
	return r, nil
}

// saveDoc validates raw and stores it as a new version of its name.
func (s *Store) saveDoc(k docKind, raw []byte, o SaveOpts) (string, int, error) {
	name, err := k.parse(raw)
	if err != nil {
		return "", 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", 0, err
	}
	defer tx.Rollback()
	current := func(n string) (int, bool, error) {
		var v int
		err := tx.QueryRow(s.q(`SELECT version FROM `+k.table+` WHERE name = ?`+s.forUpdate()), n).Scan(&v)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return v, err == nil, err
	}
	if o.OldName != "" {
		v, ok, err := current(o.OldName)
		if err != nil {
			return "", 0, err
		}
		if !ok {
			return "", 0, fmt.Errorf("%s %q: %w", k.noun, o.OldName, ErrNotFound)
		}
		if o.BaseVersion != 0 && v != o.BaseVersion {
			return "", 0, fmt.Errorf("%s %q is at version %d, you edited version %d: %w", k.noun, o.OldName, v, o.BaseVersion, ErrConflict)
		}
	}
	if o.OldName != name {
		if _, exists, err := current(name); err != nil {
			return "", 0, err
		} else if exists && !o.Overwrite {
			return "", 0, fmt.Errorf("%s %q already exists", k.noun, name)
		}
	}
	var next int
	if err := tx.QueryRow(s.q(`SELECT COALESCE(MAX(version), 0) + 1 FROM `+k.versions+` WHERE name = ?`), name).Scan(&next); err != nil {
		return "", 0, err
	}
	now := ts(time.Now())
	if _, err := tx.Exec(s.q(`INSERT INTO `+k.versions+` (name, version, raw, created_at) VALUES (?, ?, ?, ?)`), name, next, string(raw), now); err != nil {
		return "", 0, err
	}
	if _, err := tx.Exec(s.q(`INSERT INTO `+k.table+` (name, raw, version, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET raw = excluded.raw, version = excluded.version, updated_at = excluded.updated_at`),
		name, string(raw), next, now); err != nil {
		return "", 0, err
	}
	if o.OldName != "" && o.OldName != name {
		if _, err := tx.Exec(s.q(`DELETE FROM `+k.table+` WHERE name = ?`), o.OldName); err != nil {
			return "", 0, err
		}
	}
	return name, next, tx.Commit()
}

func (s *Store) deleteDoc(k docKind, name string) error {
	res, err := s.db.Exec(s.q(`DELETE FROM `+k.table+` WHERE name = ?`), name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%s %q: %w", k.noun, name, ErrNotFound)
	}
	return nil
}

func (s *Store) docVersions(k docKind, name string) ([]Version, error) {
	rows, err := s.db.Query(s.q(`SELECT version, created_at, LENGTH(raw) FROM `+k.versions+` WHERE name = ? ORDER BY version DESC`), name)
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
		return nil, fmt.Errorf("%s %q: %w", k.noun, name, ErrNotFound)
	}
	return out, rows.Err()
}

func (s *Store) rawDocVersion(k docKind, name string, version int) ([]byte, error) {
	var raw string
	err := s.db.QueryRow(s.q(`SELECT raw FROM `+k.versions+` WHERE name = ? AND version = ?`), name, version).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%s %q version %d: %w", k.noun, name, version, ErrNotFound)
	}
	return []byte(raw), err
}
