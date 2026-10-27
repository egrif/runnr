package store

import (
	"time"

	"github.com/egrif/runnr/internal/runtest"
	"github.com/egrif/runnr/internal/ver"
)

// Fake libraries and test suites are stored like function libraries:
// named, versioned YAML documents.
var (
	fakesKind = docKind{noun: "fake library", table: "fake_libraries", versions: "fake_library_versions",
		parse: func(raw []byte) (string, error) {
			l, err := runtest.ParseLibrary(raw)
			if err != nil {
				return "", err
			}
			return l.Name, nil
		}}
	suiteKind = docKind{noun: "test suite", table: "test_suites", versions: "test_suite_versions",
		parse: func(raw []byte) (string, error) {
			s, err := runtest.ParseSuite(raw)
			if err != nil {
				return "", err
			}
			return s.Name, nil
		}}
)

// TestDoc is a stored fake library or test suite, as written.
type TestDoc struct {
	Name      string    `json:"name"`
	Raw       []byte    `json:"-"`
	Version   ver.ID    `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
}

func docs(rows []docRow) []TestDoc {
	out := []TestDoc{}
	for _, r := range rows {
		out = append(out, TestDoc{Name: r.Name, Raw: r.Raw, Version: r.Version, UpdatedAt: r.UpdatedAt})
	}
	return out
}

func (s *Store) ListFakes() ([]TestDoc, error) {
	rows, err := s.listDocs(fakesKind)
	return docs(rows), err
}

func (s *Store) LoadFakes(name string) (*TestDoc, error) {
	r, err := s.loadDoc(fakesKind, name)
	if err != nil {
		return nil, err
	}
	return &TestDoc{Name: r.Name, Raw: r.Raw, Version: r.Version, UpdatedAt: r.UpdatedAt}, nil
}

// Fakes loads a fake library, failing if it's missing or invalid.
func (s *Store) Fakes(name string) (*runtest.Library, error) {
	d, err := s.LoadFakes(name)
	if err != nil {
		return nil, err
	}
	return runtest.ParseLibrary(d.Raw)
}

func (s *Store) SaveFakes(raw []byte, o SaveOpts) (string, ver.ID, error) {
	return s.saveDoc(fakesKind, raw, o)
}
func (s *Store) DeleteFakes(name string) error { return s.deleteDoc(fakesKind, name) }

func (s *Store) ListSuites() ([]TestDoc, error) {
	rows, err := s.listDocs(suiteKind)
	return docs(rows), err
}

func (s *Store) LoadSuite(name string) (*TestDoc, error) {
	r, err := s.loadDoc(suiteKind, name)
	if err != nil {
		return nil, err
	}
	return &TestDoc{Name: r.Name, Raw: r.Raw, Version: r.Version, UpdatedAt: r.UpdatedAt}, nil
}

func (s *Store) SaveSuite(raw []byte, o SaveOpts) (string, ver.ID, error) {
	return s.saveDoc(suiteKind, raw, o)
}
func (s *Store) DeleteSuite(name string) error { return s.deleteDoc(suiteKind, name) }
