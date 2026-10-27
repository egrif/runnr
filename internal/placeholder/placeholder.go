// Package placeholder fills in the folders runnr files name by placeholder
// (@NAME@): @REPOS_ROOT@, @REPO_ROOT@, @PROJECT_DIR@ and any others. Files
// are kept as written, placeholders and all, so they're the same on every
// machine; each machine gives the values (for everything, and for each
// collection), and a run gets them when it starts. A run doesn't start
// while one it uses has no value, or isn't a folder here.
package placeholder

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
)

// The placeholders runnr names. Others can be given too.
const (
	ReposRoot  = "REPOS_ROOT"  // where the repositories are (for everything)
	RepoRoot   = "REPO_ROOT"   // a collection's repository
	ProjectDir = "PROJECT_DIR" // a collection's project folder
)

// Re matches a placeholder; its first group is the name.
var Re = regexp.MustCompile(`@([A-Z][A-Z0-9_]*)@`)

// ValidName says whether name can be a placeholder's.
func ValidName(name string) bool { return Re.MatchString("@" + name + "@") }

// Tokens are the placeholders s names, sorted.
func Tokens(s string) []string {
	var out []string
	for _, m := range Re.FindAllStringSubmatch(s, -1) {
		if !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	slices.Sort(out)
	return out
}

// Fill replaces the placeholders in s that values has.
func Fill(s string, values map[string]string) string {
	if !strings.Contains(s, "@") {
		return s
	}
	return Re.ReplaceAllStringFunc(s, func(m string) string {
		if v, ok := values[m[1:len(m)-1]]; ok {
			return v
		}
		return m
	})
}

// Expand fills in values that name other placeholders (REPO_ROOT as
// @REPOS_ROOT@/ops-runbooks), and a leading ~ as the home folder.
// A value naming one that has none is left with it in; naming each other
// round in a circle is an error.
func Expand(values map[string]string) (map[string]string, error) {
	out := map[string]string{}
	var visit func(name string, seen []string) (string, error)
	visit = func(name string, seen []string) (string, error) {
		if v, ok := out[name]; ok {
			return v, nil
		}
		if slices.Contains(seen, name) {
			return "", fmt.Errorf("placeholders name each other in a circle: @%s@", strings.Join(append(seen, name), "@ → @"))
		}
		v := values[name]
		var err error
		v = Re.ReplaceAllStringFunc(v, func(m string) string {
			n := m[1 : len(m)-1]
			if _, ok := values[n]; !ok || err != nil {
				return m
			}
			var x string
			x, err = visit(n, append(seen, name))
			return x
		})
		if err != nil {
			return "", err
		}
		if v == "~" || strings.HasPrefix(v, "~/") {
			if home, herr := os.UserHomeDir(); herr == nil {
				v = home + v[1:]
			}
		}
		out[name] = v
		return v, nil
	}
	for name := range values {
		if _, err := visit(name, nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Used are the placeholders named anywhere in vs (runbooks, libraries,
// inputs: any value, walked through its fields, lists and maps), sorted.
func Used(vs ...any) []string {
	var out []string
	for _, v := range vs {
		walk(reflect.ValueOf(v), map[uintptr]bool{}, func(s string) string {
			for _, t := range Tokens(s) {
				if !slices.Contains(out, t) {
					out = append(out, t)
				}
			}
			return s
		})
	}
	slices.Sort(out)
	return out
}

// Apply fills in the placeholders values has everywhere in vs, in place
// (so each must be a pointer, map or slice of the caller's own).
func Apply(values map[string]string, vs ...any) {
	for _, v := range vs {
		walk(reflect.ValueOf(v), map[uintptr]bool{}, func(s string) string { return Fill(s, values) })
	}
}

// walk calls fn on every string v reaches, setting those it can to what
// fn returns.
func walk(v reflect.Value, seen map[uintptr]bool, fn func(string) string) {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() || seen[v.Pointer()] {
			return
		}
		seen[v.Pointer()] = true
		walk(v.Elem(), seen, fn)
	case reflect.Interface:
		if v.IsNil() {
			return
		}
		e := v.Elem()
		if e.Kind() == reflect.String && v.CanSet() {
			v.Set(reflect.ValueOf(fn(e.String())).Convert(e.Type()))
			return
		}
		walk(e, seen, fn)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				walk(v.Field(i), seen, fn)
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			walk(v.Index(i), seen, fn)
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			e := reflect.New(v.Type().Elem()).Elem()
			e.Set(v.MapIndex(k))
			walk(e, seen, fn)
			v.SetMapIndex(k, e)
		}
	case reflect.String:
		if s := v.String(); strings.Contains(s, "@") {
			if r := fn(s); v.CanSet() {
				v.SetString(r)
			}
		}
	}
}

// Check says what's wrong with the values of the placeholders used: one
// without a value, or whose value isn't a folder on this machine.
func Check(used []string, values map[string]string) []Problem {
	var out []Problem
	for _, n := range used {
		v, ok := values[n]
		switch {
		case !ok || strings.TrimSpace(v) == "":
			out = append(out, Problem{n, "", "has no value"})
		case Re.MatchString(v):
			out = append(out, Problem{n, v, "names " + strings.Join(wrap(Tokens(v)), ", ") + ", which has no value"})
		case !filepath.IsAbs(v):
			out = append(out, Problem{n, v, "isn't a full path"})
		default:
			if st, err := os.Stat(v); err != nil {
				out = append(out, Problem{n, v, "isn't a folder here"})
			} else if !st.IsDir() {
				out = append(out, Problem{n, v, "is a file, not a folder"})
			}
		}
	}
	return out
}

// Problem is a placeholder a run can't have as it is.
type Problem struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
	Why   string `json:"why"`
}

func (p Problem) String() string {
	if p.Value == "" {
		return "@" + p.Name + "@ " + p.Why
	}
	return fmt.Sprintf("@%s@ (%s) %s", p.Name, p.Value, p.Why)
}

func wrap(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = "@" + n + "@"
	}
	return out
}
