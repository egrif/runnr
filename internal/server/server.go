// Package server serves the runnr web GUI and its JSON API.
package server

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egrif/runnr/internal/claude"
	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/htmlview"
	"github.com/egrif/runnr/internal/runbook"
	"github.com/egrif/runnr/internal/store"
	"github.com/egrif/runnr/internal/timeline"
)

//go:embed web
var webFS embed.FS

type Server struct {
	st   *store.Store
	cfg  Config
	mu   sync.Mutex
	live map[string]*liveRun
	// shells opened from the console (see shells.go).
	shells map[string]*liveRun
	wg     sync.WaitGroup
	// tests holds each runbook's latest test results (since the server started).
	tests map[string]*testRun
}

type Config struct {
	// Token, when set, is required on every API request, either as
	// "Authorization: Bearer <token>" or via the GUI's login cookie.
	Token string
	// Claude answers questions about runbooks (Explain); nil turns that
	// off. New sets it from ANTHROPIC_API_KEY unless NoClaude.
	Claude   claude.Provider
	NoClaude bool
	// NoUI serves only the API.
	NoUI bool
}

func New(st *store.Store, cfg Config) *Server {
	if cfg.Claude == nil && !cfg.NoClaude {
		if p := claude.FromEnv(); p != nil {
			cfg.Claude = p
		}
	}
	return &Server{st: st, cfg: cfg, live: map[string]*liveRun{}, tests: map[string]*testRun{}}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	if !s.cfg.NoUI {
		static, _ := fs.Sub(webFS, "web")
		files := http.FileServerFS(static)
		// The app's files change with every build; never let a browser run
		// stale ones against a newer server.
		mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			files.ServeHTTP(w, r)
		}))
	}
	mux.HandleFunc("POST /api/login", s.login)

	mux.HandleFunc("GET /api/runbooks", s.listRunbooks)
	mux.HandleFunc("POST /api/runbooks", s.createRunbook)
	mux.HandleFunc("GET /api/runbooks/{name}", s.getRunbook)
	mux.HandleFunc("PUT /api/runbooks/{name}", s.updateRunbook)
	mux.HandleFunc("DELETE /api/runbooks/{name}", s.deleteRunbook)
	mux.HandleFunc("GET /api/runbooks/{name}/versions", s.listVersions)
	mux.HandleFunc("GET /api/runbooks/{name}/versions/{version}", s.getVersion)
	mux.HandleFunc("POST /api/validate", s.validate)
	mux.HandleFunc("POST /api/test-run", s.testRun)
	mux.HandleFunc("GET /api/runbooks/{name}/test-values", s.getTestValues)
	mux.HandleFunc("PUT /api/runbooks/{name}/test-values", s.putTestValues)
	mux.HandleFunc("GET /api/collections", s.listCollections)
	mux.HandleFunc("POST /api/collections", s.createCollection)
	mux.HandleFunc("GET /api/collections/{name}", s.getCollection)
	mux.HandleFunc("PUT /api/collections/{name}", s.updateCollection)
	mux.HandleFunc("DELETE /api/collections/{name}", s.deleteCollection)
	mux.HandleFunc("POST /api/collections/validate", s.validateCollection)
	mux.HandleFunc("POST /api/collections/{name}/runbooks", s.addToCollection)
	mux.HandleFunc("PUT /api/collections/{name}/runbooks", s.setCollectionRunbooks)
	mux.HandleFunc("PATCH /api/collections/{name}", s.patchCollection)
	mux.HandleFunc("DELETE /api/collections/{name}/runbooks/{runbook}", s.removeFromCollection)
	mux.HandleFunc("POST /api/runbooks/{name}/move", s.moveRunbook)
	mux.HandleFunc("GET /api/runbooks/{name}/export", s.exportRunbook)
	mux.HandleFunc("GET /api/claude", s.claudeStatus)
	mux.HandleFunc("POST /api/explain", s.explain)
	mux.HandleFunc("GET /api/schedules", s.listSchedules)
	mux.HandleFunc("POST /api/schedules", s.saveSchedule)
	mux.HandleFunc("GET /api/schedules/{id}", s.getSchedule)
	mux.HandleFunc("PUT /api/schedules/{id}", s.saveSchedule)
	mux.HandleFunc("DELETE /api/schedules/{id}", s.deleteSchedule)
	mux.HandleFunc("GET /api/libraries", s.listLibraries)
	mux.HandleFunc("POST /api/libraries", s.createLibrary)
	mux.HandleFunc("GET /api/libraries/{name}", s.getLibrary)
	mux.HandleFunc("PUT /api/libraries/{name}", s.updateLibrary)
	mux.HandleFunc("DELETE /api/libraries/{name}", s.deleteLibrary)
	mux.HandleFunc("GET /api/libraries/{name}/versions", s.libraryVersions)
	mux.HandleFunc("POST /api/libraries/validate", s.validateLibrary)
	mux.HandleFunc("GET /api/template", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"raw": runbook.Scaffold(nonEmpty(r.URL.Query().Get("name"), "my-runbook"))})
	})

	mux.HandleFunc("GET /api/runs", s.listRuns)
	mux.HandleFunc("POST /api/runs", s.startRun)
	mux.HandleFunc("GET /api/runs/{id}", s.getRun)
	mux.HandleFunc("DELETE /api/runs/{id}", s.deleteRun)
	mux.HandleFunc("GET /api/manage", s.getManage)
	mux.HandleFunc("POST /api/manage/delete", s.manageDelete)
	mux.HandleFunc("POST /api/manage/restore", s.manageRestore)
	mux.HandleFunc("GET /api/runs/{id}/events", s.runEvents)
	mux.HandleFunc("GET /api/runs/{id}/log", s.runLog)
	mux.HandleFunc("GET /api/runs/{id}/timeline", s.runTimeline)
	mux.HandleFunc("GET /api/runbooks/{name}/tests", s.getTests)
	mux.HandleFunc("POST /api/runbooks/{name}/tests/run", s.runTests)
	mux.HandleFunc("POST /api/runs/{id}/answer", s.answer)
	mux.HandleFunc("GET /api/runs/{id}/steps/{n}", s.getRunStep)
	mux.HandleFunc("PUT /api/runs/{id}/steps/{n}", s.editRunStep)
	mux.HandleFunc("PUT /api/runs/{id}/inputs", s.setRunInputs)
	mux.HandleFunc("POST /api/runs/{id}/steps/{n}/reopen", s.reopenRunStep)
	mux.HandleFunc("POST /api/runs/{id}/goto", s.gotoStep)
	mux.HandleFunc("POST /api/runs/{id}/stop", s.stopStep)
	mux.HandleFunc("POST /api/runs/{id}/save-edits", s.saveRunEdits)
	mux.HandleFunc("GET /api/runs/{id}/plan", s.getRunPlan)
	mux.HandleFunc("GET /api/runbooks/{name}/steps/{n}", s.getRunbookStep)
	mux.HandleFunc("PUT /api/runbooks/{name}/steps/{n}", s.putRunbookStep)
	mux.HandleFunc("POST /api/runs/{id}/adopt-version", s.adoptVersion)
	mux.HandleFunc("POST /api/shells", s.openShell)
	mux.HandleFunc("POST /api/import/check", s.importCheck)
	mux.HandleFunc("POST /api/import", s.importDoc)
	mux.HandleFunc("GET /api/fs", s.listFS)
	mux.HandleFunc("POST /api/runbooks/{name}/files/plan", s.exportPlan)
	mux.HandleFunc("POST /api/runbooks/{name}/files", s.exportFiles)
	mux.HandleFunc("GET /api/runs/{id}/chain", s.runChain)
	mux.HandleFunc("GET /api/runs/{id}/logs", s.downloadLogs)
	mux.HandleFunc("POST /api/runs/{id}/logs", s.writeLogs)
	mux.HandleFunc("POST /api/import/scan", s.importScan)
	mux.HandleFunc("POST /api/import/project", s.importProject)
	mux.HandleFunc("GET /api/shells/{id}/events", s.shellEvents)
	mux.HandleFunc("POST /api/shells/{id}/term", s.shellInput)
	mux.HandleFunc("POST /api/runs/{id}/cancel", s.cancelRun)
	mux.HandleFunc("POST /api/runs/{id}/resume", s.resumeRun)
	mux.HandleFunc("POST /api/runs/{id}/notes", s.addNote)
	mux.HandleFunc("GET /api/runs/{id}/preview", s.runPreview)
	mux.HandleFunc("POST /api/runs/{id}/term", s.termInput)
	mux.HandleFunc("GET /api/runs/{id}/controls", s.getControls)
	mux.HandleFunc("PUT /api/runs/{id}/controls", s.setControls)
	mux.HandleFunc("POST /api/preview", s.preview)
	mux.HandleFunc("POST /api/parse", s.parse)
	mux.HandleFunc("POST /api/parse-step", s.parseStep)
	mux.HandleFunc("POST /api/format-step", s.formatStep)
	mux.HandleFunc("POST /api/format", s.format)
	return s.guard(mux)
}

