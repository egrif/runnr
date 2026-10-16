// Package runbook defines the runbook file format and its validation.
package runbook

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"text/template"
	"time"

	"gopkg.in/yaml.v3"
)

// Unattended policies for manual steps when no human is present.
const (
	UnattendedFail = "fail"
	UnattendedSkip = "skip"
	UnattendedPass = "pass"
)

type Runbook struct {
	Name        string            `yaml:"name" json:"name"`
	Description string            `yaml:"description,omitempty" json:"description,omitempty"`
	Tags        []string          `yaml:"tags,omitempty" json:"tags,omitempty"`
	Inputs      []Input           `yaml:"inputs,omitempty" json:"inputs,omitempty"`
	Env         map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	Shell       string            `yaml:"shell,omitempty" json:"shell,omitempty"`
	// Profile lists files sourced before every step (e.g. ~/.zshrc), so
	// aliases, functions and settings from your shell config work in steps.
	Profile []string `yaml:"profile,omitempty" json:"profile,omitempty"`
	Steps   []Step   `yaml:"steps" json:"steps"`
}

type Input struct {
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Default     string   `yaml:"default,omitempty" json:"default,omitempty"`
	Required    bool     `yaml:"required,omitempty" json:"required,omitempty"`
	Choices     []string `yaml:"choices,omitempty" json:"choices,omitempty"`
	Secret      bool     `yaml:"secret,omitempty" json:"secret,omitempty"`
}

