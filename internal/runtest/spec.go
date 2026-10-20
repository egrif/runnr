// Package runtest tests runbooks without touching what they act on: a test
// runs the real runbook with chosen commands faked (canned responses, as
// data), a simulated operator answering its prompts, and expectations
// about how the run went and what it called.
package runtest

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Library is a reusable set of fakes, e.g. for the AWS CLI's RDS commands.
type Library struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Fakes       []Fake `yaml:"fakes" json:"fakes"`
}

// Fake answers the calls it matches.
type Fake struct {
	// Command is the program and words its arguments must contain, in
	// order (e.g. "aws rds describe-db-instances").
	Command string `yaml:"command" json:"command"`
	// Args are flag values the call must have ("--region": "us-*"); a
	// value is a glob, and templates use the run's values.
	Args map[string]string `yaml:"args,omitempty" json:"args,omitempty"`
	// Match is a regular expression the whole call must match.
	Match string `yaml:"match,omitempty" json:"match,omitempty"`
	// Stdin is text the call's standard input must contain.
	Stdin    string `yaml:"stdin,omitempty" json:"stdin,omitempty"`
	Response `yaml:",inline" json:",inline"`
	// Sequence answers successive calls in turn; the last one repeats.
	Sequence []Response `yaml:"sequence,omitempty" json:"sequence,omitempty"`
}

// Response is what a faked call does. Text and JSON are templates over
// the run's values (the call's environment).
type Response struct {
	// JSON is the full response, for commands with --query/--output (the
	// AWS CLI): the call's own query and output format are applied to it.
	JSON   any    `yaml:"json,omitempty" json:"json,omitempty"`
	Stdout string `yaml:"stdout,omitempty" json:"stdout,omitempty"`
	Stderr string `yaml:"stderr,omitempty" json:"stderr,omitempty"`
	Exit   int    `yaml:"exit,omitempty" json:"exit,omitempty"`
	// Delay is how long the call takes (e.g. 2s).
	Delay string `yaml:"delay,omitempty" json:"delay,omitempty"`
}

// Suite is a runbook's tests.
type Suite struct {
	// Name is the suite's name in runnr (default: the runbook's).
	Name    string `yaml:"name,omitempty" json:"name,omitempty"`
	Runbook string `yaml:"runbook" json:"runbook"`
	// Uses are the fake libraries every scenario starts from.
	Uses []string `yaml:"uses,omitempty" json:"uses,omitempty"`
	// Isolate are commands that must never run for real: a call to one
	// that no fake answers fails the test.
	Isolate []string `yaml:"isolate,omitempty" json:"isolate,omitempty"`
	// Strict runs every scenario in a sandbox: no network, no writing
	// outside the test's directory, and no programs but the fakes, the
	// shell and the allow-list (DefaultAllow, plus Allow).
	Strict bool     `yaml:"strict,omitempty" json:"strict,omitempty"`
	Allow  []string `yaml:"allow,omitempty" json:"allow,omitempty"`
	// Files are fake commands at paths in the test's directory, for
	// scripts a runbook calls by path ("outhouse/bin/tool.rb": tool.rb):
	// point an input at {{.test_dir}}/outhouse.
	Files map[string]string `yaml:"files,omitempty" json:"files,omitempty"`
	// Dirs are directories to make in the test's directory (for steps
	// that cd into a checkout: point an input at {{.test_dir}}/…).
	Dirs []string `yaml:"dirs,omitempty" json:"dirs,omitempty"`
	// Functions replace library or runbook functions for the test.
	Functions map[string]string `yaml:"functions,omitempty" json:"functions,omitempty"`
	Inputs    map[string]string `yaml:"inputs,omitempty" json:"inputs,omitempty"`
	Fakes     []Fake            `yaml:"fakes,omitempty" json:"fakes,omitempty"`
	Operator  Operator          `yaml:"operator,omitempty" json:"operator,omitempty"`
	Scenarios []Scenario        `yaml:"scenarios" json:"scenarios"`
}

// Scenario is one test: the suite's setup, with its own changes.
type Scenario struct {
	Name        string            `yaml:"name" json:"name"`
	Description string            `yaml:"description,omitempty" json:"description,omitempty"`
	Inputs      map[string]string `yaml:"inputs,omitempty" json:"inputs,omitempty"`
	// Fakes come before the suite's and the libraries' (and so win).
	Fakes     []Fake            `yaml:"fakes,omitempty" json:"fakes,omitempty"`
	Functions map[string]string `yaml:"functions,omitempty" json:"functions,omitempty"`
	Operator  Operator          `yaml:"operator,omitempty" json:"operator,omitempty"`
	Expect    Expect            `yaml:"expect,omitempty" json:"expect,omitempty"`
}

// Operator is how the simulated operator answers. Each answer is a word,
// or a list of words used in turn (the last repeats).
type Operator struct {
	// Inputs answer the inputs a run asks for.
	Inputs map[string]string `yaml:"inputs,omitempty" json:"inputs,omitempty"`
	// Default answers for every step; Steps (by number, id or name) win.
	Answers `yaml:",inline" json:",inline"`
	Steps   map[string]Answers `yaml:"steps,omitempty" json:"steps,omitempty"`
}

