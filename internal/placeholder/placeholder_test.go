package placeholder

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestExpand(t *testing.T) {
	got, err := Expand(map[string]string{
		ReposRoot:  "/src",
		RepoRoot:   "@REPOS_ROOT@/ops-runbooks",
		ProjectDir: "@REPO_ROOT@/projects/rds",
		"OTHER":    "@NOPE@/x",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{ReposRoot: "/src", RepoRoot: "/src/ops-runbooks", ProjectDir: "/src/ops-runbooks/projects/rds", "OTHER": "@NOPE@/x"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	if _, err := Expand(map[string]string{"A": "@B@", "B": "@A@/x"}); err == nil || !strings.Contains(err.Error(), "circle") {
		t.Fatalf("a circle: %v", err)
	}
}

type step struct {
	Name string
	Run  string
	Env  map[string]string
	Args []string
	Opt  *step
	Any  any
	priv string
}

func TestUsedAndApply(t *testing.T) {
	s := &step{
		Name: "plain", Run: "cd @PROJECT_DIR@/tools && ./x",
		Env:  map[string]string{"D": "@REPO_ROOT@"},
		Args: []string{"@REPOS_ROOT@", "user@example.com"},
		Opt:  &step{Run: "@PROJECT_DIR@/y", Any: "@REPO_ROOT@/z"},
		priv: "@HIDDEN@",
	}
	if got := Used(s); !reflect.DeepEqual(got, []string{ProjectDir, ReposRoot, RepoRoot}) {
		t.Fatalf("used %v", got)
	}
	Apply(map[string]string{ProjectDir: "/p", RepoRoot: "/r", ReposRoot: "/s"}, s)
	if s.Run != "cd /p/tools && ./x" || s.Env["D"] != "/r" || s.Args[0] != "/s" || s.Args[1] != "user@example.com" || s.Opt.Run != "/p/y" || s.Opt.Any != "/r/z" || s.priv != "@HIDDEN@" {
		t.Fatalf("applied %+v %+v", s, s.Opt)
	}
}

func TestCheck(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	os.WriteFile(file, nil, 0o644)
	got := Check([]string{"A", "B", "C", "D", "E", "F"}, map[string]string{
		"A": dir, "B": "", "C": "rel/path", "D": filepath.Join(dir, "gone"), "E": file, "F": "@NOPE@/x",
	})
	var whys []string
	for _, p := range got {
		whys = append(whys, p.Name+": "+p.Why)
	}
	want := []string{"B: has no value", "C: isn't a full path", "D: isn't a folder here", "E: is a file, not a folder", "F: names @NOPE@, which has no value"}
	if !reflect.DeepEqual(whys, want) {
		t.Fatalf("got %q", whys)
	}
}
