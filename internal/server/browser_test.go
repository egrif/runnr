package server

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/store"
)

// The web interface in a real browser (headless Chrome): pages load
// without errors, and what an operator does on them works. Skipped where
// there's no Chrome (RUNNR_BROWSER_TESTS=1 makes that a failure, as CI does).

func chromePath() string {
	for _, p := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"} {
		if path, err := exec.LookPath(p); err == nil {
			return path
		}
	}
	return ""
}

// browser is a server with its own store, and a browser tab on it.
type browser struct {
	t    *testing.T
	st   *store.Store
	url  string
	ctx  context.Context
	mu   sync.Mutex
	errs []string // uncaught exceptions and console errors
}

func newBrowser(t *testing.T) *browser {
	t.Helper()
	chrome := chromePath()
	if chrome == "" {
		if os.Getenv("RUNNR_BROWSER_TESTS") != "" {
			t.Fatal("no Chrome to run the browser tests in")
		}
		t.Skip("no Chrome: browser tests skipped")
	}
	st, err := store.Open(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(New(st, Config{}).Handler())
	t.Cleanup(ts.Close)
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chrome), chromedp.WindowSize(1400, 1000), chromedp.Flag("no-sandbox", true))
	actx, cancelA := chromedp.NewExecAllocator(context.Background(), opts...)
	// Only Navigate and Evaluate are used: they work whichever Chrome this
	// is, where chromedp's DOM tracking needs one its protocol knows (so
	// its complaints about newer events are left out).
	ctx, cancelC := chromedp.NewContext(actx, chromedp.WithErrorf(func(string, ...any) {}))
	ctx, cancelT := context.WithTimeout(ctx, 90*time.Second)
	t.Cleanup(func() { cancelT(); cancelC(); cancelA() })
	b := &browser{t: t, st: st, url: ts.URL, ctx: ctx}
	chromedp.ListenTarget(ctx, func(ev any) {
		switch e := ev.(type) {
		case *runtime.EventExceptionThrown:
			b.mu.Lock()
			b.errs = append(b.errs, e.ExceptionDetails.Error())
			b.mu.Unlock()
		case *runtime.EventConsoleAPICalled:
			if e.Type == runtime.APITypeError {
				var parts []string
				for _, a := range e.Args {
					parts = append(parts, strings.Trim(string(a.Value), `"`))
				}
				b.mu.Lock()
				b.errs = append(b.errs, "console.error: "+strings.Join(parts, " "))
				b.mu.Unlock()
			}
		}
	})
	if err := chromedp.Run(ctx); err != nil {
		t.Fatal(err)
	}
	return b
}

func (b *browser) run(actions ...chromedp.Action) {
	b.t.Helper()
	if err := chromedp.Run(b.ctx, actions...); err != nil {
		b.t.Fatalf("%v\npage: %s", err, b.text())
	}
}

// open loads a page (by its hash route) from scratch.
func (b *browser) open(hash string) {
	b.t.Helper()
	b.run(chromedp.Navigate("about:blank"), chromedp.Navigate(b.url+"/"+hash))
	b.until(`!!document.querySelector("#topbar") && document.readyState === "complete"`, "page")
}

