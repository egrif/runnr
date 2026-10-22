package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeDetacher runs detached steps as plain processes, findable by ref.
type fakeDetacher struct {
	mu   sync.Mutex
	jobs map[string]*fakeJob
	n    int
}

type fakeJob struct {
	ref  string
	cmd  *exec.Cmd
	done chan struct{}
	out  strings.Builder
	code *int
}

func (j *fakeJob) Ref() string                 { return j.ref }
func (j *fakeJob) Done() <-chan struct{}       { return j.done }
func (j *fakeJob) Stop()                       { _ = j.cmd.Process.Kill(); <-j.done }
func (j *fakeJob) Wait() (*int, string, error) { <-j.done; return j.code, j.out.String(), nil }

func (d *fakeDetacher) Start(idx int, _ string, s DetachedSpec) (DetachedJob, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.n++
	j := &fakeJob{ref: "job" + string(rune('0'+d.n)), done: make(chan struct{})}
	j.cmd = exec.Command(s.Shell, "-c", s.Command)
	if s.Pane {
		// A shell in a pane: the operator publishes a value and leaves.
		j.cmd = exec.Command("sh", "-c", `echo from_pane=hi >> "$RUNNR_OUTPUT"; echo in the pane`)
	}
	j.cmd.Env, j.cmd.Dir, j.cmd.Stdout = s.Env, s.Dir, &j.out
	if err := j.cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		_ = j.cmd.Wait()
		c := j.cmd.ProcessState.ExitCode()
		j.code = &c
		close(j.done)
	}()
	if d.jobs == nil {
		d.jobs = map[string]*fakeJob{}
	}
	d.jobs[j.ref] = j
	return j, nil
}

func (d *fakeDetacher) Adopt(ref string) (DetachedJob, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if j, ok := d.jobs[ref]; ok {
		return j, nil
	}
	return nil, os.ErrNotExist
}

func TestDetachedBackgroundOutlivesAFailedRun(t *testing.T) {
	dir := t.TempDir()
	gate := filepath.Join(dir, "go")
	rb := mustParse(t, `
name: t
steps:
  - {name: apply, run: 'while [ ! -f `+gate+` ]; do sleep 0.05; done; echo applied', background: true, stop_after: wait}
  - {name: flaky, run: 'test -f `+gate+`'}
  - {name: wait, run: 'sleep 0.5'}
`)
	d := &fakeDetacher{}
	first := Execute(context.Background(), rb, Unattended{}, "", Options{Detached: d})
	if first.Status != StatusFailed || first.Steps[0].Status != StatusBackground || first.Steps[0].Detached == "" {
		t.Fatalf("first: %s %s %q", first.Status, statuses(first), first.Steps[0].Note)
	}
	select {
	case <-d.jobs[first.Steps[0].Detached].done:
		t.Fatal("the detached step was stopped when the run failed")
	default:
	}
	os.WriteFile(gate, nil, 0o600)
	r := Execute(context.Background(), rb, Unattended{}, "", Options{Detached: d, StartAt: ResumePoint(first), Resumed: first})
	if r.Status != StatusSucceeded || statuses(r) != "succeeded,succeeded,succeeded" || r.Steps[0].Output != "applied\n" {
		t.Fatalf("resumed: %s %s %q %q", r.Status, statuses(r), r.Steps[0].Note, r.Steps[0].Output)
	}

	// Without a detacher, a background step from the resumed run runs again.
	os.Remove(gate)
	first = Execute(context.Background(), rb, Unattended{}, "", Options{Detached: d})
	os.WriteFile(gate, nil, 0o600)
	r = Execute(context.Background(), rb, Unattended{}, "", Options{StartAt: ResumePoint(first), Resumed: first})
	if r.Status != StatusSucceeded || r.Steps[0].Output != "applied\n" || r.Steps[0].Detached != "" {
		t.Fatalf("rerun: %s %s %q", r.Status, statuses(r), r.Steps[0].Note)
	}
}

func TestWaitFor(t *testing.T) {
	rb := mustParse(t, `
name: t
steps:
  - {id: apply, name: apply, run: 'sleep 0.3; echo applied', background: true}
  - {name: meanwhile, run: 'echo meanwhile'}
  - {name: after apply, run: 'echo after', wait_for: [apply]}
  - {id: bad, name: bad, run: 'exit 3', background: true}
  - {name: after bad, run: 'echo never', wait_for: [bad]}
`)
	r := Execute(context.Background(), rb, Unattended{}, "", Options{})
	if got := statuses(r); got != "succeeded,succeeded,succeeded,failed,failed" || r.Steps[0].Output != "applied\n" {
		t.Fatalf("%s %q", got, r.Steps[0].Output)
	}
	if e := r.Steps[4].Error; !strings.Contains(e, "background step 4 (bad) failed: exited by itself with status 3") {
		t.Fatalf("%q", e)
	}
}
