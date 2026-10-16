package runbook

import (
	"bytes"
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Collection groups runbooks that belong together (e.g. the prep, cutover
// and fallback runbooks of one migration), with what they're for.
type Collection struct {
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Expect      string   `yaml:"expect,omitempty" json:"expect,omitempty"`
	Runbooks    []string `yaml:"runbooks" json:"runbooks"`
}

func ParseCollection(data []byte) (*Collection, error) {
	var c Collection
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	var errs []error
	if !slugRe.MatchString(c.Name) {
		errs = append(errs, fmt.Errorf("name %q must be lowercase letters, digits, '.', '_' or '-'", c.Name))
	}
	seen := map[string]bool{}
	for i, r := range c.Runbooks {
		if !slugRe.MatchString(r) {
			errs = append(errs, fmt.Errorf("runbooks[%d]: %q isn't a runbook name", i, r))
		}
		if seen[r] {
			errs = append(errs, fmt.Errorf("runbooks[%d]: %q is listed twice", i, r))
		}
		seen[r] = true
	}
	return &c, errors.Join(errs...)
}

func CollectionScaffold(name string) string {
	return fmt.Sprintf(`name: %s
description: What this group of runbooks is for.
expect: How things stand once they've all run.
runbooks: []
`, name)
}
