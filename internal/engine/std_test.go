package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egrif/runnr/internal/runbook"
)

// The std library's functions, in bash and zsh, against fake tools.
func TestStdLibrary(t *testing.T) {
	std, err := runbook.ParseLibrary(runbook.BuiltinLibrary("std"))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	fake := func(name, body string) {
		os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755)
	}
	fake("aws", `case "$*" in
  *get-caller-identity*--profile\ good*) echo "arn:aws:sts::1:assumed-role/AWSReservedSSO_Admin_abc/me";;
  *get-caller-identity*) exit 255;;
  *list-profiles*) printf 'good\nother\n';;
esac`)
	fake("kubectl", `case "$*" in
  *get-contexts*) echo prod-use1-0;;
  *auth\ can-i*) echo yes;;
esac`)
	fake("terraform", `case "$1" in
  plan) case "$*" in *detailed-exitcode*) echo "No changes. Your infrastructure matches."; exit ${TF_RC:-0};; esac; for a in "$@"; do case $a in -out=*) echo planning > "${a#-out=}";; esac; done;;
  show) echo "Plan: 1 to add, 0 to change, 0 to destroy.";;
  init|apply) echo "$1 ok";;
esac`)
	// A repo with an origin to check out from.
	repo, origin := filepath.Join(t.TempDir(), "repo"), filepath.Join(t.TempDir(), "origin.git")
	git := func(dir string, args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git(".", "init", "-q", "--bare", origin)
	git(".", "clone", "-q", origin, repo)
	git(repo, "commit", "-q", "--allow-empty", "-m", "first")
	git(repo, "push", "-q", "origin", "HEAD:refs/heads/staging-prep")

	for _, sh := range []string{"bash", "zsh"} {
		t.Run(sh, func(t *testing.T) {
			if _, err := exec.LookPath(sh); err != nil {
				t.Skip(sh)
			}
			rb := mustParse(t, `
name: t
shell: `+sh+`
libraries: [std]
steps:
  - {name: sso ok, run: 'is_aws_sso good Admin && echo yes'}
  - {name: sso wrong role, run: 'is_aws_sso good ReadOnly', continue_on_error: true}
  - {name: sso expired, run: 'require_aws_sso stale', continue_on_error: true}
  - {name: profiles, run: 'has_aws_profile good && ! has_aws_profile nope 2>/dev/null && echo ok'}
  - {name: kube, run: 'require_kube_access prod-use1-0 greenhouse && ! has_kube_context nope 2>/dev/null && echo ok'}
  - {name: branch, run: 'git_fresh_branch `+repo+` staging-prep'}
  - {name: plan, run: 'tf_init `+repo+` -backend-config=./state.conf && tf_plan `+repo+` plan.out'}
  - {name: apply, run: 'tf_apply `+repo+` plan.out'}
  - {name: nochange, run: 'tf_nochange `+repo+`'}
  - {name: changes, run: 'TF_RC=2 tf_nochange `+repo+`', continue_on_error: true}
`)
			r := Execute(context.Background(), rb, Unattended{}, "", Options{Libraries: []*runbook.Library{std},
				Env: []string{"PATH=" + bin + ":" + os.Getenv("PATH")}})
			want := "succeeded,failed,failed,succeeded,succeeded,succeeded,succeeded,succeeded,succeeded,failed"
			if got := statuses(r); got != want {
				for _, s := range r.Steps {
					t.Logf("%s: %s %q %q", s.Name, s.Status, s.Output, s.Error)
				}
				t.Fatalf("%s", got)
			}
			for i, w := range map[int]string{2: "aws sso login --profile stale", 5: "staging-prep at ", 6: "Plan: 1 to add", 9: "has changes to apply"} {
				if !strings.Contains(r.Steps[i].Output, w) {
					t.Errorf("step %d output %q lacks %q", i+1, r.Steps[i].Output, w)
				}
			}
		})
	}
}
