package runtest

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/runbook"
)

// Sources resolves what a suite names.
type Sources struct {
	Runbook   func(name string) (*runbook.Runbook, error)
	Fakes     func(name string) (*Library, error)
	Libraries func(names []string) ([]*runbook.Library, error)
}

// Options shape a test run.
type Options struct {
	// Only runs the scenarios with these names (all when empty).
	Only []string
	// Keep keeps each scenario's directory (call log, fakes) for a look.
	Keep bool
	// Exe is runnr itself, which the fake commands call back into.
	Exe string
	// Timeout bounds each scenario (default 2m).
	Timeout time.Duration
}

// Result is how one scenario went.
type Result struct {
	Scenario string        `json:"scenario"`
	Passed   bool          `json:"passed"`
	Problems []string      `json:"problems,omitempty"`
	Run      *engine.Run   `json:"run"`
	Calls    []Call        `json:"calls"`
	Unused   []string      `json:"unused_fakes,omitempty"` // fakes no call matched (suite and scenario ones)
	Asked    []string      `json:"asked,omitempty"`        // the operator's answers
	Warnings []string      `json:"warnings,omitempty"`     // code that could get past the fakes
	Strict   bool          `json:"strict,omitempty"`       // ran in the sandbox
	Dir      string        `json:"dir,omitempty"`          // kept directory
	Took     time.Duration `json:"took"`
}

// Run runs a suite's scenarios.
func Run(ctx context.Context, s *Suite, src Sources, o Options) ([]Result, error) {
	rb, err := src.Runbook(s.Runbook)
	if err != nil {
		return nil, err
	}
	libs, err := src.Libraries(rb.Libraries)
	if err != nil {
		return nil, err
	}
	var shared []sourced
	for _, f := range s.Fakes {
		shared = append(shared, sourced{f, "suite"})
	}
	for _, name := range s.Uses {
		l, err := src.Fakes(name)
		if err != nil {
			return nil, fmt.Errorf("uses: %w", err)
		}
		for _, f := range l.Fakes {
			shared = append(shared, sourced{f, "library " + l.Name})
		}
	}
	if o.Timeout == 0 {
		o.Timeout = 2 * time.Minute
	}
	if s.Strict {
		if err := SandboxAvailable(); err != nil {
			return nil, fmt.Errorf("suite %s is strict: %w", s.Runbook, err)
		}
	}
	warnings := escapes(rb, libs, s.Isolate)
	var out []Result
	for _, sc := range s.Scenarios {
		if len(o.Only) > 0 && !slices.Contains(o.Only, sc.Name) {
			continue
		}
		r, err := runScenario(ctx, s, sc, rb, libs, shared, o)
		if err != nil {
			return out, fmt.Errorf("scenario %s: %w", sc.Name, err)
		}
		r.Warnings = append(slices.Clone(warnings), r.Warnings...)
		out = append(out, r)
	}
	if len(o.Only) > 0 && len(out) == 0 {
		return nil, fmt.Errorf("no scenario named %s", strings.Join(o.Only, ", "))
	}
	return out, nil
}

