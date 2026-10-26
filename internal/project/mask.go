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
)

// Masked is runs as they leave runnr (an export, a raw log, a timeline):
// every text in them scanned for credentials again, with the built-in
// patterns and the runbook's own sensitive ones, so what was recorded
// before runnr knew to hide it (an older runnr, a pattern added since, an
// imported run) isn't passed on. The runs given are left as they are.
func Masked(st *store.Store, runs []*engine.Run) []*engine.Run {
	cache := map[string]*runbook.Masker{}
	out := make([]*engine.Run, 0, len(runs))
	for _, r := range runs {
		out = append(out, MaskRun(r, maskerFor(st, r.Runbook, cache)))
	}
	return out
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
