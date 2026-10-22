package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egrif/runnr/internal/runbook"
)

// The operator's own background commands and shells, at a step prompt:
//
// StartJob runs a command in the background with the run's environment at
// a step (a window of its own under tmux, else a terminal of the run's),
// recorded with the step; it ends with the run unless it ends first.
//
// ShellInPane (runs detached under tmux only) opens a shell at a step in a
// pane beside the run, without holding up the prompt. Like a shell
// alongside the run, what it publishes reaches the run when the next step
// starts, and its transcript is recorded with the step.

// JobRecord is a command the operator ran in the background at a step.
type JobRecord struct {
	Command  string    `json:"command"`
	Started  time.Time `json:"started"`
	Ended    time.Time `json:"ended,omitzero"`
	ExitCode *int      `json:"exit_code,omitempty"`
	Output   string    `json:"output,omitempty"`
	Note     string    `json:"note,omitempty"`
	Detached string    `json:"detached,omitempty"`
}

// JobStarter is implemented by sessions that run the operator's commands
// in the background.
type JobStarter interface {
	StartJob(idx int, command string) error
}

// PaneSheller is implemented by sessions that can open a shell beside the
// run (CanPane: they run detached, under tmux).
type PaneSheller interface {
	CanPane() bool
	ShellInPane(idx int) error
}

var (
	_ JobStarter  = (*executor)(nil)
	_ PaneSheller = (*executor)(nil)
)

type adhocJob struct {
	step int
	n    int // its place in the step's Jobs
	job  bgJob
	tmp  string
}

func (x *executor) StartJob(i int, command string) error {
	if strings.TrimSpace(command) == "" {
		return errors.New("give a command")
	}
	if i < 0 || i >= len(x.run.Steps) {
		return fmt.Errorf("no step %d", i+1)
	}
	step := x.def(i)
	job, tmp, ref, err := x.launchJob(x.runCtx, i, step, command, fmt.Sprintf("job at step %d: %s", i+1, jobLine(command)))
	if err != nil {
		return err
	}
	res := &x.run.Steps[i]
	res.Jobs = append(res.Jobs, JobRecord{Command: x.mask(command), Started: time.Now(), Detached: ref, Note: "running in the background until the run ends"})
	x.jobs = append(x.jobs, &adhocJob{step: i, n: len(res.Jobs) - 1, job: job, tmp: tmp})
	x.notify(Event{Kind: EventOutput, Step: i, Stream: "info", Text: "◐ started in the background: " + x.mask(jobLine(command))})
	x.save()
	return nil
}

// tendJobs records the operator's jobs that ended; final stops the rest.
func (x *executor) tendJobs(final bool) {
	var still []*adhocJob
	for _, j := range x.jobs {
		ended := false
		select {
		case <-j.job.Done():
			ended = true
		default:
		}
		if !ended && !final {
			still = append(still, j)
			continue
		}
		if !ended {
			j.job.Stop()
		}
		code, transcript, _ := j.job.Wait()
		os.RemoveAll(j.tmp)
		r := &x.run.Steps[j.step].Jobs[j.n]
		r.Ended, r.Output, r.ExitCode, r.Note = time.Now(), x.mask(transcript), code, "exited"
		if !ended {
			r.ExitCode, r.Note = nil, "stopped when the run ended"
		}
		x.notify(Event{Kind: EventOutput, Step: j.step, Stream: "info", Text: fmt.Sprintf("background job at step %d (%s): %s", j.step+1, jobLine(r.Command), r.Note)})
		x.save()
	}
	x.jobs = still
}

func (x *executor) CanPane() bool { return x.opts.Detached != nil }

func (x *executor) ShellInPane(i int) error {
	d := x.opts.Detached
	if d == nil {
		return errors.New("a shell in a pane needs the run in tmux (runnr run --tmux)")
	}
	x.aside.mu.Lock()
	closed := x.aside.closed
	x.aside.mu.Unlock()
	if closed {
		return errors.New("the run has ended")
	}
	var step runbook.Step
	if i >= 0 && i < len(x.rb.Steps) {
		step = x.def(i)
	}
	x.vmu.RLock()
	extra, err := x.runnrEnv(step)
	var dir string
	if err == nil {
		dir, err = x.render(step.Dir)
	}
	x.vmu.RUnlock()
	if err != nil {
		return err
	}
	tmp, err := x.tempDir("runnr-shell-")
	if err != nil {
		return err
	}
	outFile, secFile := filepath.Join(tmp, "output"), filepath.Join(tmp, "secret")
	cmd, shellEnv, cleanup, err := x.interactiveShell(nonEmpty(step.Shell, nonEmpty(x.rb.Shell, "sh")))
	if err != nil {
		os.RemoveAll(tmp)
		return err
	}
	env := append(append(append(x.baseEnv(), extra...), shellEnv...), "RUNNR_OUTPUT="+outFile, "RUNNR_SECRET="+secFile, "RUNNR_SHELL=1", "TERM="+termName())
	title := fmt.Sprintf("shell at step %d: %s", i+1, step.Name)
	job, err := d.Start(i, title, DetachedSpec{Args: cmd.Args, Env: env, Dir: dir, Tmp: tmp, Pane: true})
	if err != nil {
		cleanup()
		os.RemoveAll(tmp)
		return err
	}
	x.notify(Event{Kind: EventOutput, Step: i, Stream: "info", Text: "⌨ a shell opened in a pane beside the run: what it publishes reaches the run when the next step starts"})
	go func() {
		<-job.Done()
		_, transcript, _ := job.Wait()
		var res cmdResult
		x.readOutputs(i, &res, outFile, secFile)
		cleanup()
		os.RemoveAll(tmp)
		x.aside.mu.Lock()
		if !x.aside.closed {
			x.aside.pending = append(x.aside.pending, asideResult{idx: i, transcript: strings.TrimSpace(x.mask(transcript)), res: res})
		}
		x.aside.mu.Unlock()
	}()
	return nil
}

// jobLine is a command's first line, for messages.
func jobLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}
