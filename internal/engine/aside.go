package engine

import (
	"context"
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
// variables as they are when it opens; what it publishes reaches the run
// when the next step starts (or the run ends), so the shell never changes
// the run under a step that's running. Its transcript is recorded with its
// step then too, so far as it has gone if it's still open. It can outlive
// the run: what it leaves after that isn't applied.

// AsideSheller is implemented by sessions that can open such a shell.
type AsideSheller interface {
	// ShellAside runs a shell at step idx (-1: none) on conn until it ends
	// or ctx is done.
	ShellAside(ctx context.Context, idx int, conn TermConn) error
}

var _ AsideSheller = (*executor)(nil)

// asideResult is what an aside shell left for the run.
type asideResult struct {
	idx        int
	transcript string
	res        cmdResult
	sh         *asideShell
}

// asideShell is a shell alongside the run, open or just ended.
type asideShell struct {
	idx  int
	sess *termSession
	// slot is where its transcript is in its step's Shells (-1: not yet),
	// last what was put there.
	slot int
	last string
}

type asideState struct {
	mu      sync.Mutex
	pending []asideResult
	open    map[*asideShell]bool
	// closed: the run has finished; nothing more is taken.
	closed bool
}

func (x *executor) ShellAside(ctx context.Context, idx int, conn TermConn) error {
	if x.run.Begun.IsZero() {
		return errors.New("the run hasn't started its steps yet")
	}
	x.aside.mu.Lock()
	closed := x.aside.closed
	x.aside.mu.Unlock()
	if closed {
		return errors.New("the run has ended")
	}
	var step runbook.Step
	if idx >= 0 && idx < len(x.rb.Steps) {
		step = x.def(idx)
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
	sh := &asideShell{idx: idx, slot: -1}
	res, transcript, err := x.shellOn(ctx, idx, step, extra, dir, conn, func(s *termSession) {
		x.aside.mu.Lock()
		defer x.aside.mu.Unlock()
		if x.aside.open == nil {
			x.aside.open = map[*asideShell]bool{}
		}
		sh.sess, x.aside.open[sh] = s, true
	})
	x.aside.mu.Lock()
	delete(x.aside.open, sh)
	closed = x.aside.closed
	if !closed {
		x.aside.pending = append(x.aside.pending, asideResult{idx: idx, transcript: strings.TrimSpace(x.mask(transcript)), res: res, sh: sh})
	}
	x.aside.mu.Unlock()
	if n := len(res.outputs) + len(res.secrets); n > 0 {
		if closed {
			x.notify(Event{Kind: EventOutput, Step: idx, Stream: "info", Text: fmt.Sprintf("a shell published %d value(s) after the run ended; they weren't applied", n)})
		} else {
			x.notify(Event{Kind: EventOutput, Step: idx, Stream: "info", Text: fmt.Sprintf("the shell alongside the run published %d value(s); the run takes them when its next step starts", n)})
		}
	}
	return err
}

// shellOn runs an interactive shell at step (the step's shell, else the
// runbook's, else sh) on conn, and returns what it published and its
// transcript. started, if given, is told of the session once it's going.
func (x *executor) shellOn(ctx context.Context, idx int, step runbook.Step, extra []string, dir string, conn TermConn, started func(*termSession)) (cmdResult, string, error) {
	var res cmdResult
	tmp, err := x.tempDir("runnr-shell-")
	if err != nil {
		conn.Close()
		return res, "", err
	}
	defer os.RemoveAll(tmp)
	outFile, secFile := filepath.Join(tmp, "output"), filepath.Join(tmp, "secret")
	extra = append(extra, "RUNNR_OUTPUT="+outFile, "RUNNR_SECRET="+secFile, "RUNNR_SHELL=1")
	shell := nonEmpty(step.Shell, nonEmpty(x.rb.Shell, "sh"))
	s, err := x.startTerm(ctx, shell, "", extra, dir, conn)
	if err != nil {
		conn.Close()
		return res, "", err
	}
	if started != nil {
		started(s)
	}
	_, transcript, err := s.wait()
	if errors.Is(err, context.Canceled) {
		err = nil // hung up
	}
	x.readOutputs(idx, &res, outFile, secFile)
	return res, transcript, err
}

// ShellSpec is a shell with a runbook's environment outside a run: before
// it starts (the inputs as given, else their defaults) or after it ended
// (its inputs and the values it published).
type ShellSpec struct {
	Runbook *runbook.Runbook
	// Step is the step it's at (-1: none): its shell, dir and env.
	Step  int
	Vars  map[string]string
	State map[string]string
	RunID string
	// Options supplies the libraries (for functions), BaseEnv, TempDir and Wrap.
	Options Options
}

// OpenShell runs a shell with spec's environment on conn until it ends or
// ctx is done. What it publishes goes nowhere; it returns its transcript.
func OpenShell(ctx context.Context, spec ShellSpec, conn TermConn) (string, error) {
	// Its placeholders are this machine's folders, as in a run.
	fo := spec.Options
	fo.Vars = spec.Vars
	if _, err := fillFolders(spec.Runbook, fo); err != nil {
		conn.Close()
		return "", err
	}
	p := NewPreviewer(spec.Runbook, spec.Vars, spec.State, false)
	x := p.x
	x.opts = spec.Options
	x.vf.off = true
	x.run.ID = spec.RunID
	if err := x.setupFunctions(); err != nil {
		conn.Close()
		return "", err
	}
	defer os.Remove(x.funcFile)
	var step runbook.Step
	if spec.Step >= 0 && spec.Step < len(x.rb.Steps) {
		step = x.rb.Steps[spec.Step]
	}
	extra, err := x.runnrEnv(step)
	var dir string
	if err == nil {
		dir, err = x.render(step.Dir)
	}
	if err != nil {
		conn.Close()
		return "", err
	}
	_, transcript, err := x.shellOn(ctx, spec.Step, step, extra, dir, conn, nil)
	return strings.TrimSpace(x.mask(transcript)), err
}

// takeAside applies what aside shells left, and records the transcripts
// of those still open so far (on the run's own goroutine, between steps).
func (x *executor) takeAside() {
	x.aside.mu.Lock()
	pending := x.aside.pending
	x.aside.pending = nil
	var open []*asideShell
	for sh := range x.aside.open {
		if sh.sess != nil {
			open = append(open, sh)
		}
	}
	x.aside.mu.Unlock()
	recorded := false
	for _, a := range pending {
		recorded = x.recordAside(a.sh, a.idx, a.transcript) || recorded
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
	for _, sh := range open {
		recorded = x.recordAside(sh, sh.idx, strings.TrimSpace(x.mask(sh.sess.snapshot()))) || recorded
	}
	if len(pending) > 0 || recorded {
		x.refreshState()
		x.save()
	}
}

// recordAside puts a shell's transcript with step idx: in its place there
// if it has one (sh: the shell alongside, nil for others), else after the
// others. It says whether the record changed.
func (x *executor) recordAside(sh *asideShell, idx int, transcript string) bool {
	if transcript == "" || idx < 0 || idx >= len(x.run.Steps) {
		return false
	}
	shells := x.run.Steps[idx].Shells
	if sh == nil || sh.slot < 0 || sh.slot >= len(shells) {
		x.run.Steps[idx].Shells = append(shells, transcript)
		if sh != nil {
			sh.slot, sh.last = len(shells), transcript
		}
		return true
	}
	if sh.last == transcript {
		return false
	}
	shells[sh.slot], sh.last = transcript, transcript
	return true
}

// endAside takes what aside shells have left, and closes the run to them:
// shells still open carry on, but what they leave isn't applied.
func (x *executor) endAside() {
	x.takeAside()
	x.aside.mu.Lock()
	x.aside.closed = true
	x.aside.mu.Unlock()
}
