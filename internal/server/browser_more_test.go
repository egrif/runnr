package server

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egrif/runnr/internal/claude"
	"github.com/egrif/runnr/internal/store"
)

// Runbook tests, Explain, libraries and the schedule, in a browser.

// A runbook's tests: the Tests tab runs them and shows what passed and
// what didn't; the editor runs them against what's in it, before saving.
func TestBrowserRunbookTests(t *testing.T) {
	b := newBrowser(t)
	b.runbook("name: greet\nsteps:\n  - {name: say, run: echo hi}\n")
	b.open("#/runbooks/greet/tests")
	b.waitText("No tests yet")

	if _, _, err := b.st.SaveSuite([]byte(`runbook: greet
scenarios:
  - {name: says hi, expect: {status: succeeded, output: {1: "hi"}}}
  - {name: says bye, expect: {output: {1: "bye"}}}
`), store.SaveOpts{}); err != nil {
		t.Fatal(err)
	}
	b.open("#/runbooks/greet/tests")
	b.waitText("says hi")
	b.click("[data-run-all]")
	b.waitText("1/2 passing")
	var pass, fail int
	b.eval(`document.querySelectorAll(".scenario.pass").length`, &pass)
	b.eval(`document.querySelectorAll(".scenario.fail").length`, &fail)
	if pass != 1 || fail != 1 {
		t.Fatalf("%d passed, %d failed", pass, fail)
	}
	// One scenario on its own; its definition opens.
	b.click(`[data-run="says hi"]`)
	b.waitText("1/2 passing")
	b.click(`[data-toggle="says bye"]`)
	b.waitText(`{1: "bye"}`)

	// The editor: against a draft that says bye, both pass; nothing's saved.
	b.open("#/runbooks/greet/edit")
	b.waitFor("#struct-pane")
	b.toYAML()
	b.typeYAML("name: greet\nsteps:\n  - {name: say, run: echo hi; echo bye}\n")
	b.click("#run-tests")
	b.waitText("2/2")
	if strings.Contains(b.raw("greet"), "bye") {
		t.Fatal("running the draft's tests saved it")
	}
	b.noErrors("runbook tests")
}

// fakeClaude answers every question the same way, and keeps what it was asked.
type fakeClaude struct {
	mu      sync.Mutex
	asked   []string
	history []int // how many messages came before each question
}

func (f *fakeClaude) Name() string      { return "anthropic" }
func (f *fakeClaude) ModelName() string { return "fake-model" }
func (f *fakeClaude) Stream(_ context.Context, _ []claude.Block, history []claude.Message, question string, _ int, onText func(string)) (claude.Usage, error) {
	f.mu.Lock()
	f.asked = append(f.asked, question)
	f.history = append(f.history, len(history))
	n := len(f.asked)
	f.mu.Unlock()
	onText("**Answer " + string(rune('0'+n)) + "**: it ")
	onText("snapshots the database.")
	return claude.Usage{Input: 100, Output: 20}, nil
}

func (f *fakeClaude) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.asked)
}

// Explain: a step explained (streamed, as Markdown), a follow-up that
// knows what came before, the same question answered from runnr's store,
// and asked again; a runbook reviewed. With Claude off, none of it shows.
func TestBrowserExplain(t *testing.T) {
	fc := &fakeClaude{}
	b := newBrowser(t, Config{Claude: fc})
	b.runbook("name: upgrade\nsteps:\n  - {name: snapshot, run: echo snap}\n")

	b.open("#/runbooks/upgrade")
	b.click(`#launch-steps [data-open="0"]`)
	b.click(`#launch-steps [data-explain="1"]`)
	b.waitText("snapshots the database.")
	var bold string
	b.eval(`document.querySelector("#explain-panel .ex-a b")?.textContent || ""`, &bold)
	if bold != "Answer 1" {
		t.Fatalf("Markdown: %q", bold)
	}
	b.waitText("fake-model")

	b.setValue("#explain-panel .ex-ask input", "Is it safe to skip?")
	b.eval(`document.querySelector("#explain-panel .ex-ask").requestSubmit(); true`, nil)
	b.eventually("the follow-up", func() bool { return fc.calls() == 2 })
	b.waitText("Is it safe to skip?")
	fc.mu.Lock()
	if fc.history[1] != 2 || fc.asked[1] != "Is it safe to skip?" {
		t.Fatalf("follow-up: %q after %d messages", fc.asked[1], fc.history[1])
	}
	fc.mu.Unlock()

	// The same step again: from runnr's store, free; Ask again asks.
	b.click("#explain-panel [data-ex-close]")
	b.click(`#launch-steps [data-explain="1"]`)
	b.waitText("answered from runnr's store")
	if fc.calls() != 2 {
		t.Fatalf("asked Claude again: %d calls", fc.calls())
	}
	b.click("#explain-panel .ex-usage button")
	b.eventually("asking again", func() bool { return fc.calls() == 3 })

	// The runbook as a whole.
	b.open("#/runbooks/upgrade")
	b.click(`.claude-only [data-explain="0"]`)
	b.waitText("Review: upgrade")
	b.eventually("the review", func() bool { return fc.calls() == 4 })
	b.noErrors("explain")

	// Without Claude: no Explain or Review buttons.
	off := newBrowser(t)
	off.runbook("name: upgrade\nsteps:\n  - {name: snapshot, run: echo snap}\n")
	off.open("#/runbooks/upgrade")
	off.waitText("snapshot")
	var shown int
	off.eval(`[...document.querySelectorAll(".claude-only, .explain-btn")].filter((e) => e.getClientRects().length).length`, &shown)
	if shown != 0 {
		t.Fatalf("%d Claude buttons with Claude off", shown)
	}
}

