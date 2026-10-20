package runtest

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// Sections splits a suite's YAML into its setup (everything but the
// scenarios) and each scenario's own text, as written, comments and all.
func Sections(raw []byte) (setup string, scenarios map[string]string, err error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return "", nil, err
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return string(raw), nil, nil
	}
	top := doc.Content[0].Content
	scenarios = map[string]string{}
	for i := 0; i+1 < len(top); i += 2 {
		if top[i].Value != "scenarios" || top[i+1].Kind != yaml.SequenceNode {
			continue
		}
		// The scenarios run from their key to the next top-level key.
		from, to := top[i].Line-1, len(lines)
		if i+2 < len(top) {
			to = top[i+2].Line - 1
		}
		items := top[i+1].Content
		// Each item starts at the comment lines just above it.
		starts := make([]int, len(items))
		for j, it := range items {
			starts[j] = it.Line - 1
			for starts[j] > from+1 && strings.HasPrefix(strings.TrimSpace(lines[starts[j]-1]), "#") {
				starts[j]--
			}
		}
		for j, it := range items {
			end := to
			if j+1 < len(items) {
				end = starts[j+1]
			}
			name := ""
			for k := 0; k+1 < len(it.Content); k += 2 {
				if it.Content[k].Value == "name" {
					name = it.Content[k+1].Value
				}
			}
			scenarios[name] = dedent(trimTail(lines[starts[j]:end]), it.Column-1)
		}
		setup = strings.Join(trimTail(append(append([]string{}, lines[:from]...), lines[to:]...)), "\n")
		return setup, scenarios, nil
	}
	return string(raw), scenarios, nil
}

// trimTail drops trailing blank and comment-only lines (they introduce
// what comes next).
func trimTail(ls []string) []string {
	for len(ls) > 0 {
		t := strings.TrimSpace(ls[len(ls)-1])
		if t != "" && !strings.HasPrefix(t, "#") {
			break
		}
		ls = ls[:len(ls)-1]
	}
	return ls
}

// dedent removes a sequence item's indent (its content starts at column
// col) and its "- ".
func dedent(ls []string, col int) string {
	out := make([]string, len(ls))
	dash := false
	for i, l := range ls {
		t := strings.TrimLeft(l, " ")
		switch {
		case !dash && strings.HasPrefix(t, "- ") && len(l)-len(t)+2 == col:
			out[i], dash = t[2:], true
		case strings.HasPrefix(t, "#") && len(l)-len(t) < col:
			out[i] = t
		case len(l) >= col && strings.TrimSpace(l[:col]) == "":
			out[i] = l[col:]
		default:
			out[i] = strings.TrimLeft(l, " ")
		}
	}
	return strings.Join(out, "\n")
}
