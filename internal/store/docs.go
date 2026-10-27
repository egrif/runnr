package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/egrif/runnr/internal/ver"
)

// docKind is a kind of named, versioned YAML document (runbooks,
// libraries, collections, fake libraries, test suites). A version is the
// hash of the document's text (ver.ID); every version is kept.
type docKind struct {
	kind     string // KindRunbook …
	noun     string // for messages
	table    string
	versions string
	// parse validates raw and returns the document's name.
	parse func([]byte) (string, error)
}

func docSchema(k docKind) []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS ` + k.table + ` (
			name       TEXT PRIMARY KEY,
			raw        TEXT NOT NULL,
			hash       TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS ` + k.versions + ` (
			name       TEXT NOT NULL,
			hash       TEXT NOT NULL,
			raw        TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY (name, hash)
		)`,
	}
}

func docKinds() []docKind {
	return []docKind{runbookKind, libraryKind, collectionKind, fakesKind, suiteKind}
}

type docRow struct {
	Name string
	// Raw is the document as written (placeholders and all); Filled, with
	// its placeholders filled in (FillErr if some have no value here).
	Raw       []byte
	Filled    []byte
	FillErr   error
	Version   ver.ID
	UpdatedAt time.Time
}

func (s *Store) fill(k docKind, r *docRow) {
	r.Filled, r.FillErr = filled(k.noun, r.Name, r.Raw, s.values(k.kind, r.Name, nil))
}

func (s *Store) listDocs(k docKind) ([]docRow, error) {
	rows, err := s.db.Query(`SELECT name, raw, hash, updated_at FROM ` + k.table + ` ORDER BY name`)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close() // SQLite has one connection: free it for the origins
	for i := range out {
		s.fill(k, &out[i])
	}
	return out, nil
}

func (s *Store) loadDoc(k docKind, name string) (*docRow, error) {
	var raw, updated string
	r := &docRow{Name: name}
	err := s.db.QueryRow(s.q(`SELECT raw, hash, updated_at FROM `+k.table+` WHERE name = ?`), name).Scan(&raw, &r.Version, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%s %q: %w", k.noun, name, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	r.Raw, r.UpdatedAt = []byte(raw), parseTS(updated)
	s.fill(k, r)
	return r, nil
}

// saveDoc validates raw and makes it the current version of its name. Text
// saved before under this name is that version again, not a new one. raw
// is kept as written; it's validated with its placeholders filled in (from
// o.Origin, else where the document came from before).
func (s *Store) saveDoc(k docKind, raw []byte, o SaveOpts) (string, ver.ID, error) {
	was := o.OldName
	if was == "" {
		was, _ = k.parse(raw) // its name, if it parses as written
	}
	full, err := filled(k.noun, was, raw, s.values(k.kind, was, o.Origin))
	if err != nil {
		return "", "", err
	}
	name, err := k.parse(full)
	if err != nil {
		return "", "", err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	current := func(n string) (ver.ID, bool, error) {
		var v ver.ID
		err := tx.QueryRow(s.q(`SELECT hash FROM `+k.table+` WHERE name = ?`+s.forUpdate()), n).Scan(&v)
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return v, err == nil, err
	}
	if o.OldName != "" {
		v, ok, err := current(o.OldName)
		if err != nil {
			return "", "", err
		}
		if !ok {
			return "", "", fmt.Errorf("%s %q: %w", k.noun, o.OldName, ErrNotFound)
		}
		if o.Base != "" && v != o.Base {
			return "", "", fmt.Errorf("%s %q is at version %s, you edited version %s: %w", k.noun, o.OldName, v.Short(), o.Base.Short(), ErrConflict)
		}
	}
	if o.OldName != name {
		if _, exists, err := current(name); err != nil {
			return "", "", err
		} else if exists && !o.Overwrite {
			return "", "", fmt.Errorf("%s %q already exists", k.noun, name)
		}
	}
	if o.Origin != nil {
		if err := s.setOrigin(tx, k.kind, name, o.Origin); err != nil {
			return "", "", err
		}
	} else if o.OldName != "" && o.OldName != name {
		// Renamed: where it came from follows it.
		if _, err := tx.Exec(s.q(`UPDATE doc_origins SET name = ? WHERE kind = ? AND name = ?`), name, k.kind, o.OldName); err != nil {
			return "", "", err
		}
	}
	id := ver.Of(raw)
	if o.OldName == "" || o.OldName == name {
		if v, ok, err := current(name); err != nil {
			return "", "", err
		} else if ok && v == id {
			return name, id, tx.Commit() // the origin may be new
		}
	}
	now := ts(time.Now())
	if _, err := tx.Exec(s.q(`INSERT INTO `+k.versions+` (name, hash, raw, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (name, hash) DO NOTHING`), name, id, string(raw), now); err != nil {
		return "", "", err
	}
	if _, err := tx.Exec(s.q(`INSERT INTO `+k.table+` (name, raw, hash, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET raw = excluded.raw, hash = excluded.hash, updated_at = excluded.updated_at`),
		name, string(raw), id, now); err != nil {
		return "", "", err
	}
	if o.OldName != "" && o.OldName != name {
		if _, err := tx.Exec(s.q(`DELETE FROM `+k.table+` WHERE name = ?`), o.OldName); err != nil {
			return "", "", err
		}
	}
	return name, id, tx.Commit()
}

func (s *Store) forUpdate() string {
	if s.postgres {
		return " FOR UPDATE"
	}
	return "" // SQLite transactions here are IMMEDIATE, i.e. already exclusive.
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

// Version is one saved version of a document.
type Version struct {
	Version   ver.ID    `json:"version"`
	CreatedAt time.Time `json:"created_at"` // when it was first saved
	Bytes     int       `json:"bytes"`
	Current   bool      `json:"current,omitempty"`
}

// docVersions lists a document's versions, the most recently first saved
// first.
func (s *Store) docVersions(k docKind, name string) ([]Version, error) {
	var cur ver.ID
	_ = s.db.QueryRow(s.q(`SELECT hash FROM `+k.table+` WHERE name = ?`), name).Scan(&cur)
	rows, err := s.db.Query(s.q(`SELECT hash, created_at, LENGTH(raw) FROM `+k.versions+` WHERE name = ? ORDER BY created_at DESC, hash`), name)
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
		v.CreatedAt, v.Current = parseTS(created), v.Version == cur
		out = append(out, v)
	}
	if len(out) == 0 && rows.Err() == nil {
		return nil, fmt.Errorf("%s %q: %w", k.noun, name, ErrNotFound)
	}
	return out, rows.Err()
}

// rawDocVersion is the text of the version of name that ref names (the
// hash, a prefix of it, or its short form), and that version.
func (s *Store) rawDocVersion(k docKind, name, ref string) ([]byte, ver.ID, error) {
	rows, err := s.db.Query(s.q(`SELECT hash FROM `+k.versions+` WHERE name = ?`), name)
	if err != nil {
		return nil, "", err
	}
	var ids []ver.ID
	for rows.Next() {
		var id ver.ID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, "", err
		}
		ids = append(ids, id)
	}
	rows.Close()
	id, err := ver.Find(ids, ref)
	if err != nil {
		var nf *ver.NotFoundError
		if errors.As(err, &nf) {
			return nil, "", fmt.Errorf("%s %q version %s: %w", k.noun, name, ref, ErrNotFound)
		}
		return nil, "", fmt.Errorf("%s %q: %w", k.noun, name, err)
	}
	var raw string
	err = s.db.QueryRow(s.q(`SELECT raw FROM `+k.versions+` WHERE name = ? AND hash = ?`), name, id).Scan(&raw)
	return []byte(raw), id, err
}

func init() {
	for _, k := range docKinds() {
		schema = append(schema, docSchema(k)...)
	}
}

// AddVersion keeps raw as a version of name (a document of kind, by the
// names KindRunbook etc.), as when a run imported from another runnr brings
// the runbook it ran. It becomes current only if there's no document by
// that name; it says whether it did.
func (s *Store) AddVersion(kind, name string, raw []byte) (bool, error) {
	t, _, err := tables(kind)
	if err != nil {
		return false, err
	}
	var k docKind
	for _, x := range docKinds() {
		if x.table == t {
			k = x
		}
	}
	if n, err := k.parse(raw); err != nil {
		return false, err
	} else if n != name {
		return false, fmt.Errorf("%s %q is named %q", k.noun, name, n)
	}
	id, now := ver.Of(raw), ts(time.Now())
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(s.q(`INSERT INTO `+k.versions+` (name, hash, raw, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (name, hash) DO NOTHING`), name, id, string(raw), now); err != nil {
		return false, err
	}
	res, err := tx.Exec(s.q(`INSERT INTO `+k.table+` (name, raw, hash, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (name) DO NOTHING`), name, string(raw), id, now)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, tx.Commit()
}
