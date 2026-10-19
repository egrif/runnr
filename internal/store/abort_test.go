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
			go s.WatchAbort(ctx, "alive", cancel)
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
