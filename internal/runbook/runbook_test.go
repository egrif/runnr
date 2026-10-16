package runbook

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestScaffoldIsValid(t *testing.T) {
	if _, err := Parse([]byte(Scaffold("x"))); err != nil {
		t.Fatal(err)
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]string{
		"name":        "name: Bad Name\nsteps: [{name: a, run: x}]",
		"no steps":    "name: a",
		"both":        "name: a\nsteps: [{name: a, run: x, manual: y}]",
		"neither":     "name: a\nsteps: [{name: a}]",
		"timeout":     "name: a\nsteps: [{name: a, run: x, timeout: soon}]",
		"unattended":  "name: a\nsteps: [{name: a, manual: x, unattended: maybe}]",
		"choices":     "name: a\ninputs: [{name: e, default: z, choices: [x, y]}]\nsteps: [{name: a, run: x}]",
		"dup input":   "name: a\ninputs: [{name: e}, {name: e}]\nsteps: [{name: a, run: x}]",
		"template":    "name: a\nsteps: [{name: a, run: '{{.x'}]",
		"unknown key": "name: a\nsteps: [{name: a, run: x, bogus: 1}]",
		"manual opts": "name: a\nsteps: [{name: a, manual: x, retries: 2}]",
	}
	for name, y := range cases {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestRender(t *testing.T) {
	got, err := Render(`{{.a | upper}} {{quote .b}} {{default "d" .c}}`, map[string]string{"a": "x", "b": "it's", "c": ""})
	if err != nil || got != `X 'it'\''s' d` {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := Render("{{.missing}}", map[string]string{}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected missing key error, got %v", err)
	}
}

func TestFormatRoundTrip(t *testing.T) {
	for _, f := range []string{"../../examples/hello.yaml", "../../examples/deploy-service.yaml", os.Getenv("RUNNR_FORMAT_CHECK")} {
		if f == "" {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		a, err := Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		out, err := Format(a)
		if err != nil {
			t.Fatal(err)
		}
		b, err := Parse(out)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("%s: round trip changed the runbook:\n%s", f, out)
		}
	}
}

func TestSensitive(t *testing.T) {
	if _, err := Parse([]byte("name: a\nsensitive: {x: 'no placeholder'}\nsteps: [{name: a, run: x}]")); err == nil {
		t.Fatal("pattern without placeholder should be rejected")
	}
	if _, err := Parse([]byte("name: a\nsensitive: {x: '(unclosed {{SENSITIVE}}'}\nsteps: [{name: a, run: x}]")); err == nil {
		t.Fatal("bad regex should be rejected")
	}
	m := NewMasker(&Runbook{Sensitive: map[string]string{"api": "X-Api-Key: {{SENSITIVE}}", "pair": "user={{SENSITIVE}} pass={{SENSITIVE}};"}})
	cases := map[string]string{
		"postgres://app:pw-123@db.example.com:5432/app": "postgres://app:****@db.example.com:5432/app",
		"curl -H 'Authorization: Bearer abc.def' x":     "curl -H 'Authorization: Bearer ****' x",
		"aws_secret_access_key = wJalrXUtn":             "aws_secret_access_key = ****",
		"X-Api-Key: k123 rest":                          "X-Api-Key: **** rest",
		"user=bob pass=hunter2; ok":                     "user=**** pass=****; ok",
		"nothing to see https://example.com/path":       "nothing to see https://example.com/path",
	}
	for in, want := range cases {
		if got := m.Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
}
