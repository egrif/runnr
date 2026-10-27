package server

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egrif/runnr/internal/placeholder"
	"github.com/egrif/runnr/internal/project"
	"github.com/egrif/runnr/internal/runbook"
	"github.com/egrif/runnr/internal/store"
	"github.com/egrif/runnr/internal/ver"
)

// Importing a file (a runbook or a library) from the GUI:
//
//	POST /api/import/check   (the YAML) → what it is, and whether its name is taken
//	POST /api/import?as=NAME (the YAML) → saves it under NAME (renamed in the YAML)
//	POST /api/import?replace=1          → saves it as a new version of the one with its name
//
// Without as or replace, a name that's taken is refused (409).

type importCheck struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
	// Exists: one by that name is stored (Version, UpdatedAt); Same: and
	// it's identical, so importing changes nothing.
	Exists    bool      `json:"exists"`
	Version   ver.ID    `json:"version,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
	Same      bool      `json:"same,omitempty"`
	// Diff: what replacing the stored one would change (a unified diff).
	Diff string `json:"diff,omitempty"`
	// Builtin: a library runnr ships with that name (the import would
	// take its place).
	Builtin bool `json:"builtin,omitempty"`
	// Placeholders (@NAME@) in the file: kept, and filled in with this
	// machine's folders when it runs.
	Placeholders []string `json:"placeholders,omitempty"`
	Error        string   `json:"error,omitempty"`
}

func (s *Server) checkImport(raw []byte) importCheck {
	c := importCheck{Kind: runbook.DocKind(raw)}
	switch c.Kind {
	case "runbook":
		rb, err := runbook.Parse(raw)
		if err != nil {
			c.Error = err.Error()
			return c
		}
		c.Name = rb.Name
		if d, err := s.st.Load(rb.Name); err == nil {
			c.Exists, c.Version, c.UpdatedAt, c.Same = true, d.Version, d.UpdatedAt, string(d.Raw) == string(raw)
			c.Diff = project.Diff(string(d.Raw), string(raw), fmt.Sprintf("%s (version %s, stored)", rb.Name, d.Version.Short()), "this file", 3)
		}
	case "library":
		lib, err := runbook.ParseLibrary(raw)
		if err != nil {
			c.Error = err.Error()
			return c
		}
		c.Name = lib.Name
		if d, err := s.st.LoadLibrary(lib.Name); err == nil {
			if d.Builtin {
				c.Builtin = true
			} else {
				c.Exists, c.Version, c.UpdatedAt, c.Same = true, d.Version, d.UpdatedAt, string(d.Raw) == string(raw)
				c.Diff = project.Diff(string(d.Raw), string(raw), fmt.Sprintf("%s (version %s, stored)", lib.Name, d.Version.Short()), "this file", 3)
			}
		}
	default:
		c.Error = "this isn't a runbook (it has no steps:) or a library (no functions:)"
	}
	return c
}

func (s *Server) importCheck(w http.ResponseWriter, r *http.Request) {
	raw, err := readBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	c := s.checkImport(raw)
	c.Placeholders = placeholder.Tokens(string(raw))
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) importDoc(w http.ResponseWriter, r *http.Request) {
	raw, err := readBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if as := r.URL.Query().Get("as"); as != "" {
		if raw, err = runbook.SetName(raw, as); err != nil {
			writeErr(w, err)
			return
		}
	}
	c := s.checkImport(raw)
	if c.Error != "" {
		writeErr(w, errors.New(c.Error))
		return
	}
	replace := r.URL.Query().Get("replace") == "1"
	if c.Exists && !replace {
		writeJSON(w, http.StatusConflict, map[string]any{"error": c.Kind + " " + c.Name + " already exists", "check": c})
		return
	}
	o := store.SaveOpts{Overwrite: replace}
	var rev ver.ID
	if c.Kind == "runbook" {
		_, rev, err = s.st.Save(raw, o)
	} else {
		_, rev, err = s.st.SaveLibrary(raw, o)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	res := map[string]any{"kind": c.Kind, "name": c.Name, "version": rev}
	if c.Exists {
		res["replaced"] = c.Version
	}
	if col := r.URL.Query().Get("collection"); col != "" && c.Kind == "runbook" {
		if _, _, err := s.st.AddToCollection(col, true, c.Name); err != nil {
			writeErr(w, err)
			return
		}
		res["collection"] = col
	}
	writeJSON(w, http.StatusCreated, res)
}

// Importing a folder on this machine, as a project (see package project):
//
//	POST /api/import/scan     {dir, renames, include, skip} → the plan
//	POST /api/import/project  {…, replace: [paths] | replace_all, collection} → the plan and what was done

type projectReq struct {
	Dir     string            `json:"dir"`
	Renames map[string]string `json:"renames"`
	Include []string          `json:"include"`
	Skip    []string          `json:"skip"`
	// Replace are the files (paths) to save as the current version of the
	// one with their name; ReplaceAll, every such file.
	Replace    []string `json:"replace"`
	ReplaceAll bool     `json:"replace_all"`
	Collection string   `json:"collection"`
}

func (s *Server) scanProject(req projectReq) (*project.Plan, error) {
	if req.Dir == "" && len(req.Include) == 0 {
		return nil, errors.New("give a folder, or files to import")
	}
	return project.Scan(project.Options{Dir: expandHome(req.Dir), Renames: req.Renames, Include: req.Include, Skip: req.Skip, Lookup: project.StoreLookup(s.st)})
}

func (s *Server) importScan(w http.ResponseWriter, r *http.Request) {
	var req projectReq
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	p, err := s.scanProject(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) importProject(w http.ResponseWriter, r *http.Request) {
	var req projectReq
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	p, err := s.scanProject(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	res := project.Apply(p, func(d project.Doc) bool { return req.ReplaceAll || slices.Contains(req.Replace, d.Path) }, project.StoreSaver(s.st))
	if req.Collection != "" {
		var names []string
		for _, x := range res {
			if x.Kind == project.Runbook && x.Status != "failed" && x.Status != "skipped" {
				names = append(names, x.Name)
			}
		}
		if len(names) > 0 {
			if _, _, err := s.st.AddToCollection(req.Collection, true, names...); err != nil {
				writeErr(w, err)
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"plan": p, "results": res})
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