func runScenario(ctx context.Context, s *Suite, sc Scenario, base *runbook.Runbook, libs []*runbook.Library, shared []sourced, o Options) (Result, error) {
	res := Result{Scenario: sc.Name}
	start := time.Now()
	dir, err := os.MkdirTemp("", "runnr-test-")
	if err != nil {
		return res, err
	}
	if dir, err = filepath.EvalSymlinks(dir); err != nil { // /var → /private/var, as the sandbox sees it
		return res, err
	}
	if o.Keep {
		res.Dir = dir
	} else {
		defer os.RemoveAll(dir)
	}

	// The fakes: the scenario's first, so they win ties.
	p := plan{Isolate: s.Isolate, Shims: filepath.Join(dir, "bin")}
	for _, f := range sc.Fakes {
		p.Fakes = append(p.Fakes, sourced{f, "scenario"})
	}
	p.Fakes = append(p.Fakes, shared...)
	// A shim on PATH for every faked or isolated command; fake files where
	// the suite puts them.
	if err := os.MkdirAll(p.Shims, 0o755); err != nil {
		return res, err
	}
	names := map[string]bool{}
	for _, c := range s.Isolate {
		names[c] = true
	}
	for _, f := range p.Fakes {
		if w := strings.Fields(f.Command); len(w) > 0 {
			names[w[0]] = true
		}
	}
	for _, d := range s.Dirs {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			return res, err
		}
	}
	for rel, cmd := range s.Files {
		if err := writeShim(filepath.Join(dir, rel), cmd, o.Exe); err != nil {
			return res, err
		}
		p.Files = append(p.Files, cmd)
		delete(names, cmd) // only at its path
	}
	for n := range names {
		if err := writeShim(filepath.Join(p.Shims, n), n, o.Exe); err != nil {
			return res, err
		}
	}
	b, _ := json.Marshal(p)
	if err := os.WriteFile(filepath.Join(dir, "fakes.json"), b, 0o600); err != nil {
		return res, err
	}

	// The runbook, with the test's functions in place of the real ones.
	rb := *base
	rb.Functions = maps.Clone(base.Functions)
	if rb.Functions == nil {
		rb.Functions = map[string]runbook.Function{}
	}
	for _, m := range []map[string]string{s.Functions, sc.Functions} {
		for name, body := range m {
			rb.Functions[name] = runbook.Function{Body: body, Description: "faked by the test"}
		}
	}
	// (Defined in the runbook, they win over the libraries' too.)
	inputs := map[string]string{}
	tvars := map[string]string{"test_dir": dir}
	for _, m := range []map[string]string{s.Inputs, sc.Inputs} {
		for k, v := range m {
			r, err := renderText(v, tvars)
			if err != nil {
				return res, fmt.Errorf("inputs.%s: %w", k, err)
			}
			inputs[k] = r
		}
	}
	steps, err := stepAnswers(&rb, s.Operator, sc.Operator)
	if err != nil {
		return res, err
	}
	op := newOperator(mergeOperator(s.Operator, sc.Operator), steps)

	// A scratch HOME and TMPDIR, no credentials in the environment; for a
	// strict suite, a PATH of only the shims and the allowed tools, and the
	// sandbox around every command.
	for _, d := range []string{"home", "tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			return res, err
		}
	}
	path := p.Shims + string(filepath.ListSeparator) + os.Getenv("PATH")
	var wrap func(string, []string) (string, []string)
	if s.Strict {
		allowed := filepath.Join(dir, "allowed")
		execs, missing, err := allowDir(allowed, append(slices.Clone(DefaultAllow), s.Allow...))
		if err != nil {
			return res, err
		}
		// A tool the suite allows must be there; a default one that isn't
		// is only a warning (a step that uses it fails as not found).
		for _, n := range slices.Sorted(maps.Keys(missing)) {
			if slices.Contains(s.Allow, n) {
				return res, fmt.Errorf("allow: %w", missing[n])
			}
			res.Warnings = append(res.Warnings, "the sandbox can't allow "+n+": "+missing[n].Error())
		}
		for _, sh := range []string{"/bin/sh", "/bin/zsh", "/bin/bash", o.Exe} {
			execs = append(execs, sh)
			if r, err := filepath.EvalSymlinks(sh); err == nil {
				execs = append(execs, r)
			}
		}
		sb, err := newSandbox(dir, execs)
		if err != nil {
			return res, err
		}
		path = p.Shims + string(filepath.ListSeparator) + allowed
		if err := sb.probe(o.Exe, dir, cleanEnv(dir, path, nil)); err != nil {
			return res, err
		}
		wrap, res.Strict = sb.wrap, true
	}
	rctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	run := engine.Execute(rctx, &rb, op, "", engine.Options{
		Vars: inputs, Interactive: true, Libraries: libs, Sanctioned: true, Hurry: true, SkipRequires: true,
		BaseEnv: cleanEnv(dir, path, map[string]string{envDir: dir, envExe: o.Exe}),
		TempDir: filepath.Join(dir, "tmp"),
		Wrap:    wrap,
	})
	res.Run, res.Took = run, time.Since(start)
	res.Asked = op.asked
	if errorsTimedOut(rctx) {
		res.Problems = append(res.Problems, fmt.Sprintf("the run took longer than %s and was stopped", o.Timeout))
	}
	res.Calls = readCalls(filepath.Join(dir, "calls.jsonl"))
	res.Problems = append(res.Problems, check(&rb, run, res.Calls, sc.Expect)...)
	used := map[int]bool{}
	for _, c := range res.Calls {
		if c.Fake >= 0 {
			used[c.Fake] = true
		}
	}
	for i, f := range p.Fakes {
		if !used[i] && !strings.HasPrefix(f.From, "library") {
			res.Unused = append(res.Unused, f.From+": "+f.Command)
		}
	}
	res.Passed = len(res.Problems) == 0
	return res, nil
}

func errorsTimedOut(ctx context.Context) bool { return ctx.Err() == context.DeadlineExceeded }

// writeShim writes a script that hands the call to runnr's fakes.
func writeShim(path, name, exe string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	script := fmt.Sprintf("#!/bin/sh\nexec \"${%s:-%s}\" __fake %s \"$@\"\n", envExe, exe, q(name))
	return os.WriteFile(path, []byte(script), 0o755)
}

func readCalls(path string) []Call {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []Call
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		var c Call
		if json.Unmarshal(sc.Bytes(), &c) == nil {
			out = append(out, c)
		}
	}
	slices.SortStableFunc(out, func(a, b Call) int { return a.Time.Compare(b.Time) })
	return out
}

