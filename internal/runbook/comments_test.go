package runbook

import (
	"strings"
	"testing"
)

func TestCommentsRoundTrip(t *testing.T) {
	raw := `# Top of the file
name: demo
# what it's for
description: Demo
steps:
  # the first step
  - name: one
    run: echo one  # prints one
  - name: two
    # careful: this
    # deletes things
    run: rm -rf /tmp/x
    ways: []
`
	raw = strings.Replace(raw, "    ways: []\n", "", 1)
	c, err := ExtractComments([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"": "Top of the file", "description": "what it's for", "steps.0": "the first step", "steps.0.run": "prints one", "steps.1.run": "careful: this\ndeletes things"}
	for k, v := range want {
		if c[k] != v {
			t.Errorf("comment %q = %q, want %q (all: %q)", k, c[k], v, c)
		}
	}
	rb, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	out, err := FormatWithComments(rb, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(out); err != nil {
		t.Fatalf("doesn't parse back: %v\n%s", err, out)
	}
	c2, _ := ExtractComments(out)
	for k, v := range want {
		if c2[k] != v {
			t.Errorf("after formatting, %q = %q, want %q\n%s", k, c2[k], v, out)
		}
	}
	// A comment for a field that isn't written goes on the item instead.
	out, _ = FormatWithComments(rb, Comments{"steps.1.section": "gone field"})
	if !strings.Contains(string(out), "# gone field") {
		t.Fatalf("lost comment:\n%s", out)
	}
}
