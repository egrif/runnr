package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/egrif/runnr/internal/placeholder"
)

// A document is stored as written, its placeholders (@PROJECT_DIR@ …)
// left in, so its version is the same wherever it's imported from. Where
// it came from on this machine, and what its placeholders stand for here,
// is kept beside it: they're filled in when it's used.

func init() {
	schema = append(schema, `CREATE TABLE IF NOT EXISTS doc_origins (
		kind   TEXT NOT NULL,
		name   TEXT NOT NULL,
		origin TEXT NOT NULL,
		PRIMARY KEY (kind, name)
	)`)
}

// Origin is where a document was imported from on this machine, and the
// values its placeholders take here.
type Origin struct {
	// Path is the file (absolute); RepoRoot, the git repository it's in;
	// Remote, that repository's origin remote; RepoPath, Path within it.
	Path     string `json:"path,omitempty"`
	RepoRoot string `json:"repo_root,omitempty"`
	Remote   string `json:"remote,omitempty"`
	RepoPath string `json:"repo_path,omitempty"`
	// Values are the placeholders' values, by token (PROJECT_DIR,
	// FILE_DIR|-1).
	Values     map[string]string `json:"values,omitempty"`
	ImportedAt time.Time         `json:"imported_at"`
}

// Origin is where a document of kind (KindRunbook …) came from, or nil.
func (s *Store) Origin(kind, name string) (*Origin, error) {
	var raw string
	err := s.db.QueryRow(s.q(`SELECT origin FROM doc_origins WHERE kind = ? AND name = ?`), kind, name).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var o Origin
	return &o, json.Unmarshal([]byte(raw), &o)
}

func (s *Store) setOrigin(tx *sql.Tx, kind, name string, o *Origin) error {
	b, err := json.Marshal(o)
	if err != nil {
		return err
	}
	_, err = tx.Exec(s.q(`INSERT INTO doc_origins (kind, name, origin) VALUES (?, ?, ?)
		ON CONFLICT (kind, name) DO UPDATE SET origin = excluded.origin`), kind, name, string(b))
	return err
}

// values are the placeholder values for a document: given, else its
// origin's.
func (s *Store) values(kind, name string, given *Origin) map[string]string {
	if given != nil {
		return given.Values
	}
	if o, err := s.Origin(kind, name); err == nil && o != nil {
		return o.Values
	}
	return nil
}

// filled is raw with its placeholders filled in from values; one without
// a value is an error.
func filled(noun, name string, raw []byte, values map[string]string) ([]byte, error) {
	if !placeholder.Re.Match(raw) {
		return raw, nil
	}
	out, _, missing := placeholder.Fill(raw, values)
	if len(missing) > 0 {
		return out, fmt.Errorf("%s %s: placeholders without a value here: @%s@ (import it from its folder, or give them with --set)", noun, name, strings.Join(missing, "@, @"))
	}
	return out, nil
}
