package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/store"
)

// Who's watching a live run, who may drive it, and whose window size its
// terminals take.
//
// Each browser tab says who it is with X-Runnr-User (the name it's given,
// as for notes) and X-Runnr-Session (an id of its own); event streams,
// which can't send headers, say it as ?name=&session=. Names aren't
// checked: read-only is a safety catch against driving someone else's
// run by mistake, not a lock. A read-only viewer can override it
// (X-Runnr-Override: 1); everyone watching is told, and the run notes it.

const (
	hdrUser     = "X-Runnr-User"
	hdrSession  = "X-Runnr-Session"
	hdrOverride = "X-Runnr-Override"
)

// Access is who may drive a live run: everyone, only the people named,
// or everyone except them.
type Access struct {
	Mode  string    `json:"mode"` // everyone | only | except
	Names []string  `json:"names,omitempty"`
	By    string    `json:"by,omitempty"`
	At    time.Time `json:"at,omitzero"`
}

// Viewer is someone with the run open.
type Viewer struct {
	Session string    `json:"session"`
	Name    string    `json:"name"`
	Since   time.Time `json:"since"`
	// Typed: they typed last in one of the run's terminals (whose size it
	// takes).
	Typed bool `json:"typed,omitempty"`
	conns int
}

// Presence is a live run's viewers and access, as its events carry them.
type Presence struct {
	Viewers   []Viewer `json:"viewers"`
	Access    Access   `json:"access"`
	StartedBy string   `json:"started_by,omitempty"`
}

type presence struct {
	mu        sync.Mutex
	viewers   map[string]*Viewer
	access    Access
	startedBy string
	// overrode: sessions that have overridden read-only (told once).
	overrode map[string]bool
	// terms: per terminal, who typed last and each viewer's window size.
	terms map[string]*termSize
}

type termSize struct {
	last  string
	sizes map[string][2]uint16
}

func sameName(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// mayDrive says whether name may drive the run under a.
func (a Access) mayDrive(name string) bool {
	in := slices.ContainsFunc(a.Names, func(n string) bool { return sameName(n, name) })
	switch a.Mode {
	case "only":
		return in && strings.TrimSpace(name) != ""
	case "except":
		return !in
	}
	return true
}

func (a Access) describe() string {
	switch a.Mode {
	case "only":
		return "only " + strings.Join(a.Names, ", ") + " can drive this run"
	case "except":
		return strings.Join(a.Names, ", ") + " can only watch this run"
	}
	return "everyone can drive this run"
}

func (l *liveRun) presenceNow() Presence {
	l.pres.mu.Lock()
	defer l.pres.mu.Unlock()
	p := Presence{Viewers: []Viewer{}, Access: l.pres.access, StartedBy: l.pres.startedBy}
	if p.Access.Mode == "" {
		p.Access.Mode = "everyone"
	}
	last := ""
	for _, t := range l.pres.terms {
		last = t.last
	}
	for _, v := range l.pres.viewers {
		c := *v
		c.Typed = last != "" && c.Session == last
		p.Viewers = append(p.Viewers, c)
	}
	slices.SortFunc(p.Viewers, func(a, b Viewer) int { return a.Since.Compare(b.Since) })
	return p
}

// broadcastPresence tells everyone watching who's there and who may drive
// (not kept in the backlog: a new viewer is sent it as it joins).
func (l *liveRun) broadcastPresence() {
	p := l.presenceNow()
	l.broadcast(WireEvent{Seq: -1, Event: engine.Event{Kind: "presence", Step: -1}, Presence: &p})
}

func (l *liveRun) join(session, name string) {
	if session == "" {
		return
	}
	l.pres.mu.Lock()
	if l.pres.viewers == nil {
		l.pres.viewers = map[string]*Viewer{}
	}
	v := l.pres.viewers[session]
	if v == nil {
		v = &Viewer{Session: session, Since: time.Now()}
		l.pres.viewers[session] = v
	}
	v.Name = strings.TrimSpace(name)
	v.conns++
	l.pres.mu.Unlock()
	l.broadcastPresence()
}

func (l *liveRun) leave(session string) {
	l.pres.mu.Lock()
	if v := l.pres.viewers[session]; v != nil {
		if v.conns--; v.conns <= 0 {
			delete(l.pres.viewers, session)
		}
	}
	l.pres.mu.Unlock()
	l.broadcastPresence()
}

// allowed checks that the request may drive l. A read-only viewer gets
// 403 {code: read_only} unless it overrides; the first override from a
// session is announced and noted on the run.
func (s *Server) allowed(w http.ResponseWriter, r *http.Request, l *liveRun) bool {
	name, session := strings.TrimSpace(r.Header.Get(hdrUser)), r.Header.Get(hdrSession)
	l.pres.mu.Lock()
	a := l.pres.access
	l.pres.mu.Unlock()
	if a.mayDrive(name) {
		return true
	}
	if r.Header.Get(hdrOverride) == "" {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": fmt.Sprintf("read-only for you: %s (set by %s). Override to go ahead anyway.", a.describe(), nonEmpty(a.By, "someone")),
			"code":  "read_only", "run": l.id, "access": a,
		})
		return false
	}
	l.pres.mu.Lock()
	first := !l.pres.overrode[session+"\x00"+name]
	if l.pres.overrode == nil {
		l.pres.overrode = map[string]bool{}
	}
	l.pres.overrode[session+"\x00"+name] = true
	l.pres.mu.Unlock()
	if first {
		who := nonEmpty(name, "someone without a name")
		text := fmt.Sprintf("%s overrode read-only (%s) to drive the run", who, a.describe())
		// A note: everyone watching sees it, and it stays with the run.
		if note, err := s.st.AddNote(l.id, -1, who, "⚠ "+text); err == nil {
			l.publish(WireEvent{Event: engine.Event{Kind: "note", Step: -1}, Note: &note})
		}
	}
	return true
}

