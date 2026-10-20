package runtest

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/egrif/runnr/internal/runbook"
)

// The test binary stands in for runnr when a fake command calls back.
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "__fake" {
		os.Exit(Dispatch(os.Args[2], os.Args[3:]))
	}
	os.Exit(m.Run())
}

func sources(t *testing.T) Sources {
	t.Helper()
	raw, err := os.ReadFile("testdata/replica.yaml")
	if err != nil {
		t.Fatal(err)
	}
	rb, err := runbook.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	lraw, _ := os.ReadFile("testdata/aws-rds.yaml")
	lib, err := ParseLibrary(lraw)
	if err != nil {
		t.Fatal(err)
	}
	return Sources{
		Runbook:   func(string) (*runbook.Runbook, error) { return rb, nil },
		Fakes:     func(string) (*Library, error) { return lib, nil },
		Libraries: func([]string) ([]*runbook.Library, error) { return nil, nil },
	}
}

func exe(t *testing.T) string {
	e, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestSuite(t *testing.T) {
	raw, _ := os.ReadFile("testdata/suite.yaml")
	s, err := ParseSuite(raw)
	if err != nil {
		t.Fatal(err)
	}
	results, err := Run(context.Background(), s, sources(t), Options{Exe: exe(t)})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("%d results", len(results))
	}
	for _, r := range results {
		if !r.Passed {
			var calls []string
			for _, c := range r.Calls {
				calls = append(calls, c.Line())
			}
			t.Errorf("%s: %v\ncalls:\n  %s\nlog:\n%s", r.Scenario, r.Problems, strings.Join(calls, "\n  "), r.Run.Error)
		}
	}
	// The check repeated (creating, then available) without really waiting a minute.
	happy := results[0]
	if n := strings.Count(strings.Join(lines(happy.Calls), "\n"), "describe-db-clusters"); n != 2 {
		t.Errorf("describe-db-clusters called %d times, want 2", n)
	}
	if happy.Took.Seconds() > 20 {
		t.Errorf("took %s", happy.Took)
	}
}

func lines(cs []Call) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Line())
	}
	return out
}

// An isolated command no fake answers is refused, and fails the scenario
// whatever it expects; expectations that don't hold are reported.
func TestIsolationAndExpectations(t *testing.T) {
	s, err := ParseSuite([]byte(`
runbook: replica-demo
isolate: [aws]
functions: {ready: "return 0"}
scenarios:
  - name: nothing-faked
    expect: {status: succeeded, called: [aws rds promote-read-replica]}
`))
	if err != nil {
		t.Fatal(err)
	}
	rs, err := Run(context.Background(), s, sources(t), Options{Exe: exe(t)})
	if err != nil {
		t.Fatal(err)
	}
	r := rs[0]
	all := strings.Join(r.Problems, "\n")
	for _, want := range []string{"no fake answered a call to isolated aws (step 1): aws rds describe-db-instances", "expected succeeded", `expected a call to "aws rds promote-read-replica"`} {
		if !strings.Contains(all, want) {
			t.Errorf("problems lack %q:\n%s", want, all)
		}
	}
	if r.Passed {
		t.Error("should fail")
	}
}

func TestAWSText(t *testing.T) {
	for _, c := range []struct {
		v    any
		want string
	}{
		{"x", "x\n"},
		{[]any{"a", "b"}, "a\tb\n"},
		{[]any{[]any{"a", 1.0}, []any{"b", true}}, "a\t1\nb\tTrue\n"},
		{map[string]any{"B": "2", "A": "1"}, "1\t2\n"},
		{nil, "None\n"},
	} {
		got, _ := formatOutput(c.v, "text")
		if got != c.want {
			t.Errorf("%v: %q, want %q", c.v, got, c.want)
		}
	}
}

func TestKubectlJSONPath(t *testing.T) {
	app := map[string]any{"data": map[string]any{"maintenance.txt": "# Current phase: closed"}, "status": map[string]any{"health": map[string]any{"status": "Healthy"}, "sync": map[string]any{"status": "Synced"}},
		"items": []any{map[string]any{"metadata": map[string]any{"name": "a"}}, map[string]any{"metadata": map[string]any{"name": "b"}}}}
	for tmpl, want := range map[string]string{
		"{.status.health.status}/{.status.sync.status}": "Healthy/Synced",
		"{.items[*].metadata.name}":                     "a b",
		`{range .items[*]}{.metadata.name}{"\n"}{end}`:  "a\nb\n",
		"{.items[-1].metadata.name}":                    "b",
		"{.status['health'].status}":                    "Healthy",
		"{range .missing[*]}{.x}{end}done":              "done",
		`{.data.maintenance\.txt}`:                      "# Current phase: closed",
	} {
		got, err := kubectlOutput(app, "jsonpath="+tmpl)
		if err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", tmpl, got, err, want)
		}
	}
}

func TestLenientLiterals(t *testing.T) {
	for in, want := range map[string]string{
		"a[?starts_with(@, `arn:`)]": "a[?starts_with(@, `\"arn:\"`)]",
		"a[?x == `true`]":            "a[?x == `true`]",
		"a[?x == `\"q\"`].b":         "a[?x == `\"q\"`].b",
		"[Status,R||`promoted`]":     "[Status,R||`\"promoted\"`]",
	} {
		if got := lenientLiterals(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}
