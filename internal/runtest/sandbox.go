package runtest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/egrif/runnr/internal/runbook"
)

// Every test runs without the operator's credentials: a scrubbed
// environment and a scratch HOME, so even a call that got past the fakes
// would reach nothing it could act on. A strict suite also runs every step
// in an operating-system sandbox (macOS sandbox-exec): no network, no
// programs but the fakes and an allow-list of harmless tools, no writing
// outside the test's directory. A probe proves the sandbox holds before
// each scenario starts; if it doesn't, the scenario doesn't run.

// keptEnv are the variables a test's steps keep from runnr's environment.
var keptEnv = []string{"LANG", "LC_ALL", "LC_CTYPE", "LC_MESSAGES", "TERM", "TZ", "USER", "LOGNAME", "SHELL"}

// DefaultAllow are the commands a strict suite's steps may run, besides
// the fakes: text tools, nothing that reaches the network or the cloud.
var DefaultAllow = []string{
	"awk", "basename", "cat", "cmp", "column", "cut", "date", "diff", "dirname", "echo", "egrep", "env", "expr",
	"false", "fgrep", "find", "grep", "head", "id", "jq", "ls", "mkdir", "mktemp", "od", "paste", "printf",
	"readlink", "rm", "sed", "seq", "sleep", "sort", "tail", "tee", "test", "touch", "tr", "true", "uname",
	"uniq", "wc", "whoami", "xargs", "yes", "clear", "tput",
}

// cleanEnv is the environment a scenario's steps start from.
func cleanEnv(dir, path string, extra map[string]string) []string {
	var env []string
	for _, k := range keptEnv {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	env = append(env,
		"HOME="+filepath.Join(dir, "home"),
		"TMPDIR="+filepath.Join(dir, "tmp")+"/", "TMPPREFIX="+filepath.Join(dir, "tmp", "zsh"), // zsh here-documents
		"PATH="+path,
		// Nowhere to find credentials, even for a real CLI.
		"AWS_CONFIG_FILE=/dev/null", "AWS_SHARED_CREDENTIALS_FILE=/dev/null", "AWS_EC2_METADATA_DISABLED=true",
		"KUBECONFIG=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
	)
	for _, k := range sortedStrings(extra) {
		env = append(env, k+"="+extra[k])
	}
	return env
}

// allowDir links the allowed commands into dir, and returns their real
// paths (for the sandbox) and, for any it can't use, why.
func allowDir(dir string, names []string) (real []string, missing map[string]error, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, err
	}
	for _, n := range names {
		p, err := lookTool(n, os.Getenv("PATH"))
		if err != nil {
			if missing == nil {
				missing = map[string]error{}
			}
			missing[n] = err
			continue
		}
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			r = p
		}
		if err := os.Symlink(r, filepath.Join(dir, n)); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, nil, err
		}
		real = append(real, r)
		if r != p {
			real = append(real, p)
		}
	}
	return real, missing, nil
}

// lookTool finds the program the sandbox should allow for name: the first
// on path that isn't a version manager's shim (asdf, mise, rbenv, pyenv…:
// a script that runs the manager, which the sandbox won't run), else what
// the shim stands for (the manager's "which").
func lookTool(name, path string) (string, error) {
	var shims []string
	for _, d := range filepath.SplitList(path) {
		if d == "" {
			continue
		}
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
			continue
		}
		if isShimDir(d) {
			shims = append(shims, p)
			continue
		}
		return p, nil
	}
	if len(shims) == 0 {
		return "", fmt.Errorf("%s isn't on PATH", name)
	}
	p, err := shimTarget(shims[0], name)
	if err != nil {
		return "", fmt.Errorf("%s is only a version manager's shim (%s), which can't run in the sandbox, and what it stands for wasn't found: %w", name, shims[0], err)
	}
	return p, nil
}

// isShimDir says whether d holds a version manager's shims
// (~/.asdf/shims, ~/.local/share/mise/shims, ~/.rbenv/shims…).
func isShimDir(d string) bool {
	return filepath.Base(filepath.Clean(d)) == "shims"
}

// shimTarget asks the version manager that owns shim (the directory above
// its shims, e.g. ~/.asdf → asdf) which program it runs for name.
func shimTarget(shim, name string) (string, error) {
	mgr := strings.TrimPrefix(filepath.Base(filepath.Dir(filepath.Dir(shim))), ".")
	exe, err := exec.LookPath(mgr)
	if err != nil {
		return "", fmt.Errorf("its manager (%s) isn't on PATH", mgr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, "which", name).Output()
	if err != nil {
		return "", fmt.Errorf("%s which %s: %w", mgr, name, err)
	}
	p := strings.TrimSpace(string(out))
	if fi, err := os.Stat(p); !filepath.IsAbs(p) || err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 || isShimDir(filepath.Dir(p)) {
		return "", fmt.Errorf("%s which %s: %q isn't a program", mgr, name, p)
	}
	return p, nil
}

// sandbox is a macOS sandbox-exec profile for one scenario.
type sandbox struct {
	profile string // path of the .sb file
	exe     string // sandbox-exec
}

// SandboxAvailable says whether strict tests can run here.
func SandboxAvailable() error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("strict tests need macOS's sandbox-exec (this is %s)", runtime.GOOS)
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		return errors.New("strict tests need sandbox-exec, which isn't on PATH")
	}
	return nil
}

