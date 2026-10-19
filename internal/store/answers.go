package store

import (
	"database/sql"
	"errors"
	"time"
)

func init() {
	schema = append(schema, `CREATE TABLE IF NOT EXISTS ai_answers (
		key        TEXT PRIMARY KEY,
		model      TEXT NOT NULL,
		answer     TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`)
}

// GetAnswer returns a stored answer (claude.Cache): the same question
// about the same runbook context, asked of the same model, before.
func (s *Store) GetAnswer(key string) (string, bool) {
	var a string
	err := s.db.QueryRow(s.q(`SELECT answer FROM ai_answers WHERE key = ?`), key).Scan(&a)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return "", false
	}
	return a, true
}

// PutAnswer stores an answer (claude.Cache).
func (s *Store) PutAnswer(key, model, answer string) error {
	_, err := s.db.Exec(s.q(`INSERT INTO ai_answers (key, model, answer, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET model = excluded.model, answer = excluded.answer, created_at = excluded.created_at`),
		key, model, answer, ts(time.Now()))
	return err
}

// PruneAnswers forgets stored answers older than cutoff.
func (s *Store) PruneAnswers(cutoff time.Time) (int64, error) {
	res, err := s.db.Exec(s.q(`DELETE FROM ai_answers WHERE created_at < ?`), ts(cutoff))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