const cookieName = "runnr_token"

func (s *Server) tokenOK(t string) bool {
	return t != "" && subtle.ConstantTimeCompare([]byte(t), []byte(s.cfg.Token)) == 1
}

// guard enforces auth and rejects cross-origin state changes, so neither
// other hosts nor a malicious web page can drive runnr (which runs commands).
// Bearer-token requests are exempt from the browser checks: browsers never
// attach that header on their own.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isAPI := strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/login"
		if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			if s.cfg.Token != "" && !s.tokenOK(bearer) {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token"})
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if isAPI && s.cfg.Token != "" {
			c, err := r.Cookie(cookieName)
			if err != nil || !s.tokenOK(c.Value) {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "token required"})
				return
			}
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host && origin != "https://"+r.Host {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
			if r.Header.Get("X-Runnr") != "1" {
				http.Error(w, "missing X-Runnr header", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// login exchanges a token for an HttpOnly cookie for the GUI.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct{ Token string }
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	if s.cfg.Token != "" && !s.tokenOK(body.Token) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token"})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: body.Token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	hs := &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	s.mu.Lock()
	for _, l := range s.live {
		l.cancel(&engine.AbortError{Reason: "runnr serve stopped"})
	}
	s.mu.Unlock()
	s.wg.Wait()
	sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return hs.Shutdown(sctx)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	switch {
	case errors.Is(err, store.ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, store.ErrConflict):
		code = http.StatusConflict
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func readBody(r *http.Request) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, 1<<20))
}

func (s *Server) listRunbooks(w http.ResponseWriter, _ *http.Request) {
	entries, err := s.st.List()
	if err != nil {
		writeErr(w, err)
		return
	}
	if entries == nil {
		entries = []store.Entry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) getRunbook(w http.ResponseWriter, r *http.Request) {
	d, err := s.st.Load(r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	resp := map[string]any{"raw": string(d.Raw), "version": d.Version, "updated_at": d.UpdatedAt}
	if d.ParseErr != nil {
		resp["error"] = d.ParseErr.Error()
	} else {
		resp["runbook"] = d.Runbook
	}
	w.Header().Set("ETag", strconv.Itoa(d.Version))
	writeJSON(w, http.StatusOK, resp)
}

// createRunbook saves a new runbook; ?collection=NAME also adds it to that
// collection (created if it doesn't exist).
func (s *Server) createRunbook(w http.ResponseWriter, r *http.Request) {
	col := r.URL.Query().Get("collection")
	if col == "" {
		s.saveRunbook(w, r, store.SaveOpts{}, http.StatusCreated)
		return
	}
	raw, err := readBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	rb, ver, err := s.st.Save(raw, store.SaveOpts{})
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, _, err := s.st.AddToCollection(col, true, rb.Name); err != nil {
		writeErr(w, fmt.Errorf("saved %s, but adding it to collection %s failed: %w", rb.Name, col, err))
		return
	}
	w.Header().Set("ETag", strconv.Itoa(ver))
	writeJSON(w, http.StatusCreated, map[string]any{"name": rb.Name, "version": ver, "runbook": rb, "collection": col})
}

// updateRunbook saves a new version. Send "If-Match: <version>" to fail with
// 409 instead of overwriting someone else's newer save.
func (s *Server) updateRunbook(w http.ResponseWriter, r *http.Request) {
	o := store.SaveOpts{OldName: r.PathValue("name")}
	if m := strings.Trim(r.Header.Get("If-Match"), `" `); m != "" {
		v, err := strconv.Atoi(m)
		if err != nil {
			writeErr(w, fmt.Errorf("If-Match must be a version number"))
			return
		}
		o.BaseVersion = v
	}
	s.saveRunbook(w, r, o, http.StatusOK)
}

func (s *Server) saveRunbook(w http.ResponseWriter, r *http.Request, o store.SaveOpts, code int) {
	raw, err := readBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	rb, ver, err := s.st.Save(raw, o)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("ETag", strconv.Itoa(ver))
	writeJSON(w, code, map[string]any{"name": rb.Name, "version": ver, "runbook": rb})
}

func (s *Server) listVersions(w http.ResponseWriter, r *http.Request) {
	vs, err := s.st.Versions(r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, vs)
}

func (s *Server) getVersion(w http.ResponseWriter, r *http.Request) {
	v, err := strconv.Atoi(r.PathValue("version"))
	if err != nil {
		writeErr(w, fmt.Errorf("bad version"))
		return
	}
	raw, err := s.st.RawVersion(r.PathValue("name"), v)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": v, "raw": string(raw)})
}

func (s *Server) deleteRunbook(w http.ResponseWriter, r *http.Request) {
	if _, err := s.st.DeleteRunbook(r.PathValue("name")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) validate(w http.ResponseWriter, r *http.Request) {
	raw, err := readBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, err := runbook.Parse(raw); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type startReq struct {
	// test: a "Run this" from the editor; not recorded in history.
	test bool

	Runbook    string            `json:"runbook"`
	Vars       map[string]string `json:"vars"`
	Unattended bool              `json:"unattended"`
	Yes        bool              `json:"yes"`
	// UsePreferred acknowledges that steps set to ask which way will use
	// their preferred way in an unattended run (required if there are any).
	UsePreferred bool `json:"use_preferred"`
	DryRun       bool `json:"dry_run"`
	// From starts at a later step (id, 1-based number or name).
	From string `json:"from"`
	// NewRun (resume): a new run that points back at the resumed one,
	// instead of carrying that run on in its own record.
	NewRun bool `json:"new_run"`
	// Wait blocks until the run finishes and returns its record.
	Wait bool `json:"wait"`
	// Skip lists 1-based step numbers to leave out. On resume, sending it
	// (even empty) replaces the automatic "start where it stopped".
	Skip []int `json:"skip"`
	// PauseBefore / PauseAfter list 1-based steps to stop at (interactive).
	PauseBefore []int `json:"pause_before"`
	PauseAfter  []int `json:"pause_after"`
	// StepMode pauses before every step (interactive runs).
	StepMode bool `json:"step_mode"`
	// Previous is a run ID to compare against ("last time"); defaults to the
	// resumed run, else the runbook's latest run.
	Previous string `json:"previous"`
}

// stepList shows step indexes as 1-based numbers.
func stepList(idx []int) string {
	var s []string
	for _, i := range idx {
		s = append(s, strconv.Itoa(i+1))
	}
	return strings.Join(s, ", ")
}

// toControls converts 1-based API step lists.
func toControls(req startReq, n int) (engine.ControlsView, error) {
	cv := engine.ControlsView{StepAll: req.StepMode}
	for _, f := range []struct {
		src []int
		dst *[]int
	}{{req.Skip, &cv.Skip}, {req.PauseBefore, &cv.PauseBefore}, {req.PauseAfter, &cv.PauseAfter}} {
		for _, i := range f.src {
			if i < 1 || i > n {
				return cv, fmt.Errorf("no step %d (steps are 1-%d)", i, n)
			}
			*f.dst = append(*f.dst, i-1)
		}
	}
	return cv, nil
}

// controlsJSON is ControlsView with 1-based step numbers, as the API uses.
type controlsJSON struct {
	StepAll     bool  `json:"step_mode"`
	Skip        []int `json:"skip"`
	PauseBefore []int `json:"pause_before"`
	PauseAfter  []int `json:"pause_after"`
}

func toJSON(cv engine.ControlsView) controlsJSON {
	plus := func(xs []int) []int {
		out := make([]int, len(xs))
		for i, x := range xs {
			out[i] = x + 1
		}
		return out
	}
	return controlsJSON{cv.StepAll, plus(cv.Skip), plus(cv.PauseBefore), plus(cv.PauseAfter)}
}

func decode(r *http.Request, v any) error {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func (s *Server) startRun(w http.ResponseWriter, r *http.Request) {
	var req startReq
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	rb, ver, err := s.st.Get(req.Runbook)
	if err != nil {
		writeErr(w, err)
		return
	}
	start := 0
	if req.From != "" {
		if start, err = rb.StepIndex(req.From); err != nil {
			writeErr(w, err)
			return
		}
	}
	s.launch(w, r, rb, ver, req, start, nil, nil)
}

func (s *Server) resumeRun(w http.ResponseWriter, r *http.Request) {
	var req startReq
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	prev, err := s.st.LoadRun(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	start := engine.ResumePoint(prev)
	if start < 0 {
		writeErr(w, fmt.Errorf("run %s has no unfinished steps", prev.ID))
		return
	}
	rb, ver, err := s.st.Get(prev.Runbook)
	if err != nil {
		writeErr(w, err)
		return
	}
	if engine.RunbookChanged(prev, rb) {
		if req.NewRun {
			writeErr(w, fmt.Errorf("runbook %q changed since run %s; start a new run with \"from\", or resume without new_run to carry the run on with this version", rb.Name, prev.ID))
			return
		}
		// The run carries on with this version: its steps matched by name.
		aligned, moved := engine.AlignRun(prev, rb)
		names := map[int]string{}
		for i, st := range prev.Steps {
			names[i] = st.Name
		}
		if err := s.st.MoveRunNotes(prev.ID, moved, names); err != nil {
			writeErr(w, err)
			return
		}
		prev = aligned
		if start = engine.ResumePoint(prev); start < 0 {
			writeErr(w, fmt.Errorf("run %s has no unfinished steps in this version", prev.ID))
			return
		}
	}
	vars := engine.ResumeInputs(prev)
	for k, v := range req.Vars {
		vars[k] = v
	}
	req.Vars = vars
	if req.Skip != nil {
		start = 0 // the operator chose the steps explicitly
	}
	if req.Previous == "" {
		req.Previous = prev.ID
		if !req.NewRun {
			// The same run: compared with what it was compared with before.
			req.Previous = prev.Previous
			if req.Previous == "" {
				req.Previous = "-"
			}
		}
	}
	s.launch(w, r, rb, ver, req, start, prev, engine.ResumeState(prev))
}

func (s *Server) launch(w http.ResponseWriter, r *http.Request, rb *runbook.Runbook, ver int, req startReq, start int, resumed *engine.Run, state map[string]string) {
	resumes, cont := "", resumed != nil && !req.NewRun
	if resumed != nil && !cont {
		resumes = resumed.ID
	}
	cv, err := toControls(req, len(rb.Steps))
	if err != nil {
		writeErr(w, err)
		return
	}
	previous, err := s.previousRun(rb.Name, req.Previous)
	if err != nil {
		writeErr(w, err)
		return
	}
	libs, err := s.st.Libraries(rb.Libraries)
	if err != nil {
		writeErr(w, err)
		return
	}
	if req.Wait && !req.Unattended {
		writeErr(w, errors.New("wait requires unattended (nobody could answer prompts)"))
		return
	}
	if req.Unattended && !req.UsePreferred && !req.test {
		if ch := rb.ChoosingSteps(cv.Skip); len(ch) > 0 {
			writeErr(w, fmt.Errorf("steps %s normally ask which way to do them; unattended, they'll use their preferred way. Send use_preferred: true to go ahead", stepList(ch)))
			return
		}
	}
	id := engine.NewRunID()
	if cont {
		id = resumed.ID // the run carries on, in its own record
		if l := s.getLive(id); l != nil {
			l.mu.Lock()
			done := l.done
			l.mu.Unlock()
			if !done {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "run " + id + " is still going"})
				return
			}
		}
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	l := newLiveRun(id, cancel)
	if !req.test && !req.DryRun {
		// Another runnr (runnr abort) can ask for it to stop.
		go s.st.WatchAbort(ctx, id, cancel)
	}
	l.ctl = engine.NewControls(cv)
	var ui engine.Interactor = l
	if req.Unattended {
		ui = unattendedWeb{engine.Unattended{OnEvent: l.Notify, AssumeYes: req.Yes}, l}
	}
	s.mu.Lock()
	s.live[id] = l
	s.mu.Unlock()

	done := make(chan *engine.Run, 1)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel(nil)
		done <- engine.Execute(ctx, rb, ui, id, engine.Options{
			Owner:          engine.ThisProcess("server"),
			Vars:           req.Vars,
			DryRun:         req.DryRun,
			Interactive:    !req.Unattended,
			StartAt:        start,
			Controls:       l.ctl,
			Previous:       previous,
			Libraries:      libs,
			State:          state,
			Resumes:        resumes,
			Resumed:        resumed,
			Continue:       cont,
			RunbookVersion: ver,
			// Only a recorded run of a saved runbook can run dangerous
			// steps without a double check.
			Sanctioned: !req.test && !req.DryRun && ver > 0,
			// Unless the operator chose the steps, a resume leaves out
			// every step that finished, not only those before start.
			CarryDone: resumed != nil && req.Skip == nil,
			Save: func(run *engine.Run) {
				if req.test {
					return
				}
				if err := s.st.SaveRun(run); err != nil {
					log.Printf("saving run %s: %v", run.ID, err)
				}
			},
		})
		// Keep the event log around briefly for late subscribers.
		time.AfterFunc(10*time.Minute, func() {
			s.mu.Lock()
			if s.live[id] == l { // not a resume that carries it on
				delete(s.live, id)
			}
			s.mu.Unlock()
		})
	}()
	if !req.Wait {
		writeJSON(w, http.StatusAccepted, map[string]string{"id": id})
		return
	}
	select {
	case run := <-done:
		writeJSON(w, http.StatusOK, run)
	case <-r.Context().Done():
		// Client went away; the run carries on and stays in history.
	}
}

// previousRun loads the run to compare against: id if given, else the
// runbook's latest run (nil if none).
func (s *Server) previousRun(runbookName, id string) (*engine.Run, error) {
	if id == "-" {
		return nil, nil
	}
	if id == "" {
		runs, err := s.st.ListRuns(runbookName, 1)
		if err != nil || len(runs) == 0 {
			return nil, err
		}
		id = runs[0].ID
	}
	return s.st.LoadRun(id)
}

type previewReq struct {
	Runbook string            `json:"runbook"`
	Raw     string            `json:"raw"` // preview unsaved YAML instead of a stored runbook
	Vars    map[string]string `json:"vars"`
	// FromRun fills in values from that run's state (as a resume would).
	FromRun string `json:"from_run"`
}

// preview shows what each step would do with the given inputs.
func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	var req previewReq
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	var rb *runbook.Runbook
	var err error
	if req.Raw != "" {
		rb, err = runbook.Parse([]byte(req.Raw))
	} else {
		rb, _, err = s.st.Get(req.Runbook)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	var state map[string]string
	if req.FromRun != "" {
		prev, err := s.st.LoadRun(req.FromRun)
		if err != nil {
			writeErr(w, err)
			return
		}
		state = engine.ResumeState(prev)
	}
	writeJSON(w, http.StatusOK, map[string]any{"steps": engine.Preview(rb, req.Vars, state)})
}

// runPreview shows what a run's steps would do with its inputs and the
// state it had reached (for steps it hasn't run yet).
func (s *Server) runPreview(w http.ResponseWriter, r *http.Request) {
	run, err := s.st.LoadRun(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	rb, _, err := s.st.Get(run.Runbook)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"steps": engine.Preview(rb, engine.ResumeInputs(run), engine.ResumeState(run))})
}

// controls reads or changes a live run's skip/pause settings; changes
// apply to steps it hasn't reached.
func (s *Server) getControls(w http.ResponseWriter, r *http.Request) {
	l := s.getLive(r.PathValue("id"))
	if l == nil || l.ctl == nil {
		writeErr(w, fmt.Errorf("run not live: %w", store.ErrNotFound))
		return
	}
	writeJSON(w, http.StatusOK, toJSON(l.ctl.View()))
}

func (s *Server) setControls(w http.ResponseWriter, r *http.Request) {
	l := s.getLive(r.PathValue("id"))
	if l == nil || l.ctl == nil {
		writeErr(w, fmt.Errorf("run not live: %w", store.ErrNotFound))
		return
	}
	var req startReq
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	n := 1 << 30
	if run, err := s.st.LoadRun(l.id); err == nil {
		n = len(run.Steps)
	}
	cv, err := toControls(req, n)
	if err != nil {
		writeErr(w, err)
		return
	}
	l.ctl.Set(cv)
	l.publish(WireEvent{Event: engine.Event{Kind: "controls", Step: -1}, Controls: ptr(toJSON(cv))})
	writeJSON(w, http.StatusOK, toJSON(cv))
}

func ptr[T any](v T) *T { return &v }

// termInput delivers keystrokes and size changes to a live run's terminal.
func (s *Server) termInput(w http.ResponseWriter, r *http.Request) {
	l := s.getLive(r.PathValue("id"))
	if l == nil {
		writeErr(w, fmt.Errorf("run not live: %w", store.ErrNotFound))
		return
	}
	termTo(w, r, l)
}

// termTo delivers keystrokes, a size or a hang-up to one of l's terminals.
func termTo(w http.ResponseWriter, r *http.Request, l *liveRun) {
	var req struct {
		ID   string `json:"id"`
		Data string `json:"data"`
		Rows uint16 `json:"rows"`
		Cols uint16 `json:"cols"`
		// End hangs up the session (like closing a terminal window).
		End bool `json:"end"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	c := l.term(req.ID)
	if c == nil {
		writeErr(w, fmt.Errorf("terminal %q has ended: %w", req.ID, store.ErrNotFound))
		return
	}
	if req.End {
		c.Close()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	c.SetSize(req.Rows, req.Cols)
	if req.Data != "" {
		c.Input([]byte(req.Data))
	}
	w.WriteHeader(http.StatusNoContent)
}

// parse turns YAML into the structured form the editor uses.
func (s *Server) parse(w http.ResponseWriter, r *http.Request) {
	raw, err := readBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	rb, err := runbook.Parse(raw)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	comments, _ := runbook.ExtractComments(raw)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "runbook": rb, "comments": comments})
}

// format turns the structured form back into YAML. The body is the
// runbook, or {runbook, comments} to put comments back on their fields.
func (s *Server) format(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var wrapped struct {
		Runbook  *runbook.Runbook `json:"runbook"`
		Comments runbook.Comments `json:"comments"`
	}
	var rb runbook.Runbook
	if json.Unmarshal(body, &wrapped) == nil && wrapped.Runbook != nil {
		rb = *wrapped.Runbook
	} else if err := json.Unmarshal(body, &rb); err != nil {
		writeErr(w, err)
		return
	}
	raw, err := runbook.FormatWithComments(&rb, wrapped.Comments)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "raw": string(raw)})
}

func (s *Server) getLive(id string) *liveRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live[id]
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 1000 {
		limit = n
	}
	runs, err := s.st.ListRuns(r.URL.Query().Get("runbook"), limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.st.LoadRun(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	resp := map[string]any{"run": run, "live": false}
	if l := s.getLive(run.ID); l != nil {
		resp["live"] = true
		if ed, ok := l.session.(engine.Editor); ok && ed != nil {
			// The record may not have the latest edits yet.
			if e := ed.Edits(); len(e) > len(run.Edits) {
				run.Edits = e
				engine.MarkEdited(run)
			}
		}
		if l.ctl != nil {
			resp["controls"] = toJSON(l.ctl.View())
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// runLog is a run as a plain-text log (?download=1 saves it as a file).
// runTimeline is the run laid out in time, as a page of its own.
func (s *Server) runTimeline(w http.ResponseWriter, r *http.Request) {
	run, err := s.st.LoadRun(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.URL.Query().Get("download") != "" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="run-%s.timeline.html"`, run.ID))
	}
	w.Write(timeline.Build(run, time.Now()).HTML())
}

func (s *Server) runLog(w http.ResponseWriter, r *http.Request) {
	run, err := s.st.LoadRun(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.URL.Query().Get("download") != "" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.log"`, run.ID))
	}
	io.WriteString(w, engine.RawLog(run))
}

func (s *Server) runEvents(w http.ResponseWriter, r *http.Request) {
	l := s.getLive(r.PathValue("id"))
	if l == nil {
		writeErr(w, fmt.Errorf("run %q is not live: %w", r.PathValue("id"), store.ErrNotFound))
		return
	}
	streamEvents(w, r, l)
}

// streamEvents sends l's events (those so far, then as they come) as
// server-sent events.
func streamEvents(w http.ResponseWriter, r *http.Request, l *liveRun) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	send := func(e WireEvent) {
		b, _ := json.Marshal(e)
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	backlog, ch := l.subscribe()
	for _, e := range backlog {
		send(e)
	}
	flusher.Flush()
	if ch == nil {
		return
	}
	defer l.unsubscribe(ch)
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case e, ok := <-ch:
			if !ok {
				return
			}
			send(e)
			flusher.Flush()
		}
	}
}

