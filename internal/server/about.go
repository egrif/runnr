package server

import (
	"net/http"
	"runtime"
	"strings"
	"time"
)

// Build is what the server says about the runnr it is (set by the CLI).
type Build struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	Committed string `json:"committed,omitempty"`
	Modified  bool   `json:"modified,omitempty"`
}

// about is runnr's version and build, where its data is, and how long the
// server has been up: GET /api/about.
func (s *Server) about(w http.ResponseWriter, _ *http.Request) {
	b := s.cfg.Build
	if b.Version == "" {
		b.Version = "dev"
	}
	db := "SQLite"
	if strings.HasPrefix(s.st.Location, "postgres") {
		db = "PostgreSQL"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"build":    b,
		"go":       runtime.Version(),
		"platform": runtime.GOOS + "/" + runtime.GOARCH,
		"database": map[string]string{"kind": db, "location": s.st.Location},
		"started":  s.started.Format(time.RFC3339),
		"claude":   s.cfg.Claude != nil,
	})
}
