package project

import (
	"fmt"

	"github.com/egrif/runnr/internal/store"
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
		}
		return Stored{}, false
	}
}

// StoreSaver saves files to a runnr store.
func StoreSaver(st *store.Store) Saver {
	return func(kind string, raw []byte, overwrite bool) (int, error) {
		o := store.SaveOpts{Overwrite: overwrite}
		var v int
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
		default:
			err = fmt.Errorf("unknown kind %q", kind)
		}
		return v, err
	}
}