func (s *Server) answer(w http.ResponseWriter, r *http.Request) {
	l := s.getLive(r.PathValue("id"))
	if l == nil {
		writeErr(w, fmt.Errorf("run not live: %w", store.ErrNotFound))
		return
	}
	var a Answer
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&a); err != nil {
		writeErr(w, err)
		return
	}
	if err := l.answer(a); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// runEditor is a live run's step editor (nil when the run isn't live).
func (s *Server) runEditor(id string) (*liveRun, engine.Editor) {
	l := s.getLive(id)
	if l == nil {
		return nil, nil
	}
	ed, _ := l.session.(engine.Editor)
	return l, ed
}

func stepNumber(r *http.Request) (int, error) {
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 {
		return 0, fmt.Errorf("step must be a number from 1")
	}
	return n - 1, nil
}

// getRunStep returns a live run's step definition as YAML, for editing.
func (s *Server) getRunStep(w http.ResponseWriter, r *http.Request) {
	i, err := stepNumber(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	_, ed := s.runEditor(r.PathValue("id"))
	if ed == nil {
		writeErr(w, fmt.Errorf("run not live: %w", store.ErrNotFound))
		return
	}
	def := ed.StepDef(i)
	raw, err := runbook.FormatStep(def)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"yaml": string(raw), "current": ed.Current() + 1, "editable": i >= ed.Current()})
}

