package project

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/store"
)

// Importing runs: the records an export writes (--as json, or the zip the
// GUI downloads), from another runnr or a backup. A raw log is for reading
// and can't be imported: it doesn't keep enough.

// RunFile is a run read from a file to import.
type RunFile struct {
	Run  *engine.Run
	From string // the file (and the file in a zip) it came from
}

// RunFileError is a file (or part of one) that isn't run records.
type RunFileError struct {
	From  string `json:"from"`
	Error string `json:"error"`
}

var runIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ReadRuns reads the run records in a file: a run, a list of runs, or a
// zip of such files (a zip's other files are left out).
func ReadRuns(name string, data []byte) ([]RunFile, []RunFileError) {
	if bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, []RunFileError{{name, "not a zip that can be read: " + err.Error()}}
		}
		var out []RunFile
		var bad []RunFileError
		for _, f := range zr.File {
			if f.FileInfo().IsDir() || !strings.EqualFold(path.Ext(f.Name), ".json") {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				bad = append(bad, RunFileError{name + ": " + f.Name, err.Error()})
				continue
			}
			b, err := io.ReadAll(io.LimitReader(rc, 256<<20))
			rc.Close()
			if err != nil {
				bad = append(bad, RunFileError{name + ": " + f.Name, err.Error()})
				continue
			}
			rs, errs := ReadRuns(name+": "+f.Name, b)
			out, bad = append(out, rs...), append(bad, errs...)
		}
		if len(out) == 0 && len(bad) == 0 {
			bad = append(bad, RunFileError{name, "no run records (.json) in the zip"})
		}
		return out, bad
	}
	t := bytes.TrimSpace(data)
	var runs []*engine.Run
	var err error
	switch {
	case bytes.HasPrefix(t, []byte("[")):
		err = json.Unmarshal(t, &runs)
	case bytes.HasPrefix(t, []byte("{")):
		var r engine.Run
		err = json.Unmarshal(t, &r)
		runs = []*engine.Run{&r}
	case bytes.HasPrefix(t, []byte("run ")) || bytes.HasPrefix(t, []byte("════")):
		return nil, []RunFileError{{name, "a raw log, which can't be imported: export the run as records (JSON) instead"}}
	default:
		return nil, []RunFileError{{name, "not run records (an export's .json, or a zip of them)"}}
	}
	if err != nil {
		return nil, []RunFileError{{name, "not run records: " + err.Error()}}
	}
	var out []RunFile
	var bad []RunFileError
	for _, r := range runs {
		switch {
		case r == nil || r.ID == "" || r.Runbook == "" || r.Started.IsZero():
			bad = append(bad, RunFileError{name, "not a run record (it needs an id, a runbook and when it started)"})
		case !runIDRe.MatchString(r.ID):
			bad = append(bad, RunFileError{name, fmt.Sprintf("run id %q isn't one runnr makes", r.ID)})
		default:
			out = append(out, RunFile{r, name})
		}
	}
	return out, bad
}

// ReadRunPaths reads the run records in files, and in folders (their .json
// and .zip files, as an export writes them).
func ReadRunPaths(paths []string) ([]RunFile, []RunFileError) {
	var files []RunFile
	bad := []RunFileError{}
	add := func(name string) {
		b, err := os.ReadFile(name)
		if err != nil {
			bad = append(bad, RunFileError{name, err.Error()})
			return
		}
		rs, errs := ReadRuns(name, b)
		files, bad = append(files, rs...), append(bad, errs...)
	}
	for _, p := range paths {
		if strings.HasPrefix(p, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				p = filepath.Join(home, p[2:])
			}
		}
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			js, _ := filepath.Glob(filepath.Join(p, "*.json"))
			zs, _ := filepath.Glob(filepath.Join(p, "*.zip"))
			if len(js)+len(zs) == 0 {
				bad = append(bad, RunFileError{p, "no .json or .zip files in this folder"})
			}
			for _, f := range append(js, zs...) {
				add(f)
			}
			continue
		}
		add(p)
	}
	return files, bad
}

// RunImport is what importing a run does (or did).
type RunImport struct {
	ID      string        `json:"id"`
	Runbook string        `json:"runbook"`
	Status  engine.Status `json:"status"`
	Started time.Time     `json:"started"`
	Steps   int           `json:"steps"`
	From    string        `json:"from"`
	// Exists: a run with its id is here already (left as it is).
	Exists bool `json:"exists,omitempty"`
	// Unfinished: it was still going when exported; it's imported aborted.
	Unfinished bool `json:"unfinished,omitempty"`
	Imported   bool `json:"imported,omitempty"`
}

// unfinished statuses: the run was still going where it was exported from.
func unfinished(s engine.Status) bool {
	return s == engine.StatusRunning || s == engine.StatusPending || s == engine.StatusBackground || s == ""
}

// ImportRuns saves runs that aren't here yet (only those in ids, if
// given); with dry, it only says what it would do. A run imported while
// it was still going is saved aborted (nothing here is running it), and
// every imported run says when it was imported.
func ImportRuns(st *store.Store, files []RunFile, ids []string, dry bool) ([]RunImport, error) {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	seen := map[string]bool{}
	var out []RunImport
	for _, f := range files {
		r := f.Run
		if seen[r.ID] || len(want) > 0 && !want[r.ID] {
			continue
		}
		seen[r.ID] = true
		ri := RunImport{ID: r.ID, Runbook: r.Runbook, Status: r.Status, Started: r.Started, Steps: len(r.Steps), From: f.From, Unfinished: unfinished(r.Status)}
		if _, err := st.LoadRun(r.ID); err == nil {
			ri.Exists = true
		} else if !errors.Is(err, store.ErrNotFound) {
			return out, err
		}
		if !ri.Exists && !dry {
			if ri.Unfinished {
				markUnfinished(r)
			}
			r.Imported = time.Now()
			r.Owner = nil
			if err := st.SaveRun(r); err != nil {
				return out, fmt.Errorf("run %s: %w", r.ID, err)
			}
			ri.Imported = true
		}
		out = append(out, ri)
	}
	return out, nil
}

// markUnfinished ends a run that was still going where it was exported.
func markUnfinished(r *engine.Run) {
	r.Status = engine.StatusAborted
	if r.Error == "" {
		r.Error = "imported while it was still running"
	}
	for i := range r.Steps {
		if s := r.Steps[i].Status; s == engine.StatusRunning || s == engine.StatusBackground {
			r.Steps[i].Status = engine.StatusAborted
		}
	}
	if r.Ended.IsZero() {
		r.Ended = r.Started
		for _, s := range r.Steps {
			if s.Ended.After(r.Ended) {
				r.Ended = s.Ended
			}
			if s.Started.After(r.Ended) {
				r.Ended = s.Started
			}
		}
	}
}
