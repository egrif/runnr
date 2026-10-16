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

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/runbook"
	"github.com/egrif/runnr/internal/store"
)

//go:embed web
var webFS embed.FS

type Server struct {
	st   *store.Store
	cfg  Config
	mu   sync.Mutex
	live map[string]*liveRun
	wg   sync.WaitGroup
}

type Config struct {
	// Token, when set, is required on every API request, either as
	// "Authorization: Bearer <token>" or via the GUI's login cookie.
	Token string
	// NoUI serves only the API.
	NoUI bool
}

func New(st *store.Store, cfg Config) *Server {
	return &Server{st: st, cfg: cfg, live: map[string]*liveRun{}}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	if !s.cfg.NoUI {
		static, _ := fs.Sub(webFS, "web")
		mux.Handle("GET /", http.FileServerFS(static))
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
	mux.HandleFunc("GET /api/template", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"raw": runbook.Scaffold(nonEmpty(r.URL.Query().Get("name"), "my-runbook"))})
	})

	mux.HandleFunc("GET /api/runs", s.listRuns)
	mux.HandleFunc("POST /api/runs", s.startRun)
	mux.HandleFunc("GET /api/runs/{id}", s.getRun)
	mux.HandleFunc("GET /api/runs/{id}/events", s.runEvents)
	mux.HandleFunc("POST /api/runs/{id}/answer", s.answer)
	mux.HandleFunc("POST /api/runs/{id}/cancel", s.cancelRun)
	mux.HandleFunc("POST /api/runs/{id}/resume", s.resumeRun)
	mux.HandleFunc("POST /api/runs/{id}/notes", s.addNote)
	mux.HandleFunc("GET /api/runs/{id}/preview", s.runPreview)
	mux.HandleFunc("POST /api/runs/{id}/term", s.termInput)
	mux.HandleFunc("GET /api/runs/{id}/controls", s.getControls)
	mux.HandleFunc("PUT /api/runs/{id}/controls", s.setControls)
	mux.HandleFunc("POST /api/preview", s.preview)
	mux.HandleFunc("POST /api/parse", s.parse)
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
		l.cancel()
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

func (s *Server) createRunbook(w http.ResponseWriter, r *http.Request) {
	s.saveRunbook(w, r, store.SaveOpts{}, http.StatusCreated)
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
	if err := s.st.Delete(r.PathValue("name")); err != nil {
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
	Runbook    string            `json:"runbook"`
	Vars       map[string]string `json:"vars"`
	Unattended bool              `json:"unattended"`
	Yes        bool              `json:"yes"`
	DryRun     bool              `json:"dry_run"`
	// From starts at a later step (id, 1-based number or name).
	From string `json:"from"`
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
	s.launch(w, r, rb, ver, req, start, "", nil)
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
	if len(rb.Steps) != len(prev.Steps) || rb.Steps[start].Name != prev.Steps[start].Name {
		writeErr(w, fmt.Errorf("runbook %q changed since run %s; start a new run with \"from\"", rb.Name, prev.ID))
		return
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
	}
	s.launch(w, r, rb, ver, req, start, prev.ID, engine.ResumeState(prev))
}

func (s *Server) launch(w http.ResponseWriter, r *http.Request, rb *runbook.Runbook, ver int, req startReq, start int, resumes string, state map[string]string) {
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
	if req.Wait && !req.Unattended {
		writeErr(w, errors.New("wait requires unattended (nobody could answer prompts)"))
		return
	}
	id := engine.NewRunID()
	ctx, cancel := context.WithCancel(context.Background())
	l := newLiveRun(id, cancel)
	l.ctl = engine.NewControls(cv)
	var ui engine.Interactor = l
	if req.Unattended {
		ui = engine.Unattended{OnEvent: l.Notify, AssumeYes: req.Yes}
	}
	s.mu.Lock()
	s.live[id] = l
	s.mu.Unlock()

	done := make(chan *engine.Run, 1)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		done <- engine.Execute(ctx, rb, ui, id, engine.Options{
			Vars:           req.Vars,
			DryRun:         req.DryRun,
			Interactive:    !req.Unattended,
			StartAt:        start,
			Controls:       l.ctl,
			Previous:       previous,
			State:          state,
			Resumes:        resumes,
			RunbookVersion: ver,
			Save: func(run *engine.Run) {
				if err := s.st.SaveRun(run); err != nil {
					log.Printf("saving run %s: %v", run.ID, err)
				}
			},
		})
		// Keep the event log around briefly for late subscribers.
		time.AfterFunc(10*time.Minute, func() {
			s.mu.Lock()
			delete(s.live, id)
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
		state = prev.State
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
	writeJSON(w, http.StatusOK, map[string]any{"steps": engine.Preview(rb, run.Inputs, run.State)})
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
	var req struct {
		ID   string `json:"id"`
		Data string `json:"data"`
		Rows uint16 `json:"rows"`
		Cols uint16 `json:"cols"`
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
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "runbook": rb})
}

// format turns the structured form back into YAML.
func (s *Server) format(w http.ResponseWriter, r *http.Request) {
	var rb runbook.Runbook
	if err := decode(r, &rb); err != nil {
		writeErr(w, err)
		return
	}
	raw, err := runbook.Format(&rb)
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
		if l.ctl != nil {
			resp["controls"] = toJSON(l.ctl.View())
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) runEvents(w http.ResponseWriter, r *http.Request) {
	l := s.getLive(r.PathValue("id"))
	if l == nil {
		writeErr(w, fmt.Errorf("run %q is not live: %w", r.PathValue("id"), store.ErrNotFound))
		return
	}
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

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	l := s.getLive(r.PathValue("id"))
	if l == nil {
		writeErr(w, fmt.Errorf("run not live: %w", store.ErrNotFound))
		return
	}
	l.cancel()
	w.WriteHeader(http.StatusNoContent)
}