// Step is either an automated command (Run) or a manual instruction (Manual).
type Step struct {
	ID              string            `yaml:"id,omitempty" json:"id,omitempty"`
	Name            string            `yaml:"name" json:"name"`
	Section         string            `yaml:"section,omitempty" json:"section,omitempty"`
	Owner           string            `yaml:"owner,omitempty" json:"owner,omitempty"`
	Run             string            `yaml:"run,omitempty" json:"run,omitempty"`
	Manual          string            `yaml:"manual,omitempty" json:"manual,omitempty"`
	Shell           string            `yaml:"shell,omitempty" json:"shell,omitempty"`
	Dir             string            `yaml:"dir,omitempty" json:"dir,omitempty"`
	Env             map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	When            string            `yaml:"when,omitempty" json:"when,omitempty"`
	Confirm         bool              `yaml:"confirm,omitempty" json:"confirm,omitempty"`
	Timeout         string            `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Retries         int               `yaml:"retries,omitempty" json:"retries,omitempty"`
	RetryDelay      string            `yaml:"retry_delay,omitempty" json:"retry_delay,omitempty"`
	TTY             bool              `yaml:"tty,omitempty" json:"tty,omitempty"`
	Always          bool              `yaml:"always,omitempty" json:"always,omitempty"`
	Capture         string            `yaml:"capture,omitempty" json:"capture,omitempty"`
	ContinueOnError bool              `yaml:"continue_on_error,omitempty" json:"continue_on_error,omitempty"`
	Unattended      string            `yaml:"unattended,omitempty" json:"unattended,omitempty"`
	// Expect describes what success looks like (shown when reviewing,
	// inspecting, or running a step by hand). Template.
	Expect string `yaml:"expect,omitempty" json:"expect,omitempty"`
	// Outputs and SecretOutputs name shell variables to capture into the
	// run's state when the step ends, for later steps to use.
	Outputs       []string `yaml:"outputs,omitempty" json:"outputs,omitempty"`
	SecretOutputs []string `yaml:"secret_outputs,omitempty" json:"secret_outputs,omitempty"`
}

func (s Step) IsManual() bool { return s.Manual != "" }

func (s Step) TimeoutDuration() time.Duration {
	d, _ := time.ParseDuration(s.Timeout)
	return d
}

func (s Step) RetryDelayDuration() time.Duration {
	d, _ := time.ParseDuration(s.RetryDelay)
	return d
}

func (s Step) UnattendedPolicy() string {
	if s.Unattended == "" {
		return UnattendedFail
	}
	return s.Unattended
}

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// Parse decodes YAML, rejecting unknown fields, and validates the result.
func Parse(data []byte) (*Runbook, error) {
	var rb Runbook
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&rb); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	// A section applies to the steps after it until the next one.
	for i := 1; i < len(rb.Steps); i++ {
		if rb.Steps[i].Section == "" {
			rb.Steps[i].Section = rb.Steps[i-1].Section
		}
	}
	if err := rb.Validate(); err != nil {
		return nil, err
	}
	return &rb, nil
}

func (rb *Runbook) Marshal() ([]byte, error) { return yaml.Marshal(rb) }

// Format validates rb and renders it as YAML, the way the structured
// editor saves. A section is written only where it changes. Comments from
// a previous YAML version aren't kept.
func Format(rb *Runbook) ([]byte, error) {
	if err := rb.Validate(); err != nil {
		return nil, err
	}
	out := *rb
	out.Steps = append([]Step(nil), rb.Steps...)
	for i := len(out.Steps) - 1; i > 0; i-- {
		if out.Steps[i].Section == out.Steps[i-1].Section {
			out.Steps[i].Section = ""
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&out); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	// Round-trip check: what we write must read back as the same runbook.
	if _, err := Parse(buf.Bytes()); err != nil {
		return nil, fmt.Errorf("formatted YAML doesn't parse back: %w", err)
	}
	return buf.Bytes(), nil
}

// Validate returns all problems found, joined.
func (rb *Runbook) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if !slugRe.MatchString(rb.Name) {
		add("name %q must be lowercase letters, digits, '.', '_' or '-'", rb.Name)
	}
	seen := map[string]bool{}
	for i, in := range rb.Inputs {
		if !identRe.MatchString(in.Name) {
			add("inputs[%d]: name %q must be a valid identifier", i, in.Name)
		}
		if seen[in.Name] {
			add("inputs[%d]: duplicate name %q", i, in.Name)
		}
		seen[in.Name] = true
		if in.Default != "" && len(in.Choices) > 0 && !contains(in.Choices, in.Default) {
			add("inputs[%d]: default %q not in choices", i, in.Default)
		}
	}
	if len(rb.Steps) == 0 {
		add("runbook has no steps")
	}
	for i, f := range rb.Profile {
		if strings.TrimSpace(f) == "" {
			add("profile[%d]: empty path", i)
		}
	}
	ids := map[string]bool{}
	for i, s := range rb.Steps {
		where := fmt.Sprintf("steps[%d] (%s)", i, s.Name)
		if s.Name == "" {
			add("steps[%d]: name is required", i)
		}
		if (s.Run == "") == (s.Manual == "") {
			add("%s: exactly one of run or manual is required", where)
		}
		if s.ID != "" {
			if !identRe.MatchString(s.ID) {
				add("%s: id %q must be a valid identifier", where, s.ID)
			}
			if ids[s.ID] {
				add("%s: duplicate id %q", where, s.ID)
			}
			ids[s.ID] = true
		}
		if s.Capture != "" && !identRe.MatchString(s.Capture) {
			add("%s: capture %q must be a valid identifier", where, s.Capture)
		}
		if s.Timeout != "" {
			if _, err := time.ParseDuration(s.Timeout); err != nil {
				add("%s: invalid timeout %q", where, s.Timeout)
			}
		}
		if s.RetryDelay != "" {
			if _, err := time.ParseDuration(s.RetryDelay); err != nil {
				add("%s: invalid retry_delay %q", where, s.RetryDelay)
			}
		}
		if s.TTY && s.Capture != "" {
			add("%s: capture can't be used with tty (write to $RUNNR_OUTPUT instead)", where)
		}
		if s.Retries < 0 {
			add("%s: retries must be >= 0", where)
		}
		switch s.Unattended {
		case "", UnattendedFail, UnattendedSkip, UnattendedPass:
		default:
			add("%s: unattended must be fail, skip or pass", where)
		}
		if s.IsManual() && (s.Capture != "" || s.Retries > 0 || s.Timeout != "" || s.RetryDelay != "" || s.TTY || len(s.Outputs)+len(s.SecretOutputs) > 0) {
			add("%s: capture, retries, retry_delay, timeout, tty and outputs only apply to run steps", where)
		}
		for _, o := range append(append([]string{}, s.Outputs...), s.SecretOutputs...) {
			if !identRe.MatchString(o) {
				add("%s: output %q must be a valid identifier", where, o)
			}
		}
		for field, text := range map[string]string{"run": s.Run, "manual": s.Manual, "when": s.When, "dir": s.Dir, "expect": s.Expect} {
			if _, err := template.New("").Funcs(TemplateFuncs).Option("missingkey=error").Parse(text); err != nil {
				add("%s: %s template: %v", where, field, err)
			}
		}
	}
	return errors.Join(errs...)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// TemplateFuncs are available in run, manual, when and dir templates.
var TemplateFuncs = template.FuncMap{
	"default": func(def, v string) string {
		if v == "" {
			return def
		}
		return v
	},
	"upper":    strings.ToUpper,
	"lower":    strings.ToLower,
	"trim":     strings.TrimSpace,
	"contains": strings.Contains,
	"quote":    func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" },
}

// Render expands a template against vars. Unknown variables are an error.
func Render(text string, vars map[string]string) (string, error) {
	if !strings.Contains(text, "{{") {
		return text, nil
	}
	t, err := template.New("").Funcs(TemplateFuncs).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, vars); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// Truthy interprets a rendered `when` expression.
func Truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "false", "0", "no", "off":
		return false
	}
	return true
}

// Scaffold returns a starter runbook for `runnr new`.
func Scaffold(name string) string {
	return fmt.Sprintf(`name: %s
description: Describe what this runbook does.
inputs:
  - name: target
    description: What to operate on
    default: staging
    choices: [staging, production]
steps:
  - name: Show target
    run: echo "Operating on {{.target}}"
  - name: Check with a human
    manual: Confirm nothing unusual is happening on {{.target}}.
    unattended: pass
  - name: Do the work
    run: echo "doing work"
    confirm: true
`, name)
}

// StepIndex resolves a step reference: an id, a 1-based number, or an exact name.
func (rb *Runbook) StepIndex(ref string) (int, error) {
	for i, s := range rb.Steps {
		if s.ID != "" && s.ID == ref {
			return i, nil
		}
	}
	var n int
	if _, err := fmt.Sscanf(ref, "%d", &n); err == nil && fmt.Sprint(n) == ref {
		if n < 1 || n > len(rb.Steps) {
			return 0, fmt.Errorf("step %d out of range 1-%d", n, len(rb.Steps))
		}
		return n - 1, nil
	}
	for i, s := range rb.Steps {
		if s.Name == ref {
			return i, nil
		}
	}
	return 0, fmt.Errorf("no step %q (use an id, number or exact name)", ref)
}
