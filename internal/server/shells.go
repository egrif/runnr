package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/runbook"
	"github.com/egrif/runnr/internal/store"
)

// Shells opened from the console (its Shell panes) are sessions of their
// own, not the run's, so one can open before a run has begun and stay open
// after it ends. Each has its own event stream (term_start, term,
// term_end) and takes keystrokes like a run's terminal:
//
//	POST /api/shells               {run, step} or {runbook, vars}
//	GET  /api/shells/{id}/events
//	POST /api/shells/{id}/term     {id, data, rows, cols, end}
//
// While the run's steps are going, the shell is alongside the run: what it
// publishes reaches the run at its next step, and its transcript is
// recorded with the step. Otherwise it has the runbook's environment (the
// inputs given, else their defaults; after a run, its inputs and values),
// and what it publishes goes nowhere.

type shellReq struct {
	// Run is the run it's for; Step (1-based; 0: the step the run is on)
	// is where it opens.
	Run  string `json:"run"`
	Step int    `json:"step"`
	// Before a run: the runbook and the inputs as filled in so far.
	Runbook string            `json:"runbook"`
	Vars    map[string]string `json:"vars"`
}

func (s *Server) openShell(w http.ResponseWriter, r *http.Request) {
	var req shellReq
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	id := "sh-" + randHex(6)
	hub := newLiveRun(id, nil)
	ctx, cancel := context.WithCancel(context.Background())
	var open func(engine.TermConn) error
	var title, phase string
	idx := req.Step - 1

	if req.Run != "" {
		if l := s.getLive(req.Run); l != nil && l.session != nil {
			// A shell alongside it can change it: that's driving it.
			if !s.allowed(w, r, l) {
				cancel()
				return
			}
			as, ok := l.session.(engine.AsideSheller)
			if rec, err := s.st.LoadRun(req.Run); ok && err == nil && rec.HasBegun() && rec.Status == engine.StatusRunning && rec.Ended.IsZero() {
				if req.Step <= 0 {
					idx = -1
					if ed, ok := l.session.(engine.Editor); ok {
						idx = ed.Current()
					}
				}
				if idx >= len(rec.Steps) {
					idx = -1
				}
				hub.session = l.session
				phase, title = "alongside", "shell alongside the run"
				if idx >= 0 {
					title += fmt.Sprintf(" · step %d: %s", idx+1, rec.Steps[idx].Name)
				}
				open = func(c engine.TermConn) error { return as.ShellAside(ctx, idx, c) }
			}
		}
	}
	if open == nil {
		spec := engine.ShellSpec{Step: idx, Vars: req.Vars}
		var err error
		if req.Run != "" {
			// A run not under way: before its steps (settling its inputs) or ended.
			var rec *engine.Run
			if rec, err = s.st.LoadRun(req.Run); err == nil {
				spec.Runbook, err = s.runbookOf(rec)
				spec.Vars, spec.RunID = engine.ResumeInputs(rec), rec.ID
				if rec.Status != engine.StatusRunning || !rec.Ended.IsZero() {
					spec.State, phase = engine.ResumeState(rec), "after"
				} else {
					phase = "before"
				}
			}
		} else {
			spec.Runbook, _, err = s.st.Get(req.Runbook)
			phase = "before"
		}
		if err == nil {
			spec.Options.Libraries, err = s.st.Libraries(spec.Runbook.Libraries)
		}
		if err != nil {
			cancel()
			writeErr(w, err)
			return
		}
		if spec.Step >= len(spec.Runbook.Steps) {
			spec.Step, idx = -1, -1
		}
		title = "shell " + map[string]string{"before": "before the run", "after": "after the run"}[phase]
		if idx >= 0 {
			title += fmt.Sprintf(" · step %d: %s", idx+1, spec.Runbook.Steps[idx].Name)
		}
		p := engine.NewPreviewer(spec.Runbook, spec.Vars, spec.State, false)
		hub.mask = p.Mask
		hub.funcs = shellFunctions(spec.Runbook, spec.Options.Libraries)
		if idx >= 0 {
			hub.view = func(int) engine.StepView { return p.Plan()[idx] }
		}
		open = func(c engine.TermConn) error { _, err := engine.OpenShell(ctx, spec, c); return err }
	}

	s.mu.Lock()
	if s.shells == nil {
		s.shells = map[string]*liveRun{}
	}
	s.shells[id] = hub
	s.mu.Unlock()
	conn := hub.startTerm(idx, title, false)
	go func() {
		defer cancel()
		if err := open(conn); err != nil {
			hub.Notify(engine.Event{Kind: engine.EventOutput, Step: idx, Stream: "info", Text: "shell: " + err.Error()})
		}
		conn.Close()
		// Keep its events a little while for a late look.
		time.AfterFunc(time.Minute, func() {
			s.mu.Lock()
			delete(s.shells, id)
			s.mu.Unlock()
		})
	}()
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "title": title, "step": idx + 1, "phase": phase})
}

func (s *Server) getShell(id string) (*liveRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l := s.shells[id]; l != nil {
		return l, nil
	}
	return nil, fmt.Errorf("shell %q has ended: %w", id, store.ErrNotFound)
}

func (s *Server) shellEvents(w http.ResponseWriter, r *http.Request) {
	l, err := s.getShell(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	streamEvents(w, r, l)
}

func (s *Server) shellInput(w http.ResponseWriter, r *http.Request) {
	l, err := s.getShell(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	termTo(w, r, l)
}

// runbookOf is the runbook version a run ran.
func (s *Server) runbookOf(rec *engine.Run) (*runbook.Runbook, error) {
	if rec.RunbookVersion != "" {
		if raw, _, err := s.st.RawVersion(rec.Runbook, string(rec.RunbookVersion)); err == nil {
			return runbook.Parse(raw)
		}
	}
	rb, _, err := s.st.Get(rec.Runbook)
	if err != nil {
		return nil, errors.New("its runbook is gone: " + err.Error())
	}
	return rb, nil
}

// shellFunctions lists the functions a shell outside a run can call.
func shellFunctions(rb *runbook.Runbook, libs []*runbook.Library) []engine.FuncInfo {
	all := map[string]engine.FuncInfo{}
	for _, l := range libs {
		for n, f := range l.Functions {
			all[n] = engine.FuncInfo{Name: n, Description: f.Description, From: l.Name}
		}
	}
	for n, f := range rb.Functions {
		all[n] = engine.FuncInfo{Name: n, Description: f.Description, From: "runbook"}
	}
	var out []engine.FuncInfo
	for _, f := range all {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
