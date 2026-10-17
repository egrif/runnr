// Package runbook defines the runbook file format and its validation.
package runbook

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"slices"
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
	// MaintenanceWindow is the time production is down: from the step
	// marked window: open to the one marked window: close.
	MaintenanceWindow *MaintenanceWindow `yaml:"maintenance_window,omitempty" json:"maintenance_window,omitempty"`
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
	// WindowOffset is when the section is planned to start, relative to
	// the maintenance window opening (e.g. -30m, 0, +12m). OffsetInput
	// names an input that overrides it when given a value.
	WindowOffset string `yaml:"window_offset,omitempty" json:"window_offset,omitempty"`
	OffsetInput  string `yaml:"offset_input,omitempty" json:"offset_input,omitempty"`
}

// MaintenanceWindow describes the planned production downtime.
type MaintenanceWindow struct {
	// Target is how long it's expected (or allowed) to last, e.g. 45m.
	// TargetInput names an input that overrides it when given a value.
	Target      string `yaml:"target,omitempty" json:"target,omitempty"`
	TargetInput string `yaml:"target_input,omitempty" json:"target_input,omitempty"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// Window marks on steps.
const (
	WindowOpen  = "open"  // production goes down when this step starts
	WindowClose = "close" // production is back when this step finishes
)

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
	ID      string            `yaml:"id,omitempty" json:"id,omitempty"`
	Name    string            `yaml:"name" json:"name"`
	Section string            `yaml:"section,omitempty" json:"section,omitempty"`
	Owner   string            `yaml:"owner,omitempty" json:"owner,omitempty"`
	Run     string            `yaml:"run,omitempty" json:"run,omitempty"`
	Manual  string            `yaml:"manual,omitempty" json:"manual,omitempty"`
	Shell   string            `yaml:"shell,omitempty" json:"shell,omitempty"`
	Dir     string            `yaml:"dir,omitempty" json:"dir,omitempty"`
	Env     map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	When    string            `yaml:"when,omitempty" json:"when,omitempty"`
	Confirm bool              `yaml:"confirm,omitempty" json:"confirm,omitempty"`
	// Dangerous steps need a typed double check unless they run in
	// sequence in a recorded run of the saved runbook.
	Dangerous       bool   `yaml:"dangerous,omitempty" json:"dangerous,omitempty"`
	Timeout         string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Retries         int    `yaml:"retries,omitempty" json:"retries,omitempty"`
	RetryDelay      string `yaml:"retry_delay,omitempty" json:"retry_delay,omitempty"`
	TTY             bool   `yaml:"tty,omitempty" json:"tty,omitempty"`
	Always          bool   `yaml:"always,omitempty" json:"always,omitempty"`
	Capture         string `yaml:"capture,omitempty" json:"capture,omitempty"`
	ContinueOnError bool   `yaml:"continue_on_error,omitempty" json:"continue_on_error,omitempty"`
	Unattended      string `yaml:"unattended,omitempty" json:"unattended,omitempty"`
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

	// Ways are different ways to do the step (e.g. Scripted, Manual, or a
	// named alternative); the preferred one is used unless Choose is set,
	// which asks each time (unattended runs always use the preferred one).
	Ways   []Way `yaml:"ways,omitempty" json:"ways,omitempty"`
	Choose bool  `yaml:"choose,omitempty" json:"choose,omitempty"`

	// Window marks where the maintenance window opens or closes;
	// WindowOffset is when the step is planned to start relative to its
	// opening (e.g. -30m, +12m).
	Window       string `yaml:"window,omitempty" json:"window,omitempty"`
	WindowOffset string `yaml:"window_offset,omitempty" json:"window_offset,omitempty"`
	// OffsetInput names an input that overrides WindowOffset when given a value.
	OffsetInput string `yaml:"offset_input,omitempty" json:"offset_input,omitempty"`

	// Alternatives was replaced by Ways; it's kept only to say so.
	Alternatives yaml.Node `yaml:"alternatives,omitempty" json:"-"`
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

// Way is one way to do a step: a command, instructions for a person, or
// a group of checks.
type Way struct {
	Name      string  `yaml:"name" json:"name"`
	Preferred bool    `yaml:"preferred,omitempty" json:"preferred,omitempty"`
	Run       string  `yaml:"run,omitempty" json:"run,omitempty"`
	Manual    string  `yaml:"manual,omitempty" json:"manual,omitempty"`
	Checks    []Check `yaml:"checks,omitempty" json:"checks,omitempty"`
	TTY       bool    `yaml:"tty,omitempty" json:"tty,omitempty"`
}

// Kind is run, manual or checks.
func (w Way) Kind() string {
	switch {
	case w.Manual != "":
		return "manual"
	case len(w.Checks) > 0:
		return "checks"
	}
	return "run"
}

func (s Step) IsManual() bool { return s.Manual != "" }

// Kind is run, manual, checks, or ways (several ways to do it).
func (s Step) Kind() string {
	switch {
	case len(s.Ways) > 1:
		return "ways"
	case len(s.Ways) == 1:
		return s.Ways[0].Kind()
	case s.Manual != "":
		return "manual"
	case len(s.Checks) > 0:
		return "checks"
	}
	return "run"
}

// WayList is the step's ways: its Ways, or the one way its run, manual or
// checks make.
func (s Step) WayList() []Way {
	if len(s.Ways) > 0 {
		return s.Ways
	}
	return []Way{{Name: wayName(s.Kind()), Preferred: true, Run: s.Run, Manual: s.Manual, Checks: s.Checks, TTY: s.TTY}}
}

func wayName(kind string) string {
	return map[string]string{"run": "Scripted", "manual": "Manual", "checks": "Checks"}[kind]
}

// PreferredWay is the index of the preferred way (the first if none is marked).
func (s Step) PreferredWay() int {
	for i, w := range s.Ways {
		if w.Preferred {
			return i
		}
	}
	return 0
}

// WithWay is the step done the given way: a plain run, manual or checks step.
func (s Step) WithWay(w Way) Step {
	s.Ways, s.Choose = nil, false
	s.Run, s.Manual, s.Checks = w.Run, w.Manual, w.Checks
	s.TTY = w.Kind() == "run" && (s.TTY || w.TTY)
	return s
}

// ParseOffset reads a window offset: a signed duration such as -30m,
// +1h15m or 0.
func ParseOffset(v string) (time.Duration, error) {
	t := strings.TrimSpace(v)
	if t == "0" {
		return 0, nil
	}
	return time.ParseDuration(strings.TrimPrefix(t, "+"))
}

// offsetOf is an offset field, unless its override input has a value in vars.
func offsetOf(literal, input string, vars map[string]string) string {
	if v := strings.TrimSpace(vars[input]); input != "" && v != "" {
		return v
	}
	return literal
}

// PlannedOffset is when step i is planned to start relative to the window
// opening: its own offset, or its section's for a section's first step.
// Inputs named by offset_input override the offsets they're on.
func (rb *Runbook) PlannedOffset(i int, vars map[string]string) (time.Duration, bool) {
	st := rb.Steps[i]
	v := offsetOf(st.WindowOffset, st.OffsetInput, vars)
	if v == "" && rb.sectionStarts(i) {
		v = rb.SectionOffset(st.Section, vars)
	}
	if v == "" {
		return 0, false
	}
	d, err := ParseOffset(v)
	return d, err == nil
}

// sectionStarts reports whether step i is its section's first step.
func (rb *Runbook) sectionStarts(i int) bool {
	st := rb.Steps[i]
	return st.Section != "" && (i == 0 || rb.Steps[i-1].Section != st.Section)
}

// SectionOffset is a section's planned start ("" if it has none).
func (rb *Runbook) SectionOffset(name string, vars map[string]string) string {
	for _, sec := range rb.Sections {
		if sec.Name == name {
			return offsetOf(sec.WindowOffset, sec.OffsetInput, vars)
		}
	}
	return ""
}

// WindowTarget is the maintenance window's target length ("" if none).
func (rb *Runbook) WindowTarget(vars map[string]string) string {
	if rb.MaintenanceWindow == nil {
		return ""
	}
	return offsetOf(rb.MaintenanceWindow.Target, rb.MaintenanceWindow.TargetInput, vars)
}

// CheckWindowInputs checks that inputs overriding window offsets or the
// target hold valid durations.
func (rb *Runbook) CheckWindowInputs(vars map[string]string) error {
	var errs []error
	check := func(input string, target bool) {
		v := strings.TrimSpace(vars[input])
		if input == "" || v == "" {
			return
		}
		if target {
			if d, err := time.ParseDuration(v); err != nil || d <= 0 {
				errs = append(errs, fmt.Errorf("input %s: %q isn't a window length like 45m", input, v))
			}
		} else if _, err := ParseOffset(v); err != nil {
			errs = append(errs, fmt.Errorf("input %s: %q isn't an offset like -30m, 0 or +12m", input, v))
		}
	}
	if mw := rb.MaintenanceWindow; mw != nil {
		check(mw.TargetInput, true)
	}
	for _, sec := range rb.Sections {
		check(sec.OffsetInput, false)
	}
	for _, st := range rb.Steps {
		check(st.OffsetInput, false)
	}
	return errors.Join(errs...)
}

// TimelineEntry is a planned point in a runbook's maintenance window:
// the window itself (Kind "window", with its target length), a section's
// start, or a step's start, relative to the window opening.
type TimelineEntry struct {
	Kind   string        `json:"kind"` // window | section | step
	Name   string        `json:"name"`
	Step   int           `json:"step"` // step index (sections: their first step)
	Offset time.Duration `json:"offset_ns"`
	Length time.Duration `json:"length_ns,omitempty"` // window only
}

// Timeline lists the window and every section and step with a planned
// start (steps only where they set one, not where they take their
// section's), in order.
func (rb *Runbook) Timeline(vars map[string]string) []TimelineEntry {
	var out []TimelineEntry
	if rb.MaintenanceWindow == nil {
		return nil
	}
	w := TimelineEntry{Kind: "window", Name: "Maintenance window"}
	w.Step, _ = rb.WindowSteps()
	if d, err := time.ParseDuration(rb.WindowTarget(vars)); err == nil {
		w.Length = d
	}
	out = append(out, w)
	for i, st := range rb.Steps {
		if rb.sectionStarts(i) {
			if d, err := ParseOffset(rb.SectionOffset(st.Section, vars)); err == nil && rb.SectionOffset(st.Section, vars) != "" {
				out = append(out, TimelineEntry{Kind: "section", Name: st.Section, Step: i, Offset: d})
			}
		}
		if v := offsetOf(st.WindowOffset, st.OffsetInput, vars); v != "" {
			if d, err := ParseOffset(v); err == nil {
				out = append(out, TimelineEntry{Kind: "step", Name: st.Name, Step: i, Offset: d})
			}
		}
	}
	return out
}

// ChoosingSteps lists the steps (indexes) set to ask which way, leaving
// out skip (indexes).
func (rb *Runbook) ChoosingSteps(skip []int) []int {
	var out []int
	for i, st := range rb.Steps {
		if st.Choose && len(st.Ways) > 1 && !slices.Contains(skip, i) {
			out = append(out, i)
		}
	}
	return out
}

// WindowSteps returns the indexes of the steps that open and close the
// maintenance window (-1 if none).
func (rb *Runbook) WindowSteps() (open, close int) {
	open, close = -1, -1
	for i, st := range rb.Steps {
		switch st.Window {
		case WindowOpen:
			open = i
		case WindowClose:
			close = i
		}
	}
	return open, close
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
	errs = append(errs, rb.validateWindow()...)
	ids := map[string]bool{}
	for i, st := range rb.Steps {
		if st.ID != "" {
			if ids[st.ID] {
				add("steps[%d] (%s): duplicate id %q", i, st.Name, st.ID)
			}
			ids[st.ID] = true
		}
		errs = append(errs, validateStep(fmt.Sprintf("steps[%d] (%s)", i, st.Name), st)...)
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

func (rb *Runbook) validateWindow() []error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	used := false
	opens, closes := 0, 0
	for i, st := range rb.Steps {
		switch st.Window {
		case "":
		case WindowOpen:
			opens++
		case WindowClose:
			closes++
		default:
			add("steps[%d] (%s): window must be open or close", i, st.Name)
		}
		if st.WindowOffset != "" {
			used = true
			if _, err := ParseOffset(st.WindowOffset); err != nil {
				add("steps[%d] (%s): window_offset %q must be a duration like -30m, 0 or +12m", i, st.Name, st.WindowOffset)
			}
		}
	}
	for i, sec := range rb.Sections {
		if sec.WindowOffset != "" {
			used = true
			if _, err := ParseOffset(sec.WindowOffset); err != nil {
				add("sections[%d] (%s): window_offset %q must be a duration like -30m, 0 or +12m", i, sec.Name, sec.WindowOffset)
			}
		}
	}
	if opens > 1 || closes > 1 {
		add("only one step can open the maintenance window and one close it")
	}
	if o, c := rb.WindowSteps(); o >= 0 && c >= 0 && c < o {
		add("the step closing the maintenance window comes before the one opening it")
	} else if c >= 0 && o < 0 {
		add("a step closes the maintenance window, but none opens it")
	}
	inputs := map[string]bool{}
	for _, in := range rb.Inputs {
		inputs[in.Name] = true
	}
	checkInput := func(where, name string) {
		if name != "" {
			used = true
			if !inputs[name] {
				add("%s: offset_input %q isn't an input of this runbook", where, name)
			}
		}
	}
	for i, st := range rb.Steps {
		checkInput(fmt.Sprintf("steps[%d] (%s)", i, st.Name), st.OffsetInput)
	}
	for i, sec := range rb.Sections {
		checkInput(fmt.Sprintf("sections[%d] (%s)", i, sec.Name), sec.OffsetInput)
	}
	if mw := rb.MaintenanceWindow; mw != nil && mw.TargetInput != "" && !inputs[mw.TargetInput] {
		add("maintenance_window: target_input %q isn't an input of this runbook", mw.TargetInput)
	}
	if (used || opens+closes > 0) && rb.MaintenanceWindow == nil {
		add("window and window_offset need a maintenance_window (e.g. maintenance_window: {target: 45m})")
	}
	if mw := rb.MaintenanceWindow; mw != nil && mw.Target != "" {
		if d, err := time.ParseDuration(mw.Target); err != nil || d <= 0 {
			add("maintenance_window: target %q must be a duration like 45m", mw.Target)
		}
	}
	return errs
}

func validTemplate(text string) error {
	_, err := template.New("").Funcs(TemplateFuncs).Option("missingkey=error").Parse(text)
	return err
}

// validateStep checks one step.
func validateStep(where string, s Step) []error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(where+": "+format, a...)) }

	if s.Name == "" {
		add("name is required")
	}
	if !s.Alternatives.IsZero() {
		add("alternatives was replaced by ways: list each way with a name and run, manual or checks, and mark one preferred")
	}
	kinds := 0
	for _, on := range []bool{s.Run != "", s.Manual != "", len(s.Checks) > 0, len(s.Ways) > 0} {
		if on {
			kinds++
		}
	}
	if kinds != 1 {
		add("exactly one of run, manual, checks or ways is required")
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
	hasKind := func(k string) bool {
		return slices.ContainsFunc(s.WayList(), func(w Way) bool { return w.Kind() == k })
	}
	if runOnly && !hasKind("run") {
		add("capture, retries, retry_delay, tty and outputs only apply to commands (checks use repeat)")
	}
	if s.Timeout != "" && !hasKind("run") && !hasKind("checks") {
		add("timeout doesn't apply to a manual step")
	}
	if s.Choose && len(s.Ways) < 2 {
		add("choose needs at least two ways")
	}
	seenWay := map[string]bool{}
	preferred := 0
	for j, w := range s.Ways {
		ww := fmt.Sprintf("ways[%d] (%s)", j, w.Name)
		if w.Name == "" {
			add("%s: name is required", ww)
		} else if seenWay[w.Name] {
			add("%s: two ways have that name", ww)
		}
		seenWay[w.Name] = true
		n := 0
		for _, on := range []bool{w.Run != "", w.Manual != "", len(w.Checks) > 0} {
			if on {
				n++
			}
		}
		if n != 1 {
			add("%s: exactly one of run, manual or checks is required", ww)
		}
		if w.TTY && w.Run == "" {
			add("%s: tty applies to run", ww)
		}
		if w.Preferred {
			preferred++
		}
		for field, text := range map[string]string{"run": w.Run, "manual": w.Manual} {
			if err := validTemplate(text); err != nil {
				add("%s: %s template: %v", ww, field, err)
			}
		}
		for k, c := range w.Checks {
			if c.Name == "" || c.Run == "" {
				add("%s: checks[%d]: name and run are required", ww, k)
			}
			if err := validTemplate(c.Run); err != nil {
				add("%s: checks[%d] (%s): run template: %v", ww, k, c.Name, err)
			}
		}
	}
	if preferred > 1 {
		add("only one way can be preferred")
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
		if !hasKind("checks") {
			add("repeat only applies to checks")
		}
		every, err1 := time.ParseDuration(s.Repeat.Every)
		timeout, err2 := time.ParseDuration(s.Repeat.Timeout)
		if err1 != nil || err2 != nil || every <= 0 || timeout < every {
			add("repeat needs every and timeout durations, with timeout >= every (e.g. every: 20s, timeout: 10m)")
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

// ParseStep decodes one step (YAML), checking its fields are known. The
// caller validates it in its runbook.
func ParseStep(data []byte) (Step, error) {
	var s Step
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return s, fmt.Errorf("parse: %w", err)
	}
	return s, nil
}

// FormatStep renders one step as YAML.
func FormatStep(s Step) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	return buf.Bytes(), enc.Close()
}
