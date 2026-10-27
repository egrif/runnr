package store

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/egrif/runnr/internal/runbook"
	"github.com/egrif/runnr/internal/ver"
)

var libraryKind = docKind{noun: "library", table: "libraries", versions: "library_versions",
	parse: func(raw []byte) (string, error) {
		l, err := runbook.ParseLibrary(raw)
		if err != nil {
			return "", err
		}
		return l.Name, nil
	}}

// LibraryEntry is a library listing row.
type LibraryEntry struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Functions   []string  `json:"functions"`
	Version     ver.ID    `json:"version"`
	UpdatedAt   time.Time `json:"updated_at"`
	Err         string    `json:"error,omitempty"`
	// Builtin: it ships with runnr (its version is its text's, like any other); saving one by that name
	// replaces it.
	Builtin bool `json:"builtin,omitempty"`
}

func (s *Store) ListLibraries() ([]LibraryEntry, error) {
	rows, err := s.listDocs(libraryKind)
	if err != nil {
		return nil, err
	}
	out := []LibraryEntry{}
	for _, r := range rows {
		e := LibraryEntry{Name: r.Name, Version: r.Version, UpdatedAt: r.UpdatedAt, Functions: []string{}}
		if lib, err := runbook.ParseLibrary(r.Raw); err != nil {
			e.Err = err.Error()
		} else {
			e.Description = lib.Description
			for f := range lib.Functions {
				e.Functions = append(e.Functions, f)
			}
		}
		out = append(out, e)
	}
	for _, n := range runbook.BuiltinLibraries() {
		if slices.ContainsFunc(out, func(e LibraryEntry) bool { return e.Name == n }) {
			continue
		}
		e := LibraryEntry{Name: n, Version: ver.Of(runbook.BuiltinLibrary(n)), Builtin: true, Functions: []string{}}
		if lib, err := runbook.ParseLibrary(runbook.BuiltinLibrary(n)); err == nil {
			e.Description = lib.Description
			for f := range lib.Functions {
				e.Functions = append(e.Functions, f)
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// LibraryDoc is a stored library; Library is nil if Raw doesn't parse.
type LibraryDoc struct {
	Name      string
	Raw       []byte
	Version   ver.ID
	UpdatedAt time.Time
	Library   *runbook.Library
	ParseErr  error
	Builtin   bool
}

func (s *Store) LoadLibrary(name string) (*LibraryDoc, error) {
	r, err := s.loadDoc(libraryKind, name)
	if errors.Is(err, ErrNotFound) {
		if raw := runbook.BuiltinLibrary(name); raw != nil {
			d := &LibraryDoc{Name: name, Raw: raw, Version: ver.Of(raw), Builtin: true}
			d.Library, d.ParseErr = runbook.ParseLibrary(raw)
			return d, nil
		}
	}
	if err != nil {
		return nil, err
	}
	d := &LibraryDoc{Name: name, Raw: r.Raw, Version: r.Version, UpdatedAt: r.UpdatedAt}
	d.Library, d.ParseErr = runbook.ParseLibrary(d.Raw)
	return d, nil
}

// Libraries loads the named libraries, in order, failing on any that are
// missing or invalid.
func (s *Store) Libraries(names []string) ([]*runbook.Library, error) {
	var out []*runbook.Library
	for _, n := range names {
		d, err := s.LoadLibrary(n)
		if err != nil {
			return nil, err
		}
		if d.ParseErr != nil {
			return nil, fmt.Errorf("library %q: %w", n, d.ParseErr)
		}
		out = append(out, d.Library)
	}
	return out, nil
}

// SaveLibrary validates raw YAML and stores it as a new version.
func (s *Store) SaveLibrary(raw []byte, o SaveOpts) (*runbook.Library, ver.ID, error) {
	if _, ver, err := s.saveDoc(libraryKind, raw, o); err != nil {
		return nil, "", err
	} else {
		lib, _ := runbook.ParseLibrary(raw)
		return lib, ver, nil
	}
}

func (s *Store) DeleteLibrary(name string) error { return s.deleteDoc(libraryKind, name) }
func (s *Store) LibraryVersions(name string) ([]Version, error) {
	return s.docVersions(libraryKind, name)
}
func (s *Store) RawLibraryVersion(name, ref string) ([]byte, ver.ID, error) {
	return s.rawDocVersion(libraryKind, name, ref)
}

// LibrariesUsed are the current versions of the named libraries (built-in
// ones too), for a run's record; a missing one is left out.
func (s *Store) LibrariesUsed(names []string) map[string]ver.ID {
	out := map[string]ver.ID{}
	for _, n := range names {
		if d, err := s.LoadLibrary(n); err == nil {
			out[n] = d.Version
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
