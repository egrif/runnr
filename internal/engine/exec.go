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

	cmd := exec.CommandContext(ctx, shell, "-c", command)
	cmd.Env = env
	cmd.Dir = dir
	cmd.WaitDelay = 5 * time.Second

	if a, ok := x.ui.(Attacher); ok && step.TTY {
		// Attached: the command owns the terminal, so it stays in our
		// process group (for job control) and its output isn't recorded.
		cmd.Stdin, cmd.Stdout, cmd.Stderr = a.AttachedIO()
		err := cmd.Run()
		res := x.finishCmd(ctx, step, cmd, err)
		res.combined = "(attached to terminal; output not recorded)\n"
		x.readOutputs(i, &res, outFile, secFile)
		return res
	}
	setProcessGroup(cmd)

	var stdout bytes.Buffer
	combined := &ringBuffer{}
	outPipe, _ := cmd.StdoutPipe()
	errPipe, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return cmdResult{err: err}
	}

	var wg sync.WaitGroup
	pump := func(r io.Reader, stream string, also io.Writer) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			if also != nil {
				also.Write([]byte(line + "\n"))
			}
			combined.Write([]byte(line + "\n"))
			x.notify(Event{Kind: EventOutput, Step: i, Stream: stream, Text: x.mask(line)})
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
	// Mask secrets in what this step already produced.
	for _, v := range res.secrets {
		if len(v) >= 4 {
			res.stdout = strings.ReplaceAll(res.stdout, v, "****")
			res.combined = strings.ReplaceAll(res.combined, v, "****")
		}
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
