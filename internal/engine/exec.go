package engine

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/egrif/runnr/internal/runbook"
)

type cmdResult struct {
	outputs  map[string]string
	secrets  map[string]string
	stdout   string
	combined string
	exitCode *int
	err      error
}

// ringBuffer keeps the last maxOutput bytes written.
type ringBuffer struct {
	mu  sync.Mutex
	buf []byte
	cut bool
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > maxOutput {
		r.buf = r.buf[len(r.buf)-maxOutput:]
		r.cut = true
	}
	return len(p), nil
}

func (r *ringBuffer) String() string {
	if r.cut {
		return "…[truncated]\n" + string(r.buf)
	}
	return string(r.buf)
}

func (x *executor) exec(ctx context.Context, i int, step runbook.Step, command string) cmdResult {
	return x.execStream(ctx, i, step, command, true)
}

// execStream is exec; stream=false keeps the output out of the live log.
func (x *executor) execStream(ctx context.Context, i int, step runbook.Step, command string, stream bool) cmdResult {
	if d := step.TimeoutDuration(); d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	shell := step.Shell
	if shell == "" {
		shell = x.rb.Shell
	}
	if shell == "" {
		shell = "sh"
	}
	env, err := x.env(step)
	if err != nil {
		return cmdResult{err: err}
	}
	dir, err := x.render(step.Dir)
	if err != nil {
		return cmdResult{err: fmt.Errorf("dir: %w", err)}
	}

	tmp, err := os.MkdirTemp("", "runnr-step-")
	if err != nil {
		return cmdResult{err: err}
	}
	defer os.RemoveAll(tmp)
	outFile, secFile := filepath.Join(tmp, "output"), filepath.Join(tmp, "secret")
	env = append(env, "RUNNR_OUTPUT="+outFile, "RUNNR_SECRET="+secFile)

	if command, err = x.withProfile(command, tmp); err != nil {
		return cmdResult{err: err}
	}
	command = withOutputs(step, command)

	if step.TTY {
		// The command owns a terminal (ours, or a new window); its output
		// isn't recorded.
		extra, _ := x.runnrEnv(step)
		extra = append(extra, "RUNNR_OUTPUT="+outFile, "RUNNR_SECRET="+secFile)
		code, transcript, err := x.inTerminal(ctx, i, fmt.Sprintf("step %d: %s", i+1, step.Name), shell, command, extra, dir)
		res := cmdResult{exitCode: code, combined: transcript, stdout: transcript}
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			res.err = fmt.Errorf("timed out after %s", step.Timeout)
		case err != nil:
			res.err = err
		case code != nil && *code != 0:
			res.err = fmt.Errorf("exit status %d", *code)
		}
		x.readOutputs(i, &res, outFile, secFile)
		return res
	}

	cmd := exec.CommandContext(ctx, shell, "-c", command)
	cmd.Env = env
	cmd.Dir = dir
	cmd.WaitDelay = 5 * time.Second
	setProcessGroup(cmd)

	var stdout bytes.Buffer
	combined := &ringBuffer{}
	outPipe, _ := cmd.StdoutPipe()
	errPipe, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return cmdResult{err: err}
	}

	var wg sync.WaitGroup
	pump := func(r io.Reader, name string, also io.Writer) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			if also != nil {
				also.Write([]byte(line + "\n"))
			}
			combined.Write([]byte(line + "\n"))
			if stream {
				x.notify(Event{Kind: EventOutput, Step: i, Stream: name, Text: x.mask(line)})
			}
		}
	}
	wg.Add(2)
	go pump(outPipe, "stdout", &stdout)
	go pump(errPipe, "stderr", nil)
	wg.Wait()
	err = cmd.Wait()

	res := x.finishCmd(ctx, step, cmd, err)
	res.stdout, res.combined = stdout.String(), combined.String()
	x.readOutputs(i, &res, outFile, secFile)
	return res
}

// readOutputs parses key=value lines that a step wrote to $RUNNR_OUTPUT or
// $RUNNR_SECRET. Outputs are only applied if the step succeeds.
func (x *executor) readOutputs(i int, res *cmdResult, outFile, secFile string) {
	parse := func(path string) map[string]string {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		m := map[string]string{}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok || !identRe.MatchString(k) {
				x.notify(Event{Kind: EventOutput, Step: i, Stream: "info", Text: "ignoring malformed output line (want key=value)"})
				continue
			}
			m[k] = v
		}
		return m
	}
	res.outputs, res.secrets = parse(outFile), parse(secFile)
	// Mask secrets in what this step already produced (longest first).
	vals := make([]string, 0, len(res.secrets))
	for _, v := range res.secrets {
		if len(v) >= 4 {
			vals = append(vals, v)
		}
	}
	slices.SortFunc(vals, func(a, b string) int { return len(b) - len(a) })
	for _, v := range vals {
		shown := x.maskedValue(v)
		res.stdout = strings.ReplaceAll(res.stdout, v, shown)
		res.combined = strings.ReplaceAll(res.combined, v, shown)
	}
}

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (x *executor) finishCmd(ctx context.Context, step runbook.Step, cmd *exec.Cmd, err error) cmdResult {
	var res cmdResult
	if cmd.ProcessState != nil {
		code := cmd.ProcessState.ExitCode()
		res.exitCode = &code
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.err = fmt.Errorf("timed out after %s", step.Timeout)
	case err != nil:
		var ee *exec.ExitError
		if errors.As(err, &ee) && res.exitCode != nil && *res.exitCode >= 0 {
			res.err = fmt.Errorf("exit status %d", *res.exitCode)
		} else {
			res.err = errors.New(strings.TrimSpace(err.Error()))
		}
	}
	return res
}

// withProfile sources the runbook's profile files before the step. The
// step is then read from a file, so it's parsed after the profile has
// defined its aliases and functions.
func (x *executor) withProfile(command, tmp string) (string, error) {
	if len(x.rb.Profile) == 0 {
		return command, nil
	}
	script := filepath.Join(tmp, "step.sh")
	if err := os.WriteFile(script, []byte(command), 0o600); err != nil {
		return "", err
	}
	var b strings.Builder
	home, _ := os.UserHomeDir()
	for _, f := range x.rb.Profile {
		if rest, ok := strings.CutPrefix(f, "~/"); ok {
			f = filepath.Join(home, rest)
		}
		// Quiet: shell configs often print things meant for interactive use.
		fmt.Fprintf(&b, "[ -r %[1]s ] && source %[1]s >/dev/null 2>&1\n", shQuote(f))
	}
	fmt.Fprintf(&b, "source %s\n", shQuote(script))
	return b.String(), nil
}

// withOutputs makes the shell write the step's declared outputs to
// $RUNNR_OUTPUT / $RUNNR_SECRET when it exits (only set variables).
func withOutputs(step runbook.Step, command string) string {
	if len(step.Outputs)+len(step.SecretOutputs) == 0 {
		return command
	}
	var b strings.Builder
	b.WriteString("__runnr_outputs() {\n")
	for file, names := range map[string][]string{"RUNNR_OUTPUT": step.Outputs, "RUNNR_SECRET": step.SecretOutputs} {
		for _, n := range names {
			fmt.Fprintf(&b, "  [ -n \"${%[1]s+x}\" ] && printf '%%s=%%s\\n' %[1]s \"$%[1]s\" >> \"$%[2]s\"\n", n, file)
		}
	}
	b.WriteString("  return 0\n}\ntrap __runnr_outputs EXIT\n")
	return b.String() + command
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
