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
	Name string `yaml:"name" json:"name"`
	// Description says what the runbook is for and what it does.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Expect is where things stand once the runbook has run.
	Expect string   `yaml:"expect,omitempty" json:"expect,omitempty"`
	Tags   []string `yaml:"tags,omitempty" json:"tags,omitempty"`
	// Sections describe the groups of steps (named by the steps' section).
	Sections []Section         `yaml:"sections,omitempty" json:"sections,omitempty"`
	Inputs   []Input           `yaml:"inputs,omitempty" json:"inputs,omitempty"`
	Env      map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	Shell    string            `yaml:"shell,omitempty" json:"shell,omitempty"`
	// Profile lists files sourced before every step (e.g. ~/.zshrc), so
	// aliases, functions and settings from your shell config work in steps.
	Profile []string `yaml:"profile,omitempty" json:"profile,omitempty"`
	// Sensitive maps names to patterns of credentials to hide in output,
	// state and history. {{SENSITIVE}} marks the part to hide, e.g.
	// "postgres://[^:/@]+:{{SENSITIVE}}@" hides just a URL's password.
	Sensitive map[string]string `yaml:"sensitive,omitempty" json:"sensitive,omitempty"`
	// Libraries names shared function libraries every step can call.
	Libraries []string `yaml:"libraries,omitempty" json:"libraries,omitempty"`
	// Functions are this runbook's own shell functions (they win over a
	// library's of the same name).
	Functions map[string]Function `yaml:"functions,omitempty" json:"functions,omitempty"`
	Steps     []Step              `yaml:"steps" json:"steps"`
}

// Section describes a group of steps: what it does and how things should
// stand at its end.
type Section struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Expect      string `yaml:"expect,omitempty" json:"expect,omitempty"`
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
	// Description says what this step is doing and why. Template.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Expect is how things should stand when the step is done (shown when
	// reviewing, at prompts, beside shells). Template.
	Expect string `yaml:"expect,omitempty" json:"expect,omitempty"`
	// Outputs and SecretOutputs name shell variables to capture into the
	// run's state when the step ends, for later steps to use.
	Outputs       []string `yaml:"outputs,omitempty" json:"outputs,omitempty"`
	SecretOutputs []string `yaml:"secret_outputs,omitempty" json:"secret_outputs,omitempty"`

	// Background starts the command and moves on; it runs on its own
	// terminal until the step named by StopAfter finishes (or the run ends).
	Background bool   `yaml:"background,omitempty" json:"background,omitempty"`
	StopAfter  string `yaml:"stop_after,omitempty" json:"stop_after,omitempty"`

	// Checks run as a group; the step passes when all of them pass.
	// Repeat retries the group until it does.
	Checks []Check `yaml:"checks,omitempty" json:"checks,omitempty"`
	Repeat *Repeat `yaml:"repeat,omitempty" json:"repeat,omitempty"`

	// Alternatives: one is chosen when the step runs.
	Alternatives []Alternative `yaml:"alternatives,omitempty" json:"alternatives,omitempty"`
}

// Check is one command in a step's group of checks; it passes if it exits 0.
type Check struct {
	Name string `yaml:"name" json:"name"`
	Run  string `yaml:"run" json:"run"`
}

// Repeat re-runs a group of checks until all pass.
type Repeat struct {
	Every   string `yaml:"every" json:"every"`
	Timeout string `yaml:"timeout" json:"timeout"`
}

// Alternative is one way to do a step: a command, or a short list of steps.
type Alternative struct {
	Name string `yaml:"name" json:"name"`
	// When, if set, must be true for the alternative to be chosen unattended.
	When  string `yaml:"when,omitempty" json:"when,omitempty"`
	Run   string `yaml:"run,omitempty" json:"run,omitempty"`
	TTY   bool   `yaml:"tty,omitempty" json:"tty,omitempty"`
	Steps []Step `yaml:"steps,omitempty" json:"steps,omitempty"`
}

func (s Step) IsManual() bool { return s.Manual != "" }

// Kind is run, manual, checks or alternatives.
func (s Step) Kind() string {
	switch {
	case s.Manual != "":
		return "manual"
	case len(s.Checks) > 0:
		return "checks"
	case len(s.Alternatives) > 0:
		return "alternatives"
	}
	return "run"
}

func (r Repeat) EveryDuration() time.Duration {
	d, _ := time.ParseDuration(r.Every)
	return d
}

func (r Repeat) TimeoutDuration() time.Duration {
	d, _ := time.ParseDuration(r.Timeout)
	return d
}

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
	for name, p := range rb.Sensitive {
		if _, err := CompileSensitive(p); err != nil {
			add("sensitive %q: %v", name, err)
		}
	}
	errs = append(errs, validateFunctions("", rb.Functions)...)
	used := map[string]bool{}
	for _, st := range rb.Steps {
		used[st.Section] = true
	}
	seenSec := map[string]bool{}
	for i, sec := range rb.Sections {
		switch {
		case sec.Name == "":
			add("sections[%d]: name is required", i)
		case !used[sec.Name]:
			add("sections[%d]: no step is in section %q", i, sec.Name)
		case seenSec[sec.Name]:
			add("sections[%d]: %q is described twice", i, sec.Name)
		}
		seenSec[sec.Name] = true
	}
	for i, l := range rb.Libraries {
		if !slugRe.MatchString(l) {
			add("libraries[%d]: %q isn't a library name", i, l)
		}
	}
	for i, f := range rb.Profile {
		if strings.TrimSpace(f) == "" {
			add("profile[%d]: empty path", i)
		}
	}
	ids := map[string]bool{}
	for i, st := range rb.Steps {
		if st.ID != "" {
			if ids[st.ID] {
				add("steps[%d] (%s): duplicate id %q", i, st.Name, st.ID)
			}
			ids[st.ID] = true
		}
		errs = append(errs, validateStep(fmt.Sprintf("steps[%d] (%s)", i, st.Name), st, false)...)
		if st.StopAfter != "" {
			if j, err := rb.StepIndex(st.StopAfter); err != nil {
				add("steps[%d] (%s): stop_after: %v", i, st.Name, err)
			} else if j <= i {
				add("steps[%d] (%s): stop_after must name a later step", i, st.Name)
			}
		}
	}
	return errors.Join(errs...)
}

