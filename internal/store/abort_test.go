package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/egrif/runnr/internal/engine"
)

func TestAbortRun(t *testing.T) {
	for kind, s := range stores(t) {
		t.Run(kind, func(t *testing.T) {
			host, _ := os.Hostname()
			running := func(id string, owner *engine.Owner) {
				r := &engine.Run{ID: id, Runbook: "x", Status: engine.StatusRunning, Started: time.Now(), Owner: owner,
					Steps: []engine.StepResult{{Name: "a", Status: engine.StatusSucceeded}, {Name: "b", Status: engine.StatusRunning}}}
				if err := s.SaveRun(r); err != nil {
					t.Fatal(err)
				}
			}

			// Its process is gone (this host, no such pid): marked aborted.
			running("gone", &engine.Owner{Host: host, PID: 1 << 30, Via: "cli"})
			if _, err := s.AddNote("gone", 1, "ann", "looked stuck"); err != nil {
				t.Fatal(err)
			}
			res, err := s.AbortRun("gone", "ann", "stuck", false, time.Second)
			if err != nil || !res.Aborted {
				t.Fatalf("%+v %v", res, err)
			}
			r, _ := s.LoadRun("gone")
			if r.Status != engine.StatusAborted || r.Error != "aborted by ann: stuck" || r.Steps[1].Status != engine.StatusAborted {
				t.Errorf("run = %s %q, step %s", r.Status, r.Error, r.Steps[1].Status)
			}
			if len(r.Steps[1].Notes) != 1 {
				t.Errorf("notes = %v (kept apart from the record, once)", r.Steps[1].Notes)
			}
			if _, err := s.AbortRun("gone", "ann", "", false, 0); err == nil {
				t.Error("aborting a finished run should fail")
			}

			// Its process is alive (this one) but doesn't stop: asked, not marked.
			running("alive", &engine.Owner{Host: host, PID: os.Getpid(), Via: "server"})
			res, err = s.AbortRun("alive", "bo", "", false, 300*time.Millisecond)
			if err != nil || res.Aborted || !strings.Contains(res.Message, "hasn't stopped") {
				t.Fatalf("%+v %v", res, err)
			}
			// ... and the process running it sees the request.
			ctx, cancel := context.WithCancelCause(context.Background())
			go s.WatchAbort(ctx, "alive", cancel, nil)
			select {
			case <-ctx.Done():
				var ae *engine.AbortError
				if !errors.As(context.Cause(ctx), &ae) || ae.By != "bo" {
					t.Errorf("cause = %v", context.Cause(ctx))
				}
			case <-time.After(5 * time.Second):
				t.Fatal("WatchAbort didn't cancel the run")
			}

			// Another host: only --force marks it.
			running("far", &engine.Owner{Host: "elsewhere", PID: 7, Via: "cli"})
			if res, _ = s.AbortRun("far", "cy", "", false, 0); res.Aborted || !strings.Contains(res.Message, "--force") {
				t.Errorf("%+v", res)
			}
			if res, err = s.AbortRun("far", "cy", "host died", true, 0); err != nil || !res.Aborted {
				t.Errorf("%+v %v", res, err)
			}
		})
	}
}

func TestSuspendRun(t *testing.T) {
	for kind, s := range stores(t) {
		t.Run(kind, func(t *testing.T) {
			s.db.Exec("DELETE FROM run_suspends")
			host, _ := os.Hostname()
			running := func(id string, owner *engine.Owner) {
				r := &engine.Run{ID: id, Runbook: "x", Status: engine.StatusRunning, Started: time.Now(), Owner: owner,
					Steps: []engine.StepResult{{Name: "a", Status: engine.StatusSucceeded}, {Name: "b", Status: engine.StatusRunning}}}
				if err := s.SaveRun(r); err != nil {
					t.Fatal(err)
				}
			}
			// Its process is gone: marked suspended, the step it was on to do again.
			running("s-gone", &engine.Owner{Host: host, PID: 1 << 30, Via: "cli"})
			res, err := s.SuspendRun("s-gone", &engine.SuspendError{By: "ann", Reason: "later"}, time.Second)
			if r, _ := s.LoadRun("s-gone"); err != nil || !res.Suspended || r.Status != engine.StatusSuspended || r.Error != "suspended by ann: later" || r.Steps[1].Status != engine.StatusSuspended || engine.ResumePoint(r) != 1 {
				t.Fatalf("%+v %v %s", res, err, r.Status)
			}
			// Alive (this process): asked; the process running it passes it on to its controls, once.
			running("s-alive", &engine.Owner{Host: host, PID: os.Getpid(), Via: "server"})
			res, err = s.SuspendRun("s-alive", &engine.SuspendError{By: "bo", Now: true}, 300*time.Millisecond)
			if err != nil || res.Suspended || !strings.Contains(res.Message, "step 2 (b) finishes") {
				t.Fatalf("%+v %v", res, err)
			}
			ctl := engine.NewControls(engine.ControlsView{})
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			go s.WatchAbort(ctx, "s-alive", cancel, ctl)
			for deadline := time.Now().Add(5 * time.Second); ctl.Suspending() == nil; time.Sleep(50 * time.Millisecond) {
				if time.Now().After(deadline) {
					t.Fatal("the request didn't reach the controls")
				}
			}
			if e := ctl.Suspending(); e.By != "bo" || !e.Now {
				t.Fatalf("%+v", e)
			}
			if e, _ := s.takeSuspend("s-alive"); e != nil {
				t.Fatal("request not cleared")
			}
			if _, err := s.SuspendRun("s-gone", &engine.SuspendError{}, 0); err == nil {
				t.Fatal("suspending a run that isn't running")
			}
		})
	}
}
