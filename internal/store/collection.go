package store

import (
	"time"

	"github.com/egrif/runnr/internal/runbook"
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
	Version    int                 `json:"version"`
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

func (s *Store) SaveCollection(raw []byte, o SaveOpts) (string, int, error) {
	return s.saveDoc(collectionKind, raw, o)
}

func (s *Store) DeleteCollection(name string) error { return s.deleteDoc(collectionKind, name) }
func (s *Store) CollectionVersions(name string) ([]Version, error) {
	return s.docVersions(collectionKind, name)
}
func (s *Store) RawCollectionVersion(name string, v int) ([]byte, error) {
	return s.rawDocVersion(collectionKind, name, v)
}
