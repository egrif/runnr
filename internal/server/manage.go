package server

import (
	"net/http"
	"time"

	"github.com/egrif/runnr/internal/runtest"
	"github.com/egrif/runnr/internal/store"
)

// Managing what's stored: everything by kind, what uses each, deleting
// (several at once) and restoring what was deleted (its versions are
// kept).
//
//	GET  /api/manage           everything stored, what uses it, and what's deleted
//	POST /api/manage/delete    {items: [{kind, name}]}
//	POST /api/manage/restore   {items: [{kind, name}]}
//	DELETE /api/runs/{id}      a finished run's record

type manageItem struct {
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Version   int       `json:"version,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
	// UsedBy are what refer to it: runbooks using a library, suites of a
	// runbook or using fakes, collections holding a runbook.
	UsedBy []ref `json:"used_by,omitempty"`
	// Schedules: scheduled runs of a runbook (deleting it removes them).
	Schedules int    `json:"schedules,omitempty"`
	Builtin   bool   `json:"builtin,omitempty"`
	Error     string `json:"error,omitempty"`
}

type ref struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

func (s *Server) manageList() ([]manageItem, error) {
	var items []manageItem
	used := map[ref][]ref{}
	rbs, err := s.st.List()
	if err != nil {
		return nil, err
	}
	for _, e := range rbs {
		it := manageItem{Kind: store.KindRunbook, Name: e.Name, Version: e.Version, UpdatedAt: e.UpdatedAt, Error: e.Err}
		if d, err := s.st.Load(e.Name); err == nil && d.Runbook != nil {
			for _, l := range d.Runbook.Libraries {
				k := ref{store.KindLibrary, l}
				used[k] = append(used[k], ref{store.KindRunbook, e.Name})
			}
		}
		items = append(items, it)
	}
	libs, err := s.st.ListLibraries()
	if err != nil {
		return nil, err
	}
	for _, l := range libs {
		items = append(items, manageItem{Kind: store.KindLibrary, Name: l.Name, Version: l.Version, UpdatedAt: l.UpdatedAt, Builtin: l.Builtin, Error: l.Err})
	}
	fakes, err := s.st.ListFakes()
	if err != nil {
		return nil, err
	}
	for _, f := range fakes {
		items = append(items, manageItem{Kind: store.KindFakes, Name: f.Name, Version: f.Version, UpdatedAt: f.UpdatedAt})
	}
	suites, err := s.st.ListSuites()
	if err != nil {
		return nil, err
	}
	for _, su := range suites {
		items = append(items, manageItem{Kind: store.KindSuite, Name: su.Name, Version: su.Version, UpdatedAt: su.UpdatedAt})
		if d, err := s.st.LoadSuite(su.Name); err == nil {
			if spec, err := runtest.ParseSuite(d.Raw); err == nil {
				k := ref{store.KindRunbook, spec.Runbook}
				used[k] = append(used[k], ref{store.KindSuite, su.Name})
				for _, u := range spec.Uses {
					k := ref{store.KindFakes, u}
					used[k] = append(used[k], ref{store.KindSuite, su.Name})
				}
			}
		}
	}
	cols, err := s.st.ListCollections()
	if err != nil {
		return nil, err
	}
	for _, c := range cols {
		items = append(items, manageItem{Kind: store.KindCollection, Name: c.Name, Version: c.Version, UpdatedAt: c.UpdatedAt, Error: c.Err})
		if c.Collection != nil {
			for _, rb := range c.Collection.Runbooks {
				k := ref{store.KindRunbook, rb}
				used[k] = append(used[k], ref{store.KindCollection, c.Name})
			}
		}
	}
	scheds, _ := s.st.ListSchedules(time.Now())
	for i := range items {
		items[i].UsedBy = used[ref{items[i].Kind, items[i].Name}]
		if items[i].Kind == store.KindRunbook {
			for _, sc := range scheds {
				if sc.Runbook == items[i].Name {
					items[i].Schedules++
				}
			}
		}
	}
	return items, nil
}

func (s *Server) getManage(w http.ResponseWriter, r *http.Request) {
	items, err := s.manageList()
	if err != nil {
		writeErr(w, err)
		return
	}
	deleted := []store.Deleted{}
	for _, k := range []string{store.KindRunbook, store.KindLibrary, store.KindFakes, store.KindSuite, store.KindCollection} {
		d, err := s.st.ListDeleted(k)
		if err != nil {
			writeErr(w, err)
			return
		}
		deleted = append(deleted, d...)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "deleted": deleted})
}

type manageResult struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Version int    `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
	// Also: what went with it (a runbook's place in collections, its
	// scheduled runs).
	Also []string `json:"also,omitempty"`
}

// manageDelete deletes items. A runbook also leaves the collections it's
// in, and its scheduled runs are removed (they couldn't run).
func (s *Server) manageDelete(w http.ResponseWriter, r *http.Request) {
	var req struct{ Items []ref }
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	var out []manageResult
	for _, it := range req.Items {
		res := manageResult{Kind: it.Kind, Name: it.Name}
		var err error
		if it.Kind == store.KindRunbook {
			res.Also, err = s.st.DeleteRunbook(it.Name)
		} else {
			err = s.st.DeleteDoc(it.Kind, it.Name)
		}
		if err != nil {
			res.Error = err.Error()
		} else {
			res.OK = true
		}
		out = append(out, res)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

func (s *Server) manageRestore(w http.ResponseWriter, r *http.Request) {
	var req struct{ Items []ref }
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	var out []manageResult
	for _, it := range req.Items {
		res := manageResult{Kind: it.Kind, Name: it.Name}
		if v, err := s.st.Restore(it.Kind, it.Name); err != nil {
			res.Error = err.Error()
		} else {
			res.OK, res.Version = true, v
		}
		out = append(out, res)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

func (s *Server) deleteRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if l := s.getLive(id); l != nil {
		l.mu.Lock()
		done := l.done
		l.mu.Unlock()
		if !done {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "the run is still going: abort it first"})
			return
		}
	}
	if err := s.st.DeleteRun(id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
