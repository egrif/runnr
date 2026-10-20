package runtest

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// CheckAWS compares each AWS CLI fake's JSON with the response the CLI
// itself describes (aws SERVICE OPERATION --generate-cli-skeleton output,
// which needs no credentials), and reports fields the real response
// doesn't have. Other fakes, and ones without JSON, aren't checked. aws is
// the real AWS CLI ("aws" on PATH when empty).
func CheckAWS(fakes []Fake, aws string) []string {
	if aws == "" {
		aws = "aws"
	}
	skeletons := map[string]any{}
	var probs []string
	for i, f := range fakes {
		w := strings.Fields(f.Command)
		if len(w) < 3 || w[0] != "aws" {
			continue
		}
		resps := append([]Response{f.Response}, f.Sequence...)
		op := w[1] + " " + w[2]
		for k, r := range resps {
			if r.JSON == nil {
				continue
			}
			sk, ok := skeletons[op]
			if !ok {
				out, err := exec.Command(aws, w[1], w[2], "--generate-cli-skeleton", "output").Output()
				if err != nil {
					probs = append(probs, fmt.Sprintf("fakes[%d] (%s): can't get the CLI's response shape: %v", i, f.Command, err))
					skeletons[op] = nil
					continue
				}
				if err := json.Unmarshal(out, &sk); err != nil {
					probs = append(probs, fmt.Sprintf("fakes[%d] (%s): the CLI's response shape: %v", i, f.Command, err))
				}
				skeletons[op] = sk
			}
			if sk == nil {
				continue
			}
			v, _ := renderJSON(r.JSON, nil)
			where := fmt.Sprintf("fakes[%d] (%s)", i, f.Command)
			if k > 0 {
				where += fmt.Sprintf(" sequence[%d]", k-1)
			}
			for _, p := range shapeDiff(v, sk, "") {
				probs = append(probs, where+": "+p)
			}
		}
	}
	return probs
}

// shapeDiff lists fields of v that aren't in the shape (a skeleton:
// objects with every member, lists with one example element).
func shapeDiff(v, shape any, at string) []string {
	var out []string
	switch t := v.(type) {
	case map[string]any:
		sm, ok := shape.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s is an object; the response has %s there", name(at), kind(shape))}
		}
		for _, k := range sortedKeys(t) {
			sub, ok := sm[k]
			if !ok {
				out = append(out, fmt.Sprintf("%s isn't in the response (did you mean %s?)", name(join(at, k)), closest(k, sm)))
				continue
			}
			out = append(out, shapeDiff(t[k], sub, join(at, k))...)
		}
	case []any:
		sl, ok := shape.([]any)
		if !ok {
			return []string{fmt.Sprintf("%s is a list; the response has %s there", name(at), kind(shape))}
		}
		if len(sl) == 0 {
			return nil
		}
		for i, x := range t {
			out = append(out, shapeDiff(x, sl[0], fmt.Sprintf("%s[%d]", at, i))...)
		}
	}
	return out
}

func join(at, k string) string {
	if at == "" {
		return k
	}
	return at + "." + k
}

func name(at string) string {
	if at == "" {
		return "the response"
	}
	return at
}

func kind(v any) string {
	switch v.(type) {
	case map[string]any:
		return "an object"
	case []any:
		return "a list"
	}
	return "a value"
}

// closest is the response's field nearest to k (case and edits).
func closest(k string, m map[string]any) string {
	best, bd := "", 1<<30
	for c := range m {
		if strings.EqualFold(c, k) {
			return c
		}
		if d := editDistance(strings.ToLower(k), strings.ToLower(c)); d < bd {
			best, bd = c, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			c := 1
			if a[i-1] == b[j-1] {
				c = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+c)
		}
		prev = cur
	}
	return prev[len(b)]
}
