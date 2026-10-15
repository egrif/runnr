package engine

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/egrif/runnr/internal/runbook"
)

type cmdResult struct {
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
	dir, err := runbook.Render(step.Dir, x.vars)
	if err != nil {
		return cmdResult{err: fmt.Errorf("dir: %w", err)}
	}

	cmd := exec.CommandContext(ctx, shell, "-c", command)
	cmd.Env = env
	cmd.Dir = dir
	setProcessGroup(cmd)
	cmd.WaitDelay = 5 * time.Second

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

	res := cmdResult{stdout: stdout.String(), combined: combined.String()}
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
