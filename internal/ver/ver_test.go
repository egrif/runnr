package ver

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestShortAndMatch(t *testing.T) {
	id := Of([]byte("name: x\n"))
	if len(id) != 64 {
		t.Fatalf("len %d", len(id))
	}
	s := id.Short()
	if len([]rune(s)) != 9 || s[:4] != string(id[:4]) {
		t.Fatalf("short %q", s)
	}
	dots := string(id[:4]) + "...." + string(id[60:])
	for _, ref := range []string{string(id), string(id[:4]), s, dots, string(id[:4]) + "..." + string(id[60:])} {
		if !id.Match(ref) {
			t.Errorf("%q should match", ref)
		}
	}
	for _, ref := range []string{"", string(id[:3]), "zzzz", string(id[:4]) + "…0000", string(id[:3]) + "…" + string(id[60:])} {
		if id.Match(ref) {
			t.Errorf("%q shouldn't match", ref)
		}
	}
}

func TestFind(t *testing.T) {
	a, b := ID("abcd1111"+string(Of(nil)[8:])), ID("abcd2222"+string(Of(nil)[8:]))
	if got, err := Find([]ID{a, b}, "abcd1"); err != nil || got != a {
		t.Fatalf("got %v %v", got, err)
	}
	var amb *AmbiguousError
	if _, err := Find([]ID{a, b}, "abcd"); !errors.As(err, &amb) {
		t.Fatalf("want ambiguous, got %v", err)
	}
	var nf *NotFoundError
	if _, err := Find([]ID{a, b}, "ffff"); !errors.As(err, &nf) {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestUnmarshalLegacyNumber(t *testing.T) {
	var v struct {
		V ID `json:"v"`
	}
	if err := json.Unmarshal([]byte(`{"v":3}`), &v); err != nil || v.V != "" {
		t.Fatalf("got %q %v", v.V, err)
	}
	if err := json.Unmarshal([]byte(`{"v":"abc"}`), &v); err != nil || v.V != "abc" {
		t.Fatalf("got %q %v", v.V, err)
	}
}
