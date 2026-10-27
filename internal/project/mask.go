package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/runbook"
	"github.com/egrif/runnr/internal/store"
	"github.com/egrif/runnr/internal/ver"
)

// Masked is runs as they leave runnr (an export, a raw log, a timeline):
// every text in them scanned for credentials again, with the built-in
// patterns and the runbook's own sensitive ones, so what was recorded
// before runnr knew to hide it (an older runnr, a pattern added since, an
// imported run) isn't passed on; and the folders this machine filled its
// placeholders in with written as the placeholders again (FoldersBack).
// The runs given are left as they are.
func Masked(st *store.Store, runs []*engine.Run) []*engine.Run {
	cache := map[string]*runbook.Masker{}
	out := make([]*engine.Run, 0, len(runs))
	for _, r := range runs {
		out = append(out, FoldersBack(MaskRun(r, maskerFor(st, r.Runbook, cache))))
	}
	return out
}

// FoldersBack is a copy of r with the folders it filled its placeholders
// in with (r.Folders) written as the placeholders again wherever they
// appear (a command, its output, an input), the longest first, so this
// machine's paths don't leave it. The folders themselves are left out
// (the collection stays).
func FoldersBack(r *engine.Run) *engine.Run {
	if len(r.Folders) == 0 {
		return r
	}
	type pair struct{ path, name string }
	var pairs []pair
	for n, p := range r.Folders {
		if p = strings.TrimRight(p, "/"); len(p) > 1 {
			pairs = append(pairs, pair{p, "@" + n + "@"})
		}
	}
	slices.SortFunc(pairs, func(a, b pair) int { return len(b.path) - len(a.path) })
	v, ok := asTree(r)
	if !ok {
		return r
	}
	v = mapStrings(v, func(s string) string {
		for _, p := range pairs {
			s = replacePath(s, p.path, p.name)
		}
		return s
	})
	b, err := json.Marshal(v)
	if err != nil {
		return r
	}
	var x engine.Run
	if json.Unmarshal(b, &x) != nil {
		return r
	}
	x.Folders, x.FolderSet = nil, nil
	return &x
}

// replacePath replaces path in s where it's the whole of a path or the
// start of a longer one (/src/repo in /src/repo/x, not in /src/repository).
func replacePath(s, path, with string) string {
	if !strings.Contains(s, path) {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, path)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		end := i + len(path)
		if end < len(s) && pathChar(s[end]) {
			b.WriteString(s[:end])
		} else {
			b.WriteString(s[:i])
			b.WriteString(with)
		}
		s = s[end:]
	}
}

func pathChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.'
}

// mapStrings is v (as JSON values) with fn applied to every string in it.
func mapStrings(v any, fn func(string) string) any {
	switch t := v.(type) {
	case string:
		return fn(t)
	case []any:
		for i := range t {
			t[i] = mapStrings(t[i], fn)
		}
	case map[string]any:
		for k := range t {
			t[k] = mapStrings(t[k], fn)
		}
	}
	return v
}

// MaskRun is a copy of r with RunMask applied to every string in it (and
// the values of inputs and variables named as credentials hidden whole).
func MaskRun(r *engine.Run, m *runbook.Masker) *engine.Run {
	v, ok := asTree(r)
	if !ok {
		return r
	}
	b, err := json.Marshal(maskAll(v, RunMask(r, m)))
	if err != nil {
		return r
	}
	var x engine.Run
	if json.Unmarshal(b, &x) != nil {
		return r
	}
	return &x
}

// RunMask masks text from run r: m's patterns, and the values r holds
// under credential names (db_password, not marked secret) wherever they
// appear.
func RunMask(r *engine.Run, m *runbook.Masker) func(string) string {
	var known []string
	if v, ok := asTree(r); ok {
		credentialValues(v, &known)
	}
	// Longest first, so a value inside another goes after it.
	slices.SortFunc(known, func(a, b string) int { return len(b) - len(a) })
	return func(s string) string {
		for _, k := range known {
			s = strings.ReplaceAll(s, k, "****")
		}
		return m.Mask(s)
	}
}