// driving wraps a handler that drives the run {id}: if it's live here, the
// request must be allowed to drive it.
func (s *Server) driving(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if l := s.getLive(r.PathValue("id")); l != nil && !s.allowed(w, r, l) {
			return
		}
		h(w, r)
	}
}

// setAccess sets who may drive a live run (PUT /api/runs/{id}/access
// {mode, names}).
func (s *Server) setAccess(w http.ResponseWriter, r *http.Request) {
	l := s.getLive(r.PathValue("id"))
	if l == nil {
		writeErr(w, fmt.Errorf("run not live: %w", store.ErrNotFound))
		return
	}
	var a Access
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		writeErr(w, err)
		return
	}
	var names []string
	for _, n := range a.Names {
		if n = strings.TrimSpace(n); n != "" && !slices.ContainsFunc(names, func(m string) bool { return sameName(m, n) }) {
			names = append(names, n)
		}
	}
	switch a.Mode {
	case "everyone", "":
		a.Mode, names = "everyone", nil
	case "only", "except":
		if len(names) == 0 {
			writeErr(w, fmt.Errorf("%s: name at least one person", a.Mode))
			return
		}
	default:
		writeErr(w, fmt.Errorf("mode %q: everyone, only or except", a.Mode))
		return
	}
	a.Names, a.By, a.At = names, nonEmpty(strings.TrimSpace(r.Header.Get(hdrUser)), "someone"), time.Now()
	l.pres.mu.Lock()
	l.pres.access = a
	l.pres.mu.Unlock()
	l.publish(WireEvent{Event: engine.Event{Kind: engine.EventOutput, Step: -1, Stream: "info", Text: fmt.Sprintf("%s: %s", a.By, a.describe())}})
	l.broadcastPresence()
	writeJSON(w, http.StatusOK, l.presenceNow())
}

// sizeFor says what size a terminal takes after a message from session:
// whoever typed last sets it (a viewer who only resizes doesn't, until
// they type).
func (l *liveRun) sizeFor(term, session string, typed bool, rows, cols uint16) (uint16, uint16, bool) {
	l.pres.mu.Lock()
	defer l.pres.mu.Unlock()
	if l.pres.terms == nil {
		l.pres.terms = map[string]*termSize{}
	}
	t := l.pres.terms[term]
	if t == nil {
		t = &termSize{sizes: map[string][2]uint16{}}
		l.pres.terms[term] = t
	}
	if rows > 0 && cols > 0 {
		t.sizes[session] = [2]uint16{rows, cols}
	}
	changed := typed && t.last != session
	if typed {
		t.last = session
	}
	if t.last != "" && t.last != session {
		return 0, 0, false
	}
	sz, ok := t.sizes[session]
	return sz[0], sz[1], ok && (changed || rows > 0)
}
