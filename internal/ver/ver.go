// Package ver names versions of stored documents by their content: a
// version is the SHA-256 of its bytes, so the same text is the same version
// in any runnr.
package ver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// ID is a version: the hex SHA-256 of a document's bytes. Empty means not
// known (a run from a file, or a record from before versions were hashes).
type ID string

// Of is the version of raw.
func Of(raw []byte) ID {
	sum := sha256.Sum256(raw)
	return ID(hex.EncodeToString(sum[:]))
}

// Short is how a version is shown: its first and last four characters,
// 3f2a…9c1d ("unknown" when it isn't known).
func (id ID) Short() string {
	if id == "" {
		return "unknown"
	}
	if len(id) <= 9 {
		return string(id)
	}
	return string(id[:4]) + "…" + string(id[len(id)-4:])
}

// Match says whether ref names this version: the whole hash, a prefix of
// at least four characters, or the short form (3f2a…9c1d, or with dots,
// 3f2a....9c1d).
func (id ID) Match(ref string) bool {
	ref = strings.ToLower(strings.TrimSpace(ref))
	if id == "" || len(ref) < 4 {
		return false
	}
	if head, tail, ok := cutEllipsis(ref); ok {
		return len(head) >= 4 && len(tail) >= 4 && strings.HasPrefix(string(id), head) && strings.HasSuffix(string(id), tail)
	}
	return strings.HasPrefix(string(id), ref)
}

func cutEllipsis(ref string) (string, string, bool) {
	if h, t, ok := strings.Cut(ref, "…"); ok {
		return h, t, true
	}
	if i := strings.Index(ref, ".."); i >= 0 {
		return ref[:i], strings.TrimLeft(ref[i:], "."), true
	}
	return "", "", false
}

// UnmarshalJSON reads a version; a number (a record from before versions
// were hashes, whose number only meant something in the runnr it came from)
// reads as not known.
func (id *ID) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		var n json.Number
		if json.Unmarshal(b, &n) == nil {
			*id = ""
			return nil
		}
		return err
	}
	*id = ID(s)
	return nil
}

// Find picks the version ref names from ids: an error says none or several
// do.
func Find(ids []ID, ref string) (ID, error) {
	var found []ID
	for _, id := range ids {
		if id.Match(ref) && !contains(found, id) {
			found = append(found, id)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", &NotFoundError{Ref: ref}
	}
	return "", &AmbiguousError{Ref: ref, IDs: found}
}

func contains(ids []ID, id ID) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// NotFoundError: no version matches.
type NotFoundError struct{ Ref string }

func (e *NotFoundError) Error() string { return "no version " + e.Ref }

// AmbiguousError: several versions match.
type AmbiguousError struct {
	Ref string
	IDs []ID
}

func (e *AmbiguousError) Error() string {
	var s []string
	for _, id := range e.IDs {
		s = append(s, string(id[:12]))
	}
	return "version " + e.Ref + " could be " + strings.Join(s, ", ") + ": give more of it"
}
