package project

import (
	"fmt"
	"slices"
	"strings"

	"github.com/egrif/runnr/internal/ver"
)

// Saver stores a file (overwrite: as the current version of the one by its
// name), with where it came from, returning the version it's at.
type Saver func(d Doc, overwrite bool) (ver.ID, error)

// Result is what importing a file did.
type Result struct {
	Rel  string `json:"rel"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Status: imported, replaced, unchanged, skipped, exists (its name is
	// taken and replacing wasn't chosen) or failed.
	Status   string `json:"status"`
	Version  ver.ID `json:"version,omitempty"`
	Replaced ver.ID `json:"replaced,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (r Result) String() string {
	switch r.Status {
	case "imported":
		if r.Kind == Run {
			return fmt.Sprintf("imported %s → run %s", r.Rel, r.Name)
		}
		return fmt.Sprintf("imported %s → %s %s (version %s)", r.Rel, r.Kind, r.Name, r.Version.Short())
	case "replaced":
		return fmt.Sprintf("replaced %s %s with %s (version %s → %s)", r.Kind, r.Name, r.Rel, r.Replaced.Short(), r.Version.Short())
	case "unchanged":
		if r.Kind == Run {
			return fmt.Sprintf("already there %s → run %s", r.Rel, r.Name)
		}
		return fmt.Sprintf("unchanged %s → %s %s (version %s)", r.Rel, r.Kind, r.Name, r.Version.Short())
	case "skipped":
		return fmt.Sprintf("skipped %s", r.Rel)
	case "exists":
		return fmt.Sprintf("not imported %s: %s %s already exists (version %s); replace it (--force) or rename it (--rename %s=NEW)", r.Rel, r.Kind, r.Name, r.Version.Short(), r.Name)
	}
	return fmt.Sprintf("failed %s: %s", r.Rel, r.Error)
}

// Apply saves the plan's files, what they refer to first. A file whose
// name is taken is saved as its current version only if replace says so. A
// file with an error, a placeholder without a value or a reference that's
// missing isn't saved.
func Apply(p *Plan, replace func(Doc) bool, save Saver) []Result {
	var out []Result
	unset := map[string]bool{}
	for _, u := range p.Unset {
		unset[u] = true
	}
	// What's in the store once each file is done (for the files after it).
	there := map[string]bool{}
	for _, d := range p.Docs {
		r := Result{Rel: d.Rel, Kind: d.Kind, Name: d.Name}
		var why []string
		if d.Error != "" {
			why = append(why, d.Error)
		}
		for _, ph := range d.Placeholders {
			if unset[ph] {
				why = append(why, "@"+ph+"@ has no value")
			}
		}
		for _, ref := range d.Refs {
			switch {
			case ref.Found == "":
				why = append(why, fmt.Sprintf("needs %s %s", ref.Kind, ref.Name))
			case ref.Found == "import" && !there[ref.Kind+":"+ref.Name]:
				why = append(why, fmt.Sprintf("needs %s %s, which wasn't imported", ref.Kind, ref.Name))
			}
		}
		switch {
		case d.Skipped:
			r.Status = "skipped"
		case len(why) > 0:
			r.Status, r.Error = "failed", strings.Join(slices.Compact(why), "; ")
		case d.Same && d.Kind == Run:
			r.Status, r.Version = "unchanged", d.Version
		case d.Same:
			// The same text: no new version, but where it came from (and so
			// its placeholders' values) may be new.
			if _, err := save(d, true); err != nil {
				r.Status, r.Error = "failed", err.Error()
			} else {
				r.Status, r.Version = "unchanged", d.Version
			}
		case d.Exists && !replace(d):
			r.Status, r.Version = "exists", d.Version
		default:
			v, err := save(d, d.Exists)
			switch {
			case err != nil:
				r.Status, r.Error = "failed", err.Error()
			case d.Exists:
				r.Status, r.Version, r.Replaced = "replaced", v, d.Version
			default:
				r.Status, r.Version = "imported", v
			}
		}
		switch r.Status {
		case "imported", "replaced", "unchanged", "exists":
			there[d.Kind+":"+d.Name] = true
		}
		out = append(out, r)
	}
	return out
}