func validTemplate(text string) error {
	_, err := template.New("").Funcs(TemplateFuncs).Option("missingkey=error").Parse(text)
	return err
}

// validateStep checks one step; nested is set for steps inside an alternative.
func validateStep(where string, s Step, nested bool) []error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(where+": "+format, a...)) }

	if s.Name == "" {
		add("name is required")
	}
	kinds := 0
	for _, on := range []bool{s.Run != "", s.Manual != "", len(s.Checks) > 0, len(s.Alternatives) > 0} {
		if on {
			kinds++
		}
	}
	if kinds != 1 {
		add("exactly one of run, manual, checks or alternatives is required")
	}
	if s.ID != "" && !identRe.MatchString(s.ID) {
		add("id %q must be a valid identifier", s.ID)
	}
	if s.Capture != "" && !identRe.MatchString(s.Capture) {
		add("capture %q must be a valid identifier", s.Capture)
	}
	for field, v := range map[string]string{"timeout": s.Timeout, "retry_delay": s.RetryDelay} {
		if v != "" {
			if _, err := time.ParseDuration(v); err != nil {
				add("invalid %s %q", field, v)
			}
		}
	}
	if s.TTY && s.Capture != "" {
		add("capture can't be used with tty (write to $RUNNR_OUTPUT instead)")
	}
	if s.Retries < 0 {
		add("retries must be >= 0")
	}
	switch s.Unattended {
	case "", UnattendedFail, UnattendedSkip, UnattendedPass:
	default:
		add("unattended must be fail, skip or pass")
	}
	runOnly := s.Capture != "" || s.Retries > 0 || s.RetryDelay != "" || s.TTY || len(s.Outputs)+len(s.SecretOutputs) > 0
	switch s.Kind() {
	case "manual":
		if runOnly || s.Timeout != "" {
			add("capture, retries, retry_delay, timeout, tty and outputs only apply to commands")
		}
	case "checks":
		if runOnly {
			add("capture, retries, retry_delay, tty and outputs don't apply to checks (use repeat)")
		}
	case "alternatives":
		if s.Capture != "" || s.Retries > 0 || s.RetryDelay != "" || s.TTY {
			add("capture, retries, retry_delay and tty go on the alternatives' steps")
		}
	}
	for _, o := range append(append([]string{}, s.Outputs...), s.SecretOutputs...) {
		if !identRe.MatchString(o) {
			add("output %q must be a valid identifier", o)
		}
	}
	for field, text := range map[string]string{"run": s.Run, "manual": s.Manual, "when": s.When, "dir": s.Dir, "expect": s.Expect, "description": s.Description} {
		if err := validTemplate(text); err != nil {
			add("%s template: %v", field, err)
		}
	}

	if s.Background {
		if s.Kind() != "run" {
			add("background only applies to a command")
		}
		if s.Capture != "" || s.Retries > 0 || len(s.Outputs)+len(s.SecretOutputs) > 0 {
			add("a background command can't capture or publish outputs, or retry")
		}
		if nested {
			add("background steps can't be inside an alternative")
		}
	} else if s.StopAfter != "" {
		add("stop_after only applies to background steps")
	}

	for j, c := range s.Checks {
		if c.Name == "" || c.Run == "" {
			add("checks[%d]: name and run are required", j)
		}
		if err := validTemplate(c.Run); err != nil {
			add("checks[%d] (%s): run template: %v", j, c.Name, err)
		}
	}
	if s.Repeat != nil {
		if len(s.Checks) == 0 {
			add("repeat only applies to checks")
		}
		every, err1 := time.ParseDuration(s.Repeat.Every)
		timeout, err2 := time.ParseDuration(s.Repeat.Timeout)
		if err1 != nil || err2 != nil || every <= 0 || timeout < every {
			add("repeat needs every and timeout durations, with timeout >= every (e.g. every: 20s, timeout: 10m)")
		}
	}

	if len(s.Alternatives) > 0 && nested {
		add("alternatives can't be nested")
	}
	for j, a := range s.Alternatives {
		aw := fmt.Sprintf("alternatives[%d] (%s)", j, a.Name)
		if a.Name == "" {
			add("%s: name is required", aw)
		}
		if (a.Run == "") == (len(a.Steps) == 0) {
			add("%s: exactly one of run or steps is required", aw)
		}
		if a.TTY && a.Run == "" {
			add("%s: tty applies to run", aw)
		}
		for field, text := range map[string]string{"run": a.Run, "when": a.When} {
			if err := validTemplate(text); err != nil {
				add("%s: %s template: %v", aw, field, err)
			}
		}
		for k, sub := range a.Steps {
			errs = append(errs, validateStep(fmt.Sprintf("%s: %s.steps[%d] (%s)", where, aw, k, sub.Name), sub, true)...)
		}
	}
	return errs
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