// Answers to each kind of prompt (default in brackets):
// confirm: yes|no [yes]; danger: type|decline [type]; manual:
// done|skip|fail [done]; failure: abort|retry|skip|way|force [abort];
// before: run|skip|abort [run]; after: continue|retry|fail|abort
// [continue]; way: the name of the way to choose [the preferred one].
type Answers struct {
	Confirm Words `yaml:"confirm,omitempty" json:"confirm,omitempty"`
	Danger  Words `yaml:"danger,omitempty" json:"danger,omitempty"`
	Manual  Words `yaml:"manual,omitempty" json:"manual,omitempty"`
	Failure Words `yaml:"failure,omitempty" json:"failure,omitempty"`
	Before  Words `yaml:"before,omitempty" json:"before,omitempty"`
	After   Words `yaml:"after,omitempty" json:"after,omitempty"`
	Way     Words `yaml:"way,omitempty" json:"way,omitempty"`
}

// Words is one word or a list of them.
type Words []string

func (w *Words) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*w = Words{n.Value}
		return nil
	}
	var l []string
	if err := n.Decode(&l); err != nil {
		return err
	}
	*w = l
	return nil
}

// Expect is how a scenario's run should go.
type Expect struct {
	// Status is the run's status (succeeded, failed, aborted).
	Status string `yaml:"status,omitempty" json:"status,omitempty"`
	// Steps are step statuses, by number, id or name.
	Steps map[string]string `yaml:"steps,omitempty" json:"steps,omitempty"`
	// Called and NotCalled are calls (a program and words, in order).
	Called    []string `yaml:"called,omitempty" json:"called,omitempty"`
	NotCalled []string `yaml:"not_called,omitempty" json:"not_called,omitempty"`
	// Order is calls that must happen in this order.
	Order []string `yaml:"order,omitempty" json:"order,omitempty"`
	// Output is text steps' recorded output must contain, by step.
	Output map[string]string `yaml:"output,omitempty" json:"output,omitempty"`
}

// ParseLibrary decodes and checks a fake library.
func ParseLibrary(raw []byte) (*Library, error) {
	var l Library
	if err := decodeStrict(raw, &l); err != nil {
		return nil, err
	}
	if l.Name == "" {
		return nil, errors.New("name is required")
	}
	var errs []error
	for i, f := range l.Fakes {
		errs = append(errs, f.check(fmt.Sprintf("fakes[%d]", i))...)
	}
	return &l, errors.Join(errs...)
}

// ParseSuite decodes and checks a test suite.
func ParseSuite(raw []byte) (*Suite, error) {
	var s Suite
	if err := decodeStrict(raw, &s); err != nil {
		return nil, err
	}
	if s.Name == "" {
		s.Name = s.Runbook
	}
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if s.Runbook == "" {
		add("runbook is required")
	}
	if len(s.Scenarios) == 0 {
		add("scenarios: at least one is required")
	}
	for i, f := range s.Fakes {
		errs = append(errs, f.check(fmt.Sprintf("fakes[%d]", i))...)
	}
	errs = append(errs, s.Operator.check("operator")...)
	seen := map[string]bool{}
	for i, sc := range s.Scenarios {
		where := fmt.Sprintf("scenarios[%d]", i)
		if sc.Name == "" {
			add("%s: name is required", where)
		} else if seen[sc.Name] {
			add("%s: duplicate name %q", where, sc.Name)
		}
		seen[sc.Name] = true
		for j, f := range sc.Fakes {
			errs = append(errs, f.check(fmt.Sprintf("%s (%s).fakes[%d]", where, sc.Name, j))...)
		}
		errs = append(errs, sc.Operator.check(where+".operator")...)
	}
	for _, d := range s.Dirs {
		if strings.HasPrefix(d, "/") || strings.Contains(d, "..") {
			add("dirs: %q must be a relative path inside the test directory", d)
		}
	}
	for p, cmd := range s.Files {
		if strings.HasPrefix(p, "/") || strings.Contains(p, "..") || cmd == "" {
			add("files: %q must be a relative path inside the test directory, naming a command", p)
		}
	}
	return &s, errors.Join(errs...)
}

func (f Fake) check(where string) []error {
	var errs []error
	if strings.TrimSpace(f.Command) == "" {
		errs = append(errs, fmt.Errorf("%s: command is required", where))
	}
	if f.Match != "" {
		if _, err := regexp.Compile(f.Match); err != nil {
			errs = append(errs, fmt.Errorf("%s: match: %v", where, err))
		}
	}
	return errs
}

var answerWords = map[string][]string{
	"confirm": {"yes", "no"}, "danger": {"type", "decline"}, "manual": {"done", "skip", "fail"},
	"failure": {"abort", "retry", "skip", "way", "force"}, "before": {"run", "skip", "abort"},
	"after": {"continue", "retry", "fail", "abort"},
}

func (o Operator) check(where string) []error {
	var errs []error
	checkA := func(w string, a Answers) {
		for kind, ws := range map[string]Words{"confirm": a.Confirm, "danger": a.Danger, "manual": a.Manual, "failure": a.Failure, "before": a.Before, "after": a.After} {
			for _, v := range ws {
				ok := false
				for _, allowed := range answerWords[kind] {
					ok = ok || v == allowed
				}
				if !ok {
					errs = append(errs, fmt.Errorf("%s: %s: %q isn't one of %s", w, kind, v, strings.Join(answerWords[kind], ", ")))
				}
			}
		}
	}
	checkA(where, o.Answers)
	for k, a := range o.Steps {
		checkA(where+".steps."+k, a)
	}
	return errs
}

func decodeStrict(raw []byte, v any) error {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	return nil
}
