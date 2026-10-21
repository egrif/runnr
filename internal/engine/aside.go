package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/egrif/runnr/internal/runbook"
)

// A shell alongside the run: opened on demand (the console's Shell pane)
// at the step the run is on, while the run carries on. It reads the run's
// variables as they are when it opens; what it publishes, and its
// transcript, reach the run when the next step starts (or the run ends),
// so the shell never changes the run under a step that's running. It's
// hung up when the run ends.

// AsideSheller is implemented by sessions that can open such a shell.
type AsideSheller interface {
	ShellAside(idx int) error
}

var _ AsideSheller = (*executor)(nil)

// asideResult is what an aside shell left for the run.
type asideResult struct {
	idx        int
	transcript string
	res        cmdResult
}

type asideState struct {
	mu      sync.Mutex
	pending []asideResult
	wg      sync.WaitGroup
}

// ShellAside opens a shell at step idx (-1: none) alongside the run and
// returns once it ends.
func (x *executor) ShellAside(idx int) error {
	if x.run.Begun.IsZero() {
		return errors.New("the run hasn't started its steps yet")
	}
	if x.ctx == nil || x.ctx.Err() != nil {
		return errors.New("the run has ended")
	}
	x.aside.wg.Add(1)
	defer x.aside.wg.Done()
	var step runbook.Step
	title := "shell alongside the run"
	if idx >= 0 && idx < len(x.rb.Steps) {
		step = x.def(idx)
		title = fmt.Sprintf("shell alongside the run · step %d: %s", idx+1, step.Name)
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
	defer os.RemoveAll(tmp)
	outFile, secFile := filepath.Join(tmp, "output"), filepath.Join(tmp, "secret")
	extra = append(extra, "RUNNR_OUTPUT="+outFile, "RUNNR_SECRET="+secFile, "RUNNR_SHELL=1")
	shell := ""
	if idx >= 0 && idx < len(x.rb.Steps) {
		shell = nonEmpty(step.Shell, nonEmpty(x.rb.Shell, "sh"))
	}
	_, transcript, err := x.inTerminal(x.ctx, idx, title, shell, "", extra, dir)
	var res cmdResult
	x.readOutputs(idx, &res, outFile, secFile)
	x.aside.mu.Lock()
	x.aside.pending = append(x.aside.pending, asideResult{idx: idx, transcript: strings.TrimSpace(x.mask(transcript)), res: res})
	x.aside.mu.Unlock()
	if n := len(res.outputs) + len(res.secrets); n > 0 {
		x.notify(Event{Kind: EventOutput, Step: idx, Stream: "info", Text: fmt.Sprintf("the shell alongside the run published %d value(s); the run takes them when its next step starts", n)})
	}
	return err
}

// takeAside applies what aside shells left (on the run's own goroutine,
// between steps).
func (x *executor) takeAside() {
	x.aside.mu.Lock()
	pending := x.aside.pending
	x.aside.pending = nil
	x.aside.mu.Unlock()
	for _, a := range pending {
		if a.transcript != "" && a.idx >= 0 && a.idx < len(x.run.Steps) {
			x.run.Steps[a.idx].Shells = append(x.run.Steps[a.idx].Shells, a.transcript)
		}
		x.apply(runbook.Step{}, a.res)
		var names []string
		for k := range a.res.outputs {
			names = append(names, k)
		}
		for k := range a.res.secrets {
			names = append(names, k+" (secret)")
		}
		if len(names) > 0 {
			slices.Sort(names)
			x.notify(Event{Kind: EventOutput, Step: a.idx, Stream: "info", Text: "from the shell alongside the run: " + strings.Join(names, ", ")})
		}
	}
	if len(pending) > 0 {
		x.refreshState()
		x.save()
	}
}

// endAside waits for aside shells, once their context has ended them.
func (x *executor) endAside() {
	x.aside.wg.Wait()
	x.takeAside()
}
