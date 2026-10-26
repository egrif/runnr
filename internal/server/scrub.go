package server

import (
	"net/http"

	"github.com/egrif/runnr/internal/project"
)

// scrubRuns masks credentials in stored runs (the history page's Scrub
// credentials…; runnr history --scrub).
//
//	POST /api/runs/scrub {ids?, check?} → {runs: [{id, runbook, record, notes, asks, running?}], compacted}
func (s *Server) scrubRuns(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs   []string `json:"ids"`
		Check bool     `json:"check"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	res, err := project.Scrub(s.st, req.IDs, req.Check)
	if err != nil {
		writeErr(w, err)
		return
	}
	type row struct {
		ID      string `json:"id"`
		Runbook string `json:"runbook"`
		Record  bool   `json:"record,omitempty"`
		Notes   int    `json:"notes,omitempty"`
		Asks    int    `json:"asks,omitempty"`
		Running bool   `json:"running,omitempty"`
	}
	out := []row{}
	changed := false
	for _, x := range res {
		if x.Changed() || x.Err != nil {
			out = append(out, row{x.ID, x.Runbook, x.Record, x.Notes, x.Asks, x.Err != nil})
		}
		changed = changed || x.Changed()
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": out, "total": len(res), "compacted": changed && !req.Check})
}
