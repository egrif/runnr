package engine

import (
	"context"
	"os"
	"strings"
	"testing"
)

// A resume that carries the run on: the same record, the steps that
// finished as they went, the earlier try of a step it does again kept.
func TestResumeContinuesTheRun(t *testing.T) {
	dir := t.TempDir()
	rb := mustParse(t, "name: t\nshell: sh\nsteps:\n  - {name: one, run: 'echo ONE'}\n  - {name: two, run: 'test -f "+dir+"/ok && echo TWO'}\n  - {name: three, run: 'echo THREE'}\n")
	run1 := Execute(context.Background(), rb, &scripted{failures: []FailureAction{FailAbort}}, "", Options{})
	if run1.Status != StatusFailed {
		t.Fatalf("first: %s", run1.Status)
	}
	os.WriteFile(dir+"/ok", nil, 0o644)
	start := ResumePoint(run1)
	run2 := Execute(context.Background(), rb, &scripted{}, run1.ID, Options{Resumed: run1, Continue: true, StartAt: start, CarryDone: true, Vars: ResumeInputs(run1)})
	if run2.Status != StatusSucceeded || run2.ID != run1.ID || !run2.Started.Equal(run1.Started) || run2.Resumes != "" {
		t.Fatalf("%s %s %v %q", run2.Status, run2.ID, run2.Started, run2.Resumes)
	}
	one, two := run2.Steps[0], run2.Steps[1]
	if !one.Carried || one.Status != StatusSucceeded || !strings.Contains(one.Output, "ONE") {
		t.Fatalf("one %+v", one)
	}
	if two.Status != StatusSucceeded || len(two.Before) != 1 || two.Before[0].Status != StatusFailed {
		t.Fatalf("two %+v", two)
	}
	if len(run2.Resumptions) != 1 || run2.Resumptions[0].Status != StatusFailed || run2.Resumptions[0].From != 1 {
		t.Fatalf("resumptions %+v", run2.Resumptions)
	}
	log := RawLog(run2)
	for _, want := range []string{"resumed ", "from step 2 (it had failed", "2. two (run, before the run was resumed)", "THREE"} {
		if !strings.Contains(log, want) {
			t.Fatalf("raw log lacks %q:\n%s", want, log)
		}
	}
	if strings.Count(log, "1. one (run") != 1 {
		t.Fatalf("step one twice:\n%s", log)
	}
}

func TestAlignRun(t *testing.T) {
	old := &Run{Steps: []StepResult{{Name: "a", Status: StatusSucceeded}, {Name: "b", Status: StatusSucceeded}, {Name: "c", Status: StatusFailed}}}
	rb := mustParse(t, "name: t\nsteps:\n  - {name: a, run: 'true'}\n  - {name: x, run: 'true'}\n  - {name: c, run: 'true'}\n  - {name: d, run: 'true'}\n")
	if !RunbookChanged(old, rb) {
		t.Fatal("not changed")
	}
	al, moved := AlignRun(old, rb)
	var got []string
	for _, s := range al.Steps {
		got = append(got, s.Name+":"+string(s.Status))
	}
	if strings.Join(got, ",") != "a:succeeded,x:pending,c:failed,d:pending" || moved[0] != 0 || moved[1] != -1 || moved[2] != 2 {
		t.Fatalf("%v %v", got, moved)
	}
	if len(al.Removed) != 1 || al.Removed[0].Name != "b" || ResumePoint(al) != 1 {
		t.Fatalf("removed %+v, resume at %d", al.Removed, ResumePoint(al))
	}
}
