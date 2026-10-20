package runbook

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Requirement is a program a runbook or library needs: on PATH, and (if
// Version is set) at least that version.
type Requirement struct {
	Cmd string `yaml:"cmd" json:"cmd"`
	// Version is the lowest version that will do (e.g. "1.5", ">= 1.5").
	Version string `yaml:"version,omitempty" json:"version,omitempty"`
	// Check prints the version (default: CMD --version).
	Check string `yaml:"check,omitempty" json:"check,omitempty"`
	// Install says how to get it ("brew install terraform").
	Install string `yaml:"install,omitempty" json:"install,omitempty"`
	// Optional requirements only warn.
	Optional bool `yaml:"optional,omitempty" json:"optional,omitempty"`
	// Why says what needs it.
	Why string `yaml:"why,omitempty" json:"why,omitempty"`
}

// UnmarshalYAML takes a bare name ("kubectl") as well as the full form.
func (r *Requirement) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		r.Cmd = n.Value
		return nil
	}
	type plain Requirement
	return n.Decode((*plain)(r))
}

func (r Requirement) validate() error {
	if r.Cmd == "" {
		return fmt.Errorf("requires: cmd is required")
	}
	if r.Version != "" {
		if _, err := parseVersion(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(r.Version), ">="))); err != nil {
			return fmt.Errorf("requires %s: version %q isn't like 1.5 or >= 1.5", r.Cmd, r.Version)
		}
	}
	return nil
}

// RequirementResult is how one requirement stands on this machine.
type RequirementResult struct {
	Requirement
	Path  string `json:"path,omitempty"`
	Found string `json:"found,omitempty"` // the version found, if asked
	// Problem is why it isn't met ("" when it is).
	Problem string `json:"problem,omitempty"`
}

var versionRe = regexp.MustCompile(`\d+(?:\.\d+)+|\d+`)

func parseVersion(s string) ([]int, error) {
	m := versionRe.FindString(s)
	if m == "" {
		return nil, fmt.Errorf("no version in %q", s)
	}
	var v []int
	for _, p := range strings.Split(m, ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, err
		}
		v = append(v, n)
	}
	return v, nil
}

func versionAtLeast(have, want []int) bool {
	for i := range max(len(have), len(want)) {
		h, w := 0, 0
		if i < len(have) {
			h = have[i]
		}
		if i < len(want) {
			w = want[i]
		}
		if h != w {
			return h > w
		}
	}
	return true
}

// CheckRequirements sees whether each requirement is met here.
func CheckRequirements(reqs []Requirement) []RequirementResult {
	var out []RequirementResult
	for _, r := range reqs {
		res := RequirementResult{Requirement: r}
		p, err := exec.LookPath(r.Cmd)
		if err != nil {
			res.Problem = r.Cmd + " isn't installed (not on PATH)"
			out = append(out, res)
			continue
		}
		res.Path = p
		if r.Version != "" {
			want, _ := parseVersion(strings.TrimPrefix(strings.TrimSpace(r.Version), ">="))
			check := r.Check
			if check == "" {
				check = r.Cmd + " --version"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			b, _ := exec.CommandContext(ctx, "sh", "-c", check).CombinedOutput()
			cancel()
			have, err := parseVersion(string(b))
			switch {
			case err != nil:
				res.Problem = fmt.Sprintf("couldn't tell %s's version from %q", r.Cmd, strings.TrimSpace(firstLine(strings.TrimSpace(string(b)))))
			case !versionAtLeast(have, want):
				res.Found = joinVersion(have)
				res.Problem = fmt.Sprintf("%s is %s; %s or later is needed", r.Cmd, res.Found, joinVersion(want))
			default:
				res.Found = joinVersion(have)
			}
		}
		out = append(out, res)
	}
	return out
}

// Requirements are a runbook's and its libraries' together, each program
// once (the strictest version wins; required beats optional).
func Requirements(rb *Runbook, libs []*Library) []Requirement {
	var all []Requirement
	all = append(all, rb.Requires...)
	for _, l := range libs {
		for _, r := range l.Requires {
			if r.Why == "" {
				r.Why = "library " + l.Name
			}
			all = append(all, r)
		}
	}
	var out []Requirement
	idx := map[string]int{}
	for _, r := range all {
		j, seen := idx[r.Cmd]
		if !seen {
			idx[r.Cmd] = len(out)
			out = append(out, r)
			continue
		}
		o := &out[j]
		if !r.Optional {
			o.Optional = false
		}
		if r.Version != "" {
			nv, _ := parseVersion(r.Version)
			ov, err := parseVersion(o.Version)
			if o.Version == "" || err != nil || !versionAtLeast(ov, nv) {
				o.Version, o.Check = r.Version, r.Check
			}
		}
		if o.Install == "" {
			o.Install = r.Install
		}
	}
	return out
}

func joinVersion(v []int) string {
	s := make([]string, len(v))
	for i, n := range v {
		s[i] = strconv.Itoa(n)
	}
	return strings.Join(s, ".")
}