// mergeOperator is the suite's operator with the scenario's answers on top.
func mergeOperator(s, sc Operator) Operator {
	o := Operator{Inputs: maps.Clone(s.Inputs), Answers: s.Answers}
	if o.Inputs == nil {
		o.Inputs = map[string]string{}
	}
	maps.Copy(o.Inputs, sc.Inputs)
	o.Answers = overlay(o.Answers, sc.Answers)
	return o
}

func overlay(a, b Answers) Answers {
	pick := func(x, y Words) Words {
		if len(y) > 0 {
			return y
		}
		return x
	}
	return Answers{Confirm: pick(a.Confirm, b.Confirm), Danger: pick(a.Danger, b.Danger), Typed: pick(a.Typed, b.Typed), Manual: pick(a.Manual, b.Manual),
		Failure: pick(a.Failure, b.Failure), Before: pick(a.Before, b.Before), After: pick(a.After, b.After), Way: pick(a.Way, b.Way)}
}

// stepAnswers resolves the operator's per-step answers to step indexes.
func stepAnswers(rb *runbook.Runbook, s, sc Operator) (map[int]Answers, error) {
	out := map[int]Answers{}
	for _, o := range []Operator{s, sc} {
		for key, a := range o.Steps {
			i, err := stepIndex(rb, key)
			if err != nil {
				return nil, fmt.Errorf("operator.steps.%s: %w", key, err)
			}
			out[i] = overlay(out[i], a)
		}
	}
	return out, nil
}

// stepIndex finds a step by number (1-based), id or name.
func stepIndex(rb *runbook.Runbook, key string) (int, error) {
	if n, err := strconv.Atoi(key); err == nil {
		if n < 1 || n > len(rb.Steps) {
			return 0, fmt.Errorf("no step %d (the runbook has %d)", n, len(rb.Steps))
		}
		return n - 1, nil
	}
	return rb.StepIndex(key)
}

// check compares a run with what the scenario expects.
func check(rb *runbook.Runbook, run *engine.Run, calls []Call, e Expect) []string {
	var probs []string
	add := func(format string, a ...any) { probs = append(probs, fmt.Sprintf(format, a...)) }
	for _, c := range calls {
		if c.Unfaked {
			where := ""
			if c.Step > 0 {
				where = fmt.Sprintf(" (step %d)", c.Step)
			}
			add("no fake answered a call to isolated %s%s: %s", c.Argv[0], where, c.Line())
		}
		if c.Error != "" {
			add("a fake failed%s: %s", stepNote(c.Step), c.Error)
		}
	}
	if e.Status != "" && string(run.Status) != e.Status {
		msg := ""
		if run.Error != "" {
			msg = " (" + run.Error + ")"
		}
		add("run: %s%s, expected %s", run.Status, msg, e.Status)
	}
	for _, key := range slices.Sorted(maps.Keys(e.Steps)) {
		i, err := stepIndex(rb, key)
		if err != nil {
			add("expect.steps.%s: %v", key, err)
			continue
		}
		if got := string(run.Steps[i].Status); got != e.Steps[key] {
			why := ""
			if er := run.Steps[i].Error; er != "" {
				why = " (" + er + ")"
			}
			add("step %d (%s): %s%s, expected %s", i+1, run.Steps[i].Name, got, why, e.Steps[key])
		}
	}
	for _, want := range e.Called {
		if findCall(calls, want, 0) < 0 {
			add("expected a call to %q; none was made", want)
		}
	}
	for _, not := range e.NotCalled {
		if i := findCall(calls, not, 0); i >= 0 {
			add("expected no call to %q, but%s: %s", not, stepNote(calls[i].Step), calls[i].Line())
		}
	}
	at := 0
	for _, want := range e.Order {
		i := findCall(calls, want, at)
		if i < 0 {
			add("expected %q next in order; it isn't called after the calls before it in the list", want)
			break
		}
		at = i + 1
	}
	for _, key := range slices.Sorted(maps.Keys(e.Output)) {
		i, err := stepIndex(rb, key)
		if err != nil {
			add("expect.output.%s: %v", key, err)
			continue
		}
		if !strings.Contains(run.Steps[i].Output, e.Output[key]) {
			add("step %d (%s): its output doesn't contain %q", i+1, run.Steps[i].Name, e.Output[key])
		}
	}
	return probs
}

func stepNote(step int) string {
	if step > 0 {
		return fmt.Sprintf(" at step %d", step)
	}
	return ""
}

// findCall is the first call from index from that has these words (the
// program, then words in order), or -1.
func findCall(calls []Call, want string, from int) int {
	w := strings.Fields(want)
	for i := from; i < len(calls); i++ {
		c := calls[i]
		if len(w) > 0 && len(c.Argv) > 0 && c.Argv[0] == w[0] && hasWords(c.Argv[1:], w[1:]) {
			return i
		}
	}
	return -1
}

func sortedKeys(m map[string]any) []string { return slices.Sorted(maps.Keys(m)) }
