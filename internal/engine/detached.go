package engine

import (
	"context"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/egrif/runnr/internal/runbook"
)

// Detacher runs background steps outside runnr's process (e.g. each in a
// tmux window), so a step outlives the runnr that started it and a resumed
// run can pick it up again.
type Detacher interface {
	// Start runs spec and returns at once.
	Start(idx int, title string, spec DetachedSpec) (DetachedJob, error)
	// Adopt finds a job an earlier runnr started, by its Ref.
	Adopt(ref string) (DetachedJob, error)
}

// DetachedSpec is what a detached background step runs.
type DetachedSpec struct {
	Shell   string   `json:"shell"`
	Command string   `json:"command"`
	Env     []string `json:"env"`
	Dir     string   `json:"dir,omitempty"`
	// Tmp is the step's own directory ($RUNNR_OUTPUT is in it); the job
	// keeps its exit status and output there too.
	Tmp string `json:"tmp"`
}

// DetachedJob is a running (or finished) detached step.
type DetachedJob interface {
	// Ref finds the job again (recorded with the step).
	Ref() string
	Done() <-chan struct{}
	// Stop ends it (hang-up), waiting a moment for it to go.
	Stop()
	// Wait returns its exit status (nil: unknown) and output, once done.
	Wait() (code *int, transcript string, err error)
}

// bgJob is a background step's session: on runnr's own terminal or
// detached.
type bgJob interface {
	Done() <-chan struct{}
	Stop()
	Wait() (*int, string, error)
}

func (s *termSession) Done() <-chan struct{}       { return s.done }
func (s *termSession) Stop()                       { s.stop() }
func (s *termSession) Wait() (*int, string, error) { return s.wait() }

// adoptBackground takes over the background steps of the run this one
// resumes: one still running (or that finished on its own) is followed
// again; one that can't be found is done again, since it may never have
// finished its work.
func (x *executor) adoptBackground() {
	prev := x.opts.Resumed
	if prev == nil || len(prev.Steps) != len(x.run.Steps) {
		return
	}
	for i, p := range prev.Steps {
		if p.Status != StatusBackground {
			continue
		}
		step := x.rb.Steps[i]
		var job DetachedJob
		var err error
		switch {
		case p.Detached == "":
			err = fmt.Errorf("it ran on the earlier runnr's terminal")
		case x.opts.Detached == nil:
			err = fmt.Errorf("this run can't follow detached steps (run it with --tmux)")
		default:
			job, err = x.opts.Detached.Adopt(p.Detached)
		}
		res := &x.run.Steps[i]
		if err != nil {
			x.notify(Event{Kind: EventOutput, Step: i, Stream: "info", Text: fmt.Sprintf("background step %d (%s) from the resumed run can't be followed (%v): it runs again", i+1, step.Name, err)})
			if skip, _, _ := x.ctl.Get(i); skip {
				x.ctl.Toggle("skip", i)
			}
			res.Status, res.Note = StatusPending, ""
			continue
		}
		stopAfter := -1
		if step.StopAfter != "" {
			stopAfter, _ = x.rb.StepIndex(step.StopAfter)
		}
		if x.background == nil {
			x.background = map[int]*bgStep{}
		}
		x.background[i] = &bgStep{session: job, stopAfter: stopAfter, started: p.Started}
		*res = p
		res.Note = "still running from the resumed run (" + p.Note + ")"
		x.notify(Event{Kind: EventOutput, Step: i, Stream: "info", Text: fmt.Sprintf("background step %d (%s) from the resumed run: followed again", i+1, step.Name)})
		// Stopped (or done) where the earlier run would have stopped it.
		if stopAfter >= 0 && stopAfter < x.opts.StartAt && slices.Contains([]Status{StatusSucceeded, StatusForced, StatusSkipped}, prev.Steps[stopAfter].Status) {
			x.tendBackground(stopAfter, false)
		}
	}
}

// waitFile waits for path to appear, until d passes.
func waitFile(path string, d time.Duration) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// leaveDetached, when a run ends without finishing, leaves its detached
// background steps running (a Terraform apply mustn't be killed halfway):
// resuming the run follows them again.
func (x *executor) leaveDetached() {
	for i, b := range x.background {
		if _, ok := b.session.(DetachedJob); !ok {
			continue
		}
		select {
		case <-b.session.Done():
			continue // ended: recorded as usual
		default:
		}
		delete(x.background, i)
		res := &x.run.Steps[i]
		res.Note = "left running, detached: resume the run to follow it"
		x.notify(Event{Kind: EventOutput, Step: i, Stream: "info", Text: fmt.Sprintf("background step %d (%s) is left running (detached); resume the run to follow it", i+1, res.Name)})
	}
}

// waitFor waits for the background steps step i names in wait_for to end
// by themselves, and fails if one of them failed.
func (x *executor) waitFor(ctx context.Context, i int, step runbook.Step) error {
	for _, w := range step.WaitFor {
		j, err := x.rb.StepIndex(w)
		if err != nil {
			return err
		}
		if b, running := x.background[j]; running {
			x.notify(Event{Kind: EventOutput, Step: i, Stream: "info", Text: fmt.Sprintf("waiting for background step %d (%s) to finish…", j+1, x.run.Steps[j].Name)})
			select {
			case <-b.session.Done():
			case <-ctx.Done():
				return ctx.Err()
			}
			x.tendBackground(-1, false)
		}
		switch r := x.run.Steps[j]; r.Status {
		case StatusSucceeded, StatusForced:
		case StatusSkipped:
			x.notify(Event{Kind: EventOutput, Step: i, Stream: "info", Text: fmt.Sprintf("background step %d (%s) was skipped", j+1, r.Name)})
		default:
			return fmt.Errorf("background step %d (%s) %s%s", j+1, r.Name, r.Status, suffix(r.Note))
		}
	}
	return nil
}
