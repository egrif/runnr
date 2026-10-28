//go:build unix

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egrif/runnr/internal/engine"
)

const reviewPrompt = "review (enter to start, ? for help): "

// record is a run as runnr history <id> --json gives it.
func (h *cliHome) record(id string) *engine.Run {
	h.t.Helper()
	var r engine.Run
	if err := json.Unmarshal([]byte(h.must("history", id, "--json")), &r); err != nil {
		h.t.Fatal(err)
	}
	return &r
}

// runs is the runbook's runs, latest first.
func (h *cliHome) runs(runbook string) []engine.Run {
	h.t.Helper()
	var rs []engine.Run
	if err := json.Unmarshal([]byte(h.must("history", "--json", "--runbook", runbook)), &rs); err != nil {
		h.t.Fatal(err)
	}
	return rs
}

// A step that fails until the file ok is in the work folder.
func untilOK(h *cliHome) string {
	return fmt.Sprintf("test -f %s/ok", h.dir)
}

// Starting a run while the latest for the same target stopped partway asks
// first: quit leaves nothing behind, resume carries that run on, and
// another target isn't asked about.
func TestCLIUnfinishedRun(t *testing.T) {
	h := newCLIHome(t)
	h.runbook(`name: db-upgrade
inputs:
  - {name: cluster, identifier: true, default: orders-1}
steps:
  - {name: snapshot, run: "echo snap"}
  - {name: upgrade, run: "` + untilOK(h) + `"}
`)
	if out, err := h.run("run", "db-upgrade", "-u"); err == nil {
		t.Fatalf("the upgrade should fail: %s", out)
	}
	failed := h.runs("db-upgrade")[0]

	// Quit: not started, and no record of it.
	s := h.start("run", "db-upgrade")
	s.answer("orders-1: ", "")
	s.expect("⚠ run " + failed.ID + " for cluster=orders-1 stopped at step 2 (upgrade): failed")
	s.answer("[r]esume it / [n]ew run anyway / [q]uit", "q")
	s.expect("not started")
	if s.wait() == 0 {
		t.Fatal("quit, yet it succeeded")
	}
	if n := len(h.runs("db-upgrade")); n != 1 {
		t.Fatalf("%d runs after quitting", n)
	}

	// Another cluster: nothing to ask.
	s = h.start("run", "db-upgrade")
	s.answer("orders-1: ", "orders-2")
	s.answer(reviewPrompt, "q")
	s.wait()
	s.notSeen("⚠ run")

	// Resume: the same run carries on, and now succeeds.
	os.WriteFile(filepath.Join(h.dir, "ok"), nil, 0o644)
	s = h.start("run", "db-upgrade")
	s.answer("orders-1: ", "")
	s.answer("[r]esume it / [n]ew run anyway / [q]uit", "r")
	s.expect("resuming " + failed.ID + " at step 2 (upgrade)")
	s.answer(reviewPrompt, "")
	s.expect("run succeeded")
	if c := s.wait(); c != 0 {
		t.Fatalf("exit %d", c)
	}
	if r := h.record(failed.ID); r.Status != engine.StatusSucceeded {
		t.Fatalf("resumed run: %s", r.Status)
	}

	// Done now: a new run isn't asked about.
	s = h.start("run", "db-upgrade")
	s.answer("orders-1: ", "")
	s.answer(reviewPrompt, "q")
	s.wait()
	s.notSeen("⚠ run")

	// Unattended, it's a note, and the run goes ahead.
	os.Remove(filepath.Join(h.dir, "ok"))
	h.run("run", "db-upgrade", "-u", "-v", "cluster=orders-3")
	out, _ := h.run("run", "db-upgrade", "-u", "-v", "cluster=orders-3")
	if !strings.Contains(out, "note: run ") || !strings.Contains(out, "for cluster=orders-3 stopped at step 2") {
		t.Fatalf("unattended: %s", out)
	}
}

// Dangerous steps run like any other, whatever was skipped before them;
// pausing before them is the operator's choice, set in the review.
func TestCLIPauseDangerous(t *testing.T) {
	h := newCLIHome(t)
	h.runbook(`name: cutover
steps:
  - {name: prep, run: "echo prep"}
  - {name: drain, run: "echo drain"}
  - {name: switch traffic, run: "echo switched", dangerous: true}
`)
	// Skipping drain doesn't make switch traffic ask for anything.
	s := h.start("run", "cutover")
	s.expect("⚠ dangerous")
	s.answer(reviewPrompt, "2")
	s.expect("drain (skip)")
	s.answer(reviewPrompt, "")
	s.expect("run succeeded")
	if c := s.wait(); c != 0 {
		t.Fatalf("exit %d", c)
	}
	s.notSeen("Type 3")

	// danger in the review: it stops before switch traffic, and only there.
	s = h.start("run", "cutover")
	s.answer(reviewPrompt, "danger")
	s.expect("pausing before dangerous steps")
	s.answer(reviewPrompt, "")
	s.expect("[3/3] switch traffic")
	s.answer("[r]un / [s]kip", "s")
	s.expect("run succeeded")
	s.wait()
	if strings.Count(s.text(), "[r]un / [s]kip") != 1 {
		t.Fatalf("paused more than once:\n%s", s.text())
	}
	r := h.runs("cutover")[0]
	if st := h.record(r.ID).Steps[2]; st.Status != engine.StatusSkipped || st.Note != "skipped by operator" {
		t.Fatalf("switch traffic: %+v", st)
	}

	// --pause-dangerous, unattended: no one to ask, so no pause.
	if out := h.must("run", "cutover", "-u", "--pause-dangerous"); !strings.Contains(out, "switched") {
		t.Fatalf("unattended: %s", out)
	}
}

