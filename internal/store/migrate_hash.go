package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/egrif/runnr/internal/ver"
)

// Versions used to be numbers counted per name (1, 2, 3 …), which meant
// nothing outside the database that counted them. A database from then is
// converted once, when it's opened: each kind's tables are rebuilt keyed by
// hash (the same text saved twice becomes one version, first saved at its
// earliest), and run records' version numbers become the hashes of what
// they ran. A SQLite file is copied aside first.
func (s *Store) migrateHashes(ctx context.Context) error {
	var old []docKind
	for _, k := range docKinds() {
		if !s.hasColumn(ctx, k.table, "hash") {
			old = append(old, k)
		}
	}
	if len(old) == 0 {
		return nil
	}
	if !s.postgres {
		backup := fmt.Sprintf("%s.before-hashes-%s", s.Location, time.Now().Format("20060102-150405"))
		if _, err := s.db.ExecContext(ctx, `VACUUM INTO '`+strings.ReplaceAll(backup, `'`, `''`)+`'`); err != nil {
			return fmt.Errorf("copying the database aside before converting its versions: %w", err)
		}
		_ = os.Chmod(backup, 0o600)
		fmt.Fprintf(os.Stderr, "runnr: versions are hashes now; converting %s (a copy from before is %s)\n", s.Location, backup)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var runbookHashes map[string]map[int64]ver.ID
	for _, k := range old {
		hashes, err := s.rekey(ctx, tx, k)
		if err != nil {
			return fmt.Errorf("converting %s: %w", k.table, err)
		}
		if k.table == runbookKind.table {
			runbookHashes = hashes
		}
	}
	if runbookHashes != nil {
		if err := convertRuns(ctx, tx, s, runbookHashes); err != nil {
			return fmt.Errorf("converting runs: %w", err)
		}
	}
	return tx.Commit()
}

func (s *Store) hasColumn(ctx context.Context, table, col string) bool {
	var x any
	err := s.db.QueryRowContext(ctx, `SELECT `+col+` FROM `+table+` WHERE 1 = 0`).Scan(&x)
	return err == sql.ErrNoRows
}

// rekey rebuilds a kind's tables keyed by hash, returning each name's old
// version numbers' hashes.
func (s *Store) rekey(ctx context.Context, tx *sql.Tx, k docKind) (map[string]map[int64]ver.ID, error) {
	tmp := docKind{table: k.table + "_rekey", versions: k.versions + "_rekey"}
	for _, q := range docSchema(tmp) {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return nil, err
		}
	}
	hashes := map[string]map[int64]ver.ID{}
	rows, err := tx.QueryContext(ctx, `SELECT name, version, raw, created_at FROM `+k.versions+` ORDER BY name, version`)
	if err != nil {
		return nil, err
	}
	type row struct {
		name, raw, created string
		v                  int64
	}
	var vs []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.name, &r.v, &r.raw, &r.created); err != nil {
			rows.Close()
			return nil, err
		}
		vs = append(vs, r)
	}
	rows.Close()
	for _, r := range vs {
		id := ver.Of([]byte(r.raw))
		if hashes[r.name] == nil {
			hashes[r.name] = map[int64]ver.ID{}
		}
		hashes[r.name][r.v] = id
		// In version order, so text saved more than once keeps its first save.
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO `+tmp.versions+` (name, hash, raw, created_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (name, hash) DO NOTHING`), r.name, id, r.raw, r.created); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+tmp.table+` (name, raw, hash, updated_at) SELECT name, raw, '', updated_at FROM `+k.table); err != nil {
		return nil, err
	}
	cur, err := tx.QueryContext(ctx, `SELECT name, raw FROM `+tmp.table)
	if err != nil {
		return nil, err
	}
	current := map[string]string{}
	for cur.Next() {
		var n, raw string
		if err := cur.Scan(&n, &raw); err != nil {
			cur.Close()
			return nil, err
		}
		current[n] = raw
	}
	cur.Close()
	for n, raw := range current {
		if _, err := tx.ExecContext(ctx, s.q(`UPDATE `+tmp.table+` SET hash = ? WHERE name = ?`), ver.Of([]byte(raw)), n); err != nil {
			return nil, err
		}
	}
	for _, q := range []string{
		`DROP TABLE ` + k.table, `DROP TABLE ` + k.versions,
		`ALTER TABLE ` + tmp.table + ` RENAME TO ` + k.table,
		`ALTER TABLE ` + tmp.versions + ` RENAME TO ` + k.versions,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return nil, err
		}
	}
	return hashes, nil
}

// convertRuns replaces the version numbers in run records with the hashes
// of what those numbers were. A number with no saved version behind it
// (a run imported from another runnr) is dropped: it's not known.
func convertRuns(ctx context.Context, tx *sql.Tx, s *Store, hashes map[string]map[int64]ver.ID) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, record FROM runs`)
	if err != nil {
		return err
	}
	recs := map[string]string{}
	for rows.Next() {
		var id, rec string
		if err := rows.Scan(&id, &rec); err != nil {
			rows.Close()
			return err
		}
		recs[id] = rec
	}
	rows.Close()
	for id, rec := range recs {
		out, changed, err := convertRecord([]byte(rec), hashes)
		if err != nil || !changed {
			continue // not a record we can read: leave it as it is
		}
		if _, err := tx.ExecContext(ctx, s.q(`UPDATE runs SET record = ? WHERE id = ?`), string(out), id); err != nil {
			return err
		}
	}
	return nil
}

func convertRecord(rec []byte, hashes map[string]map[int64]ver.ID) ([]byte, bool, error) {
	d := json.NewDecoder(bytes.NewReader(rec))
	d.UseNumber()
	var r map[string]any
	if err := d.Decode(&r); err != nil {
		return nil, false, err
	}
	name, _ := r["runbook"].(string)
	changed := false
	fix := func(m map[string]any, key string) {
		n, ok := m[key].(json.Number)
		if !ok {
			return
		}
		changed = true
		v, _ := n.Int64()
		if id, ok := hashes[name][v]; ok {
			m[key] = string(id)
		} else {
			delete(m, key)
		}
	}
	fix(r, "runbook_version")
	fix(r, "edits_saved")
	if rs, ok := r["resumptions"].([]any); ok {
		for _, x := range rs {
			if m, ok := x.(map[string]any); ok {
				fix(m, "runbook_version")
				fix(m, "was_version")
			}
		}
	}
	if !changed {
		return rec, false, nil
	}
	out, err := json.Marshal(r)
	return out, true, err
}
