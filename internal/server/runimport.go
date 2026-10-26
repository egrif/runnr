package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/egrif/runnr/internal/project"
)

// Importing runs (the history page's ⇪ Import runs…): exported run
// records from files on this machine (paths) or sent from the browser
// (uploads), checked first, then saved.
//
//	POST /api/runs/import {paths, uploads: [{name, data (base64)}], ids, check}
//	  → {runs: [project.RunImport], errors: [{from, error}]}
type runImportReq struct {
	Paths   []string `json:"paths"`
	Uploads []struct {
		Name string `json:"name"`
		Data []byte `json:"data"`
	} `json:"uploads"`
	// IDs: only these (from a check); all of them if none.
	IDs   []string `json:"ids"`
	Check bool     `json:"check"`
}

func (s *Server) importRuns(w http.ResponseWriter, r *http.Request) {
	var req runImportReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 512<<20)).Decode(&req); err != nil {
		writeErr(w, err)
		return
	}
	files, bad := project.ReadRunPaths(req.Paths)
	for _, u := range req.Uploads {
		rs, errs := project.ReadRuns(u.Name, u.Data)
		files, bad = append(files, rs...), append(bad, errs...)
	}
	if len(req.Paths)+len(req.Uploads) == 0 {
		writeErr(w, errors.New("no files to import"))
		return
	}
	out, err := project.ImportRuns(s.st, files, req.IDs, req.Check)
	if err != nil {
		writeErr(w, err)
		return
	}
	if out == nil {
		out = []project.RunImport{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": out, "errors": bad})
}
