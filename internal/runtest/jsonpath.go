package runtest

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// kubectlOutput prints v as kubectl's -o does: json (the default, for a
// fake's JSON), yaml, or jsonpath=TEMPLATE.
func kubectlOutput(v any, format string) (string, error) {
	switch {
	case format == "" || format == "json":
		b, err := json.MarshalIndent(v, "", "    ")
		return string(b) + "\n", err
	case format == "yaml":
		b, err := yaml.Marshal(v)
		return string(b), err
	case strings.HasPrefix(format, "jsonpath="):
		tmpl := strings.Trim(strings.TrimPrefix(format, "jsonpath="), `'"`)
		return jsonPath(v, tmpl)
	}
	return "", fmt.Errorf("-o %q isn't supported by fakes (json, yaml or jsonpath=…)", format)
}

// jsonPath fills in a kubectl JSONPath template: literal text and {…}
// expressions: paths (.a.b, [0], [*], ['key']), "string" literals, and
// {range PATH}…{end}. Several results are joined by spaces.
func jsonPath(root any, tmpl string) (string, error) {
	toks, err := jpTokens(tmpl)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if _, err := jpRun(&b, toks, 0, root, false); err != nil {
		return "", err
	}
	return b.String(), nil
}

type jpTok struct {
	lit  string // literal text (expr == "")
	expr string
}

func jpTokens(t string) ([]jpTok, error) {
	var out []jpTok
	for t != "" {
		i := strings.Index(t, "{")
		if i < 0 {
			out = append(out, jpTok{lit: t})
			break
		}
		if i > 0 {
			out = append(out, jpTok{lit: t[:i]})
		}
		j := strings.Index(t[i:], "}")
		if j < 0 {
			return nil, fmt.Errorf("jsonpath: unclosed { in %q", t)
		}
		out = append(out, jpTok{expr: strings.TrimSpace(t[i+1 : i+j])})
		t = t[i+j+1:]
	}
	return out, nil
}

// jpRun writes toks from i against cur until the end (or an {end}, when in
// a range) and returns where it stopped.
func jpRun(b *strings.Builder, toks []jpTok, i int, cur any, inRange bool) (int, error) {
	for i < len(toks) {
		t := toks[i]
		switch {
		case t.expr == "":
			b.WriteString(t.lit)
		case t.expr == "end":
			if !inRange {
				return 0, fmt.Errorf("jsonpath: {end} without {range}")
			}
			return i, nil
		case strings.HasPrefix(t.expr, "range "):
			items, err := jpEval(cur, strings.TrimSpace(strings.TrimPrefix(t.expr, "range ")))
			if err != nil {
				return 0, err
			}
			end := i
			if len(items) == 0 {
				// Find the matching {end} without writing anything.
				var skip strings.Builder
				if end, err = jpRun(&skip, toks, i+1, nil, true); err != nil {
					return 0, err
				}
			}
			for _, it := range items {
				if end, err = jpRun(b, toks, i+1, it, true); err != nil {
					return 0, err
				}
			}
			i = end
		case strings.HasPrefix(t.expr, `"`):
			s, err := strconv.Unquote(t.expr)
			if err != nil {
				return 0, fmt.Errorf("jsonpath: %s: %w", t.expr, err)
			}
			b.WriteString(s)
		default:
			vals, err := jpEval(cur, t.expr)
			if err != nil {
				return 0, err
			}
			for k, v := range vals {
				if k > 0 {
					b.WriteString(" ")
				}
				b.WriteString(jpString(v))
			}
		}
		i++
	}
	if inRange {
		return 0, fmt.Errorf("jsonpath: {range} without {end}")
	}
	return i, nil
}

// jpEval follows a path from cur: .name, ['name'], [n], [*] (and a leading
// $ or @).
func jpEval(cur any, path string) ([]any, error) {
	path = strings.TrimPrefix(strings.TrimPrefix(path, "$"), "@")
	vals := []any{cur}
	for path != "" {
		var next []any
		switch {
		case strings.HasPrefix(path, "."):
			path = path[1:]
			n := strings.IndexAny(path, ".[")
			if n < 0 {
				n = len(path)
			}
			key := path[:n]
			path = path[n:]
			for _, v := range vals {
				if m, ok := v.(map[string]any); ok {
					if x, ok := m[key]; ok {
						next = append(next, x)
					}
				}
			}
		case strings.HasPrefix(path, "["):
			n := strings.Index(path, "]")
			if n < 0 {
				return nil, fmt.Errorf("jsonpath: unclosed [ in %q", path)
			}
			sel := path[1:n]
			path = path[n+1:]
			for _, v := range vals {
				switch {
				case sel == "*":
					switch t := v.(type) {
					case []any:
						next = append(next, t...)
					case map[string]any:
						for _, k := range sortedKeys(t) {
							next = append(next, t[k])
						}
					}
				case strings.HasPrefix(sel, "'") || strings.HasPrefix(sel, `"`):
					if m, ok := v.(map[string]any); ok {
						if x, ok := m[strings.Trim(sel, `'"`)]; ok {
							next = append(next, x)
						}
					}
				default:
					idx, err := strconv.Atoi(sel)
					if err != nil {
						return nil, fmt.Errorf("jsonpath: [%s] isn't supported by fakes", sel)
					}
					if l, ok := v.([]any); ok {
						if idx < 0 {
							idx += len(l)
						}
						if idx >= 0 && idx < len(l) {
							next = append(next, l[idx])
						}
					}
				}
			}
		default:
			return nil, fmt.Errorf("jsonpath: can't read %q", path)
		}
		vals = next
	}
	return vals, nil
}

func jpString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	case bool, float64, int:
		return fmt.Sprint(t)
	}
	b, _ := json.Marshal(v)
	return string(b)
}