// asTree is r as JSON values (maps, lists, strings…).
func asTree(r *engine.Run) (any, bool) {
	b, err := json.Marshal(r)
	if err != nil {
		return nil, false
	}
	var v any
	return v, json.Unmarshal(b, &v) == nil
}

// credentialValues finds the values held under credential names (at least
// four characters: shorter ones would hide unrelated text).
func credentialValues(v any, out *[]string) {
	switch t := v.(type) {
	case []any:
		for _, x := range t {
			credentialValues(x, out)
		}
	case map[string]any:
		for k, x := range t {
			if str, ok := x.(string); ok && len(str) >= 4 && !strings.Contains(str, "****") && runbook.SensitiveName(k) && !slices.Contains(*out, str) {
				*out = append(*out, str)
			}
			credentialValues(x, out)
		}
	}
}

func maskAll(v any, mask func(string) string) any {
	switch t := v.(type) {
	case string:
		return mask(t)
	case []any:
		for i := range t {
			t[i] = maskAll(t[i], mask)
		}
	case map[string]any:
		for k := range t {
			if str, ok := t[k].(string); ok && str != "" && runbook.SensitiveName(k) {
				t[k] = "****" // however short
				continue
			}
			t[k] = maskAll(t[k], mask)
		}
	}
	return v
}

// maskerFor is the masker for a runbook's runs: the built-in patterns and
// the runbook's own (as it is now; built-in only if it's gone).
func maskerFor(st *store.Store, name string, cache map[string]*runbook.Masker) *runbook.Masker {
	if m, ok := cache[name]; ok {
		return m
	}
	var rb *runbook.Runbook
	if st != nil {
		rb, _, _ = st.Get(name)
	}
	cache[name] = runbook.NewMasker(rb)
	return cache[name]
}

// Scrubbed is what Scrub did to a run.
type Scrubbed struct {
	ID      string
	Runbook string
	store.ScrubResult
	Err error // ErrRunning: left alone
}

// Scrub masks credentials in stored runs (ids, or every run if none): the
// records, their notes, and the reasons of abort and suspend requests, as
// Masked would for an export. With dry it only says what would change.
// After a real scrub that changed something, the database is compacted so
// the old text isn't left in its free space.
func Scrub(st *store.Store, ids []string, dry bool) ([]Scrubbed, error) {
	if len(ids) == 0 {
		all, err := st.RunIDs()
		if err != nil {
			return nil, err
		}
		ids = all
	}
	cache := map[string]*runbook.Masker{}
	var out []Scrubbed
	changed := false
	for _, id := range ids {
		var x Scrubbed
		x.ID = id
		res, err := st.ScrubRun(id, func(r *engine.Run) (*engine.Run, func(string) string) {
			x.Runbook = r.Runbook
			m := maskerFor(st, r.Runbook, cache)
			return MaskRun(r, m), RunMask(r, m)
		}, dry)
		if err != nil && !errors.Is(err, store.ErrRunning) {
			return out, err
		}
		x.ScrubResult, x.Err = res, err
		changed = changed || res.Changed()
		out = append(out, x)
	}
	if changed && !dry {
		if err := st.Compact(); err != nil {
			return out, fmt.Errorf("scrubbed, but compacting the database: %w", err)
		}
	}
	return out, nil
}

// MaskedRun is Masked for one run.
func MaskedRun(st *store.Store, r *engine.Run) *engine.Run { return Masked(st, []*engine.Run{r})[0] }

// WithDefinitions are runs (copies) carrying the texts of the runbook and
// library versions they ran, for exporting.
func WithDefinitions(st *store.Store, runs []*engine.Run) []*engine.Run {
	out := make([]*engine.Run, len(runs))
	for i, r := range runs {
		c := *r
		c.Definitions = map[ver.ID]string{}
		if raw, _, err := st.RawVersion(r.Runbook, string(r.RunbookVersion)); err == nil && r.RunbookVersion != "" {
			c.Definitions[r.RunbookVersion] = string(raw)
		}
		for name, v := range r.Libraries {
			if raw, _, err := st.RawLibraryVersion(name, string(v)); err == nil && v != "" {
				c.Definitions[v] = string(raw)
			}
		}
		if len(c.Definitions) == 0 {
			c.Definitions = nil
		}
		out[i] = &c
	}
	return out
}
