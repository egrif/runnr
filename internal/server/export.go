package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/project"
	"github.com/egrif/runnr/internal/ver"
)

// Exporting from the GUI, to paths on this machine (or as a download):
//
//	POST /api/runbooks/{name}/files/plan  {path}                         → the files it would write
//	POST /api/runbooks/{name}/files       {path, libraries, overwrite}   → writes them
//	GET  /api/runs/{id}/chain                                            → the run and those linked by resuming
//	GET  /api/runs/{id}/logs?ids=&format=raw|json|both&single=1          → a download (a zip for several files)
//	POST /api/runs/{id}/logs  {ids, format, single, path, overwrite}     → writes them into folder path

func (s *Server) exportPlan(w http.ResponseWriter, r *http.Request) {
	var req struct{ Path string }
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	plan, err := project.ExportPlan(s.st, r.PathValue("name"), expandHome(req.Path))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": plan})
}

func (s *Server) exportFiles(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path      string
		Libraries []string
		Overwrite bool
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	out, err := project.Export(s.st, r.PathValue("name"), expandHome(req.Path), req.Libraries, req.Overwrite)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"written": out})
}

type chainRun struct {
	ID      string        `json:"id"`
	Status  engine.Status `json:"status"`
	Started string        `json:"started"`
	Ended   string        `json:"ended,omitempty"`
	Resumes string        `json:"resumes,omitempty"`
	Version ver.ID        `json:"runbook_version,omitempty"`
	Steps   int           `json:"steps"`
}

func (s *Server) runChain(w http.ResponseWriter, r *http.Request) {
	runs, err := project.Chain(s.st, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	out := []chainRun{}
	for _, x := range runs {
		c := chainRun{ID: x.ID, Status: x.Status, Started: x.Started.Format("2006-01-02T15:04:05Z07:00"), Resumes: x.Resumes, Version: x.RunbookVersion, Steps: len(x.Steps)}
		if !x.Ended.IsZero() {
			c.Ended = x.Ended.Format("2006-01-02T15:04:05Z07:00")
		}
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, out)
}

// logFiles renders the chosen runs of id's chain.
func (s *Server) logFiles(id string, ids []string, format string, single bool) ([]project.LogFile, error) {
	chain, err := project.Chain(s.st, id)
	if err != nil {
		return nil, err
	}
	var runs []*engine.Run
	for _, x := range chain {
		if len(ids) == 0 || contains(ids, x.ID) {
			runs = append(runs, x)
		}
	}
	return project.LogFiles(project.WithDefinitions(s.st, project.Masked(s.st, runs)), format, single, "")
}

func (s *Server) downloadLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var ids []string
	if v := q.Get("ids"); v != "" {
		ids = strings.Split(v, ",")
	}
	files, err := s.logFiles(r.PathValue("id"), ids, q.Get("format"), q.Get("single") == "1")
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(files) == 1 {
		ct := "text/plain; charset=utf-8"
		if strings.HasSuffix(files[0].Name, ".json") {
			ct = "application/json"
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", files[0].Name))
		w.Write(files[0].Data)
		return
	}
	z, err := project.Zip(files)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "runs-"+r.PathValue("id")+".zip"))
	w.Write(z)
}

func (s *Server) writeLogs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs       []string `json:"ids"`
		Format    string   `json:"format"`
		Single    bool     `json:"single"`
		Path      string   `json:"path"`
		Overwrite bool     `json:"overwrite"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	files, err := s.logFiles(r.PathValue("id"), req.IDs, req.Format, req.Single)
	if err != nil {
		writeErr(w, err)
		return
	}
	paths, err := project.WriteLogFiles(files, expandHome(req.Path), req.Overwrite)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"written": paths})
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
