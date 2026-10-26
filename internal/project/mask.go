package project

import (
	"encoding/json"
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
	maskers := map[string]*runbook.Masker{}
	out := make([]*engine.Run, 0, len(runs))
	for _, r := range runs {
		m, ok := maskers[r.Runbook]
		if !ok {
			var rb *runbook.Runbook
			if st != nil {
				rb, _, _ = st.Get(r.Runbook)
			}
			m = runbook.NewMasker(rb)
			maskers[r.Runbook] = m
		}
		out = append(out, MaskRun(r, m))
	}
	return out
}

// MaskRun is a copy of r with m applied to every string in it. The values
// of inputs and variables named as credentials (db_password, not marked
// secret) are hidden whole, wherever they appear.
func MaskRun(r *engine.Run, m *runbook.Masker) *engine.Run {
	b, err := json.Marshal(r)
	if err != nil {
		return r
	}
	var v any
	if json.Unmarshal(b, &v) != nil {
		return r
	}
	var known []string
	credentialValues(v, &known)
	// Longest first, so a value inside another goes after it.
	slices.SortFunc(known, func(a, b string) int { return len(b) - len(a) })
	mask := func(s string) string {
		for _, k := range known {
			s = strings.ReplaceAll(s, k, "****")
		}
		return m.Mask(s)
	}
	b, err = json.Marshal(maskAll(v, mask))
	if err != nil {
		return r
	}
	var x engine.Run
	if json.Unmarshal(b, &x) != nil {
		return r
	}
	return &x
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

// MaskedRun is Masked for one run.
func MaskedRun(st *store.Store, r *engine.Run) *engine.Run { return Masked(st, []*engine.Run{r})[0] }
