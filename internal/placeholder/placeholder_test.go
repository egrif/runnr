package placeholder

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestResolveFromWhereTheFileIs(t *testing.T) {
	repo := t.TempDir()
	os.Mkdir(filepath.Join(repo, ".git"), 0o755)
	file := filepath.Join(repo, "projects", "rds", "runbook", "rb.yaml")
	os.MkdirAll(filepath.Dir(file), 0o755)
	raw := []byte(`a: "@PROJECT_DIR@/tools"
b: "@REPO_ROOT@/helpers"
c: "@FILE_DIR|-1@"
d: "@PROJECT_DIR|-2@"
e: "@TEAM@"
f: "@MISSING@"
`)
	toks := Tokens(raw)
	if want := []string{"FILE_DIR|-1", "MISSING", "PROJECT_DIR", "PROJECT_DIR|-2", "REPO_ROOT", "TEAM"}; !reflect.DeepEqual(toks, want) {
		t.Fatalf("tokens %v", toks)
	}
	vals, missing, err := Resolve(file, map[string]string{"TEAM": "dbre"}, toks)
	if err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(repo, "projects", "rds")
	want := map[string]string{"PROJECT_DIR": proj, "REPO_ROOT": repo, "FILE_DIR|-1": proj, "PROJECT_DIR|-2": repo, "TEAM": "dbre"}
	if !reflect.DeepEqual(vals, want) || !reflect.DeepEqual(missing, []string{"MISSING"}) {
		t.Fatalf("values %v missing %v", vals, missing)
	}
	out, used, miss := Fill(raw, vals)
	if len(used) != 6 || !reflect.DeepEqual(miss, []string{"MISSING"}) || string(out) != `a: "`+proj+`/tools"
b: "`+repo+`/helpers"
c: "`+proj+`"
d: "`+repo+`"
e: "dbre"
f: "@MISSING@"
` {
		t.Fatalf("filled %s %v %v", out, used, miss)
	}
}

func TestProjectDirNeedsAProjectsFolder(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "incident-response", "db", "rb.yaml")
	os.MkdirAll(filepath.Dir(file), 0o755)
	// A folder named "project" (singular) isn't one.
	_, missing, _ := Resolve(filepath.Join(dir, "templates", "project", "runbook", "x.yaml"), nil, []string{"PROJECT_DIR"})
	if len(missing) != 1 {
		t.Fatalf("templates/project: %v", missing)
	}
	if _, missing, _ := Resolve(file, nil, []string{"PROJECT_DIR"}); len(missing) != 1 {
		t.Fatalf("no projects folder: %v", missing)
	}
	// --set gives it one.
	if v, missing, _ := Resolve(file, map[string]string{"PROJECT_DIR": "/x/y"}, []string{"PROJECT_DIR|-1"}); len(missing) != 0 || v["PROJECT_DIR|-1"] != "/x" {
		t.Fatalf("set: %v %v", v, missing)
	}
}

func TestUpPastTheTop(t *testing.T) {
	if _, _, err := Resolve("", map[string]string{"X": "/a"}, []string{"X|-3"}); err == nil {
		t.Fatal("going up past / should fail")
	}
}
