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

// writeExe writes an executable script.
func writeExe(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// A version manager's shim (asdf's runs `asdf exec`) can't run in the
// sandbox: the allow-list uses a real copy further along PATH, or what the
// shim stands for, or says why it can't.
func TestLookToolSkipsShims(t *testing.T) {
	d := t.TempDir()
	shims, sys, mgrBin, target := filepath.Join(d, ".fakemgr", "shims"), filepath.Join(d, "sys"), filepath.Join(d, "mgrbin"), filepath.Join(d, "installs", "tool", "1.0", "bin")
	writeExe(t, filepath.Join(shims, "tool"), `exec fakemgr exec tool "$@"`)
	writeExe(t, filepath.Join(shims, "only"), `exec fakemgr exec only "$@"`)
	writeExe(t, filepath.Join(shims, "lost"), `exec fakemgr exec lost "$@"`)
	writeExe(t, filepath.Join(sys, "tool"), "echo sys")
	writeExe(t, filepath.Join(target, "only"), "echo only")
	writeExe(t, filepath.Join(mgrBin, "fakemgr"), `[ "$1 $2" = "which only" ] && echo `+filepath.Join(target, "only")+` && exit 0; echo "no $2" >&2; exit 1`)
	path := strings.Join([]string{shims, sys, mgrBin}, string(filepath.ListSeparator))
	t.Setenv("PATH", path) // for the manager

	if p, err := lookTool("tool", path); err != nil || p != filepath.Join(sys, "tool") {
		t.Errorf("tool: %q %v, want the copy in sys", p, err)
	}
	if p, err := lookTool("only", path); err != nil || p != filepath.Join(target, "only") {
		t.Errorf("only: %q %v, want what the shim stands for", p, err)
	}
	if _, err := lookTool("lost", path); err == nil || !strings.Contains(err.Error(), "shim") {
		t.Errorf("lost: %v, want an error about the shim", err)
	}
	if _, err := lookTool("nowhere", path); err == nil || !strings.Contains(err.Error(), "isn't on PATH") {
		t.Errorf("nowhere: %v", err)
	}
}

// In a strict suite, an allowed tool whose first copy on PATH is a shim
// runs (the real copy is allowed), and one that isn't there at all is an
// error, not a step failing with "operation not permitted".
func TestStrictAllowsToolBehindShim(t *testing.T) {
	if err := SandboxAvailable(); err != nil {
		t.Skip(err)
	}
	d := t.TempDir()
	shims, sys := filepath.Join(d, ".fakemgr", "shims"), filepath.Join(d, "sys")
	writeExe(t, filepath.Join(shims, "mytool"), `exec `+filepath.Join(sys, "mytool")+` "$@"`)
	writeExe(t, filepath.Join(sys, "mytool"), "echo real-tool")
	t.Setenv("PATH", shims+string(filepath.ListSeparator)+sys+string(filepath.ListSeparator)+os.Getenv("PATH"))
	rb, err := runbook.Parse([]byte("name: shimmed\nsteps:\n  - {name: use it, run: 'mytool | grep -q real-tool'}\n"))
	if err != nil {
		t.Fatal(err)
	}
	src := Sources{
		Runbook:   func(string) (*runbook.Runbook, error) { return rb, nil },
		Fakes:     func(string) (*Library, error) { return &Library{}, nil },
		Libraries: func([]string) ([]*runbook.Library, error) { return nil, nil },
	}
	s := &Suite{Runbook: "shimmed", Strict: true, Allow: []string{"mytool"}, Scenarios: []Scenario{{Name: "shimmed"}}}
	rs, err := Run(context.Background(), s, src, Options{Exe: exe(t)})
	if err != nil {
		t.Fatal(err)
	}
	if st := rs[0].Run.Steps[0]; st.Status != "succeeded" {
		t.Errorf("step: %s %s %q", st.Status, st.Error, st.Output)
	}

	s.Allow = []string{"no-such-tool-anywhere"}
	if _, err := Run(context.Background(), s, src, Options{Exe: exe(t)}); err == nil || !strings.Contains(err.Error(), "no-such-tool-anywhere isn't on PATH") {
		t.Errorf("missing tool: %v", err)
	}
}
