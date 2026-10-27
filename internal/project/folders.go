package project

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/egrif/runnr/internal/placeholder"
	"github.com/egrif/runnr/internal/runbook"
	"github.com/egrif/runnr/internal/store"
)

// A run's folders: the values of the placeholders (@PROJECT_DIR@ …) it
// names, on this machine. They come from, in order: those given for the
// run, the collection it takes them from (one it's given, or the
// runbook's own if it's in one; in several, they must agree), and those
// set for everything (@REPOS_ROOT@). A value can name others
// (@REPOS_ROOT@/ops-runbooks).

// FolderOpts are a run's choices: the collection to take values from (""
// for the runbook's own), and values for this run only.
type FolderOpts struct {
	Collection string
	Set        map[string]string
}

// Folders are where a run's placeholders come from.
type Folders struct {
	// Used are the placeholders it names.
	Used []string `json:"used"`
	// Collection is the collection the values come from ("" if none);
	// Collections are those the runbook is in.
	Collection  string   `json:"collection,omitempty"`
	Collections []string `json:"collections,omitempty"`
	// Values are each used one's folder (filled in), and From where it
	// came from: "run", "collection" or "global".
	Values map[string]string `json:"values"`
	From   map[string]string `json:"from"`
	// Problems are why the run can't start as it is.
	Problems []placeholder.Problem `json:"problems,omitempty"`
}

// Err is the run's problems as an error (nil if it can start).
func (f *Folders) Err() error {
	if len(f.Problems) == 0 {
		return nil
	}
	return foldersErr(f)
}

func foldersErr(f *Folders) error {
	var lines []string
	for _, p := range f.Problems {
		lines = append(lines, p.String())
	}
	where := "runnr folders set NAME=FOLDER"
	if f.Collection != "" {
		where = fmt.Sprintf("runnr folders set -c %s NAME=FOLDER", f.Collection)
	}
	return fmt.Errorf("can't start: %s. Give each a folder on this machine (%s, or Collections in the GUI), or for this run only (--set NAME=FOLDER)", strings.Join(lines, "; "), where)
}

// ResolveFolders works out the folders for a run of runbookName that
// names the placeholders used.
func ResolveFolders(st *store.Store, runbookName string, used []string, o FolderOpts) (*Folders, error) {
	f := &Folders{Used: used, Values: map[string]string{}, From: map[string]string{}}
	cols, err := st.CollectionsOf(runbookName)
	if err != nil {
		return nil, err
	}
	f.Collections = cols
	global, err := st.Placeholders("")
	if err != nil {
		return nil, err
	}
	all := maps.Clone(global)
	from := map[string]string{}
	for k := range global {
		from[k] = "global"
	}
	layer := func(vals map[string]string, src string) {
		for k, v := range vals {
			all[k], from[k] = v, src
		}
	}

	switch {
	case o.Collection != "":
		if _, err := st.LoadCollection(o.Collection); err != nil {
			return nil, fmt.Errorf("collection %q: %w", o.Collection, err)
		}
		f.Collection = o.Collection
	case len(cols) == 1:
		f.Collection = cols[0]
	}
	if f.Collection != "" {
		vals, err := st.Placeholders(f.Collection)
		if err != nil {
			return nil, err
		}
		layer(vals, "collection")
	} else if len(cols) > 1 {
		// In several: what they agree on (and a problem where they don't,
		// unless this run gives its own).
		agreed := map[string]string{}
		differ := map[string][]string{}
		for _, c := range cols {
			vals, err := st.Placeholders(c)
			if err != nil {
				return nil, err
			}
			for k, v := range vals {
				if prev, ok := agreed[k]; ok && prev != v {
					differ[k] = append(differ[k], c)
					continue
				}
				agreed[k] = v
			}
		}
		for k := range differ {
			delete(agreed, k)
		}
		layer(agreed, "collection")
		for _, k := range slices.Sorted(maps.Keys(differ)) {
			if _, given := o.Set[k]; !given && slices.Contains(used, k) {
				f.Problems = append(f.Problems, placeholder.Problem{Name: k, Why: fmt.Sprintf("differs between collections %s: choose one for the run", strings.Join(cols, ", "))})
			}
		}
	}
	layer(o.Set, "run")

	full, err := placeholder.Expand(all)
	if err != nil {
		return nil, err
	}
	for _, k := range used {
		if v, ok := full[k]; ok {
			f.Values[k], f.From[k] = v, from[k]
		}
	}
	for _, p := range placeholder.Check(used, full) {
		if !slices.ContainsFunc(f.Problems, func(q placeholder.Problem) bool { return q.Name == p.Name }) {
			f.Problems = append(f.Problems, p)
		}
	}
	return f, nil
}

// RunFolders works out the folders for running rb (with its libraries and
// the inputs given): an error if it can't start.
func RunFolders(st *store.Store, rb *runbook.Runbook, libs []*runbook.Library, vars map[string]string, o FolderOpts) (*Folders, error) {
	f, err := ResolveFolders(st, rb.Name, placeholder.Used(rb, libs, vars), o)
	if err != nil {
		return nil, err
	}
	return f, f.Err()
}

// SuiteFolders gives a test run its folders as a run of the runbook would
// have them (runtest.Options.Folders).
func SuiteFolders(st *store.Store, o FolderOpts) func(rb *runbook.Runbook, used []string) (map[string]string, error) {
	return func(rb *runbook.Runbook, used []string) (map[string]string, error) {
		f, err := ResolveFolders(st, rb.Name, used, o)
		if err != nil {
			return nil, err
		}
		return f.Values, f.Err()
	}
}
