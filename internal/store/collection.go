package store

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egrif/runnr/internal/runbook"
	"github.com/egrif/runnr/internal/ver"
)

var collectionKind = docKind{noun: "collection", table: "collections", versions: "collection_versions",
	parse: func(raw []byte) (string, error) {
		c, err := runbook.ParseCollection(raw)
		if err != nil {
			return "", err
		}
		return c.Name, nil
	}}

// CollectionDoc is a stored collection; Collection is nil if Raw doesn't parse.
type CollectionDoc struct {
	Name       string              `json:"name"`
	Raw        string              `json:"raw"`
	Version    ver.ID              `json:"version"`
	UpdatedAt  time.Time           `json:"updated_at"`
	Collection *runbook.Collection `json:"collection,omitempty"`
	Err        string              `json:"error,omitempty"`
}

func toCollectionDoc(r *docRow) CollectionDoc {
	d := CollectionDoc{Name: r.Name, Raw: string(r.Raw), Version: r.Version, UpdatedAt: r.UpdatedAt}
	c, err := runbook.ParseCollection(r.Raw)
	if err != nil {
		d.Err = err.Error()
	} else {
		d.Collection = c
	}
	return d
}

func (s *Store) ListCollections() ([]CollectionDoc, error) {
	rows, err := s.listDocs(collectionKind)
	if err != nil {
		return nil, err
	}
	out := []CollectionDoc{}
	for i := range rows {
		out = append(out, toCollectionDoc(&rows[i]))
	}
	return out, nil
}

func (s *Store) LoadCollection(name string) (*CollectionDoc, error) {
	r, err := s.loadDoc(collectionKind, name)
	if err != nil {
		return nil, err
	}
	d := toCollectionDoc(r)
	return &d, nil
}

func (s *Store) SaveCollection(raw []byte, o SaveOpts) (string, ver.ID, error) {
	name, v, err := s.saveDoc(collectionKind, raw, o)
	if err == nil && o.OldName != "" && o.OldName != name {
		// Its folders on this machine go with it.
		err = s.renamePlaceholderScope(o.OldName, name)
	}
	return name, v, err
}

func (s *Store) DeleteCollection(name string) error { return s.deleteDoc(collectionKind, name) }
func (s *Store) CollectionVersions(name string) ([]Version, error) {
	return s.docVersions(collectionKind, name)
}

// CollectionsOf lists the collections that include a runbook.
func (s *Store) CollectionsOf(runbookName string) ([]string, error) {
	cs, err := s.ListCollections()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range cs {
		if c.Collection != nil && slices.Contains(c.Collection.Runbooks, runbookName) {
			out = append(out, c.Name)
		}
	}
	return out, nil
}

// EditCollectionRunbooks changes a collection's runbook list with fn,
// keeping the rest of its YAML. With create, a missing collection is made
// first. It returns the version saved (the current one if nothing changed)
// and whether the collection was created.
func (s *Store) EditCollectionRunbooks(name string, create bool, fn func([]string) ([]string, error)) (version ver.ID, created bool, err error) {
	d, err := s.LoadCollection(name)
	var raw []byte
	var base ver.ID
	var list []string
	switch {
	case errors.Is(err, ErrNotFound) && create:
		created = true
		raw = []byte("name: " + name + "\nrunbooks: []\n")
	case err != nil:
		return "", false, err
	case d.Collection == nil:
		return "", false, fmt.Errorf("collection %q doesn't parse: %s", name, d.Err)
	default:
		raw, base, list = []byte(d.Raw), d.Version, d.Collection.Runbooks
	}
	next, err := fn(slices.Clone(list))
	if err != nil {
		return "", false, err
	}
	if !created && slices.Equal(next, list) {
		return base, false, nil
	}
	if raw, err = runbook.SetCollectionRunbooks(raw, next); err != nil {
		return "", false, err
	}
	o := SaveOpts{}
	if !created {
		o = SaveOpts{OldName: name, Base: base}
	}
	_, version, err = s.SaveCollection(raw, o)
	return version, created, err
}