// editRunStep replaces a step of a live run (body: the step as YAML). The
// current step, if it's waiting on a question, starts again with it.
// reopenRunStep goes back to step n, the last one that succeeded, while the
// run waits on the operator at the next one: n pauses before running again.
func (s *Server) reopenRunStep(w http.ResponseWriter, r *http.Request) {
	i, err := stepNumber(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	l := s.getLive(r.PathValue("id"))
	if l == nil || l.session == nil {
		writeErr(w, fmt.Errorf("run not live: %w", store.ErrNotFound))
		return
	}
	ro, ok := l.session.(engine.Reopener)
	ed, _ := l.session.(engine.Editor)
	if !ok || ed == nil {
		writeErr(w, errors.New("this run can't go back"))
		return
	}
	if !l.waitingOn(ed.Current()) {
		writeErr(w, errors.New("the run isn't waiting for you right now; re-open a step while it waits before the next one"))
		return
	}
	if err := ro.Reopen(i); err != nil {
		writeErr(w, err)
		return
	}
	l.withdraw(ed.Current(), answerReopen)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) editRunStep(w http.ResponseWriter, r *http.Request) {
	i, err := stepNumber(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	l, ed := s.runEditor(r.PathValue("id"))
	if ed == nil {
		writeErr(w, fmt.Errorf("run not live: %w", store.ErrNotFound))
		return
	}
	raw, err := readBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	step, err := runbook.ParseStep(raw)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := ed.EditStep(i, step); err != nil {
		writeErr(w, err)
		return
	}
	restarted := i == ed.Current() && l.restartStep(i)
	writeJSON(w, http.StatusOK, map[string]any{"restarted": restarted, "edits": ed.Edits(), "view": l.session.View(i)})
}

// saveRunEdits saves a run's step edits as a new version of its runbook.
func (s *Server) saveRunEdits(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, err := s.st.LoadRun(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	_, ed := s.runEditor(id)
	if ed != nil {
		run.Edits = ed.Edits() // the record may not have the latest yet
	}
	ver, err := s.st.SaveRunEdits(run)
	if err != nil {
		writeErr(w, err)
		return
	}
	if ed != nil {
		ed.MarkEditsSaved(ver)
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": ver})
}

// addNote attaches an operator note to a step (1-based "step"), or to the
// run when step is 0. Works for live and finished runs.
func (s *Server) addNote(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Step   int    `json:"step"`
		Author string `json:"author"`
		Text   string `json:"text"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	id := r.PathValue("id")
	n, err := s.st.AddNote(id, body.Step-1, nonEmpty(strings.TrimSpace(body.Author), "anonymous"), body.Text)
	if err != nil {
		writeErr(w, err)
		return
	}
	if l := s.getLive(id); l != nil {
		l.note(body.Step-1, n)
	}
	writeJSON(w, http.StatusCreated, n)
}

// cancelRun aborts a run: one this server is running stops now; one
// running elsewhere (runnr run in a terminal, another server) is asked to,
// and one whose process is gone is marked aborted (force: without asking).
func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Author string `json:"author"`
		Reason string `json:"reason"`
		Force  bool   `json:"force"`
	}
	if r.ContentLength != 0 {
		if err := decode(r, &body); err != nil {
			writeErr(w, err)
			return
		}
	}
	by := nonEmpty(strings.TrimSpace(body.Author), "anonymous")
	if l := s.getLive(r.PathValue("id")); l != nil {
		l.cancel(&engine.AbortError{By: by, Reason: strings.TrimSpace(body.Reason)})
		writeJSON(w, http.StatusOK, store.AbortResult{Aborted: true, Message: "aborted"})
		return
	}
	res, err := s.st.AbortRun(r.PathValue("id"), by, strings.TrimSpace(body.Reason), body.Force, 8*time.Second)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ---------- libraries ----------

func (s *Server) listLibraries(w http.ResponseWriter, _ *http.Request) {
	libs, err := s.st.ListLibraries()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, libs)
}

func (s *Server) getLibrary(w http.ResponseWriter, r *http.Request) {
	d, err := s.st.LoadLibrary(r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	resp := map[string]any{"raw": string(d.Raw), "version": d.Version, "updated_at": d.UpdatedAt}
	if d.ParseErr != nil {
		resp["error"] = d.ParseErr.Error()
	} else {
		resp["library"] = d.Library
		resp["script"] = runbook.FunctionScript(d.Library.Functions)
	}
	w.Header().Set("ETag", strconv.Itoa(d.Version))
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) saveLibrary(w http.ResponseWriter, r *http.Request, o store.SaveOpts, code int) {
	raw, err := readBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	lib, ver, err := s.st.SaveLibrary(raw, o)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, code, map[string]any{"name": lib.Name, "version": ver, "library": lib})
}

func (s *Server) createLibrary(w http.ResponseWriter, r *http.Request) {
	s.saveLibrary(w, r, store.SaveOpts{}, http.StatusCreated)
}

func (s *Server) updateLibrary(w http.ResponseWriter, r *http.Request) {
	o := store.SaveOpts{OldName: r.PathValue("name")}
	if m := strings.Trim(r.Header.Get("If-Match"), `" `); m != "" {
		v, err := strconv.Atoi(m)
		if err != nil {
			writeErr(w, fmt.Errorf("If-Match must be a version number"))
			return
		}
		o.BaseVersion = v
	}
	s.saveLibrary(w, r, o, http.StatusOK)
}

func (s *Server) deleteLibrary(w http.ResponseWriter, r *http.Request) {
	if err := s.st.DeleteLibrary(r.PathValue("name")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) libraryVersions(w http.ResponseWriter, r *http.Request) {
	vs, err := s.st.LibraryVersions(r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, vs)
}

func (s *Server) validateLibrary(w http.ResponseWriter, r *http.Request) {
	raw, err := readBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, err := runbook.ParseLibrary(raw); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- "Run this" from the editor ----------

type testRunReq struct {
	Raw   string            `json:"raw"`  // the runbook as being edited (unsaved)
	Step  int               `json:"step"` // 1-based
	Vars  map[string]string `json:"vars"` // inputs
	Extra map[string]string `json:"extra"`
	// Setup runs the always steps first, to build the environment.
	Setup      bool `json:"setup"`
	Unattended bool `json:"unattended"`
}

// testRun runs one step of a runbook being edited, optionally after its
// setup steps, with test values. It's live like any run but not recorded.
func (s *Server) testRun(w http.ResponseWriter, r *http.Request) {
	var req testRunReq
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	rb, err := runbook.Parse([]byte(req.Raw))
	if err != nil {
		writeErr(w, err)
		return
	}
	if req.Step < 1 || req.Step > len(rb.Steps) {
		writeErr(w, fmt.Errorf("no step %d", req.Step))
		return
	}
	var skip []int
	for i, st := range rb.Steps {
		// Setup is the always steps before it; nothing after it runs.
		if i+1 != req.Step && !(req.Setup && st.Always && i+1 < req.Step) {
			skip = append(skip, i+1)
		}
	}
	vars := map[string]string{}
	for _, in := range rb.Inputs {
		if v, ok := req.Vars[in.Name]; ok {
			vars[in.Name] = v
		}
	}
	state := map[string]string{}
	for k, v := range req.Extra {
		state[k] = v
	}
	sr := startReq{Vars: vars, Unattended: req.Unattended, Skip: skip, test: true, Previous: "-"}
	s.launch(w, r, rb, 0, sr, 0, nil, state)
}

func (s *Server) getTestValues(w http.ResponseWriter, r *http.Request) {
	tv, err := s.st.GetTestValues(r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tv)
}

func (s *Server) putTestValues(w http.ResponseWriter, r *http.Request) {
	var tv store.TestValues
	if err := decode(r, &tv); err != nil {
		writeErr(w, err)
		return
	}
	// Never store secret inputs.
	if d, err := s.st.Load(r.PathValue("name")); err == nil && d.Runbook != nil {
		for _, in := range d.Runbook.Inputs {
			if in.Secret {
				delete(tv.Vars, in.Name)
			}
		}
	}
	if err := s.st.SetTestValues(r.PathValue("name"), tv); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tv)
}

// exportRunbook returns the runbook as Markdown to follow by hand, or with
// ?format=html as a page to read (?v=name=value fills in inputs;
// ?download=1 saves it as a file).
func (s *Server) exportRunbook(w http.ResponseWriter, r *http.Request) {
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
	vars := map[string]string{}
	for _, kv := range r.URL.Query()["v"] {
		if k, v, ok := strings.Cut(kv, "="); ok {
			vars[k] = v
		}
	}
	if r.URL.Query().Get("format") == "html" {
		page, err := htmlview.Render(rb, htmlview.Options{Vars: vars, Libraries: libs})
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Query().Get("download") != "" {
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.html"`, rb.Name))
		}
		w.Write(page)
		return
	}
	md, err := runbook.ExportMarkdown(rb, runbook.ExportOptions{Vars: vars, Libraries: libs})
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	if r.URL.Query().Get("download") != "" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.md"`, rb.Name))
	}
	io.WriteString(w, md)
}

// ---------- Claude ----------

func (s *Server) claudeStatus(w http.ResponseWriter, _ *http.Request) {
	c := s.cfg.Claude
	if c == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "provider": c.Name(), "model": c.ModelName()})
}

// explain asks Claude about a runbook step: {runbook, raw?, step, question?}.
// raw is the runbook as being edited (it needn't be saved); without it, the
// saved runbook. The whole runbook goes as cached context, so questions
// about its other steps soon after cost much less. The answer streams back
// as server-sent events: {"text"}, then {"usage"} (or {"error"}).
func (s *Server) explain(w http.ResponseWriter, r *http.Request) {
	c := s.cfg.Claude
	if c == nil {
		writeErr(w, errors.New("no model is set up: start runnr with ANTHROPIC_API_KEY (or RUNNR_AI_PROVIDER, see runnr explain --help)"))
		return
	}
	var req struct {
		Runbook  string           `json:"runbook"`
		Raw      string           `json:"raw"`
		Step     int              `json:"step"`
		Question string           `json:"question"`
		History  []claude.Message `json:"history"`
		// Fresh asks again rather than replaying a stored answer.
		Fresh bool `json:"fresh"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	var rb *runbook.Runbook
	raw := []byte(req.Raw)
	var err error
	if len(raw) > 0 {
		rb, err = runbook.Parse(raw)
	} else {
		var d *store.Doc
		if d, err = s.st.Load(req.Runbook); err == nil {
			rb, raw, err = d.Runbook, d.Raw, d.ParseErr
		}
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	libs, err := s.st.Libraries(rb.Libraries)
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, err := claude.Question(rb, req.Step, req.Question); err != nil {
		writeErr(w, err)
		return
	}
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	send := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if flusher != nil {
			flusher.Flush()
		}
	}
	usage, err := claude.Explain(r.Context(), c, s.st, rb, raw, libs, req.Step, req.Question, req.History, req.Fresh, func(t string) { send(map[string]string{"text": t}) })
	if err != nil {
		send(map[string]string{"error": err.Error()})
		return
	}
	send(map[string]any{"usage": usage, "model": c.ModelName(), "provider": c.Name()})
}

// ---------- schedules ----------

// listSchedules returns planned runs laid out in time (?from=RFC 3339
// leaves out ones entirely before it).
func (s *Server) listSchedules(w http.ResponseWriter, r *http.Request) {
	var from time.Time
	if v := r.URL.Query().Get("from"); v != "" {
		var err error
		if from, err = time.Parse(time.RFC3339, v); err != nil {
			writeErr(w, fmt.Errorf("from must be RFC 3339, e.g. 2026-11-01T00:00:00Z"))
			return
		}
	}
	loc := time.Local
	if tz := r.URL.Query().Get("tz"); tz != "" {
		var err error
		if loc, err = time.LoadLocation(tz); err != nil {
			writeErr(w, fmt.Errorf("tz: %w", err))
			return
		}
	}
	ag, err := s.st.Agenda(from, loc)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ag)
}

func (s *Server) getSchedule(w http.ResponseWriter, r *http.Request) {
	sc, err := s.st.GetSchedule(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

// saveSchedule creates (POST) or replaces (PUT /{id}) a schedule.
func (s *Server) saveSchedule(w http.ResponseWriter, r *http.Request) {
	var sc store.Schedule
	if err := decode(r, &sc); err != nil {
		writeErr(w, err)
		return
	}
	code := http.StatusCreated
	if id := r.PathValue("id"); id != "" {
		old, err := s.st.GetSchedule(id)
		if err != nil {
			writeErr(w, err)
			return
		}
		sc.ID, sc.CreatedAt, code = id, old.CreatedAt, http.StatusOK
		if sc.CreatedBy == "" {
			sc.CreatedBy = old.CreatedBy
		}
	}
	if err := s.st.SaveSchedule(&sc); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, code, sc)
}

func (s *Server) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	if err := s.st.DeleteSchedule(r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- collections ----------

// addToCollection appends runbooks (body {"runbooks": [...], "create": bool}).
func (s *Server) addToCollection(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Runbooks []string `json:"runbooks"`
		Create   bool     `json:"create"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if len(body.Runbooks) == 0 {
		writeErr(w, errors.New("no runbooks given"))
		return
	}
	ver, created, err := s.st.AddToCollection(r.PathValue("name"), body.Create, body.Runbooks...)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": ver, "created": created})
}

// setCollectionRunbooks replaces the list (body {"runbooks": [...]}), e.g.
// to reorder it.
func (s *Server) setCollectionRunbooks(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Runbooks []string `json:"runbooks"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	ver, err := s.st.SetCollectionRunbooks(r.PathValue("name"), body.Runbooks)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": ver})
}

// patchCollection changes name, description or expect (fields left out
// stay as they are).
func (s *Server) patchCollection(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Expect      *string `json:"expect"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	name, ver, err := s.st.UpdateCollection(r.PathValue("name"), body.Name, body.Description, body.Expect)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "version": ver})
}

