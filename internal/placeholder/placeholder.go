// Package placeholder fills in the @NAME@ placeholders runnr files can
// hold, and works out their values for a file on disk.
//
// Three name folders, taken from where the file is:
//
//	@FILE_DIR@     the file's folder
//	@REPO_ROOT@    the root of the git repository it's in
//	@PROJECT_DIR@  the folder directly under the nearest folder named
//	               "projects" above it (none if there isn't one)
//
// Any of them can go up folders: @PROJECT_DIR|-1@ is its parent. Other
// names take the values given (--set NAME=VALUE), which also override the
// three.
package placeholder

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Re matches a placeholder: its name, and how many folders up (|-N).
var Re = regexp.MustCompile(`@([A-Z][A-Z0-9_]*)(?:\|-([0-9]+))?@`)

// Folder placeholders: values come from where the file is.
const (
	FileDir    = "FILE_DIR"
	RepoRoot   = "REPO_ROOT"
	ProjectDir = "PROJECT_DIR"
)

// Tokens are the placeholders in raw, as written between the @s
// (PROJECT_DIR, FILE_DIR|-2), sorted.
func Tokens(raw []byte) []string {
	seen := map[string]bool{}
	for _, m := range Re.FindAllSubmatch(raw, -1) {
		seen[string(m[0][1:len(m[0])-1])] = true
	}
	return sorted(seen)
}

// Fill replaces the placeholders values has (by token), returning the
// tokens used and those without a value (left as they are).
func Fill(raw []byte, values map[string]string) (out []byte, used, missing []string) {
	u, miss := map[string]bool{}, map[string]bool{}
	out = Re.ReplaceAllFunc(raw, func(m []byte) []byte {
		tok := string(m[1 : len(m)-1])
		u[tok] = true
		if v, ok := values[tok]; ok {
			return []byte(v)
		}
		miss[tok] = true
		return m
	})
	return out, sorted(u), sorted(miss)
}

// Resolve works out the values of tokens for a file at path ("" for text
// not from a file: only set has values). set wins over the folder names.
// A token whose name has no value is missing; one that goes up past the
// top of the filesystem is an error.
func Resolve(path string, set map[string]string, tokens []string) (map[string]string, []string, error) {
	base := map[string]string{}
	if path != "" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, nil, err
		}
		dir := filepath.Dir(abs)
		base[FileDir] = dir
		if r := FindRepoRoot(dir); r != "" {
			base[RepoRoot] = r
		}
		if p := FindProjectDir(dir); p != "" {
			base[ProjectDir] = p
		}
	}
	for k, v := range set {
		base[k] = v
	}
	values := map[string]string{}
	var missing []string
	var errs []error
	for _, tok := range tokens {
		name, up, _ := strings.Cut(tok, "|-")
		v, ok := base[name]
		if !ok {
			missing = append(missing, tok)
			continue
		}
		n, _ := strconv.Atoi(up)
		for i := 0; i < n; i++ {
			if p := filepath.Dir(v); p != v && v != "" {
				v = p
			} else {
				errs = append(errs, fmt.Errorf("@%s@ goes up %d folders from %s: there aren't that many", tok, n, base[name]))
				break
			}
		}
		values[tok] = v
	}
	return values, missing, errors.Join(errs...)
}

// FindRepoRoot is the root of the git repository dir is in, or "".
func FindRepoRoot(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}

// FindProjectDir is the folder directly under the nearest folder named
// "projects" at or above dir, or "".
func FindProjectDir(dir string) string {
	for d := dir; filepath.Dir(d) != d; d = filepath.Dir(d) {
		if filepath.Base(filepath.Dir(d)) == "projects" {
			return d
		}
	}
	return ""
}

// Remote is the URL of the repository's origin remote, or "".
func Remote(repoRoot string) string {
	if repoRoot == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", repoRoot, "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func sorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}
