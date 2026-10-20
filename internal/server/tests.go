package server

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/runbook"
	"github.com/egrif/runnr/internal/runtest"
	"github.com/egrif/runnr/internal/store"
)

// testRun is one run of a runbook's tests, as the GUI shows it.
type testRun struct {
	At      time.Time    `json:"at"`
	Draft   bool         `json:"draft,omitempty"` // against an unsaved version
	Version int          `json:"suite_version"`
	Results []testResult `json:"results"`
}

type testResult struct {
	Scenario string         `json:"scenario"`
	Passed   bool           `json:"passed"`
	Problems []string       `json:"problems,omitempty"`
	Status   string         `json:"status"`
	TookMS   int64          `json:"took_ms"`
	Steps    []testStep     `json:"steps"`
	Calls    []runtest.Call `json:"calls"`
	Asked    []string       `json:"asked,omitempty"`
	Unused   []string       `json:"unused,omitempty"`
	Log      string         `json:"log"`
}

type testStep struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
}

// suiteFor finds a runbook's test suite: one named after it, else the
// first that names it as its runbook.
func (s *Server) suiteFor(name string) (*runtest.Suite, int, error) {
	if d, err := s.st.LoadSuite(name); err == nil {
		su, err := runtest.ParseSuite(d.Raw)
		return su, d.Version, err
	}
	docs, err := s.st.ListSuites()
	if err != nil {
		return nil, 0, err
	}
	for _, d := range docs {
		if su, err := runtest.ParseSuite(d.Raw); err == nil && su.Runbook == name {
			return su, d.Version, nil
		}
	}
	return nil, 0, nil
}

// getTests is a runbook's suite (its scenarios) and its latest results.
func (s *Server) getTests(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	su, ver, err := s.suiteFor(name)
	if err != nil {
		writeErr(w, err)
		return
	}
	resp := map[string]any{"suite": nil}
	if su != nil {
		type sc struct {
			Name        string `json:"name"`
			Description string `json:"description,omitempty"`
		}
		var scs []sc
		for _, x := range su.Scenarios {
			scs = append(scs, sc{x.Name, x.Description})
		}
		resp["suite"] = map[string]any{"name": su.Name, "version": ver, "uses": su.Uses, "isolate": su.Isolate, "scenarios": scs}
	}
	s.mu.Lock()
	if t := s.tests[name]; t != nil {
		resp["last"] = t
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, resp)
}

// runTests runs a runbook's suite (or one scenario), against the saved
// runbook or, with raw, an unsaved version of it.
func (s *Server) runTests(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req struct {
		Scenario string `json:"scenario"`
		Raw      string `json:"raw"`
	}
	if r.ContentLength != 0 {
		if err := decode(r, &req); err != nil {
			writeErr(w, err)
			return
		}
	}
	su, ver, err := s.suiteFor(name)
	if err != nil {
		writeErr(w, err)
		return
	}
	if su == nil {
		writeErr(w, fmt.Errorf("runbook %q has no test suite (runnr tests import <file>): %w", name, store.ErrNotFound))
		return
	}
	var draft *runbook.Runbook
	if req.Raw != "" {
		if draft, err = runbook.Parse([]byte(req.Raw)); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "the draft isn't valid: " + err.Error()})
			return
		}
	}
	exe, err := os.Executable()
	if err != nil {
		writeErr(w, err)
		return
	}
	src := runtest.Sources{
		Runbook: func(n string) (*runbook.Runbook, error) {
			if draft != nil {
				return draft, nil
			}
			rb, _, err := s.st.Get(n)
			return rb, err
		},
		Fakes:     s.st.Fakes,
		Libraries: s.st.Libraries,
	}
	var only []string
	if req.Scenario != "" {
		only = []string{req.Scenario}
	}
	results, err := runtest.Run(r.Context(), su, src, runtest.Options{Only: only, Exe: exe})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	tr := &testRun{At: time.Now(), Draft: draft != nil, Version: ver}
	for _, x := range results {
		res := testResult{Scenario: x.Scenario, Passed: x.Passed, Problems: x.Problems, Status: string(x.Run.Status),
			TookMS: x.Took.Milliseconds(), Calls: x.Calls, Asked: x.Asked, Unused: x.Unused, Log: engine.RawLog(x.Run)}
		for _, st := range x.Run.Steps {
			res.Steps = append(res.Steps, testStep{st.Name, string(st.Status), st.Note})
		}
		if res.Calls == nil {
			res.Calls = []runtest.Call{}
		}
		tr.Results = append(tr.Results, res)
	}
	// Only a full run of the saved runbook counts as its latest results.
	if draft == nil && req.Scenario == "" {
		s.mu.Lock()
		s.tests[name] = tr
		s.mu.Unlock()
	}
	writeJSON(w, http.StatusOK, tr)
}