// Libraries: a new one (checked as it's typed), its functions listed; an
// edit makes a version; a runbook that uses it says so; delete asks.
func TestBrowserLibraries(t *testing.T) {
	b := newBrowser(t)
	b.open("#/libraries")
	b.waitText("std") // runnr's own
	b.click("#new-lib")
	b.waitFor("#yaml")
	b.typeYAML("name: db\nfunctions: [")
	b.until(`document.querySelector("#validation").classList.contains("bad")`, "a validation error")
	b.typeYAML("name: db\ndescription: Database helpers.\nfunctions:\n  backup:\n    description: Take a backup.\n    body: echo backing up\n")
	b.waitText("✔ valid")
	b.click("#save")
	b.eventually("saving", func() bool { _, err := b.st.LoadLibrary("db"); return err == nil })
	b.waitText("Take a backup.")
	b.waitText("libraries: [db]")

	// An edit: a second version, a second function.
	b.typeYAML("name: db\nfunctions:\n  backup: {body: echo backing up}\n  restore: {description: Restore one., body: echo restoring}\n")
	b.click("#save")
	b.waitText("Restore one.")
	if l, _ := b.st.LoadLibrary("db"); !strings.Contains(string(l.Raw), "restore") {
		t.Fatalf("saved: %s", l.Raw)
	}

	b.open("#/libraries")
	b.waitText("backup, restore")
	b.click(`tr[data-lib="db"]`)
	b.waitFor("#delete")
	b.eval(`window.confirm = () => true; true`, nil)
	b.click("#delete")
	b.eventually("deleting", func() bool { _, err := b.st.LoadLibrary("db"); return err != nil })
	b.noErrors("libraries")
}

// The schedule: a run planned from its page (with an input), shown in the
// agenda and the calendar, opened, changed, started (its inputs filled
// in) and deleted.
func TestBrowserSchedule(t *testing.T) {
	b := newBrowser(t)
	b.runbook("name: cutover\ninputs:\n  - {name: cluster}\nsteps:\n  - {name: switch, run: echo switched}\n")
	at := time.Now().Add(48 * time.Hour).Truncate(time.Minute)
	b.open("#/schedule")
	b.click("#new-sched")
	b.waitFor(`dialog[open] [data-in="cluster"]`)
	b.setValue("#sd-at", at.Format("2006-01-02T15:04"))
	b.setValue("#sd-title", "orders cutover")
	b.setValue(`dialog[open] [data-in="cluster"]`, "orders-1")
	b.click("dialog[open] [data-save]")
	var sc store.Schedule
	b.eventually("scheduling", func() bool {
		ss, _ := b.st.ListSchedules(time.Now().Add(-time.Hour))
		if len(ss) == 1 {
			sc = ss[0]
		}
		return len(ss) == 1
	})
	if !sc.At.Equal(at) || sc.Inputs["cluster"] != "orders-1" || sc.Title != "orders cutover" {
		t.Fatalf("saved: %+v (at %v)", sc, at)
	}

	// The agenda and the calendar both show it.
	b.click(`[data-view="agenda"]`)
	b.waitText("orders cutover")
	b.click(`[data-view="calendar"]`)
	b.waitFor(`[data-sched="` + sc.ID + `"]`)

	// Opened: retitled.
	b.click(`[data-sched="` + sc.ID + `"]`)
	b.waitFor("dialog[open] #sd-title")
	b.setValue("#sd-title", "orders-1 cutover")
	b.click("dialog[open] [data-save]")
	b.eventually("saving", func() bool { s, _ := b.st.GetSchedule(sc.ID); return s != nil && s.Title == "orders-1 cutover" })

	// Start this run…: the launch screen, its input filled in.
	b.click(`[data-sched="` + sc.ID + `"]`)
	b.click("dialog[open] [data-start]")
	b.until(`document.querySelector('[name="cluster"]')?.value === "orders-1"`, "the scheduled input")

	// Deleted (asked first).
	b.open("#/schedule")
	b.click(`[data-sched="` + sc.ID + `"]`)
	b.eval(`window.confirm = () => true; true`, nil)
	b.click("dialog[open] [data-del]")
	b.eventually("deleting", func() bool { s, _ := b.st.GetSchedule(sc.ID); return s == nil })
	b.noErrors("schedule")
}
