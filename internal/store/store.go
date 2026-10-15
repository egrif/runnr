// Package store persists runbooks and run records under a home directory.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/egrif/runnr/internal/runbook"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	Home string
}

// DefaultHome is $RUNNR_HOME, else ~/.runnr.
func DefaultHome() string {
	if h := os.Getenv("RUNNR_HOME"); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".runnr")
	}
	return ".runnr"
}

func New(home string) (*Store, error) {
	s := &Store{Home: home}
	for _, d := range []string{s.runbookDir(), s.runDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) runbookDir() string { return filepath.Join(s.Home, "runbooks") }
func (s *Store) runDir() string     { return filepath.Join(s.Home, "runs") }

func (s *Store) Path(name string) string {
	return filepath.Join(s.runbookDir(), name+".yaml")
}

// Entry is a listing row; Err is set when the file fails to parse.
type Entry struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Steps       int      `json:"steps"`
	Err         string   `json:"error,omitempty"`
}

func (s *Store) List() ([]Entry, error) {
	files, err := filepath.Glob(filepath.Join(s.runbookDir(), "*.yaml"))
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".yaml")
		rb, err := s.Get(name)
		if err != nil {
			out = append(out, Entry{Name: name, Err: err.Error()})
			continue
		}
		out = append(out, Entry{Name: name, Description: rb.Description, Tags: rb.Tags, Steps: len(rb.Steps)})
	}
	return out, nil
}

func (s *Store) Raw(name string) ([]byte, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(s.Path(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("runbook %q: %w", name, ErrNotFound)
	}
	return b, err
}

func (s *Store) Get(name string) (*runbook.Runbook, error) {
	b, err := s.Raw(name)
	if err != nil {
		return nil, err
	}
	return runbook.Parse(b)
}

// Resolve loads a runbook by name, or from a file path if ref looks like one.
func (s *Store) Resolve(ref string) (*runbook.Runbook, error) {
	if strings.ContainsAny(ref, `/\`) || strings.HasSuffix(ref, ".yaml") || strings.HasSuffix(ref, ".yml") {
		b, err := os.ReadFile(ref)
		if err != nil {
			return nil, err
		}
		return runbook.Parse(b)
	}
	return s.Get(ref)
}

// Save validates raw YAML and writes it under its declared name.
// If oldName differs from the declared name, the old file is removed.
func (s *Store) Save(raw []byte, oldName string) (*runbook.Runbook, error) {
	rb, err := runbook.Parse(raw)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(s.Path(rb.Name), raw, 0o644); err != nil {
		return nil, err
	}
	if oldName != "" && oldName != rb.Name {
		_ = s.Delete(oldName)
	}
	return rb, nil
}

func (s *Store) Delete(name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	err := os.Remove(s.Path(name))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("runbook %q: %w", name, ErrNotFound)
	}
	return err
}

func (s *Store) Exists(name string) bool {
	_, err := os.Stat(s.Path(name))
	return err == nil
}

func checkName(name string) error {
	if name == "" || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		return fmt.Errorf("invalid runbook name %q", name)
	}
	return nil
}

// SaveRun writes a run record as JSON.
func (s *Store) SaveRun(id string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.runDir(), id+".json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.runDir(), id+".json"))
}

func (s *Store) LoadRun(id string, v any) error {
	if err := checkName(id); err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(s.runDir(), id+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("run %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// RunIDs returns run IDs newest first (IDs are time-sortable).
func (s *Store) RunIDs() ([]string, error) {
	files, err := filepath.Glob(filepath.Join(s.runDir(), "*.json"))
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(files))
	for i, f := range files {
		ids[i] = strings.TrimSuffix(filepath.Base(f), ".json")
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	return ids, nil
}
