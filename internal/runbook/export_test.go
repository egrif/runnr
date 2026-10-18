package runbook

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

const exportRB = `name: exp
description: |
  Try the export.
  Second line.
expect: Everything printed.
shell: zsh
inputs:
  - {name: env, default: staging, choices: [staging, prod]}
  - {name: who, required: true, description: who runs it}
env: {GREETING: "hi {{.who}}"}
functions:
  shout: {body: 'echo "$1!"'}
maintenance_window: {target: 30m}
sections:
  - {name: Prep, description: Getting ready}
steps:
  - name: Derive
    section: Prep
    description: Work out names.
    always: true
    run: |
      set -e
      region={{if eq .env "prod"}}use1{{else}}usw2{{end}}
      [ -n "$region" ] || exit 1
      name="{{.who}}-$region"
    outputs: [region, name]
  - name: In a dir
    dir: "{{.tmp}}"
    run: pwd > where; echo "dir done"
  - name: Captured
    run: echo "cap-$region"
    capture: cap
  - name: Publish by file
    run: echo "filed=yes-$name" >> "$RUNNR_OUTPUT"
  - name: Down
    run: echo down
    window: open
  - name: Gate
    description: Wait for it.
    checks:
      - {name: counter, run: 'echo x >> "{{.tmp}}/n"; [ $(wc -l < "{{.tmp}}/n") -ge 2 ]'}
    repeat: {every: 1s, timeout: 5s}
  - name: Look
    manual: Look at {{.who}}'s dashboard.
  - name: Swap
    ways:
      - {name: Scripted, run: 'shout "$GREETING $name $cap $filed"', preferred: true}
      - {name: Manual, manual: Do it by hand.}
  - name: Up
    run: echo up; exit 0
    window: close
  - name: Reads input
    run: |
      read -r answer
      echo "answer=$answer"
  - name: After
    run: echo after-read
`

func TestExportMarkdownIsPasteable(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("no zsh")
	}
	tmp := t.TempDir()
	rb, err := Parse([]byte(strings.Replace(exportRB, "inputs:\n", "inputs:\n  - {name: tmp}\n", 1)))
	if err != nil {
		t.Fatal(err)
	}
	md, err := ExportMarkdown(rb, ExportOptions{Vars: map[string]string{"who": "eric", "env": "prod", "tmp": tmp}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<details>\n<summary><b>Overview</b></summary>", "## Primary states", "### 1. Prep", "**APP IS DOWN**", "**APP IS BACK**",
		"- Work out names.", "> Look at ${who}'s dashboard.", "<summary>Or: Manual</summary>", "<summary>Helper functions (shout)</summary>"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in:\n%s", want, md)
		}
	}
	// Paste every block (not the "Or:" alternatives), in order, into one
	// interactive zsh, then check it's still alive and has the values.
	var script strings.Builder
	alt := false
	for _, part := range regexp.MustCompile("(?s)<summary>Or:.*?</details>|```zsh\n.*?\n```").FindAllString(md, -1) {
		if strings.HasPrefix(part, "<summary>Or:") {
			alt = true
			continue
		}
		_ = alt
		script.WriteString(strings.TrimSuffix(strings.TrimPrefix(part, "```zsh\n"), "\n```") + "\n")
	}
	// A person types the answer for the step that reads input.
	sc := strings.Replace(script.String(), "echo \"answer=$answer\"\n}\n", "echo \"answer=$answer\"\n}\nyes-typed\n", 1)
	script.Reset()
	script.WriteString(sc)
	script.WriteString(`echo "alive region=$region name=$name cap=$cap filed=$filed where=$(cat ` + tmp + `/where)"` + "\n")
	cmd := exec.Command(zsh, "-i", "-s")
	cmd.Env = append(os.Environ(), "ZDOTDIR="+t.TempDir())
	cmd.Dir = t.TempDir()
	cmd.Stdin = strings.NewReader(script.String())
	out, _ := cmd.CombinedOutput()
	got := string(out)
	for _, want := range []string{"dir done", "✅ counter", "eric-use1 cap-use1 yes-eric-use1!", "alive region=use1 name=eric-use1 cap=cap-use1 filed=yes-eric-use1 where=" + tmp, "hi eric", "answer=yes-typed", "after-read"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in output:\n%s\n--- script ---\n%s", want, got, script.String())
		}
	}
	if strings.Contains(got, "❌") && !strings.Contains(got, "trying again") {
		t.Errorf("checks failed:\n%s", got)
	}
}
