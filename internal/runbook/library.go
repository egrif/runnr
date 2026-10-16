package runbook

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Function is a shell function steps (and shells) can call.
type Function struct {
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Body        string `yaml:"body" json:"body"`
}

// Library is a named collection of shell functions that runbooks can use.
type Library struct {
	Name        string              `yaml:"name" json:"name"`
	Description string              `yaml:"description,omitempty" json:"description,omitempty"`
	Functions   map[string]Function `yaml:"functions" json:"functions"`
}

var funcNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ParseLibrary decodes and validates a library.
func ParseLibrary(data []byte) (*Library, error) {
	var lib Library
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&lib); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if err := lib.Validate(); err != nil {
		return nil, err
	}
	return &lib, nil
}

func (l *Library) Validate() error {
	var errs []error
	if !slugRe.MatchString(l.Name) {
		errs = append(errs, fmt.Errorf("name %q must be lowercase letters, digits, '.', '_' or '-'", l.Name))
	}
	if len(l.Functions) == 0 {
		errs = append(errs, errors.New("library has no functions"))
	}
	errs = append(errs, validateFunctions("", l.Functions)...)
	return errors.Join(errs...)
}

func validateFunctions(where string, fns map[string]Function) []error {
	var errs []error
	for name, f := range fns {
		if !funcNameRe.MatchString(name) {
			errs = append(errs, fmt.Errorf("%sfunction %q: name must be letters, digits and _", where, name))
		}
		if strings.TrimSpace(f.Body) == "" {
			errs = append(errs, fmt.Errorf("%sfunction %q: body is empty", where, name))
		}
	}
	return errs
}

// FunctionScript renders functions as shell definitions, in name order.
func FunctionScript(fns map[string]Function) string {
	names := make([]string, 0, len(fns))
	for n := range fns {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		f := fns[n]
		if f.Description != "" {
			for _, l := range strings.Split(strings.TrimSpace(f.Description), "\n") {
				fmt.Fprintf(&b, "# %s\n", l)
			}
		}
		fmt.Fprintf(&b, "%s() {\n%s\n}\n\n", n, strings.TrimRight(f.Body, "\n"))
	}
	return b.String()
}

// LibraryScaffold is a starter library for `runnr lib new`.
func LibraryScaffold(name string) string {
	return fmt.Sprintf(`name: %s
description: Shell functions shared by runbooks.
functions:
  greet:
    description: Say hello. Usage: greet NAME
    body: |
      echo "hello, ${1:-world}"
`, name)
}
