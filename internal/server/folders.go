package server

import (
	"maps"
	"net/http"
	"strings"

	"github.com/egrif/runnr/internal/placeholder"
	"github.com/egrif/runnr/internal/project"
)

// The folders placeholders (@REPOS_ROOT@, @REPO_ROOT@, @PROJECT_DIR@ …)
// stand for on this machine, for everything and for each collection:
//
//	GET /api/folders                    → {scopes: {"" | collection: {NAME: folderValue}}}
//	PUT /api/folders {collection, name, value}  (value "" removes it) → the same
//	GET /api/runbooks/{name}/folders?collection=&set=NAME=FOLDER → project.Folders

type folderValue struct {
	Raw   string `json:"raw"`           // as set (it can name others)
	Value string `json:"value"`         // filled in
	Why   string `json:"why,omitempty"` // why it can't be used here
}

func (s *Server) listFolders(w http.ResponseWriter, _ *http.Request) {
	all, err := s.st.AllPlaceholders()
	if err != nil {
		writeErr(w, err)
		return
	}
	out := map[string]map[string]folderValue{"": {}}
	for scope, vals := range all {
		ctx := maps.Clone(all[""])
		if ctx == nil {
			ctx = map[string]string{}
		}
		maps.Copy(ctx, vals)
		full, err := placeholder.Expand(ctx)
		if err != nil {
			writeErr(w, err)
			return
		}
		out[scope] = map[string]folderValue{}
		for n, raw := range vals {
			fv := folderValue{Raw: raw, Value: full[n]}
			if p := placeholder.Check([]string{n}, full); len(p) > 0 {
				fv.Why = p[0].Why
			}
			out[scope][n] = fv
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"scopes": out})
}

func (s *Server) setFolder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Collection string `json:"collection"`
		Name       string `json:"name"`
		Value      string `json:"value"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.Collection != "" {
		if _, err := s.st.LoadCollection(req.Collection); err != nil {
			writeErr(w, err)
			return
		}
	}
	if err := s.st.SetPlaceholder(req.Collection, req.Name, req.Value); err != nil {
		writeErr(w, err)
		return
	}
	s.listFolders(w, r)
}

// runbookFolders says what a run of the runbook would fill its
// placeholders with (and whether it can start).
func (s *Server) runbookFolders(w http.ResponseWriter, r *http.Request) {
	rb, _, err := s.st.Get(r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	libs, err := s.st.Libraries(rb.Libraries)
	if err != nil {
		writeErr(w, err)
		return
	}
	q := r.URL.Query()
	o := project.FolderOpts{Collection: q.Get("collection"), Set: map[string]string{}}
	for _, kv := range q["set"] {
		if k, v, ok := strings.Cut(kv, "="); ok && v != "" {
			o.Set[k] = v
		}
	}
	f, err := project.ResolveFolders(s.st, rb.Name, placeholder.Used(rb, libs), o)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}
