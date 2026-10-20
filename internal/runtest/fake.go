package runtest

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"text/template"
	"time"

	"github.com/jmespath/go-jmespath"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

// The environment a faked call sees (set by the runner on every step).
const (
	envDir = "RUNNR_TEST_DIR" // the scenario's directory: fakes.json, calls.jsonl
	envExe = "RUNNR_TEST_EXE" // runnr itself, which the shims call back into
)

// plan is what a scenario's faked calls are answered from (fakes.json).
type plan struct {
	Fakes   []sourced `json:"fakes"`
	Isolate []string  `json:"isolate"`
	// Shims is the directory of shims on PATH, left out when looking for
	// the real command.
	Shims string `json:"shims"`
	// Files are the commands faked at paths (they have no real command).
	Files []string `json:"files"`
}

type sourced struct {
	Fake
	From string `json:"from"` // where the fake comes from, for the log
}

// Call is one call to a faked or isolated command, as logged.
type Call struct {
	Time time.Time `json:"time"`
	Step int       `json:"step,omitempty"` // 1-based; 0 outside a step
	Argv []string  `json:"argv"`
	// Fake is the fake that answered (index in the plan, -1: none) and From
	// where it came from.
	Fake int    `json:"fake"`
	From string `json:"from,omitempty"`
	Exit int    `json:"exit"`
	// Real: it ran the real command (no fake matched, not isolated);
	// Unfaked: an isolated command with no fake, refused.
	Real    bool   `json:"real,omitempty"`
	Unfaked bool   `json:"unfaked,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Line is the call as a command line.
func (c Call) Line() string { return strings.Join(c.Argv, " ") }

// Dispatch answers a call to a faked command (runnr __fake NAME ARGS…).
// It returns the exit status.
func Dispatch(name string, args []string) int {
	dir := os.Getenv(envDir)
	if dir == "" {
		fmt.Fprintln(os.Stderr, "runnr __fake: not in a test")
		return 96
	}
	var p plan
	if b, err := os.ReadFile(filepath.Join(dir, "fakes.json")); err != nil || json.Unmarshal(b, &p) != nil {
		fmt.Fprintln(os.Stderr, "runnr __fake: can't read the test's fakes:", err)
		return 96
	}
	argv := append([]string{name}, args...)
	call := Call{Time: time.Now(), Argv: argv, Fake: -1}
	call.Step, _ = strconv.Atoi(os.Getenv("RUNNR_STEP"))
	vars := environ()

	var stdin string
	if needsStdin(p.Fakes, name) && !term.IsTerminal(int(os.Stdin.Fd())) {
		b, _ := io.ReadAll(os.Stdin)
		stdin = string(b)
	}
	i := best(p.Fakes, argv, stdin, vars)
	if i < 0 {
		isolated := contains(p.Isolate, name) || contains(p.Files, name)
		if isolated {
			call.Unfaked, call.Exit = true, 97
			logCall(dir, call)
			fmt.Fprintf(os.Stderr, "runnr test: no fake answers this call to %s (isolated): %s\n", name, call.Line())
			return 97
		}
		call.Real = true
		code := runReal(p.Shims, argv, stdin)
		call.Exit = code
		logCall(dir, call)
		return code
	}
	f := p.Fakes[i]
	call.Fake, call.From = i, f.From
	r := f.Response
	if len(f.Sequence) > 0 {
		n := nextCount(dir, i)
		r = f.Sequence[min(n, len(f.Sequence)-1)]
	}
	stdout, stderr, err := r.render(argv, vars)
	if err != nil {
		call.Exit, call.Error = 96, err.Error()
		logCall(dir, call)
		fmt.Fprintf(os.Stderr, "runnr test: fake for %q: %v\n", f.Command, err)
		return 96
	}
	if d, err := time.ParseDuration(r.Delay); err == nil && d > 0 {
		time.Sleep(d)
	}
	io.WriteString(os.Stdout, stdout)
	io.WriteString(os.Stderr, stderr)
	call.Exit = r.Exit
	logCall(dir, call)
	return r.Exit
}

// best is the most specific fake matching the call (-1: none); ties go to
// the one listed first (scenario, then suite, then libraries).
func best(fakes []sourced, argv []string, stdin string, vars map[string]string) int {
	bi, bs := -1, -1
	for i, f := range fakes {
		s := f.score(argv, stdin, vars)
		if s > bs {
			bi, bs = i, s
		}
	}
	return bi
}

// score is how specifically f matches the call, or -1 if it doesn't.
func (f Fake) score(argv []string, stdin string, vars map[string]string) int {
	words := strings.Fields(f.Command)
	if len(words) == 0 || words[0] != argv[0] || !hasWords(argv[1:], words[1:]) {
		return -1
	}
	s := len(words) * 10
	for flag, want := range f.Args {
		v, ok := flagValue(argv[1:], flag)
		if !ok {
			return -1
		}
		want, err := renderText(want, vars)
		if err != nil {
			return -1
		}
		if m, _ := path.Match(want, v); !m {
			return -1
		}
		s += 5
	}
	if f.Match != "" {
		re, err := regexp.Compile(f.Match)
		if err != nil || !re.MatchString(strings.Join(argv, " ")) {
			return -1
		}
		s += 3
	}
	if f.Stdin != "" {
		if !strings.Contains(stdin, f.Stdin) {
			return -1
		}
		s += 3
	}
	return s
}

// hasWords reports whether words appear in args, in order.
func hasWords(args, words []string) bool {
	j := 0
	for _, a := range args {
		if j < len(words) && a == words[j] {
			j++
		}
	}
	return j == len(words)
}

// flagValue finds --flag value or --flag=value.
func flagValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
		if v, ok := strings.CutPrefix(a, flag+"="); ok {
			return v, true
		}
	}
	return "", false
}

func needsStdin(fakes []sourced, name string) bool {
	for _, f := range fakes {
		if f.Stdin != "" && strings.HasPrefix(f.Command, name) {
			return true
		}
	}
	return false
}

// render produces the response's output. A JSON response is filled in,
// then the call's --query and --output are applied (as the AWS CLI does).
func (r Response) render(argv []string, vars map[string]string) (string, string, error) {
	stdout, err := renderText(r.Stdout, vars)
	if err != nil {
		return "", "", err
	}
	stderr, err := renderText(r.Stderr, vars)
	if err != nil {
		return "", "", err
	}
	if r.JSON != nil {
		v, err := renderJSON(r.JSON, vars)
		if err != nil {
			return "", "", err
		}
		var s string
		switch argv[0] {
		case "aws":
			// The AWS CLI's --query (JMESPath) and --output.
			if q, ok := flagValue(argv[1:], "--query"); ok {
				if v, err = jmespath.Search(q, v); err != nil {
					return "", "", fmt.Errorf("--query %q: %w", q, err)
				}
			}
			out, _ := flagValue(argv[1:], "--output")
			if out == "" {
				out = os.Getenv("AWS_DEFAULT_OUTPUT")
			}
			s, err = formatOutput(v, out)
		case "kubectl":
			// kubectl's -o json|yaml|jsonpath=….
			out, ok := flagValue(argv[1:], "-o")
			if !ok {
				out, _ = flagValue(argv[1:], "--output")
			}
			s, err = kubectlOutput(v, out)
		default:
			s, err = formatOutput(v, "json")
		}
		if err != nil {
			return "", "", err
		}
		stdout += s
	}
	return stdout, stderr, nil
}

func renderText(text string, vars map[string]string) (string, error) {
	if !strings.Contains(text, "{{") || vars == nil {
		return text, nil
	}
	t, err := template.New("fake").Option("missingkey=error").Parse(text)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := t.Execute(&b, vars); err != nil {
		return "", err
	}
	return b.String(), nil
}

// renderJSON fills in templates in every string of a JSON value, and turns
// YAML's maps into JSON's.
func renderJSON(v any, vars map[string]string) (any, error) {
	switch t := v.(type) {
	case string:
		return renderText(t, vars)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			r, err := renderJSON(x, vars)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			r, err := renderJSON(x, vars)
			if err != nil {
				return nil, err
			}
			out[fmt.Sprint(k)] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			r, err := renderJSON(x, vars)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	}
	return v, nil
}

// formatOutput prints a value as the AWS CLI's --output does: json (the
// default), text or yaml.
func formatOutput(v any, format string) (string, error) {
	switch format {
	case "", "json":
		b, err := json.MarshalIndent(v, "", "    ")
		return string(b) + "\n", err
	case "yaml":
		b, err := yaml.Marshal(v)
		return string(b), err
	case "text":
		var b strings.Builder
		awsText(&b, v)
		return b.String(), nil
	}
	return "", fmt.Errorf("--output %q isn't supported by fakes (json, text or yaml)", format)
}

// awsText writes v as the AWS CLI's text output: a scalar or a list of
// scalars on one tab-separated line; a list of lists or objects, one line
// each; an object's scalar values in key order, its nested values after.
func awsText(b *strings.Builder, v any) {
	switch t := v.(type) {
	case []any:
		if allScalar(t) {
			b.WriteString(joinScalars(t) + "\n")
			return
		}
		for _, x := range t {
			awsText(b, x)
		}
	case map[string]any:
		keys := sortedKeys(t)
		var line []any
		for _, k := range keys {
			if isScalar(t[k]) {
				line = append(line, t[k])
			}
		}
		if len(line) > 0 {
			b.WriteString(joinScalars(line) + "\n")
		}
		for _, k := range keys {
			if !isScalar(t[k]) {
				awsText(b, t[k])
			}
		}
	default:
		b.WriteString(scalar(v) + "\n")
	}
}

func isScalar(v any) bool {
	switch v.(type) {
	case []any, map[string]any:
		return false
	}
	return true
}

func allScalar(l []any) bool {
	for _, x := range l {
		if !isScalar(x) {
			return false
		}
	}
	return true
}

func joinScalars(l []any) string {
	s := make([]string, len(l))
	for i, x := range l {
		s[i] = scalar(x)
	}
	return strings.Join(s, "\t")
}

func scalar(v any) string {
	switch t := v.(type) {
	case nil:
		return "None"
	case bool:
		if t {
			return "True"
		}
		return "False"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

// nextCount is how many times fake i has answered before (and counts this
// one), shared by every call in the scenario.
func nextCount(dir string, i int) int {
	f, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("count-%d", i)), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return 0
	}
	defer f.Close()
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	b, _ := io.ReadAll(f)
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(strconv.Itoa(n+1)), 0)
	return n
}

func logCall(dir string, c Call) {
	f, err := os.OpenFile(filepath.Join(dir, "calls.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(c)
	_, _ = f.Write(append(b, '\n'))
}

// runReal runs the real command (found on PATH, leaving out the shims).
func runReal(shims string, argv []string, stdin string) int {
	var dirs []string
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if d != shims {
			dirs = append(dirs, d)
		}
	}
	real := ""
	for _, d := range dirs {
		p := filepath.Join(d, argv[0])
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			real = p
			break
		}
	}
	if real == "" {
		fmt.Fprintf(os.Stderr, "%s: command not found\n", argv[0])
		return 127
	}
	cmd := exec.Command(real, argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	cmd.Env = append(os.Environ(), "PATH="+strings.Join(dirs, string(filepath.ListSeparator)))
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		return 127
	}
	return 0
}

func environ() map[string]string {
	m := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
