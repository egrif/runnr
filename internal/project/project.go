// Package project imports a folder of runnr files at once: runbooks,
// function libraries, fake libraries and test suites, the way a project
// keeps them on disk.
//
// Files can hold placeholders, @NAME@, filled in on the way in:
// @PROJECT_DIR@ is the folder imported, @REPO_ROOT@ its git root, and any
// others take the values given. Each file's references are checked (a
// runbook's libraries, a suite's runbook and fake libraries) against the
// files being imported, what's stored and runnr's built-in libraries; a
// missing library is looked for in the rest of the repository. Renaming a
// file on the way in changes the references to it in the other files.
package project

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/egrif/runnr/internal/runbook"
	"github.com/egrif/runnr/internal/runtest"
)

// Kinds of file, in the order they're saved (what's referred to first).
const (
	Library = "library"
	Fakes   = "fakes"
	Runbook = "runbook"
	Suite   = "suite"
)

var kindOrder = map[string]int{Library: 0, Fakes: 1, Runbook: 2, Suite: 3}

// Stored is what's stored under a name.
type Stored struct {
	Version   int
	UpdatedAt time.Time
	Raw       []byte
	// Builtin: one of runnr's own libraries (not stored).
	Builtin bool
}

// Lookup finds what's stored of a kind under a name.
type Lookup func(kind, name string) (Stored, bool)

// Options say what to scan and how.
type Options struct {
	// Dir is the folder to import (all its .yaml/.yml files, in
	// subfolders too); Paths, files to import instead (or as well).
	Dir   string
	Paths []string
	// Values fill in placeholders (they win over the defaults).
	Values map[string]string
	// Renames: "kind:name" → the name to import it as.
	Renames map[string]string
	// Include are more files to import (e.g. a library found elsewhere in
	// the repository); Skip are files not to.
	Include []string
	Skip    []string
	Lookup  Lookup
}

