package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/runbook"
	"github.com/egrif/runnr/internal/store"
)

// A run's folders: its collection's (or one chosen), those for everything,
// and those given for the run; it doesn't start without them.
func TestFolders(t *testing.T) {
	st, err := store.Open(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	src := t.TempDir()
	for _, d := range []string{"ra/projects/rds", "other/projects/rds"} {
		os.MkdirAll(filepath.Join(src, d), 0o755)
	}
	raw := []byte("name: rb\ninputs:\n  - {name: tools, default: \"@PROJECT_DIR@/tools\"}\nsteps:\n  - {name: one, run: 'echo \"$tools @REPO_ROOT@\"'}\n")
	rb, _, err := st.Save(raw, store.SaveOpts{})
	if err != nil {
		t.Fatal(err)
	}
	used := []string{"PROJECT_DIR", "REPO_ROOT"}
	resolve := func(o FolderOpts) *Folders {
		t.Helper()
		f, err := ResolveFolders(st, "rb", used, o)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}

	// None set: it can't start.
	if f := resolve(FolderOpts{}); len(f.Problems) != 2 || f.Err() == nil {
		t.Fatalf("none set: %+v", f)
	}

	// Its collection's, naming the one for everything.
	st.AddToCollection("aurora", true, "rb")
	st.SetPlaceholder("", "REPOS_ROOT", src)
	st.SetPlaceholder("aurora", "REPO_ROOT", "@REPOS_ROOT@/ra")
	st.SetPlaceholder("aurora", "PROJECT_DIR", "@REPO_ROOT@/projects/rds")
	f := resolve(FolderOpts{})
	if f.Err() != nil || f.Collection != "aurora" || f.Values["PROJECT_DIR"] != filepath.Join(src, "ra/projects/rds") || f.From["REPO_ROOT"] != "collection" {
		t.Fatalf("from its collection: %+v", f)
	}

	// In two that disagree: choose one, or give it for the run.
	st.AddToCollection("second", true, "rb")
	st.SetPlaceholder("second", "REPO_ROOT", filepath.Join(src, "other"))
	if f := resolve(FolderOpts{}); f.Err() == nil || !strings.Contains(f.Err().Error(), "differs between collections") {
		t.Fatalf("disagreeing: %+v", f)
	}
	if f := resolve(FolderOpts{Collection: "second"}); len(f.Problems) != 1 || f.Problems[0].Name != "PROJECT_DIR" {
		t.Fatalf("second has no PROJECT_DIR: %+v", f)
	}
	f = resolve(FolderOpts{Set: map[string]string{"REPO_ROOT": filepath.Join(src, "other")}})
	if f.Err() != nil || f.From["REPO_ROOT"] != "run" || f.From["PROJECT_DIR"] != "collection" {
		t.Fatalf("given for the run: %+v", f)
	}

	// A renamed collection keeps its folders.
	if _, _, err := st.UpdateCollection("aurora", ptr("aurora2"), nil, nil); err != nil {
		t.Fatal(err)
	}
	if vals, _ := st.Placeholders("aurora2"); vals["REPO_ROOT"] != "@REPOS_ROOT@/ra" {
		t.Fatalf("after rename: %v", vals)
	}

	// The run fills them in; without them, it doesn't start.
	f = resolve(FolderOpts{Collection: "aurora2"})
	run := engine.Execute(context.Background(), rb, engine.Unattended{}, "", engine.Options{Folders: f.Values, Collection: f.Collection})
	if run.Status != engine.StatusSucceeded || run.Folders["PROJECT_DIR"] != f.Values["PROJECT_DIR"] || run.Collection != "aurora2" || run.Inputs["tools"] != f.Values["PROJECT_DIR"]+"/tools" {
		t.Fatalf("run %s %s inputs %v folders %v", run.Status, run.Error, run.Inputs, run.Folders)
	}
	// Exported, its folders are the placeholders again.
	x := Masked(st, []*engine.Run{run})[0]
	out := x.Inputs["tools"] + " " + x.Steps[0].Command + " " + x.Steps[0].Output
	if strings.Contains(out, src) || x.Inputs["tools"] != "@PROJECT_DIR@/tools" || !strings.Contains(x.Steps[0].Output, "@REPO_ROOT@") || x.Folders != nil || x.Collection != "aurora2" {
		t.Fatalf("exported: %q folders %v", out, x.Folders)
	}
	if run.Folders == nil || !strings.Contains(run.Inputs["tools"], src) {
		t.Fatal("the run itself changed")
	}
	if got := replacePath("/a/repo /a/repo/x /a/repository", "/a/repo", "@R@"); got != "@R@ @R@/x /a/repository" {
		t.Fatalf("replacePath: %q", got)
	}

	rb2, _ := runbook.Parse(raw)
	run = engine.Execute(context.Background(), rb2, engine.Unattended{}, "", engine.Options{})
	if run.Status != engine.StatusFailed || !strings.Contains(run.Error, "can't start") || !run.Begun.IsZero() {
		t.Fatalf("without folders: %s %q", run.Status, run.Error)
	}
	if !errors.Is(engine.FoldersError(nil), engine.ErrFolders) {
		t.Fatal("not ErrFolders")
	}
}

func ptr(s string) *string { return &s }

// Folders given for a run become the runbook's own: they win over its
// collection's, settle where its collections disagree, and follow a rename.
func TestRunbookFolders(t *testing.T) {
	st, err := store.Open(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, b := t.TempDir(), t.TempDir()
	if _, _, err := st.Save([]byte("name: rb\nsteps:\n  - {name: one, run: 'ls @REPO_ROOT@'}\n"), store.SaveOpts{}); err != nil {
		t.Fatal(err)
	}
	st.AddToCollection("one", true, "rb")
	st.AddToCollection("two", true, "rb")
	st.SetPlaceholder("one", "REPO_ROOT", a)
	st.SetPlaceholder("two", "REPO_ROOT", b)
	used := []string{"REPO_ROOT"}
	if f, _ := ResolveFolders(st, "rb", used, FolderOpts{}); f.Err() == nil {
		t.Fatal("collections disagree, yet it can start")
	}
	if err := SaveRunFolders(st, "rb", map[string]string{"REPO_ROOT": b}); err != nil {
		t.Fatal(err)
	}
	f, _ := ResolveFolders(st, "rb", used, FolderOpts{Collection: "one"})
	if f.Err() != nil || f.Values["REPO_ROOT"] != b || f.From["REPO_ROOT"] != "runbook" {
		t.Fatalf("runbook's own: %+v", f)
	}
	if f, _ := ResolveFolders(st, "rb", used, FolderOpts{}); f.Err() != nil {
		t.Fatalf("settles the disagreement: %+v", f)
	}
	if f, _ := ResolveFolders(st, "rb", used, FolderOpts{Set: map[string]string{"REPO_ROOT": a}}); f.From["REPO_ROOT"] != "run" || f.Values["REPO_ROOT"] != a {
		t.Fatalf("the run's wins: %+v", f)
	}
	raw, _ := st.Raw("rb")
	if _, _, err := st.Save([]byte(strings.Replace(string(raw), "name: rb", "name: rb2", 1)), store.SaveOpts{OldName: "rb"}); err != nil {
		t.Fatal(err)
	}
	if vals, _ := st.Placeholders(store.RunbookScope("rb2")); vals["REPO_ROOT"] != b {
		t.Fatalf("after rename: %v", vals)
	}
}
