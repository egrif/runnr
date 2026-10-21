package runbook

import (
	"fmt"
	"regexp"
	"strings"
)

// zsh-only syntax that fails in sh and bash (and so always fails when the
// runbook's shell isn't zsh).
var zshOnly = []struct {
	re   *regexp.Regexp
	what string
}{
	{regexp.MustCompile(`(?m)(^|[\s;&|(])setopt\s`), "setopt"},
	{regexp.MustCompile(`\$\{\([A-Za-z@]+\)`), "${(flags)…} parameter flags"},
}

var (
	readCmd    = regexp.MustCompile(`(?m)(?:^|[\s;&|(])read((?:[ \t]+(?:"[^"\n]*"|'[^'\n]*'|[^\s;&|)]+))*)`)
	readWord   = regexp.MustCompile(`"[^"\n]*"|'[^'\n]*'|[^\s;&|)]+`)
	zshPromptV = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\?`)
)

// zshRead reports a read whose variable carries zsh's prompt
// ("var?prompt"); -p's argument (bash's prompt) doesn't count.
func zshRead(code string) bool {
	for _, m := range readCmd.FindAllStringSubmatch(code, -1) {
		words := readWord.FindAllString(m[1], -1)
		for k := 0; k < len(words); k++ {
			w := words[k]
			if strings.HasPrefix(w, "-") {
				if strings.HasSuffix(w, "p") || strings.HasSuffix(w, "t") || strings.HasSuffix(w, "d") || strings.HasSuffix(w, "n") || strings.HasSuffix(w, "u") {
					k++ // its argument
				}
				continue
			}
			if zshPromptV.MatchString(strings.Trim(w, `"'`)) {
				return true
			}
		}
	}
	return false
}

func isZsh(shell string) bool {
	return shell == "zsh" || strings.HasSuffix(shell, "/zsh")
}

// zshOnlyErrors finds zsh-only syntax in a runbook none of whose shells is
// zsh.
func (rb *Runbook) zshOnlyErrors() []error {
	if isZsh(rb.Shell) {
		return nil
	}
	for _, s := range rb.Steps {
		if isZsh(s.Shell) {
			return nil // can't tell which code runs where; leave it
		}
	}
	shell := rb.Shell
	if shell == "" {
		shell = "sh"
	}
	var errs []error
	check := func(where, code string) {
		if zshRead(code) {
			errs = append(errs, fmt.Errorf(`%s uses read "var?prompt" (zsh's prompt syntax; in sh and bash it's read -p "prompt" var, or use runnr_confirm / runnr_ask), which only zsh understands, but the runbook's shell is %s: add shell: zsh`, where, shell))
		}
		for _, z := range zshOnly {
			if z.re.MatchString(code) {
				errs = append(errs, fmt.Errorf("%s uses %s, which only zsh understands, but the runbook's shell is %s: add shell: zsh", where, z.what, shell))
			}
		}
	}
	for name, f := range rb.Functions {
		check("function "+name, f.Body)
	}
	for i, s := range rb.Steps {
		for _, w := range s.WayList() {
			check(fmt.Sprintf("steps[%d] (%s)", i, s.Name), w.Run)
			for _, c := range w.Checks {
				check(fmt.Sprintf("steps[%d] (%s)", i, s.Name), c.Run)
			}
		}
	}
	return errs
}
