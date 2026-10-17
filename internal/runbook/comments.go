package runbook

import (
	"bytes"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Comments maps where comments sit in a runbook's YAML to their text
// (without the leading #). Paths name fields the way the structured editor
// does: "description", "steps.3", "steps.3.run", "steps.3.ways.1.run",
// "sections.0.expect", and "" for the comment at the top of the file.
type Comments map[string]string

// ExtractComments reads the comments in a runbook's YAML, each attached to
// the field (or list item) it sits on or beside.
func ExtractComments(raw []byte) (Comments, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	c := Comments{}
	if doc.Kind == 0 {
		return c, nil
	}
	c.add("", doc.HeadComment, doc.FootComment)
	if len(doc.Content) > 0 {
		root := doc.Content[0]
		c.add("", root.HeadComment)
		c.walk("", root)
		c.add("", root.FootComment)
		// A comment above the first field is the file's.
		if root.Kind == yaml.MappingNode && len(root.Content) > 0 {
			first := root.Content[0].Value
			if t, ok := c[first]; ok && root.Content[0].HeadComment != "" {
				c.add("", "# "+strings.ReplaceAll(t, "\n", "\n# "))
				delete(c, first)
			}
		}
	}
	return c, nil
}

func (c Comments) walk(path string, n *yaml.Node) {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			p := join(path, k.Value)
			c.add(p, k.HeadComment, k.LineComment, v.LineComment, k.FootComment)
			if v.Kind == yaml.ScalarNode {
				c.add(p, v.HeadComment, v.FootComment)
			}
			c.walk(p, v)
		}
	case yaml.SequenceNode:
		for i, item := range n.Content {
			p := join(path, strconv.Itoa(i))
			c.add(p, item.HeadComment, item.LineComment, item.FootComment)
			c.walk(p, item)
		}
	}
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// add appends comment blocks (raw, with #s) to the comment at path.
func (c Comments) add(path string, blocks ...string) {
	for _, b := range blocks {
		if t := uncomment(b); t != "" {
			if c[path] != "" {
				c[path] += "\n"
			}
			c[path] += t
		}
	}
}

func uncomment(b string) string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(b), "\n") {
		l = strings.TrimSpace(l)
		l = strings.TrimPrefix(l, "#")
		lines = append(lines, strings.TrimPrefix(l, " "))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func commented(t string) string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(t), "\n") {
		lines = append(lines, strings.TrimRight("# "+l, " "))
	}
	return strings.Join(lines, "\n")
}

// FormatWithComments is Format, with comments put back above the fields
// (or list items) they belong to. A comment whose field isn't in the
// formatted YAML (say, a section a step takes from the step above) goes on
// the nearest thing that is.
func FormatWithComments(rb *Runbook, c Comments) ([]byte, error) {
	raw, err := Format(rb)
	if err != nil || len(c) == 0 {
		return raw, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	for path, text := range c {
		if strings.TrimSpace(text) == "" {
			continue
		}
		if path == "" {
			doc.HeadComment = commented(text)
			continue
		}
		n := attachPoint(doc.Content[0], strings.Split(path, "."))
		if n.HeadComment != "" {
			n.HeadComment += "\n"
		}
		n.HeadComment += commented(text)
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

// attachPoint finds the node a comment at path goes on: a mapping key, a
// list item, or the nearest of those that exists.
func attachPoint(n *yaml.Node, path []string) *yaml.Node {
	if len(path) == 0 {
		return n
	}
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == path[0] {
				if len(path) == 1 {
					return n.Content[i]
				}
				return attachPoint(n.Content[i+1], path[1:])
			}
		}
		// Not there: the first key of this mapping stands in.
		if len(n.Content) > 0 {
			return n.Content[0]
		}
	case yaml.SequenceNode:
		if i, err := strconv.Atoi(path[0]); err == nil && i < len(n.Content) {
			if len(path) == 1 {
				return n.Content[i]
			}
			return attachPoint(n.Content[i], path[1:])
		}
	}
	return n
}