// AddToCollection appends runbooks (which must exist) to a collection,
// creating it if create is set. Runbooks already in it stay where they are.
func (s *Store) AddToCollection(name string, create bool, runbooks ...string) (version ver.ID, created bool, err error) {
	for _, r := range runbooks {
		if !s.Exists(r) {
			return "", false, fmt.Errorf("runbook %q: %w", r, ErrNotFound)
		}
	}
	return s.EditCollectionRunbooks(name, create, func(list []string) ([]string, error) {
		for _, r := range runbooks {
			if !slices.Contains(list, r) {
				list = append(list, r)
			}
		}
		return list, nil
	})
}

// RemoveFromCollection takes runbooks out of a collection (they stay in runnr).
func (s *Store) RemoveFromCollection(name string, runbooks ...string) (ver.ID, error) {
	v, _, err := s.EditCollectionRunbooks(name, false, func(list []string) ([]string, error) {
		for _, r := range runbooks {
			if !slices.Contains(list, r) {
				return nil, fmt.Errorf("runbook %q isn't in collection %q", r, name)
			}
		}
		return slices.DeleteFunc(list, func(r string) bool { return slices.Contains(runbooks, r) }), nil
	})
	return v, err
}

// MoveRunbook moves a runbook into collection to, out of from. With from
// empty, it leaves the one collection it's in (if any); in several, from
// must say which.
func (s *Store) MoveRunbook(runbookName, from, to string, create bool) (moved string, err error) {
	if from == "" {
		in, err := s.CollectionsOf(runbookName)
		if err != nil {
			return "", err
		}
		in = slices.DeleteFunc(in, func(c string) bool { return c == to })
		switch len(in) {
		case 0:
		case 1:
			from = in[0]
		default:
			return "", fmt.Errorf("runbook %q is in several collections (%s); say which to move it from", runbookName, strings.Join(in, ", "))
		}
	}
	if from == to {
		return "", fmt.Errorf("runbook %q is already in %q", runbookName, to)
	}
	if _, _, err := s.AddToCollection(to, create, runbookName); err != nil {
		return "", err
	}
	if from != "" {
		if _, err := s.RemoveFromCollection(from, runbookName); err != nil {
			return "", fmt.Errorf("added to %q, but couldn't take it out of %q: %w", to, from, err)
		}
	}
	return from, nil
}

// SetCollectionRunbooks replaces a collection's runbook list (any order);
// runbooks new to it must exist.
func (s *Store) SetCollectionRunbooks(name string, runbooks []string) (ver.ID, error) {
	v, _, err := s.EditCollectionRunbooks(name, false, func(list []string) ([]string, error) {
		seen := map[string]bool{}
		for _, r := range runbooks {
			if seen[r] {
				return nil, fmt.Errorf("runbook %q is listed twice", r)
			}
			seen[r] = true
			if !slices.Contains(list, r) && !s.Exists(r) {
				return nil, fmt.Errorf("runbook %q: %w", r, ErrNotFound)
			}
		}
		return runbooks, nil
	})
	return v, err
}

// UpdateCollection changes a collection's name, description or expect
// (nil leaves one as it is), keeping the rest of its YAML.
func (s *Store) UpdateCollection(name string, newName, description, expect *string) (string, ver.ID, error) {
	d, err := s.LoadCollection(name)
	if err != nil {
		return "", "", err
	}
	raw := []byte(d.Raw)
	for key, v := range map[string]*string{"name": newName, "description": description, "expect": expect} {
		if v == nil {
			continue
		}
		if key == "name" && strings.TrimSpace(*v) == "" {
			return "", "", errors.New("a collection needs a name")
		}
		if raw, err = runbook.SetCollectionField(raw, key, strings.TrimSpace(*v)); err != nil {
			return "", "", err
		}
	}
	return s.SaveCollection(raw, SaveOpts{OldName: name, Base: d.Version})
}