// until waits for js to be true.
func (b *browser) until(js, what string) {
	b.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var ok bool
		if err := chromedp.Run(b.ctx, chromedp.Evaluate(js, &ok)); err == nil && ok {
			return
		}
		if time.Now().After(deadline) {
			b.t.Fatalf("no %s after 15s; the page:\n%s", what, b.text())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitFor waits for an element to be on the page and shown.
func (b *browser) waitFor(sel string) {
	b.t.Helper()
	b.until(fmt.Sprintf(`(() => { const e = document.querySelector(%q); return !!e && e.getClientRects().length > 0 })()`, sel), sel)
}

// setValue types a value into a field.
func (b *browser) setValue(sel, v string) {
	b.t.Helper()
	b.waitFor(sel)
	b.eval(fmt.Sprintf(`(() => { const e = document.querySelector(%q); e.value = %q; e.dispatchEvent(new Event("input", {bubbles: true})); e.dispatchEvent(new Event("change", {bubbles: true})); return true })()`, sel, v), nil)
}

func (b *browser) eval(js string, out any) {
	b.t.Helper()
	b.run(chromedp.Evaluate(js, out, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }))
}

// text is the page's visible text.
func (b *browser) text() string {
	var s string
	if err := chromedp.Run(b.ctx, chromedp.Evaluate(`document.body ? document.body.innerText : ""`, &s)); err != nil {
		return "(couldn't read the page: " + err.Error() + ")"
	}
	return s
}

// waitText waits for text to show on the page.
func (b *browser) waitText(want string) {
	b.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !strings.Contains(b.text(), want) {
		if time.Now().After(deadline) {
			b.t.Fatalf("no %q on the page:\n%s", want, b.text())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (b *browser) click(sel string) {
	b.t.Helper()
	b.waitFor(sel)
	b.eval(fmt.Sprintf(`(() => { document.querySelector(%q).click(); return true })()`, sel), nil)
}

// noErrors fails if a page threw or logged an error.
func (b *browser) noErrors(where string) {
	b.t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.errs) > 0 {
		b.t.Fatalf("%s: %s", where, strings.Join(b.errs, "\n"))
	}
}

func (b *browser) runbook(yaml string) {
	b.t.Helper()
	if _, _, err := b.st.Save([]byte(yaml), store.SaveOpts{}); err != nil {
		b.t.Fatal(err)
	}
}

const browserRB = `name: cutover
inputs:
  - {name: cluster, identifier: true, default: orders-1}
steps:
  - {name: prep, run: "echo prep"}
  - {name: switch traffic, run: "echo switched", dangerous: true}
`

// Every page loads, without an error.
func TestBrowserPagesLoad(t *testing.T) {
	b := newBrowser(t)
	b.runbook(browserRB)
	rb, _, _ := b.st.Get("cutover")
	run := engine.Execute(context.Background(), rb, engine.Unattended{}, "", engine.Options{Sanctioned: true})
	b.st.SaveRun(run)
	b.st.AddToCollection("ops", true, "cutover")
	for _, p := range []struct{ hash, want string }{
		{"#/", "cutover"},
		{"#/runbooks/cutover", "switch traffic"},
		{"#/runbooks/cutover/edit", "cutover"},
		{"#/history", run.ID},
		{"#/runs/" + run.ID, "succeeded"},
		{"#/runs/" + run.ID + "/console", "switch traffic"},
		{"#/schedule", "Schedule"},
		{"#/collections", "1 runbook"},
		{"#/libraries", "Libraries"},
		{"#/settings", "Folders"},
		{"#/manage", "Manage"},
		{"#/help", "Runbook"},
	} {
		b.open(p.hash)
		b.waitText(p.want)
		b.noErrors(p.hash)
	}
	// The ⚙ menu's About.
	b.click("#settings-menu summary")
	b.click("#about-btn")
	b.waitText("Copy details")
	b.noErrors("about")
}

// A new run shows nothing of earlier runs unless asked; starting one while
// the latest for the same cluster stopped partway asks first.
func TestBrowserUnfinishedRun(t *testing.T) {
	b := newBrowser(t)
	b.runbook(strings.Replace(browserRB, `"echo prep"`, `"exit 1"`, 1))
	rb, _, _ := b.st.Get("cutover")
	failed := engine.Execute(context.Background(), rb, engine.Unattended{}, "", engine.Options{Sanctioned: true, Vars: map[string]string{"cluster": "orders-1"}})
	if failed.Status != engine.StatusFailed {
		t.Fatalf("setup: %s", failed.Status)
	}
	b.st.SaveRun(failed)

	b.open("#/runbooks/cutover")
	b.waitText("switch traffic")
	var marks int
	b.eval(`document.querySelectorAll("#launch-steps .last").length`, &marks)
	if marks != 0 {
		t.Fatalf("%d last-time marks on a new run", marks)
	}
	// A step opens on its Details, not an earlier run.
	b.click(`#launch-steps [data-open="0"]`)
	var tab string
	b.eval(`document.querySelector("#launch-steps .side-tabs .on")?.dataset.sideTab`, &tab)
	if tab != "details" {
		t.Fatalf("step opened on %q", tab)
	}

	// Run: asked about the failed one. Cancel stays here.
	b.click("#start")
	b.waitText("Another run isn't finished")
	b.waitText(failed.ID)
	b.waitText("stopped at step 1 (prep)")
	b.click(`dialog[open] [data-v=""]`)
	var open bool
	b.eval(`!!document.querySelector("dialog[open]")`, &open)
	if open {
		t.Fatal("the dialog stayed open")
	}
	// Resume it…: the resume screen for that run.
	b.click("#start")
	b.click(`dialog[open] [data-v="resume"]`)
	b.waitText("Resume of run " + failed.ID)
	b.noErrors("unfinished")

	// Another cluster isn't asked about: it starts.
	b.open("#/runbooks/cutover")
	b.setValue(`[name="cluster"]`, "orders-2")
	b.click("#start")
	b.waitText("cluster=orders-2")
	var dialogs int
	b.eval(`document.querySelectorAll("dialog").length`, &dialogs)
	if dialogs != 0 {
		t.Fatal("asked about another cluster")
	}
	b.noErrors("new run")
}

// Pause before dangerous steps: chosen on the launch screen, it stops
// before switch traffic; the live run's checkbox shows it's on.
func TestBrowserPauseDangerous(t *testing.T) {
	b := newBrowser(t)
	b.runbook(browserRB)
	b.open("#/runbooks/cutover")
	b.click(`[name="danger"]`)
	b.click("#start")
	b.waitText("Next: switch traffic")
	var on bool
	b.eval(`document.querySelector("#pause-danger")?.checked === true`, &on)
	if !on {
		t.Fatalf("live run's Pause before dangerous steps isn't on:\n%s", b.text())
	}
	b.click(`.question [data-v="run"]`)
	var id string
	b.eval(`location.hash.split("/")[2]`, &id)
	deadline := time.Now().Add(15 * time.Second)
	r, err := b.st.LoadRun(id)
	for err == nil && r.Status == engine.StatusRunning && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		r, err = b.st.LoadRun(id)
	}
	if err != nil || r.Steps[1].Status != engine.StatusSucceeded || r.Steps[1].Output != "switched\n" {
		t.Fatalf("run %s: %v %+v", id, err, r)
	}
	b.noErrors("pause dangerous")
}

// The browser tests' runbook has a dangerous step; so does the console's
// launch toolbar.
func TestBrowserConsoleLaunch(t *testing.T) {
	b := newBrowser(t)
	b.runbook(browserRB)
	b.open("#/runbooks/cutover/console")
	b.waitText("⚠ Pause before dangerous")
	b.click(`[data-opt="danger"]`)
	var on bool
	b.eval(`document.querySelector('[data-opt="danger"]').classList.contains("on")`, &on)
	if !on {
		t.Fatal("toggle didn't turn on")
	}
	b.noErrors(fmt.Sprintf("console %v", on))
}
