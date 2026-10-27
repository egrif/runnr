package store

import (
	"fmt"
	"strings"

	"github.com/egrif/runnr/internal/placeholder"
)

// Placeholder values: the folders @NAME@ stands for on this machine, for
// everything (scope "") or for a collection's runbooks (scope: its name).
// They're this runnr's own: collections, runbooks and their exports leave
// them out.

func init() {
	schema = append(schema, `CREATE TABLE IF NOT EXISTS placeholder_values (
		scope TEXT NOT NULL,
		name TEXT NOT NULL,
		value TEXT NOT NULL,
		PRIMARY KEY (scope, name)
	)`)
}

// Placeholders are the values set for scope ("" for everything, else a
// collection's name), as given (one can name others).
func (s *Store) Placeholders(scope string) (map[string]string, error) {
	rows, err := s.db.Query(s.q(`SELECT name, value FROM placeholder_values WHERE scope = ?`), scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var n, v string
		if err := rows.Scan(&n, &v); err != nil {
			return nil, err
		}
		out[n] = v
	}
	return out, rows.Err()
}

// AllPlaceholders are every scope's values, by scope.
func (s *Store) AllPlaceholders() (map[string]map[string]string, error) {
	rows, err := s.db.Query(`SELECT scope, name, value FROM placeholder_values`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]string{}
	for rows.Next() {
		var sc, n, v string
		if err := rows.Scan(&sc, &n, &v); err != nil {
			return nil, err
		}
		if out[sc] == nil {
			out[sc] = map[string]string{}
		}
		out[sc][n] = v
	}
	return out, rows.Err()
}

// SetPlaceholder sets name's value for scope; an empty value removes it.
func (s *Store) SetPlaceholder(scope, name, value string) error {
	name = strings.Trim(strings.TrimSpace(name), "@")
	if !placeholder.ValidName(name) {
		return fmt.Errorf("%q can't be a placeholder's name: capitals, digits and _, starting with a capital (REPO_ROOT)", name)
	}
	if value = strings.TrimSpace(value); value == "" {
		_, err := s.db.Exec(s.q(`DELETE FROM placeholder_values WHERE scope = ? AND name = ?`), scope, name)
		return err
	}
	_, err := s.db.Exec(s.q(`INSERT INTO placeholder_values (scope, name, value) VALUES (?, ?, ?)
		ON CONFLICT (scope, name) DO UPDATE SET value = excluded.value`), scope, name, value)
	return err
}

// renamePlaceholderScope moves a collection's values to its new name.
func (s *Store) renamePlaceholderScope(from, to string) error {
	if from == "" || to == "" || from == to {
		return nil
	}
	if _, err := s.db.Exec(s.q(`DELETE FROM placeholder_values WHERE scope = ?`), to); err != nil {
		return err
	}
	_, err := s.db.Exec(s.q(`UPDATE placeholder_values SET scope = ? WHERE scope = ?`), to, from)
	return err
}
