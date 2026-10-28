package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"context"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/store"
)

// The editor, and the dialogs that import and export, in a browser.

// typeYAML puts text in the editor's YAML box, as typing would.
func (b *browser) typeYAML(text string) {
	b.t.Helper()
	b.setValue("#yaml", text)
}

// toYAML switches the editor to its YAML view.
func (b *browser) toYAML() {
	b.t.Helper()
	b.click("#as-yaml")
	b.waitFor("#yaml")
}

// eventually waits for cond (checked in Go) to hold.
func (b *browser) eventually(what string, cond func() bool) {
	b.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			b.t.Fatalf("%s didn't happen; the page:\n%s", what, b.text())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// hash is the page's route.
func (b *browser) hash() string {
	var h string
	b.eval(`location.hash`, &h)
	return h
}

func (b *browser) raw(name string) string {
	d, err := b.st.Load(name)
	if err != nil {
		return ""
	}
	return string(d.Raw)
}

// A new runbook written as YAML: it validates as you type, and saving
// keeps it as written, comments and all.
func TestBrowserEditorNew(t *testing.T) {
	b := newBrowser(t)
	b.open("#/new")
	b.waitFor("#struct-pane") // it opens structured
	b.toYAML()

	b.typeYAML("name: broken\nsteps: [")
	b.until(`document.querySelector("#validation").classList.contains("bad")`, "a validation error")

	yaml := "# Restarts the web tier.\nname: restart-web\nsteps:\n  - name: restart # one at a time\n    run: echo restarting\n"
	b.typeYAML(yaml)
	b.waitText("✔ valid")
	b.click("#save")
	b.eventually("saving", func() bool { return b.hash() == "#/runbooks/restart-web" })
	if got := b.raw("restart-web"); got != yaml {
		t.Fatalf("saved:\n%s", got)
	}
	b.noErrors("new runbook")
}

// The structured view: add a step and name it; the YAML has it, with the
// comments as they were; saving makes a version. A version saved by
// someone else meanwhile isn't written over. Delete asks first.
func TestBrowserEditorStructured(t *testing.T) {
	b := newBrowser(t)
	b.runbook("name: deploy\n# Build first.\nsteps:\n  - name: build # cached\n    run: echo built\n")
	b.open("#/runbooks/deploy/edit")
	b.waitFor("#struct-pane")
	b.waitText("build")

	b.click(`#struct-pane [data-act="add-step"]`)
	b.waitFor(`[data-path="steps.1.name"]`)
	b.eval(`(() => { const e = document.querySelector('[data-path="steps.1.name"]'); e.value = "verify"; e.dispatchEvent(new Event("input", {bubbles: true})); e.dispatchEvent(new Event("change", {bubbles: true})); return true })()`, nil)
	b.waitText("verify")

	b.toYAML()
	var y string
	b.eval(`document.querySelector("#yaml").value`, &y)
	for _, want := range []string{"# Build first.", "# cached", "name: verify", "run: echo hello"} {
		if !strings.Contains(y, want) {
			t.Fatalf("YAML lacks %q:\n%s", want, y)
		}
	}
	b.click("#save")
	b.eventually("saving", func() bool { return strings.Contains(b.raw("deploy"), "name: verify") })
	if vs, _ := b.st.Versions("deploy"); len(vs) != 2 {
		t.Fatalf("%d versions", len(vs))
	}

	// Someone saves meanwhile: this editor's save is refused, not lost.
	b.open("#/runbooks/deploy/edit")
	b.waitFor("#struct-pane")
	if _, _, err := b.st.Save([]byte(b.raw("deploy")+"description: theirs\n"), store.SaveOpts{OldName: "deploy"}); err != nil {
		t.Fatal(err)
	}
	b.toYAML()
	b.typeYAML(strings.Replace(b.raw("deploy"), "description: theirs\n", "", 1) + "description: mine\n")
	b.click("#save")
	b.waitText("Someone saved a newer version")
	if !strings.Contains(b.raw("deploy"), "theirs") {
		t.Fatal("their version was written over")
	}

	// Delete: asked first; no keeps it.
	b.eval(`window.confirm = () => false; true`, nil)
	b.click("#delete")
	if b.raw("deploy") == "" {
		t.Fatal("deleted though the answer was no")
	}
	b.eval(`window.confirm = () => true; true`, nil)
	b.click("#delete")
	b.eventually("deleting", func() bool { return !b.st.Exists("deploy") })
	b.noErrors("editor")
}

// Importing files: a new runbook is added; one whose name is taken shows
// what would change and can be made the current version or imported
// under another name.
func TestBrowserImportFiles(t *testing.T) {
	b := newBrowser(t)
	b.runbook("name: deploy\nsteps:\n  - {name: build, run: echo built}\n")
	b.open("#/")
	b.waitText("deploy")
	files := `[
		new File(["name: fresh\nsteps:\n  - {name: a, run: echo a}\n"], "fresh.yaml"),
		new File(["name: deploy\nsteps:\n  - {name: build, run: echo built twice}\n"], "deploy.yaml"),
	]`
	b.eval(`openImport(`+files+`); true`, nil)
	b.waitText("Adds runbook fresh")
	b.waitText("⚠ runbook deploy already exists")
	b.waitText("Show what it changes") // the diff, folded
	// deploy: as the current version.
	b.click(`dialog[open] input[name="ch1"][value="replace"]`)
	b.click(`dialog[open] [data-ok]`)
	b.eventually("importing", func() bool {
		return strings.Contains(b.raw("deploy"), "built twice") && b.st.Exists("fresh")
	})
	if vs, _ := b.st.Versions("deploy"); len(vs) != 2 {
		t.Fatalf("deploy: %d versions", len(vs))
	}

	// Again, as another name: deploy is left as it is.
	b.open("#/")
	b.eval(`openImport([new File(["name: deploy\nsteps:\n  - {name: build, run: echo third}\n"], "deploy.yaml")]); true`, nil)
	b.waitText("already exists")
	b.setValue(`dialog[open] [data-as="0"]`, "deploy-copy")
	b.click(`dialog[open] [data-ok]`)
	b.eventually("importing a copy", func() bool { return strings.Contains(b.raw("deploy-copy"), "echo third") })
	if strings.Contains(b.raw("deploy"), "third") {
		t.Fatal("deploy was changed")
	}
	b.noErrors("import files")
}

// Importing a project folder: Import… in the top bar, a folder on this
// machine, its runbook and the library it uses.
func TestBrowserImportFolder(t *testing.T) {
	b := newBrowser(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "helpers.yaml"), []byte("name: helpers\nfunctions:\n  hi: {body: 'echo hi'}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "greet.yaml"), []byte("name: greet\nlibraries: [helpers]\nsteps:\n  - {name: say, run: hi}\n"), 0o644)
	b.open("#/")
	b.click("#import-btn")
	b.setValue("dialog[open] .imp-dir", dir)
	b.click("dialog[open] [data-scan]")
	b.waitText("greet.yaml")
	b.waitText("helpers.yaml")
	b.click("dialog[open] [data-ok]")
	b.eventually("importing", func() bool { return b.st.Exists("greet") })
	if _, err := b.st.LoadLibrary("helpers"); err != nil {
		t.Fatalf("library: %v", err)
	}
	b.waitText("Done")
	b.noErrors("import folder")
}

// Export a runbook to files, with its library; a second export asks
// before writing over them.
func TestBrowserExportRunbook(t *testing.T) {
	b := newBrowser(t)
	if _, _, err := b.st.SaveLibrary([]byte("name: helpers\nfunctions:\n  hi: {body: 'echo hi'}\n"), store.SaveOpts{}); err != nil {
		t.Fatal(err)
	}
	b.runbook("name: greet\nlibraries: [helpers]\nsteps:\n  - {name: say, run: hi}\n")
	dir := t.TempDir()
	target := filepath.Join(dir, "greet.yaml")
	exportTo := func() {
		b.open("#/runbooks/greet")
		b.click(".page-head details.menu summary")
		b.click("#export-rb")
		b.setValue("dialog[open] .exp-path", target)
		b.waitText(filepath.Join(dir, "helpers.yaml")) // planned for the path given
	}
	exportTo()
	b.click("dialog[open] [data-ok]")
	b.eventually("export", func() bool {
		_, e1 := os.Stat(target)
		_, e2 := os.Stat(filepath.Join(dir, "helpers.yaml"))
		return e1 == nil && e2 == nil
	})

	exportTo()
	b.waitText("a file is there")
	var disabled bool
	b.eval(`document.querySelector("dialog[open] [data-ok]").disabled`, &disabled)
	if !disabled {
		t.Fatal("Export allowed over files without asking")
	}
	b.click("dialog[open] [data-overwrite]")
	b.eval(`document.querySelector("dialog[open] [data-ok]").disabled`, &disabled)
	if disabled {
		t.Fatal("Export still off after choosing to write over")
	}
	b.noErrors("export runbook")
}

// A run's logs, exported as records from its page, come back through the
// history page's Import runs.
func TestBrowserExportImportRun(t *testing.T) {
	b := newBrowser(t)
	b.runbook("name: deploy\nsteps:\n  - {name: build, run: echo built}\n")
	rb, _, _ := b.st.Get("deploy")
	run := engine.Execute(context.Background(), rb, engine.Unattended{}, "", engine.Options{Sanctioned: true})
	b.st.SaveRun(run)
	dir := t.TempDir()

	b.open("#/runs/" + run.ID)
	b.waitText("succeeded")
	b.click(".page-head details.menu summary")
	b.click("#export-logs")
	b.click(`dialog[open] input[name="fmt"][value="json"]`)
	b.setValue("dialog[open] .exp-path", dir)
	b.click("dialog[open] [data-ok]")
	var file string
	b.eventually("export", func() bool {
		m, _ := filepath.Glob(filepath.Join(dir, "*.json"))
		if len(m) > 0 {
			file = m[0]
		}
		return file != ""
	})

	if err := b.st.DeleteRun(run.ID); err != nil {
		t.Fatal(err)
	}
	b.open("#/history")
	b.click("[data-import-runs]")
	b.click("dialog[open] [data-folder]")
	// Its first listing (of ~) is in before the folder is typed.
	b.until(`document.querySelector("dialog.path-picker[open] .pp-list")?.children.length > 0`, "the picker's listing")
	b.setValue("dialog.path-picker[open] .pp-path", dir)
	b.click("dialog.path-picker[open] [data-go]")
	// (It may show the folder by another name: /private/var for /var.)
	b.until(fmt.Sprintf(`document.querySelector("dialog.path-picker[open] .pp-path")?.value.endsWith(%q)`, filepath.Join(filepath.Base(filepath.Dir(dir)), filepath.Base(dir))), "the folder chosen")
	b.click("dialog.path-picker[open] [data-ok]")
	b.waitText(run.ID)
	b.waitText("Import 1 run")
	b.click(".run-import [data-ok]")
	b.eventually("import", func() bool { _, err := b.st.LoadRun(run.ID); return err == nil })
	r, _ := b.st.LoadRun(run.ID)
	if r.Imported.IsZero() || r.Steps[0].Output != "built\n" {
		t.Fatalf("imported: %+v", r)
	}
	b.noErrors(fmt.Sprintf("export/import run %s", run.ID))
}
