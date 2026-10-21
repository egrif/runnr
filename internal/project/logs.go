package project

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/store"
)

// Exporting runs: a run with the runs it's linked to by resuming (the run
// it resumed, back to the first attempt, and the runs that resumed it), as
// raw logs to read, records (JSON) to import elsewhere, or both; in one
// file or one file per run.

// Chain is run id with the runs linked to it by resuming, oldest first.
func Chain(st *store.Store, id string) ([]*engine.Run, error) {
	r, err := st.LoadRun(id)
	if err != nil {
		return nil, err
	}
	all, err := st.ListRuns(r.Runbook, 10000)
	if err != nil {
		return nil, err
	}
	resumedBy := map[string][]string{}
	for _, x := range all {
		if x.Resumes != "" {
			resumedBy[x.Resumes] = append(resumedBy[x.Resumes], x.ID)
		}
	}
	seen := map[string]bool{}
	var ids []string
	var walk func(string)
	walk = func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		ids = append(ids, id)
		if x, err := st.LoadRun(id); err == nil {
			walk(x.Resumes)
		}
		for _, n := range resumedBy[id] {
			walk(n)
		}
	}
	walk(id)
	var out []*engine.Run
	for _, i := range ids {
		if x, err := st.LoadRun(i); err == nil {
			out = append(out, x)
		}
	}
	slices.SortFunc(out, func(a, b *engine.Run) int { return a.Started.Compare(b.Started) })
	return out, nil
}

// LogFile is a file of an export of runs.
type LogFile struct {
	Name string
	Data []byte
}

// LogFiles renders runs: format raw (logs to read), json (records to
// import) or both; single puts them in one file per format (raw logs one
// after another; records as a JSON list), else one file per run.
func LogFiles(runs []*engine.Run, format string, single bool, base string) ([]LogFile, error) {
	if len(runs) == 0 {
		return nil, errors.New("no runs to export")
	}
	raw, js := format == "raw" || format == "both" || format == "", format == "json" || format == "both"
	if !raw && !js {
		return nil, fmt.Errorf("format %q: raw, json or both", format)
	}
	if base == "" {
		base = runs[0].Runbook + "-" + runs[len(runs)-1].ID
	}
	var out []LogFile
	if single {
		if raw {
			var b strings.Builder
			for i, r := range runs {
				if i > 0 {
					b.WriteString("\n\n")
				}
				fmt.Fprintf(&b, "════ %d of %d: run %s ════\n", i+1, len(runs), r.ID)
				b.WriteString(engine.RawLog(r))
			}
			out = append(out, LogFile{base + ".log", []byte(b.String())})
		}
		if js {
			b, err := json.MarshalIndent(runs, "", "  ")
			if err != nil {
				return nil, err
			}
			out = append(out, LogFile{base + ".json", b})
		}
		return out, nil
	}
	for _, r := range runs {
		if raw {
			out = append(out, LogFile{"run-" + r.ID + ".log", []byte(engine.RawLog(r))})
		}
		if js {
			b, err := json.MarshalIndent(r, "", "  ")
			if err != nil {
				return nil, err
			}
			out = append(out, LogFile{"run-" + r.ID + ".json", b})
		}
	}
	return out, nil
}

// WriteLogFiles writes files into folder dir (made if need be). Files
// already there are written over only with overwrite.
func WriteLogFiles(files []LogFile, dir string, overwrite bool) ([]string, error) {
	dir = expand(dir)
	if dir == "" {
		return nil, errors.New("give a folder to export to")
	}
	var there, paths []string
	for _, f := range files {
		p := filepath.Join(dir, f.Name)
		paths = append(paths, p)
		if _, err := os.Stat(p); err == nil && !overwrite {
			there = append(there, p)
		}
	}
	if len(there) > 0 {
		return nil, fmt.Errorf("already there (write over them, or choose another folder): %s", strings.Join(there, ", "))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	for i, f := range files {
		if err := os.WriteFile(paths[i], f.Data, 0o644); err != nil {
			return nil, err
		}
	}
	return paths, nil
}

// Zip puts files in a zip archive.
func Zip(files []LogFile) ([]byte, error) {
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := z.Create(f.Name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(f.Data); err != nil {
			return nil, err
		}
	}
	err := z.Close()
	return buf.Bytes(), err
}