// Doc is one file, as it will be imported.
type Doc struct {
	Path string `json:"path"`
	// Rel is Path relative to the folder (or the repository, for a file
	// from outside it).
	Rel  string `json:"rel"`
	Kind string `json:"kind"`
	// Name is what it's imported as; Original, its name in the file.
	Name     string `json:"name"`
	Original string `json:"original"`
	Raw      []byte `json:"-"`
	Error    string `json:"error,omitempty"`
	// Placeholders it uses.
	Placeholders []string `json:"placeholders,omitempty"`
	Refs         []Ref    `json:"refs,omitempty"`
	// Included: found outside the folder (Include).
	Included bool `json:"included,omitempty"`
	Skipped  bool `json:"skipped,omitempty"`
	// What's stored by that name.
	Exists    bool      `json:"exists"`
	Version   int       `json:"version,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
	Same      bool      `json:"same,omitempty"`
	Builtin   bool      `json:"builtin,omitempty"`
}

// Ref is a reference from one file to another: Kind and Name, found in
// this import, the store, or built in ("" when it's missing).
type Ref struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Found string `json:"found"`
	// Candidates (when it's missing) are files elsewhere in the
	// repository that define it.
	Candidates []string `json:"candidates,omitempty"`
}

// Plan is what importing would do.
type Plan struct {
	Dir      string `json:"dir"`
	RepoRoot string `json:"repo_root,omitempty"`
	Docs     []Doc  `json:"docs"`
	// Values are the placeholder values used (defaults and given);
	// Unset, placeholders in the files that have none.
	Values map[string]string `json:"values"`
	Unset  []string          `json:"unset,omitempty"`
	// Ignored counts YAML files that aren't runnr's.
	Ignored []string `json:"ignored,omitempty"`
}

// OK reports whether everything can be imported as planned: no errors,
// placeholders unset or references missing (in files not skipped).
func (p *Plan) OK() error {
	var errs []error
	if len(p.Unset) > 0 {
		errs = append(errs, fmt.Errorf("placeholders without a value: %s", strings.Join(p.Unset, ", ")))
	}
	for _, d := range p.Docs {
		if d.Skipped {
			continue
		}
		if d.Error != "" {
			errs = append(errs, fmt.Errorf("%s: %s", d.Rel, d.Error))
		}
		for _, r := range d.Refs {
			if r.Found == "" {
				errs = append(errs, fmt.Errorf("%s: %s %s isn't in this import or stored", d.Rel, r.Kind, r.Name))
			}
		}
	}
	return errors.Join(errs...)
}

var placeholderRe = regexp.MustCompile(`@([A-Z][A-Z0-9_]*)@`)

// Scan reads the files and works out the plan; nothing is saved.
func Scan(o Options) (*Plan, error) {
	p := &Plan{Values: map[string]string{}}
	if o.Dir != "" {
		abs, err := filepath.Abs(o.Dir)
		if err != nil {
			return nil, err
		}
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			return nil, fmt.Errorf("%s isn't a folder", o.Dir)
		}
		p.Dir = abs
	} else {
		p.Dir, _ = os.Getwd()
	}
	p.RepoRoot = repoRoot(p.Dir)
	p.Values["PROJECT_DIR"] = p.Dir
	if p.RepoRoot != "" {
		p.Values["REPO_ROOT"] = p.RepoRoot
	}
	for k, v := range o.Values {
		p.Values[k] = v
	}

	var paths []string
	if o.Dir != "" {
		found, err := yamlFiles(p.Dir)
		if err != nil {
			return nil, err
		}
		paths = append(paths, found...)
	}
	given := map[string]string{} // files named: shown as given
	for _, f := range o.Paths {
		abs, _ := filepath.Abs(f)
		given[abs] = f
		paths = append(paths, abs)
	}
	included := map[string]bool{}
	for _, f := range o.Include {
		abs, _ := filepath.Abs(f)
		included[abs] = true
		paths = append(paths, abs)
	}
	skip := map[string]bool{}
	for _, f := range o.Skip {
		abs, _ := filepath.Abs(f)
		skip[abs] = true
	}
	seen := map[string]bool{}
	unset := map[string]bool{}
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		orig, err := os.ReadFile(path)
		d := Doc{Path: path, Rel: p.rel(path), Included: included[path], Skipped: skip[path]}
		if g, ok := given[path]; ok {
			d.Rel = g
		}
		if err != nil {
			d.Error = err.Error()
			p.Docs = append(p.Docs, d)
			continue
		}
		raw, used, missing := fill(orig, p.Values)
		d.Raw, d.Placeholders = raw, used
		if d.Kind = kindOf(raw); d.Kind == "" {
			if _, ok := given[path]; ok {
				d.Error = "this isn't a runbook (no steps:), a library (functions:), fakes (fakes:) or a test suite (scenarios:)"
				p.Docs = append(p.Docs, d)
			} else {
				p.Ignored = append(p.Ignored, d.Rel)
			}
			continue
		}
		if !d.Skipped {
			for _, m := range missing {
				unset[m] = true
			}
		}
		d.Original, d.Name, err = nameOf(d.Kind, raw)
		if err != nil {
			d.Error = err.Error()
		}
		p.Docs = append(p.Docs, d)
	}
	for k := range unset {
		p.Unset = append(p.Unset, k)
	}
	sort.Strings(p.Unset)

	p.rename(o.Renames)
	p.check(o.Lookup)
	sort.SliceStable(p.Docs, func(i, j int) bool {
		if a, b := kindOrder[p.Docs[i].Kind], kindOrder[p.Docs[j].Kind]; a != b {
			return a < b
		}
		return p.Docs[i].Rel < p.Docs[j].Rel
	})
	return p, nil
}

func (p *Plan) rel(path string) string {
	for _, base := range []string{p.Dir, p.RepoRoot} {
		if base == "" {
			continue
		}
		if r, err := filepath.Rel(base, path); err == nil && !strings.HasPrefix(r, "..") {
			return r
		}
	}
	return path
}

// rename imports files under new names, and changes the references to
// them in the other files.
func (p *Plan) rename(renames map[string]string) {
	if len(renames) == 0 {
		return
	}
	to := map[string]string{} // kind:old → new, for the files renamed
	for i := range p.Docs {
		d := &p.Docs[i]
		if d.Error != "" {
			continue
		}
		n, ok := renames[d.Kind+":"+d.Original]
		if !ok || n == "" || n == d.Original {
			continue
		}
		raw, err := setName(d.Raw, n)
		if err != nil {
			d.Error = err.Error()
			continue
		}
		d.Raw, d.Name = raw, n
		to[d.Kind+":"+d.Original] = n
	}
	for i := range p.Docs {
		d := &p.Docs[i]
		if d.Error != "" {
			continue
		}
		raw, err := rewriteRefs(d.Kind, d.Raw, to)
		if err != nil {
			d.Error = err.Error()
			continue
		}
		if !bytes.Equal(raw, d.Raw) {
			d.Raw = raw
			// A suite named after its runbook follows it.
			if d.Kind == Suite {
				if _, n, err := nameOf(Suite, raw); err == nil && d.Name == d.Original {
					d.Name = n
				}
			}
		}
	}
}

// check fills in what's stored by each name, and each file's references.
func (p *Plan) check(lookup Lookup) {
	inImport := map[string]bool{}
	for _, d := range p.Docs {
		if d.Error == "" && !d.Skipped {
			inImport[d.Kind+":"+d.Name] = true
		}
	}
	var repoLibs map[string][]string
	for i := range p.Docs {
		d := &p.Docs[i]
		if d.Error != "" {
			continue
		}
		if lookup != nil {
			if s, ok := lookup(d.Kind, d.Name); ok {
				if s.Builtin {
					d.Builtin = true
				} else {
					d.Exists, d.Version, d.UpdatedAt, d.Same = true, s.Version, s.UpdatedAt, bytes.Equal(s.Raw, d.Raw)
				}
			}
		}
		for _, r := range refsOf(d.Kind, d.Raw) {
			switch {
			case inImport[r.Kind+":"+r.Name]:
				r.Found = "import"
			case r.Kind == Library && runbook.BuiltinLibrary(r.Name) != nil:
				r.Found = "builtin"
			case lookup != nil:
				if s, ok := lookup(r.Kind, r.Name); ok {
					r.Found = map[bool]string{true: "builtin", false: "store"}[s.Builtin]
				}
			}
			if r.Found == "" && r.Kind == Library && p.RepoRoot != "" {
				if repoLibs == nil {
					repoLibs = p.libraryFiles()
				}
				r.Candidates = repoLibs[r.Name]
			}
			d.Refs = append(d.Refs, r)
		}
	}
}

// libraryFiles finds the function libraries in the repository, by name.
func (p *Plan) libraryFiles() map[string][]string {
	out := map[string][]string{}
	files, _ := yamlFiles(p.RepoRoot)
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil || kindOf(raw) != Library {
			continue
		}
		raw, _, _ = fill(raw, p.Values)
		if _, n, err := nameOf(Library, raw); err == nil {
			out[n] = append(out[n], f)
		}
	}
	return out
}

// yamlFiles lists the .yaml and .yml files under dir, leaving out hidden
// folders and dependencies.
func yamlFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if e.IsDir() {
			n := e.Name()
			if path != dir && (strings.HasPrefix(n, ".") || n == "node_modules" || n == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if ext := filepath.Ext(path); ext == ".yaml" || ext == ".yml" {
			out = append(out, path)
		}
		if len(out) > 5000 {
			return errors.New("more than 5000 YAML files: choose a smaller folder")
		}
		return nil
	})
	return out, err
}

func repoRoot(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}

// Fill replaces the placeholders (@NAME@) in raw that values has; it
// returns the ones used and the ones without a value.
func Fill(raw []byte, values map[string]string) ([]byte, []string, []string) {
	return fill(raw, values)
}

// fill replaces the placeholders that have values; it returns the ones
// used and the ones without a value.
func fill(raw []byte, values map[string]string) ([]byte, []string, []string) {
	used, missing := map[string]bool{}, map[string]bool{}
	out := placeholderRe.ReplaceAllFunc(raw, func(m []byte) []byte {
		k := string(m[1 : len(m)-1])
		used[k] = true
		if v, ok := values[k]; ok {
			return []byte(v)
		}
		missing[k] = true
		return m
	})
	return out, keys(used), keys(missing)
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// kindOf says what a file is from its top-level keys ("" if none of ours).
func kindOf(raw []byte) string {
	var top map[string]any
	if yaml.Unmarshal(raw, &top) != nil || top == nil {
		return ""
	}
	has := func(k string) bool { _, ok := top[k]; return ok }
	switch {
	case has("steps"):
		return Runbook
	case has("scenarios"):
		return Suite
	case has("functions"):
		return Library
	case has("fakes") && has("name"):
		return Fakes
	}
	return ""
}

// nameOf parses raw as kind, returning the name in the file and the one
// it's stored as (a suite without a name takes its runbook's).
func nameOf(kind string, raw []byte) (string, string, error) {
	switch kind {
	case Runbook:
		rb, err := runbook.Parse(raw)
		if err != nil {
			return "", "", err
		}
		return rb.Name, rb.Name, nil
	case Library:
		l, err := runbook.ParseLibrary(raw)
		if err != nil {
			return "", "", err
		}
		return l.Name, l.Name, nil
	case Fakes:
		l, err := runtest.ParseLibrary(raw)
		if err != nil {
			return "", "", err
		}
		return l.Name, l.Name, nil
	case Suite:
		s, err := runtest.ParseSuite(raw)
		if err != nil {
			return "", "", err
		}
		return s.Name, s.Name, nil
	}
	return "", "", fmt.Errorf("unknown kind %q", kind)
}

// refsOf lists what a file refers to.
func refsOf(kind string, raw []byte) []Ref {
	var out []Ref
	switch kind {
	case Runbook:
		if rb, err := runbook.Parse(raw); err == nil {
			for _, l := range rb.Libraries {
				out = append(out, Ref{Kind: Library, Name: l})
			}
		}
	case Suite:
		if s, err := runtest.ParseSuite(raw); err == nil {
			out = append(out, Ref{Kind: Runbook, Name: s.Runbook})
			for _, u := range s.Uses {
				out = append(out, Ref{Kind: Fakes, Name: u})
			}
		}
	}
	return out
}

// ---- editing YAML in place: only the values change, the rest stays as written ----

// setName renames a file: its top-level name (added if it has none).
func setName(raw []byte, name string) ([]byte, error) {
	root, err := topMapping(raw)
	if err != nil {
		return nil, err
	}
	if v := mapValue(root, "name"); v != nil {
		return replaceScalars(raw, []edit{{v, name}})
	}
	return append([]byte("name: "+name+"\n"), raw...), nil
}

// rewriteRefs changes references to renamed files ("kind:old" → new).
func rewriteRefs(kind string, raw []byte, to map[string]string) ([]byte, error) {
	if len(to) == 0 || (kind != Runbook && kind != Suite) {
		return raw, nil
	}
	root, err := topMapping(raw)
	if err != nil {
		return nil, err
	}
	var edits []edit
	list := func(key, refKind string) {
		if seq := mapValue(root, key); seq != nil && seq.Kind == yaml.SequenceNode {
			for _, n := range seq.Content {
				if n.Kind == yaml.ScalarNode {
					if v, ok := to[refKind+":"+n.Value]; ok {
						edits = append(edits, edit{n, v})
					}
				}
			}
		}
	}
	switch kind {
	case Runbook:
		list("libraries", Library)
	case Suite:
		if n := mapValue(root, "runbook"); n != nil && n.Kind == yaml.ScalarNode {
			if v, ok := to[Runbook+":"+n.Value]; ok {
				edits = append(edits, edit{n, v})
			}
		}
		list("uses", Fakes)
	}
	if len(edits) == 0 {
		return raw, nil
	}
	return replaceScalars(raw, edits)
}

type edit struct {
	node  *yaml.Node
	value string
}

func topMapping(raw []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("not a YAML mapping")
	}
	return doc.Content[0], nil
}

func mapValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// replaceScalars puts new values in place of scalars in raw, keeping
// their quoting.
func replaceScalars(raw []byte, edits []edit) ([]byte, error) {
	type span struct {
		at, end int
		text    string
	}
	var spans []span
	lines := bytes.SplitAfter(raw, []byte("\n"))
	for _, e := range edits {
		n := e.node
		if n.Line < 1 || n.Line > len(lines) {
			return nil, errors.New("can't find a value to change")
		}
		at := 0
		for _, l := range lines[:n.Line-1] {
			at += len(l)
		}
		line := lines[n.Line-1]
		col := 0
		for r := 1; r < n.Column && col < len(line); r++ {
			_, size := utf8.DecodeRune(line[col:])
			col += size
		}
		at += col
		var end int
		var text string
		switch n.Style {
		case yaml.DoubleQuotedStyle, yaml.SingleQuotedStyle:
			q := raw[at]
			end = bytes.IndexByte(raw[at+1:], q)
			if end < 0 {
				return nil, errors.New("unterminated quote")
			}
			end += at + 2
			text = string(q) + e.value + string(q)
		case 0:
			end = at + len(n.Value)
			if end > len(raw) || string(raw[at:end]) != n.Value {
				return nil, fmt.Errorf("can't change %q in place", n.Value)
			}
			text = e.value
		default:
			return nil, fmt.Errorf("can't change %q in place", n.Value)
		}
		spans = append(spans, span{at, end, text})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].at > spans[j].at })
	out := slices.Clone(raw)
	for _, s := range spans {
		out = append(out[:s.at], append([]byte(s.text), out[s.end:]...)...)
	}
	return out, nil
}
