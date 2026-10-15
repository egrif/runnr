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
	"net/http"
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
	if errors.Is(err, store.ErrNotFound) {
		code = http.StatusNotFound
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
	raw, err := s.st.Raw(r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	resp := map[string]any{"raw": string(raw)}
	if rb, err := runbook.Parse(raw); err != nil {
		resp["error"] = err.Error()
	} else {
		resp["runbook"] = rb
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) createRunbook(w http.ResponseWriter, r *http.Request) {
	raw, err := readBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	rb, err := runbook.Parse(raw)
	if err != nil {
		writeErr(w, err)
		return
	}
	if s.st.Exists(rb.Name) {
		writeErr(w, fmt.Errorf("runbook %q already exists", rb.Name))
		return
	}
	if _, err := s.st.Save(raw, ""); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, rb)
}

func (s *Server) updateRunbook(w http.ResponseWriter, r *http.Request) {
	old := r.PathValue("name")
	raw, err := readBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	rb, err := runbook.Parse(raw)
	if err != nil {
		writeErr(w, err)
		return
	}
	if rb.Name != old && s.st.Exists(rb.Name) {
		writeErr(w, fmt.Errorf("can't rename: runbook %q already exists", rb.Name))
		return
	}
	if _, err := s.st.Save(raw, old); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rb)
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
	rb, err := s.st.Get(req.Runbook)
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
	s.launch(w, r, rb, req, start, "")
}

func (s *Server) resumeRun(w http.ResponseWriter, r *http.Request) {
	var req startReq
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	var prev engine.Run
	if err := s.st.LoadRun(r.PathValue("id"), &prev); err != nil {
		writeErr(w, err)
		return
	}
	start := engine.ResumePoint(&prev)
	if start < 0 {
		writeErr(w, fmt.Errorf("run %s has no unfinished steps", prev.ID))
		return
	}
	rb, err := s.st.Get(prev.Runbook)
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(rb.Steps) != len(prev.Steps) || rb.Steps[start].Name != prev.Steps[start].Name {
		writeErr(w, fmt.Errorf("runbook %q changed since run %s; start a new run with \"from\"", rb.Name, prev.ID))
		return
	}
	vars := engine.ResumeInputs(&prev)
	for k, v := range req.Vars {
		vars[k] = v
	}
	req.Vars = vars
	s.launch(w, r, rb, req, start, prev.ID)
}

func (s *Server) launch(w http.ResponseWriter, r *http.Request, rb *runbook.Runbook, req startReq, start int, resumes string) {
	if req.Wait && !req.Unattended {
		writeErr(w, errors.New("wait requires unattended (nobody could answer prompts)"))
		return
	}
	id := engine.NewRunID()
	ctx, cancel := context.WithCancel(context.Background())
	l := newLiveRun(id, cancel)
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
			Vars:        req.Vars,
			DryRun:      req.DryRun,
			Interactive: !req.Unattended,
			StartAt:     start,
			Resumes:     resumes,
			Save:        func(run *engine.Run) { _ = s.st.SaveRun(run.ID, run) },
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

func (s *Server) getLive(id string) *liveRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live[id]
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	ids, err := s.st.RunIDs()
	if err != nil {
		writeErr(w, err)
		return
	}
	filter := r.URL.Query().Get("runbook")
	runs := []engine.Run{}
	for _, id := range ids {
		if len(runs) >= 100 {
			break
		}
		var run engine.Run
		if s.st.LoadRun(id, &run) != nil || (filter != "" && run.Runbook != filter) {
			continue
		}
		run.Steps = nil
		runs = append(runs, run)
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	var run engine.Run
	if err := s.st.LoadRun(r.PathValue("id"), &run); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run, "live": s.getLive(run.ID) != nil})
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

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	l := s.getLive(r.PathValue("id"))
	if l == nil {
		writeErr(w, fmt.Errorf("run not live: %w", store.ErrNotFound))
		return
	}
	l.cancel()
	w.WriteHeader(http.StatusNoContent)
}
