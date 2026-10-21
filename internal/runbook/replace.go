package runbook

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// ReplaceStep returns the runbook raw with step i (0-based) replaced by s,
// changing nothing else in the document: other steps keep their comments
// and layout, and the step keeps the comment above it. A section the step
// would inherit from the step before isn't written out.
func ReplaceStep(raw []byte, i int, s Step) ([]byte, error) {
	rb, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	if i < 0 || i >= len(rb.Steps) {
		return nil, fmt.Errorf("no step %d", i+1)
	}
	if i > 0 && s.Section == rb.Steps[i-1].Section {
		s.Section = ""
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	var steps *yaml.Node
	if len(doc.Content) == 1 && doc.Content[0].Kind == yaml.MappingNode {
		m := doc.Content[0].Content
		for k := 0; k+1 < len(m); k += 2 {
			if m[k].Value == "steps" && m[k+1].Kind == yaml.SequenceNode {
				steps = m[k+1]
			}
		}
	}
	if steps == nil || i >= len(steps.Content) {
		return nil, fmt.Errorf("can't find step %d in the runbook's YAML", i+1)
	}
	b, err := FormatStep(s)
	if err != nil {
		return nil, err
	}
	var sd yaml.Node
	if err := yaml.Unmarshal(b, &sd); err != nil {
		return nil, err
	}
	n := sd.Content[0]
	old := steps.Content[i]
	n.HeadComment, n.FootComment = old.HeadComment, old.FootComment
	steps.Content[i] = n

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	if _, err := Parse(buf.Bytes()); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
