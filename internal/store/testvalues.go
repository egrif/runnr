package store

import (
	"database/sql"
	"encoding/json"
	"errors"
)

func init() {
	schema = append(schema, `CREATE TABLE IF NOT EXISTS runbook_test_values (
		name TEXT PRIMARY KEY,
		data TEXT NOT NULL
	)`)
}

// TestValues are what the editor's "Run this" uses: inputs, extra state
// variables, and whether to run the setup (always) steps first. Secret
// inputs are never stored.
type TestValues struct {
	Vars  map[string]string `json:"vars"`
	Extra map[string]string `json:"extra"`
	Setup bool              `json:"setup"`
}

func (s *Store) GetTestValues(name string) (TestValues, error) {
	tv := TestValues{Vars: map[string]string{}, Extra: map[string]string{}, Setup: true}
	var data string
	err := s.db.QueryRow(s.q(`SELECT data FROM runbook_test_values WHERE name = ?`), name).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return tv, nil
	}
	if err != nil {
		return tv, err
	}
	return tv, json.Unmarshal([]byte(data), &tv)
}

func (s *Store) SetTestValues(name string, tv TestValues) error {
	b, err := json.Marshal(tv)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(s.q(`INSERT INTO runbook_test_values (name, data) VALUES (?, ?)
		ON CONFLICT (name) DO UPDATE SET data = excluded.data`), name, string(b))
	return err
}