// newSandbox writes a profile that denies the network, programs not in
// execs (plus the test directory's shims and links), and writes outside dir.
func newSandbox(dir string, execs []string) (*sandbox, error) {
	if err := SandboxAvailable(); err != nil {
		return nil, err
	}
	exe, _ := exec.LookPath("sandbox-exec")
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	q := func(s string) string {
		return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
	}
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n")
	b.WriteString("; no network at all\n(deny network*)\n")
	b.WriteString("; only these programs\n(deny process-exec)\n(allow process-exec\n")
	fmt.Fprintf(&b, "  (subpath %s)\n  (subpath %s)\n", q(dir), q(realDir))
	seen := map[string]bool{}
	for _, p := range execs {
		if p != "" && !seen[p] {
			seen[p] = true
			fmt.Fprintf(&b, "  (literal %s)\n", q(p))
		}
	}
	b.WriteString(")\n; no writing outside the test's directory\n(deny file-write*)\n(allow file-write*\n")
	fmt.Fprintf(&b, "  (subpath %s)\n  (subpath %s)\n", q(dir), q(realDir))
	b.WriteString("  (literal \"/dev/null\") (literal \"/dev/zero\") (literal \"/dev/dtracehelper\")\n  (regex #\"^/dev/tty\") (regex #\"^/dev/fd/\"))\n")
	path := filepath.Join(dir, "sandbox.sb")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return nil, err
	}
	return &sandbox{profile: path, exe: exe}, nil
}

// wrap runs a command in the sandbox.
func (s *sandbox) wrap(name string, args []string) (string, []string) {
	if !strings.Contains(name, "/") {
		if p, err := exec.LookPath(name); err == nil {
			name = p
		}
	}
	return s.exe, append([]string{"-f", s.profile, name}, args...)
}

// probe proves the sandbox holds, from inside it (runnr __probe): it must
// not reach the network, run a program it wasn't given, or write outside
// the test's directory.
func (s *sandbox) probe(exe, dir string, env []string) error {
	cmd := exec.Command(s.exe, "-f", s.profile, exe, "__probe", dir)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("the sandbox isn't holding, so the scenario wasn't run: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// Probe is runnr __probe DIR, run inside a scenario's sandbox: it tries
// what the sandbox must stop and exits 0 only if all of it was stopped.
func Probe(dir string) int {
	var leaks []string
	if c, err := net.DialTimeout("tcp", "1.1.1.1:443", 2*time.Second); err == nil {
		c.Close()
		leaks = append(leaks, "reached the network (1.1.1.1:443)")
	}
	outside := filepath.Join(filepath.Dir(filepath.Clean(dir)), fmt.Sprintf("runnr-probe-%d", os.Getpid()))
	if err := os.WriteFile(outside, []byte("x"), 0o600); err == nil {
		os.Remove(outside)
		leaks = append(leaks, "wrote outside the test's directory ("+outside+")")
	}
	for _, p := range []string{"/usr/bin/curl", "/usr/bin/ssh", "/usr/bin/python3"} {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if err := exec.Command(p, "--version").Run(); err == nil {
			leaks = append(leaks, "ran a program it wasn't given ("+p+")")
		}
		break
	}
	if len(leaks) > 0 {
		fmt.Fprintln(os.Stderr, strings.Join(leaks, "; "))
		return 1
	}
	return 0
}

// escapes finds code that could get past the fakes: isolated commands run
// by path, a PATH reset, env -i, command -p. (A strict suite's sandbox
// stops these anyway; this says why a call would be refused.)
func escapes(rb *runbook.Runbook, libs []*runbook.Library, isolate []string) []string {
	if len(isolate) == 0 {
		return nil
	}
	names := make([]string, len(isolate))
	for i, n := range isolate {
		names[i] = regexp.QuoteMeta(n)
	}
	byPath := regexp.MustCompile(`(?:^|[\s;|&(` + "`" + `])/[\w./-]*/(` + strings.Join(names, "|") + `)\b`)
	others := []struct {
		re  *regexp.Regexp
		why string
	}{
		{regexp.MustCompile(`(?:^|[\s;|&(])(?:export\s+)?PATH=`), "sets PATH"},
		{regexp.MustCompile(`\benv\s+-i\b`), "runs env -i (a fresh environment)"},
		{regexp.MustCompile(`\bcommand\s+-p\b`), "runs command -p (the default PATH)"},
	}
	var out []string
	check := func(where, code string) {
		for _, m := range byPath.FindAllStringSubmatch(code, -1) {
			out = append(out, fmt.Sprintf("%s runs %s by path (%s), which the fakes can't catch", where, m[1], strings.TrimSpace(m[0])))
		}
		for _, o := range others {
			if o.re.MatchString(code) {
				out = append(out, where+" "+o.why+", which can get past the fakes")
			}
		}
	}
	for i, st := range rb.Steps {
		where := fmt.Sprintf("step %d (%s)", i+1, st.Name)
		for _, w := range st.WayList() {
			check(where, w.Run)
			for _, c := range w.Checks {
				check(where, c.Run)
			}
		}
	}
	for n, f := range rb.Functions {
		check("function "+n, f.Body)
	}
	for _, l := range libs {
		for n, f := range l.Functions {
			check("function "+n+" ("+l.Name+")", f.Body)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func sortedStrings(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
