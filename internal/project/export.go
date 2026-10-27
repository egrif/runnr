package project

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/egrif/runnr/internal/store"
	"github.com/egrif/runnr/internal/ver"
)

// Exporting a runbook to a file, as written (comments and placeholders and
// all), with the libraries it uses beside it (each <name>.yaml in the same
// folder), so the folder imports again as a project. Without a file, each
// goes back where it was imported from (a library that wasn't imported
// from a file goes beside the runbook).

// ExportFile is a file an export writes.
type ExportFile struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	Version ver.ID `json:"version,omitempty"`
	// Exists: there's a file at Path already (written over only if asked);
	// Same: and it's what would be written (it's left alone).
	Exists bool `json:"exists,omitempty"`
	Same   bool `json:"same,omitempty"`
	// Builtin: one of runnr's own libraries (left out unless asked).
	Builtin bool   `json:"builtin,omitempty"`
	Missing bool   `json:"missing,omitempty"`
	raw     []byte `json:"-"`
}

// ExportPlan says what exporting runbook name to file would write: the
// runbook, then each library it uses. file can be a folder (the runbook
// goes in as <name>.yaml).
func ExportPlan(st *store.Store, name, file string) ([]ExportFile, error) {
	d, err := st.Load(name)
	if err != nil {
		return nil, err
	}
	if d.Runbook == nil {
		return nil, fmt.Errorf("runbook %s isn't valid: %v", name, d.ParseErr)
	}
	file = expand(file)
	back := file == "" // to where each came from
	if back {
		o, err := st.Origin(store.KindRunbook, name)
		if err != nil {
			return nil, err
		}
		if o == nil || o.Path == "" {
			return nil, fmt.Errorf("runbook %s wasn't imported from a file here: give a file to export to", name)
		}
		file = o.Path
	}
	if st, err := os.Stat(file); (err == nil && st.IsDir()) || strings.HasSuffix(file, "/") {
		file = filepath.Join(file, name+".yaml")
	}
	out := []ExportFile{{Kind: Runbook, Name: name, Path: file, Version: d.Version, raw: d.Raw}}
	dir := filepath.Dir(file)
	for _, l := range d.Runbook.Libraries {
		f := ExportFile{Kind: Library, Name: l, Path: filepath.Join(dir, l+".yaml")}
		if ld, err := st.LoadLibrary(l); err != nil {
			f.Missing = true
		} else {
			f.Version, f.Builtin, f.raw = ld.Version, ld.Builtin, ld.Raw
		}
		if back {
			if o, _ := st.Origin(store.KindLibrary, l); o != nil && o.Path != "" {
				f.Path = o.Path
			}
		}
		out = append(out, f)
	}
	for i := range out {
		if b, err := os.ReadFile(out[i].Path); err == nil {
			out[i].Exists, out[i].Same = true, bytes.Equal(b, out[i].raw)
		} else if _, err := os.Stat(out[i].Path); err == nil {
			out[i].Exists = true
		}
	}
	return out, nil
}

// Export writes runbook name to file, with the libraries named in libs.
// Files already there are written over only with overwrite.
func Export(st *store.Store, name, file string, libs []string, overwrite bool) ([]ExportFile, error) {
	plan, err := ExportPlan(st, name, file)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, l := range libs {
		want[l] = true
	}
	var todo []ExportFile
	var there []string
	for _, f := range plan {
		if f.Kind == Library && !want[f.Name] {
			continue
		}
		if f.Missing {
			return nil, fmt.Errorf("library %s isn't stored", f.Name)
		}
		if f.Same {
			continue
		}
		if f.Exists && !overwrite {
			there = append(there, f.Path)
		}
		todo = append(todo, f)
	}
	if len(there) > 0 {
		return nil, fmt.Errorf("already there (write over them, or choose another file): %s", strings.Join(there, ", "))
	}
	for _, f := range todo {
		if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(f.Path, f.raw, 0o644); err != nil {
			return nil, err
		}
	}
	return todo, nil
}

func expand(p string) string {
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