// A new run isn't compared with an earlier one unless asked: a rerun is,
// and --compare.
func TestCLICompare(t *testing.T) {
	h := newCLIHome(t)
	h.runbook(`name: deploy
steps:
  - {name: build, run: "echo built"}
`)
	h.must("run", "deploy", "-u")
	first := h.runs("deploy")[0].ID

	s := h.start("run", "deploy")
	s.answer(reviewPrompt, "q")
	s.wait()
	s.notSeen("last time")

	s = h.start("rerun", first)
	s.expect("last time = run " + first)
	s.answer(reviewPrompt, "q")
	s.wait()

	s = h.start("run", "deploy", "--compare", first)
	s.expect("last time = run " + first)
	s.answer(reviewPrompt, "l 1")
	s.expect("built")
	s.send("")
	s.answer(reviewPrompt, "q")
	s.wait()
}

// The review: skip and include steps, step through everything, view a
// step, help; at a failure, skip it and the run carries on.
func TestCLIReviewAndFailure(t *testing.T) {
	h := newCLIHome(t)
	h.runbook(`name: rollout
steps:
  - {name: one, run: "echo one-ran"}
  - {name: two, run: "echo two-ran"}
  - {name: flaky, run: "` + untilOK(h) + `"}
  - {name: last, run: "echo last-ran"}
`)
	s := h.start("run", "rollout")
	s.answer(reviewPrompt, "?")
	s.expect("pause before dangerous steps (toggle)")
	s.answer(reviewPrompt, "2")
	s.expect("two (skip)")
	s.answer(reviewPrompt, "2")
	s.expect("Plan (4 steps)")
	s.answer(reviewPrompt, "v 4")
	s.expect("echo last-ran")
	s.send("")
	s.answer(reviewPrompt, "step")
	s.expect("stepping through every step")
	s.answer(reviewPrompt, "step")
	s.answer(reviewPrompt, "")
	s.answer("Step failed. [r]etry / [s]kip / [f]orce success / [a]bort", "s")
	s.expect("last-ran")
	s.expect("run succeeded")
	if c := s.wait(); c != 0 {
		t.Fatalf("exit %d", c)
	}
	r := h.record(h.runs("rollout")[0].ID)
	if st := r.Steps[2]; st.Status != engine.StatusSkipped || st.Note != "skipped after failure" {
		t.Fatalf("flaky: %+v", st)
	}
	if r.Steps[1].Status != engine.StatusSucceeded {
		t.Fatalf("two was put back, yet: %+v", r.Steps[1])
	}

	// Force success takes a reason.
	s = h.start("run", "rollout", "--no-review")
	s.answer("[f]orce success / [a]bort", "f")
	s.answer("reason for accepting this failure (required): ", "known flake")
	s.expect("run succeeded")
	s.wait()
	if st := h.record(h.runs("rollout")[0].ID).Steps[2]; st.Status != engine.StatusForced || !strings.Contains(st.Note, "known flake") {
		t.Fatalf("forced: %+v", st)
	}
}

// Exported runs come back in: a raw log is rebuilt; the record imported
// after it takes its place; importing again leaves it.
func TestCLIExportImport(t *testing.T) {
	from := newCLIHome(t)
	from.runbook(`name: deploy
inputs:
  - {name: target, identifier: true, default: blue}
steps:
  - {name: build, run: "echo built"}
  - {name: ship, run: "echo shipped"}
`)
	from.must("run", "deploy", "-u")
	id := from.runs("deploy")[0].ID
	from.must("note", id, "looked fine")
	out := filepath.Join(from.dir, "out")
	from.must("history", id, "--export", out, "--as", "both", "--only")
	var logFile, jsonFile string
	filepath.Walk(out, func(p string, _ os.FileInfo, _ error) error {
		switch filepath.Ext(p) {
		case ".log":
			logFile = p
		case ".json":
			jsonFile = p
		}
		return nil
	})
	if logFile == "" || jsonFile == "" {
		t.Fatalf("export wrote %q %q", logFile, jsonFile)
	}

	to := newCLIHome(t)
	if o := to.must("history", "--import", logFile); !strings.Contains(o, "from a raw log") {
		t.Fatalf("raw: %s", o)
	}
	if r := to.record(id); !r.FromLog || r.Inputs["target"] != "blue" {
		t.Fatalf("rebuilt: %+v", r)
	}
	if o := to.must("history", "--import", jsonFile, "--check"); !strings.Contains(o, "would import (its record, in place of the copy rebuilt from its raw log)") {
		t.Fatalf("check: %s", o)
	}
	if r := to.record(id); !r.FromLog {
		t.Fatal("--check changed it")
	}
	to.must("history", "--import", jsonFile)
	r := to.record(id)
	if r.FromLog || r.Steps[1].Output != "shipped\n" || len(r.Notes) != 1 {
		t.Fatalf("record: from_log %v steps %+v notes %v", r.FromLog, r.Steps, r.Notes)
	}
	if o := to.must("history", "--import", logFile); !strings.Contains(o, "already here") {
		t.Fatalf("raw after record: %s", o)
	}
}
