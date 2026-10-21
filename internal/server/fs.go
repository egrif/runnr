package server

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Browsing this machine's folders and files, for inputs that are paths
// (type: dir or file) and for choosing where to import from or export to:
// runs, imports and exports use this machine's paths, which the browser
// can't hand over.
//
//	GET /api/fs?path=P[&hidden=1]  → the folder at P (or holding P, or the
//	                                 nearest one that exists): its entries
type fsEntry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size,omitempty"`
}

func (s *Server) listFS(w http.ResponseWriter, r *http.Request) {
	home, _ := os.UserHomeDir()
	p := expandHome(strings.TrimSpace(r.URL.Query().Get("path")))
	if p == "" {
		p = home
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(home, p)
	}
	p = filepath.Clean(p)
	asked := p
	// The folder itself, the one holding a file, or the nearest that exists.
	for {
		st, err := os.Stat(p)
		if err == nil && st.IsDir() {
			break
		}
		if filepath.Dir(p) == p {
			break
		}
		p = filepath.Dir(p)
	}
	ents, err := os.ReadDir(p)
	if err != nil {
		writeErr(w, err)
		return
	}
	hidden := r.URL.Query().Get("hidden") == "1"
	out := []fsEntry{}
	for _, e := range ents {
		if !hidden && strings.HasPrefix(e.Name(), ".") {
			continue
		}
		fe := fsEntry{Name: e.Name(), Dir: e.IsDir()}
		if info, err := e.Info(); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				if st, err := os.Stat(filepath.Join(p, e.Name())); err == nil {
					fe.Dir = st.IsDir()
				}
			}
			if !fe.Dir {
				fe.Size = info.Size()
			}
		}
		out = append(out, fe)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	st, err := os.Stat(asked)
	writeJSON(w, http.StatusOK, map[string]any{
		"path": p, "parent": filepath.Dir(p), "home": home, "entries": out,
		// What the path asked for is: a folder, a file, or nothing yet.
		"asked": asked, "exists": err == nil, "is_dir": err == nil && st.IsDir(),
	})
}
