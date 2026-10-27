package server

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/runbook"
	"github.com/egrif/runnr/internal/store"
	"github.com/egrif/runnr/internal/ver"
)

// runPlan is what the run console shows of a run's steps: each as this run
// uses it (its runbook version, with the edits made during it), and how
// the runbook has moved on since.
type runPlan struct {
	Steps []engine.StepView `json:"steps"`
	// Version is the runbook version the run is on; Latest the newest saved.
	Version ver.ID `json:"version"`
	Latest  ver.ID `json:"latest"`
	// Changed are the steps (1-based) not run yet that the latest version
	// has differently; Shape says the latest has a different number of
	// steps (it can't be adopted).
	Changed []int  `json:"changed,omitempty"`
	Shape   string `json:"shape,omitempty"`
	// Live: the console can edit steps and adopt the latest version.
	Live bool `json:"live"`
}

func (s *Server) getRunPlan(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, err := s.st.LoadRun(id)
	l := s.getLive(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	p := runPlan{Version: run.RunbookVersion}
	var defs []runbook.Step // the steps as the run uses them
	cur := 0
	if l != nil && l.session != nil && run.Status == engine.StatusRunning {
		ed, _ := l.session.(engine.Editor)
		p.Live = ed != nil
		for i := range run.Steps {
			p.Steps = append(p.Steps, l.session.View(i))
			if ed != nil {
				defs = append(defs, ed.StepDef(i))
			}
		}
		if ed != nil {
			cur = max(ed.Current(), 0)
		}
	} else {
		rb, err := s.runRunbook(run)
		if err != nil {
			writeErr(w, err)
			return
		}
		p.Steps = engine.Preview(rb, engine.ResumeInputs(run), engine.ResumeState(run))
		defs = rb.Steps
		cur = len(defs)
	}
	if latest, rev, err := s.st.Get(run.Runbook); err == nil && s.st.HasVersion(run.Runbook, run.RunbookVersion) {
		p.Latest = rev
		if rev != run.RunbookVersion && defs != nil {
			if len(latest.Steps) != len(defs) {
				p.Shape = fmt.Sprintf("version %s has %d steps; this run has %d", rev.Short(), len(latest.Steps), len(defs))
			} else {
				for i := cur; i < len(defs); i++ {
					if !reflect.DeepEqual(latest.Steps[i], defs[i]) {
						p.Changed = append(p.Changed, i+1)
					}
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, p)
}

// runRunbook is the runbook a finished run ran: its version, with the
// edits made during it.
func (s *Server) runRunbook(run *engine.Run) (*runbook.Runbook, error) {
	var rb *runbook.Runbook
	if run.RunbookVersion != "" {
		if at, _, err := s.st.RunbookAt(run.Runbook, string(run.RunbookVersion)); err == nil {
			rb = at
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	}
	if rb == nil {
		var err error
		if rb, _, err = s.st.Get(run.Runbook); err != nil {
			return nil, err
		}
	}
	if len(rb.Steps) == len(run.Steps) {
		rb.Steps = append([]runbook.Step(nil), rb.Steps...)
		for _, e := range engine.LatestEdits(run.Edits) {
			if e.Step < len(rb.Steps) {
				rb.Steps[e.Step] = e.Def
			}
		}
	}
	return rb, nil
}

// adoptVersion moves a live run onto a newer saved version of its runbook,
// for the steps it hasn't run yet (body: {"version": "<hash, a prefix of it or its short form>"}). The step waiting
// on the operator, if it changed, starts again with the new version.
func (s *Server) adoptVersion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Version ver.ID `json:"version"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	l := s.getLive(r.PathValue("id"))
	if l == nil || l.session == nil {
		writeErr(w, fmt.Errorf("run not live: %w", store.ErrNotFound))
		return
	}
	ad, ok := l.session.(engine.VersionAdopter)
	ed, _ := l.session.(engine.Editor)
	if !ok || ed == nil {
		writeErr(w, errors.New("this run can't change version"))
		return
	}
	run, err := s.st.LoadRun(l.id)
	if err != nil {
		writeErr(w, err)
		return
	}
	rb, v, err := s.st.RunbookAt(run.Runbook, string(req.Version))
	if err != nil {
		writeErr(w, err)
		return
	}
	changed, err := ad.AdoptVersion(v, rb.Steps)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	restarted := false
	n := make([]int, len(changed))
	for k, i := range changed {
		n[k] = i + 1
		if i == ed.Current() {
			restarted = l.restartStep(i)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"changed": n, "restarted": restarted, "version": v})
}

// getRunbookStep is step n of the saved runbook as YAML, for editing one
// step (the console's "Edit this step" outside a live run).
func (s *Server) getRunbookStep(w http.ResponseWriter, r *http.Request) {
	i, err := stepNumber(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	d, err := s.st.Load(r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if d.ParseErr != nil {
		writeErr(w, d.ParseErr)
		return
	}
	if i >= len(d.Runbook.Steps) {
		writeErr(w, fmt.Errorf("no step %d: %w", i+1, store.ErrNotFound))
		return
	}
	raw, err := runbook.FormatStep(d.Runbook.Steps[i])
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"yaml": string(raw), "version": d.Version})
}

// putRunbookStep replaces step n of the saved runbook (body: the step as
// YAML; If-Match: the version it was read from) and saves it as a new
// version. The rest of the runbook's YAML is left as it was.
func (s *Server) putRunbookStep(w http.ResponseWriter, r *http.Request) {
	i, err := stepNumber(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	name := r.PathValue("name")
	d, err := s.st.Load(name)
	if err != nil {
		writeErr(w, err)
		return
	}
	o := store.SaveOpts{OldName: name}
	o.Base = ifMatch(r)
	body, err := readBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	step, err := runbook.ParseStep(body)
	if err != nil {
		writeErr(w, err)
		return
	}
	raw, err := runbook.ReplaceStep(d.Raw, i, step)
	if err != nil {
		writeErr(w, err)
		return
	}
	rb, rev, err := s.st.Save(raw, o)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": rev, "runbook": rb})
}

// parseStep reads one step's YAML into the structure the editor's form
// works on ({ok, step} or {ok: false, error}).
func (s *Server) parseStep(w http.ResponseWriter, r *http.Request) {
	raw, err := readBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	step, err := runbook.ParseStep(raw)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "step": step})
}

// formatStep writes one step (JSON, as the editor's form has it) as YAML.
func (s *Server) formatStep(w http.ResponseWriter, r *http.Request) {
	raw, err := readBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	step, err := runbook.ParseStep(raw) // JSON is YAML
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	out, err := runbook.FormatStep(step)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "raw": string(out)})
}

// setRunInputs changes a live run's inputs ({"values": {name: value},
// "by": who}). They take effect before the run's next command; a step
// waiting to run its command starts again with them.
func (s *Server) setRunInputs(w http.ResponseWriter, r *http.Request) {
	l := s.getLive(r.PathValue("id"))
	if l == nil || l.session == nil {
		writeErr(w, fmt.Errorf("run not live: %w", store.ErrNotFound))
		return
	}
	is, ok := l.session.(engine.InputSetter)
	if !ok {
		writeErr(w, errors.New("this run's inputs can't be changed"))
		return
	}
	var req struct {
		Values map[string]string `json:"values"`
		By     string            `json:"by"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	res, err := is.SetInputs(req.Values, req.By)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	restarted := false
	if ed, ok := l.session.(engine.Editor); ok && len(res.Changed) > 0 {
		restarted = l.restartForInputs(ed.Current())
	}
	writeJSON(w, http.StatusOK, map[string]any{"changed": res.Changed, "stale": res.Stale, "replaces": res.Replaces, "restarted": restarted})
}

// gotoStep, while the run waits on the operator at a step, goes to step
// {"step": n} (1-based): back (it and the steps after it run again), the
// same step again, or ahead (the steps between are skipped). The step gone
// to waits before it runs. Going back past a point of no return needs
// "force" (409 without it).
func (s *Server) gotoStep(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Step  int  `json:"step"`
		Force bool `json:"force"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	l := s.getLive(r.PathValue("id"))
	if l == nil || l.session == nil {
		writeErr(w, fmt.Errorf("run not live: %w", store.ErrNotFound))
		return
	}
	nav, ok := l.session.(engine.Navigator)
	ed, _ := l.session.(engine.Editor)
	if !ok || ed == nil {
		writeErr(w, errors.New("this run can't go to another step"))
		return
	}
	cur := ed.Current()
	if !l.waitingOn(cur) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the run isn't waiting for you: stop the running step first, or wait for its question"})
		return
	}
	if err := nav.GoTo(req.Step-1, req.Force); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, engine.ErrPastNoReturn) {
			code = http.StatusConflict
		}
		writeJSON(w, code, map[string]any{"error": err.Error(), "needs_force": errors.Is(err, engine.ErrPastNoReturn)})
		return
	}
	l.withdraw(cur, answerReopen)
	w.WriteHeader(http.StatusNoContent)
}

// stopStep stops the step whose command is running ({"step": n, 1-based;
// 0: whichever}, "by"): it fails, and the run asks what to do.
func (s *Server) stopStep(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Step int    `json:"step"`
		By   string `json:"by"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	l := s.getLive(r.PathValue("id"))
	if l == nil || l.session == nil {
		writeErr(w, fmt.Errorf("run not live: %w", store.ErrNotFound))
		return
	}
	st, ok := l.session.(engine.StepStopper)
	if !ok {
		writeErr(w, errors.New("this run's steps can't be stopped"))
		return
	}
	if err := st.StopStep(req.Step-1, req.By); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
