package project

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/placeholder"
	"github.com/egrif/runnr/internal/store"
	"github.com/egrif/runnr/internal/ver"
)

// StoreLookup finds files in a runnr store.
func StoreLookup(st *store.Store) Lookup {
	return func(kind, name string) (Stored, bool) {
		switch kind {
		case Runbook:
			if d, err := st.Load(name); err == nil {
				return Stored{Version: d.Version, UpdatedAt: d.UpdatedAt, Raw: d.Raw}, true
			}
		case Library:
			if d, err := st.LoadLibrary(name); err == nil {
				return Stored{Version: d.Version, UpdatedAt: d.UpdatedAt, Raw: d.Raw, Builtin: d.Builtin}, true
			}
		case Fakes:
			if d, err := st.LoadFakes(name); err == nil {
				return Stored{Version: d.Version, UpdatedAt: d.UpdatedAt, Raw: d.Raw}, true
			}
		case Suite:
			if d, err := st.LoadSuite(name); err == nil {
				return Stored{Version: d.Version, UpdatedAt: d.UpdatedAt, Raw: d.Raw}, true
			}
		case Run:
			if r, err := st.LoadRun(name); err == nil {
				return Stored{UpdatedAt: r.Started}, true
			}
		}
		return Stored{}, false
	}
}

// StoreSaver saves files to a runnr store.
func StoreSaver(st *store.Store) Saver {
	remotes := map[string]string{}
	return func(d Doc, overwrite bool) (ver.ID, error) {
		o := store.SaveOpts{Overwrite: overwrite, Origin: originOf(d, remotes)}
		kind, raw := d.Kind, d.Raw
		var v ver.ID
		var err error
		switch kind {
		case Runbook:
			_, v, err = st.Save(raw, o)
		case Library:
			_, v, err = st.SaveLibrary(raw, o)
		case Fakes:
			_, v, err = st.SaveFakes(raw, o)
		case Suite:
			_, v, err = st.SaveSuite(raw, o)
		case Run:
			var r engine.Run
			if err = json.Unmarshal(raw, &r); err == nil {
				err = st.SaveRun(&r)
			}
		default:
			err = fmt.Errorf("unknown kind %q", kind)
		}
		return v, err
	}
}

// originOf is where d came from (remotes caches each repository's).
func originOf(d Doc, remotes map[string]string) *store.Origin {
	o := &store.Origin{Values: d.Values, ImportedAt: time.Now()}
	if d.Path == "" || d.Kind == Run {
		return o
	}
	o.Path = d.Path
	if root := placeholder.FindRepoRoot(filepath.Dir(d.Path)); root != "" {
		o.RepoRoot = root
		if _, ok := remotes[root]; !ok {
			remotes[root] = placeholder.Remote(root)
		}
		o.Remote = remotes[root]
		o.RepoPath, _ = filepath.Rel(root, d.Path)
	}
	return o
}

// OriginFor is where a single file at path came from ("" for standard
// input) and its placeholders' values (set wins over those worked out from
// where it is), with those left without a value.
func OriginFor(path string, raw []byte, set map[string]string) (*store.Origin, []string, error) {
	vals, unset, err := placeholder.Resolve(path, set, placeholder.Tokens(raw))
	if err != nil {
		return nil, nil, err
	}
	d := Doc{Values: vals, Kind: Runbook}
	if path != "" {
		if d.Path, err = filepath.Abs(path); err != nil {
			return nil, nil, err
		}
	}
	if len(vals) == 0 {
		d.Values = nil
	}
	return originOf(d, map[string]string{}), unset, nil
}
