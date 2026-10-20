package runtest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egrif/runnr/internal/runbook"
)

func TestStrictSuitePasses(t *testing.T) {
	if err := SandboxAvailable(); err != nil {
		t.Skip(err)
	}
	raw, _ := os.ReadFile("testdata/suite.yaml")
	s, err := ParseSuite(raw)
	if err != nil {
		t.Fatal(err)
	}
	s.Strict = true
	results, err := Run(context.Background(), s, sources(t), Options{Exe: exe(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if !r.Passed || !r.Strict {
			t.Errorf("%s: passed %v strict %v: %v", r.Scenario, r.Passed, r.Strict, r.Problems)
		}
	}
}

// What a strict suite's sandbox must stop, each from a step of its own.
func TestStrictSandboxHolds(t *testing.T) {
	if err := SandboxAvailable(); err != nil {
		t.Skip(err)
	}
	outside := filepath.Join(t.TempDir(), "leak")
	rb, err := runbook.Parse([]byte(`name: escape
steps:
  - {name: env, run: 'test -z "$AWS_PROFILE$AWS_ACCESS_KEY_ID" && case "$HOME" in */home) ;; *) exit 1;; esac'}
  - {name: real aws by path, run: /usr/bin/curl --version, optional: true}
  - {name: write outside, run: 'echo x > ` + outside + `', optional: true}
  - {name: unlisted tool, run: python3 -c 1, optional: true}
  - {name: listed tool, run: 'echo hi | tr a-z A-Z | grep -q HI'}
`))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_PROFILE", "should-not-leak")
	s := &Suite{Runbook: "escape", Isolate: []string{"curl"}, Strict: true,
		Operator:  Operator{Answers: Answers{Failure: Words{"skip"}}},
		Scenarios: []Scenario{{Name: "escape"}}}
	src := Sources{
		Runbook:   func(string) (*runbook.Runbook, error) { return rb, nil },
		Fakes:     func(string) (*Library, error) { return &Library{}, nil },
		Libraries: func([]string) ([]*runbook.Library, error) { return nil, nil },
	}
	rs, err := Run(context.Background(), s, src, Options{Exe: exe(t)})
	if err != nil {
		t.Fatal(err)
	}
	st := rs[0].Run.Steps
	want := []string{"succeeded", "skipped", "skipped", "skipped", "succeeded"}
	for i, w := range want {
		if string(st[i].Status) != w {
			t.Errorf("step %d (%s): %s, want %s", i+1, rb.Steps[i].Name, st[i].Status, w)
		}
	}
	if _, err := os.Stat(outside); err == nil {
		t.Error("a step wrote outside the test's directory")
	}
	if w := strings.Join(rs[0].Warnings, "\n"); !strings.Contains(w, "runs curl by path") {
		t.Errorf("warnings: %q", w)
	}
}