func (s *Server) removeFromCollection(w http.ResponseWriter, r *http.Request) {
	ver, err := s.st.RemoveFromCollection(r.PathValue("name"), r.PathValue("runbook"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": ver})
}

// moveRunbook moves a runbook to another collection (body {"to", "from"};
// from is needed only when it's in several).
func (s *Server) moveRunbook(w http.ResponseWriter, r *http.Request) {
	var body struct{ To, From string }
	if err := decode(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if body.To == "" {
		writeErr(w, errors.New(`"to" is required`))
		return
	}
	from, err := s.st.MoveRunbook(r.PathValue("name"), body.From, body.To, true)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": body.To})
}

func (s *Server) listCollections(w http.ResponseWriter, _ *http.Request) {
	cs, err := s.st.ListCollections()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cs)
}

func (s *Server) getCollection(w http.ResponseWriter, r *http.Request) {
	d, err := s.st.LoadCollection(r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) saveCollection(w http.ResponseWriter, r *http.Request, o store.SaveOpts, code int) {
	raw, err := readBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	name, ver, err := s.st.SaveCollection(raw, o)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, code, map[string]any{"name": name, "version": ver})
}

func (s *Server) createCollection(w http.ResponseWriter, r *http.Request) {
	s.saveCollection(w, r, store.SaveOpts{}, http.StatusCreated)
}

func (s *Server) updateCollection(w http.ResponseWriter, r *http.Request) {
	o := store.SaveOpts{OldName: r.PathValue("name")}
	if m := strings.Trim(r.Header.Get("If-Match"), `" `); m != "" {
		v, err := strconv.Atoi(m)
		if err != nil {
			writeErr(w, fmt.Errorf("If-Match must be a version number"))
			return
		}
		o.BaseVersion = v
	}
	s.saveCollection(w, r, o, http.StatusOK)
}

func (s *Server) deleteCollection(w http.ResponseWriter, r *http.Request) {
	if err := s.st.DeleteCollection(r.PathValue("name")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) validateCollection(w http.ResponseWriter, r *http.Request) {
	raw, err := readBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, err := runbook.ParseCollection(raw); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// unattendedWeb is an unattended run's interactor here: it asks nothing,
// but the console can still drive the run (its session: shells alongside,
// stop step, go to step, inputs) and watch its background steps.
type unattendedWeb struct {
	engine.Unattended
	l *liveRun
}

func (u unattendedWeb) Bind(se engine.Session) { u.l.Bind(se) }

func (u unattendedWeb) StartBackground(idx int, title string) engine.TermConn {
	return u.l.StartBackground(idx, title)
}
