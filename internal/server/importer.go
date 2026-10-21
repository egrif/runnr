package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/egrif/runnr/internal/runbook"
	"github.com/egrif/runnr/internal/store"
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
	Version   int       `json:"version,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
	Same      bool      `json:"same,omitempty"`
	// Builtin: a library runnr ships with that name (the import would
	// take its place).
	Builtin bool   `json:"builtin,omitempty"`
	Error   string `json:"error,omitempty"`
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
	writeJSON(w, http.StatusOK, s.checkImport(raw))
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
	var ver int
	if c.Kind == "runbook" {
		_, ver, err = s.st.Save(raw, o)
	} else {
		_, ver, err = s.st.SaveLibrary(raw, o)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	res := map[string]any{"kind": c.Kind, "name": c.Name, "version": ver}
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
