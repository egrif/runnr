package runbook

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

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

// SetCollectionRunbooks rewrites a collection's runbooks list, keeping the
// rest of its YAML (comments included) as it was.
func SetCollectionRunbooks(raw []byte, names []string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("collection isn't a YAML mapping")
	}
	m := doc.Content[0]
	var seq *yaml.Node
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == "runbooks" {
			seq = m.Content[i+1]
		}
	}
	if seq == nil {
		seq = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "runbooks"}, seq)
	}
	// Keep the existing item nodes (and their comments) where they survive.
	old := map[string]*yaml.Node{}
	for _, n := range seq.Content {
		old[n.Value] = n
	}
	seq.Kind, seq.Tag, seq.Value = yaml.SequenceNode, "!!seq", ""
	seq.Content = nil
	for _, name := range names {
		n := old[name]
		if n == nil {
			n = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}
		}
		seq.Content = append(seq.Content, n)
	}
	if len(names) == 0 {
		seq.Style = yaml.FlowStyle // runbooks: []
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// SetCollectionField sets a top-level scalar field of a collection's YAML
// (name, description, expect), or removes it if value is empty, keeping
// the rest of the YAML (comments included) as it was.
func SetCollectionField(raw []byte, key, value string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("collection isn't a YAML mapping")
	}
	m := doc.Content[0]
	at := -1
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			at = i
		}
	}
	switch {
	case value == "" && at >= 0:
		m.Content = append(m.Content[:at], m.Content[at+2:]...)
	case value == "":
	case at >= 0:
		v := m.Content[at+1]
		v.Kind, v.Tag, v.Value, v.Content = yaml.ScalarNode, "!!str", value, nil
		v.Style = 0
		if strings.Contains(value, "\n") {
			v.Style = yaml.LiteralStyle
		}
	default:
		// New fields go before runbooks: name, description, expect, runbooks.
		pos := len(m.Content)
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == "runbooks" || (key == "description" && m.Content[i].Value == "expect") {
				pos = i
				break
			}
		}
		v := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
		if strings.Contains(value, "\n") {
			v.Style = yaml.LiteralStyle
		}
		k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
		m.Content = append(m.Content[:pos], append([]*yaml.Node{k, v}, m.Content[pos:]...)...)
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), enc.Close()
}
