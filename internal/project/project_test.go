package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A repository like runbook-automation: shared libraries at the top, a
// project folder with its runbook, library, fakes and suite.
func repo(t *testing.T) (root, proj string) {
	root = t.TempDir()
	proj = filepath.Join(root, "proj")
	files := map[string]string{
		".git/HEAD":           "ref: refs/heads/main\n",
		"helpers/shared.yaml": "name: shared\nfunctions:\n  hi: {body: 'echo hi'}\n",
		"proj/runbook/rb.yaml": `name: rb
libraries: [shared, local]   # keep this comment
inputs:
  - {name: tools, default: "@PROJECT_DIR@/tools"}
  - {name: team, default: "@TEAM@"}
steps:
  - {name: one, run: 'echo "@REPO_ROOT@"'}
`,
		"proj/runbook/local.yaml":  "name: local\nfunctions:\n  lo: {body: 'echo lo'}\n",
		"proj/tests/rb.test.yaml":  "runbook: rb\nuses: [fk]\nscenarios:\n  - {name: works}\n",
		"proj/tests/fakes/fk.yaml": "name: fk\nfakes:\n  - {command: aws, stdout: ok}\n",
		"proj/deploy/k8s.yaml":     "apiVersion: v1\nkind: ConfigMap\n",
		"proj/.hidden/skipme.yaml": "name: nope\nsteps: []\n",
	}
	for p, body := range files {
		path := filepath.Join(root, p)
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte(body), 0o644)
	}
	return root, proj
}

func doc(p *Plan, kind, name string) *Doc {
	for i := range p.Docs {
		if p.Docs[i].Kind == kind && p.Docs[i].Name == name {
			return &p.Docs[i]
		}
	}
	return nil
}

func TestScan(t *testing.T) {
	root, proj := repo(t)
	p, err := Scan(Options{Dir: proj})
	if err != nil {
		t.Fatal(err)
	}
	if p.RepoRoot != root || len(p.Docs) != 4 || len(p.Ignored) != 1 {
		t.Fatalf("root %s docs %+v ignored %v", p.RepoRoot, p.Docs, p.Ignored)
	}
	// In saving order: libraries, fakes, runbooks, suites.
	var kinds []string
	for _, d := range p.Docs {
		kinds = append(kinds, d.Kind)
	}
	if strings.Join(kinds, ",") != "library,fakes,runbook,suite" {
		t.Fatalf("order %v", kinds)
	}
	rb := doc(p, Runbook, "rb")
	if !strings.Contains(string(rb.Raw), proj+"/tools") || !strings.Contains(string(rb.Raw), root) || !strings.Contains(string(rb.Raw), "@TEAM@") {
		t.Fatalf("filled %s", rb.Raw)
	}
	if strings.Join(p.Unset, ",") != "TEAM" {
		t.Fatalf("unset %v", p.Unset)
	}
	// shared isn't in the folder: found in the repository.
	var shared Ref
	for _, r := range rb.Refs {
		if r.Name == "shared" {
			shared = r
		}
	}
	if shared.Found != "" || len(shared.Candidates) != 1 || shared.Candidates[0] != filepath.Join(root, "helpers/shared.yaml") {
		t.Fatalf("shared %+v", shared)
	}
	if p.OK() == nil {
		t.Fatal("ok with a placeholder unset and a library missing")
	}

	p, _ = Scan(Options{Dir: proj, Values: map[string]string{"TEAM": "dbre"}, Include: shared.Candidates})
	if err := p.OK(); err != nil {
		t.Fatal(err)
	}
	if d := doc(p, Library, "shared"); d == nil || !d.Included || d.Rel != "helpers/shared.yaml" {
		t.Fatalf("included %+v", d)
	}
}

// Renaming a file changes the references to it, leaving the rest as written.
func TestScanRenames(t *testing.T) {
	_, proj := repo(t)
	p, _ := Scan(Options{Dir: proj, Values: map[string]string{"TEAM": "x"}, Renames: map[string]string{"runbook:rb": "rb2", "library:local": "local2", "fakes:fk": "fk2"}})
	rb, lib, suite := doc(p, Runbook, "rb2"), doc(p, Library, "local2"), doc(p, Suite, "rb2")
	if rb == nil || lib == nil || suite == nil {
		t.Fatalf("docs %+v", p.Docs)
	}
	if !strings.HasPrefix(string(rb.Raw), "name: rb2\nlibraries: [shared, local2]   # keep this comment\n") {
		t.Fatalf("runbook %s", rb.Raw)
	}
	if string(suite.Raw) != "runbook: rb2\nuses: [fk2]\nscenarios:\n  - {name: works}\n" {
		t.Fatalf("suite %s", suite.Raw)
	}
}

func TestApply(t *testing.T) {
	root, proj := repo(t)
	stored := map[string][]byte{}
	lookup := func(kind, name string) (Stored, bool) {
		raw, ok := stored[kind+":"+name]
		return Stored{Version: 1, Raw: raw}, ok
	}
	save := func(kind string, raw []byte, overwrite bool) (int, error) {
		_, n, _ := nameOf(kind, raw)
		v := 1
		if overwrite {
			v = 2
		}
		stored[kind+":"+n] = raw
		return v, nil
	}
	opts := Options{Dir: proj, Values: map[string]string{"TEAM": "x"}, Lookup: lookup}
	p, _ := Scan(opts)
	// shared is missing: the runbook can't go in, nor its suite.
	res := Apply(p, func(Doc) bool { return false }, save)
	if got := statuses(res); got != "imported,imported,failed,failed" {
		t.Fatalf("%s %v", got, res)
	}
	opts.Include = []string{filepath.Join(root, "helpers/shared.yaml")}
	p, _ = Scan(opts)
	if got := statuses(Apply(p, func(Doc) bool { return false }, save)); got != "imported,unchanged,unchanged,imported,imported" {
		t.Fatalf("second %s", got)
	}
	os.WriteFile(filepath.Join(proj, "runbook/local.yaml"), []byte("name: local\nfunctions:\n  lo: {body: 'echo LO'}\n"), 0o644)
	p, _ = Scan(opts)
	if got := statuses(Apply(p, func(Doc) bool { return false }, save)); got != "unchanged,exists,unchanged,unchanged,unchanged" {
		t.Fatalf("changed, not replacing: %s", got)
	}
	if got := statuses(Apply(p, func(Doc) bool { return true }, save)); got != "unchanged,replaced,unchanged,unchanged,unchanged" {
		t.Fatalf("replacing: %s", got)
	}
}

func statuses(rs []Result) string {
	var s []string
	for _, r := range rs {
		s = append(s, r.Status)
	}
	return strings.Join(s, ",")
}
