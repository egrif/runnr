package runbook

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestScaffoldIsValid(t *testing.T) {
	if _, err := Parse([]byte(Scaffold("x"))); err != nil {
		t.Fatal(err)
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]string{
		"name":        "name: Bad Name\nsteps: [{name: a, run: x}]",
		"no steps":    "name: a",
		"both":        "name: a\nsteps: [{name: a, run: x, manual: y}]",
		"neither":     "name: a\nsteps: [{name: a}]",
		"timeout":     "name: a\nsteps: [{name: a, run: x, timeout: soon}]",
		"unattended":  "name: a\nsteps: [{name: a, manual: x, unattended: maybe}]",
		"choices":     "name: a\ninputs: [{name: e, default: z, choices: [x, y]}]\nsteps: [{name: a, run: x}]",
		"dup input":   "name: a\ninputs: [{name: e}, {name: e}]\nsteps: [{name: a, run: x}]",
		"template":    "name: a\nsteps: [{name: a, run: '{{.x'}]",
		"unknown key": "name: a\nsteps: [{name: a, run: x, bogus: 1}]",
		"manual opts": "name: a\nsteps: [{name: a, manual: x, retries: 2}]",
	}
	for name, y := range cases {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestRender(t *testing.T) {
	got, err := Render(`{{.a | upper}} {{quote .b}} {{default "d" .c}}`, map[string]string{"a": "x", "b": "it's", "c": ""})
	if err != nil || got != `X 'it'\''s' d` {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := Render("{{.missing}}", map[string]string{}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected missing key error, got %v", err)
	}
}

func TestFormatRoundTrip(t *testing.T) {
	for _, f := range []string{"../../examples/hello.yaml", "../../examples/deploy-service.yaml", os.Getenv("RUNNR_FORMAT_CHECK")} {
		if f == "" {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		a, err := Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		out, err := Format(a)
		if err != nil {
			t.Fatal(err)
		}
		b, err := Parse(out)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("%s: round trip changed the runbook:\n%s", f, out)
		}
	}
}

func TestSensitive(t *testing.T) {
	if _, err := Parse([]byte("name: a\nsensitive: {x: 'no placeholder'}\nsteps: [{name: a, run: x}]")); err == nil {
		t.Fatal("pattern without placeholder should be rejected")
	}
	if _, err := Parse([]byte("name: a\nsensitive: {x: '(unclosed {{SENSITIVE}}'}\nsteps: [{name: a, run: x}]")); err == nil {
		t.Fatal("bad regex should be rejected")
	}
	m := NewMasker(&Runbook{Sensitive: map[string]string{"api": "X-Api-Key: {{SENSITIVE}}", "pair": "user={{SENSITIVE}} pass={{SENSITIVE}};"}})
	cases := map[string]string{
		"postgres://app:pw-123@db.example.com:5432/app": "postgres://app:****@db.example.com:5432/app",
		"curl -H 'Authorization: Bearer abc.def' x":     "curl -H 'Authorization: Bearer ****' x",
		"aws_secret_access_key = wJalrXUtn":             "aws_secret_access_key = ****",
		"X-Api-Key: k123 rest":                          "X-Api-Key: **** rest",
		"user=bob pass=hunter2; ok":                     "user=**** pass=****; ok",
		"nothing to see https://example.com/path":       "nothing to see https://example.com/path",
	}
	for in, want := range cases {
		if got := m.Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateNewKinds(t *testing.T) {
	bad := map[string]string{
		"two kinds":          "name: a\nsteps: [{name: a, run: x, checks: [{name: c, run: y}]}]",
		"stop_after earlier": "name: a\nsteps: [{name: a, run: x}, {name: b, run: y, background: true, stop_after: a}]",
		"stop_after no bg":   "name: a\nsteps: [{name: a, run: x, stop_after: b}, {name: b, run: y}]",
		"bg outputs":         "name: a\nsteps: [{name: a, run: x, background: true, outputs: [v]}]",
		"repeat no checks":   "name: a\nsteps: [{name: a, run: x, repeat: {every: 1s, timeout: 5s}}]",
		"repeat bad":         "name: a\nsteps: [{name: a, checks: [{name: c, run: y}], repeat: {every: 5s, timeout: 1s}}]",
		"check no run":       "name: a\nsteps: [{name: a, checks: [{name: c}]}]",
		"alternatives":       "name: a\nsteps: [{name: a, alternatives: [{name: x, run: y}]}]",
		"way two kinds":      "name: a\nsteps: [{name: a, ways: [{name: x, run: y, manual: z}]}]",
		"way no name":        "name: a\nsteps: [{name: a, ways: [{run: y}, {name: m, manual: z}]}]",
		"way dup name":       "name: a\nsteps: [{name: a, ways: [{name: x, run: y}, {name: x, manual: z}]}]",
		"two preferred":      "name: a\nsteps: [{name: a, ways: [{name: x, run: y, preferred: true}, {name: m, manual: z, preferred: true}]}]",
		"ways and run":       "name: a\nsteps: [{name: a, run: q, ways: [{name: x, run: y}]}]",
		"choose one way":     "name: a\nsteps: [{name: a, choose: true, ways: [{name: x, run: y}]}]",
		"way tty manual":     "name: a\nsteps: [{name: a, ways: [{name: x, manual: y, tty: true}, {name: z, run: q}]}]",
		"retry_checks bad":   "name: a\nsteps: [{name: a, checks: [{name: c, run: y}], retry_checks: some}]",
		"retry_checks run":   "name: a\nsteps: [{name: a, run: y, retry_checks: failed}]",
		"parallel run":       "name: a\nsteps: [{name: a, run: y, parallel_checks: true}]",
		"parallel each":      "name: a\nsteps: [{name: a, checks: [{name: c, run: y}], parallel_checks: true, retry_checks: each}]",
		"outputs no run":     "name: a\nsteps: [{name: a, outputs: [v], ways: [{name: x, manual: y}, {name: c, checks: [{name: c, run: z}]}]}]",
	}
	for name, y := range bad {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	good := "name: a\nsteps:\n  - {name: w, run: x, background: true, stop_after: b}\n  - {id: b, name: b, checks: [{name: c, run: y}], repeat: {every: 1s, timeout: 5s}}\n  - {name: c, choose: true, outputs: [v], repeat: {every: 1s, timeout: 5s}, ways: [{name: Scripted, run: y, tty: true}, {name: Manual, manual: q, preferred: true}, {name: gate, checks: [{name: c, run: z}]}]}\n"
	if _, err := Parse([]byte(good)); err != nil {
		t.Fatal(err)
	}
}

func TestWays(t *testing.T) {
	rb, err := Parse([]byte("name: a\nsteps:\n  - {name: s, run: x, tty: true}\n  - {name: w, ways: [{name: A, run: a}, {name: B, manual: b, preferred: true}]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if ws := rb.Steps[0].WayList(); len(ws) != 1 || ws[0].Name != "Scripted" || !ws[0].TTY {
		t.Fatalf("single way: %+v", ws)
	}
	w := rb.Steps[1]
	if w.Kind() != "ways" || w.PreferredWay() != 1 {
		t.Fatalf("kind %s preferred %d", w.Kind(), w.PreferredWay())
	}
	if m := w.WithWay(w.Ways[1]); m.Kind() != "manual" || m.Manual != "b" || len(m.Ways) != 0 {
		t.Fatalf("with way: %+v", m)
	}
}

func TestMaintenanceWindow(t *testing.T) {
	good := `name: a
maintenance_window: {target: 45m}
sections: [{name: Prep, window_offset: -30m}, {name: Cut, window_offset: "0"}]
steps:
  - {name: p1, section: Prep, run: x}
  - {name: p2, run: x, window_offset: -10m}
  - {name: down, section: Cut, run: x, window: open}
  - {name: up, run: x, window: close, window_offset: +25m}
`
	rb, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		d  time.Duration
		ok bool
	}{{-30 * time.Minute, true}, {-10 * time.Minute, true}, {0, true}, {25 * time.Minute, true}}
	for i, w := range want {
		if d, ok := rb.PlannedOffset(i, nil); d != w.d || ok != w.ok {
			t.Errorf("step %d: %v %v, want %v %v", i+1, d, ok, w.d, w.ok)
		}
	}
	if o, c := rb.WindowSteps(); o != 2 || c != 3 {
		t.Fatalf("window steps %d %d", o, c)
	}
	for name, y := range map[string]string{
		"no window":     "name: a\nsteps: [{name: a, run: x, window: open}]",
		"bad offset":    "name: a\nmaintenance_window: {target: 1h}\nsteps: [{name: a, run: x, window_offset: soon}]",
		"bad mark":      "name: a\nmaintenance_window: {}\nsteps: [{name: a, run: x, window: start}]",
		"close first":   "name: a\nmaintenance_window: {}\nsteps: [{name: a, run: x, window: close}, {name: b, run: x, window: open}]",
		"two opens":     "name: a\nmaintenance_window: {}\nsteps: [{name: a, run: x, window: open}, {name: b, run: x, window: open}]",
		"close no open": "name: a\nmaintenance_window: {}\nsteps: [{name: a, run: x, window: close}]",
		"bad target":    "name: a\nmaintenance_window: {target: long}\nsteps: [{name: a, run: x}]",
	} {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestOffsetInputsAndTimeline(t *testing.T) {
	rb, err := Parse([]byte(`name: a
inputs: [{name: t_cut}, {name: mw_len}]
maintenance_window: {target: 30m, target_input: mw_len}
sections: [{name: Prep, window_offset: -30m}, {name: Cut, window_offset: "0"}]
steps:
  - {name: p1, section: Prep, run: x}
  - {name: down, section: Cut, run: x, window: open}
  - {name: cutoff, run: x, window_offset: +5m, offset_input: t_cut}
  - {name: up, run: x, window: close}
`))
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := rb.PlannedOffset(2, nil); d != 5*time.Minute {
		t.Fatalf("default offset %s", d)
	}
	vars := map[string]string{"t_cut": "+12m", "mw_len": "1h"}
	if d, _ := rb.PlannedOffset(2, vars); d != 12*time.Minute || rb.WindowTarget(vars) != "1h" {
		t.Fatalf("overridden: %s %s", d, rb.WindowTarget(vars))
	}
	tl := rb.Timeline(vars)
	var got []string
	for _, e := range tl {
		got = append(got, fmt.Sprintf("%s:%s:%s", e.Kind, e.Name, e.Offset))
	}
	want := "window:Maintenance window:0s section:Prep:-30m0s section:Cut:0s step:cutoff:12m0s"
	if strings.Join(got, " ") != want || tl[0].Length != time.Hour || tl[0].Step != 1 {
		t.Fatalf("timeline %v (length %s)", got, tl[0].Length)
	}
	if err := rb.CheckWindowInputs(map[string]string{"t_cut": "soon"}); err == nil {
		t.Fatal("bad override should fail")
	}
	if _, err := Parse([]byte("name: a\nmaintenance_window: {}\nsteps: [{name: a, run: x, offset_input: nope}]")); err == nil {
		t.Fatal("unknown offset_input should fail")
	}
}

func TestSpans(t *testing.T) {
	day := 24 * time.Hour
	for in, want := range map[string]time.Duration{"2w": 14 * day, "1d12h": 36 * time.Hour, "45m": 45 * time.Minute, "1h30m": 90 * time.Minute, "0": 0, "1.5d": 36 * time.Hour, "90s": 90 * time.Second} {
		if got, err := ParseSpan(in); err != nil || got != want {
			t.Errorf("ParseSpan(%q) = %s, %v; want %s", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "soon", "2 w", "3mo", "1d-2h", "w"} {
		if _, err := ParseSpan(bad); err == nil {
			t.Errorf("ParseSpan(%q) should fail", bad)
		}
	}
	if d, err := ParseOffset("-2w"); err != nil || d != -14*day {
		t.Errorf("ParseOffset(-2w) = %s %v", d, err)
	}
	if d, err := ParseOffset("+1d4h"); err != nil || d != 28*time.Hour {
		t.Errorf("ParseOffset(+1d4h) = %s %v", d, err)
	}
	for d, want := range map[time.Duration]string{14 * day: "2w", 36 * time.Hour: "1d12h", 9*day + 3*time.Hour: "1w2d3h", 75 * time.Minute: "1h15m", 0: "0", -30 * time.Minute: "-30m"} {
		if got := FormatSpan(d); got != want {
			t.Errorf("FormatSpan(%s) = %q, want %q", d, got, want)
		}
	}
	rb, err := Parse([]byte("name: a\nmaintenance_window: {target: 2d}\nsections: [{name: Prep, window_offset: -2w}]\nsteps:\n  - {name: p, section: Prep, run: x}\n  - {name: d, run: x, window: open}\n  - {name: u, run: x, window: close, window_offset: +1d4h}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if tl := rb.Timeline(nil); tl[0].Length != 2*day || tl[1].Offset != -14*day || tl[2].Offset != 28*time.Hour {
		t.Fatalf("timeline %+v", tl)
	}
}

func TestOutOfOrderPlannedTimesAreUnscheduled(t *testing.T) {
	rb, err := Parse([]byte(`name: a
inputs: [{name: t_b}]
maintenance_window: {}
sections: [{name: Prep, window_offset: -1h}, {name: Cut, window_offset: "0"}]
steps:
  - {name: p1, section: Prep, run: x}
  - {name: early, run: x, window_offset: -2h}
  - {name: late, run: x, window_offset: +5m}
  - {name: p4, run: x, window_offset: -30m}
  - {name: down, section: Cut, run: x, window: open}
  - {name: b, run: x, window_offset: +20m, offset_input: t_b}
  - {name: c, run: x, window_offset: +10m}
  - {name: d, run: x, window_offset: +30m}
  - {name: up, run: x, window: close}
`))
	if err != nil {
		t.Fatal(err)
	}
	plan := func(vars map[string]string) string {
		var out []string
		for i := range rb.Steps {
			if d, ok := rb.PlannedOffset(i, vars); ok {
				out = append(out, fmt.Sprintf("%s:%s", rb.Steps[i].Name, FormatSpan(d)))
			} else {
				out = append(out, rb.Steps[i].Name+":-")
			}
		}
		return strings.Join(out, " ")
	}
	// early (-2h after -1h) and late (+5m before the window opens) are out
	// of order; c (+10m after b's +20m) too.
	if got, want := plan(nil), "p1:-1h early:- late:- p4:-30m down:0 b:20m c:- d:30m up:-"; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	// A default that an input moves earlier lets c count again; an input
	// that moves b after d drops d instead.
	if got, want := plan(map[string]string{"t_b": "+5m"}), "p1:-1h early:- late:- p4:-30m down:0 b:5m c:10m d:30m up:-"; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if got, want := plan(map[string]string{"t_b": "+45m"}), "p1:-1h early:- late:- p4:-30m down:0 b:45m c:- d:- up:-"; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if n := len(rb.Timeline(nil)); n != 6 { // window, Prep, p4, Cut, b, d
		t.Fatalf("timeline has %d entries: %+v", n, rb.Timeline(nil))
	}
}

func TestDescriptionsAndSections(t *testing.T) {
	good := "name: a\ndescription: |\n  What it's for.\nexpect: Done.\nsections: [{name: S1, description: first, expect: ok}]\nsteps: [{name: a, section: S1, run: x, description: 'doing {{.v}}'}]\n"
	if _, err := Parse([]byte(good)); err != nil {
		t.Fatal(err)
	}
	for name, y := range map[string]string{
		"unknown section": "name: a\nsections: [{name: Nope}]\nsteps: [{name: a, section: S1, run: x}]",
		"twice":           "name: a\nsections: [{name: S1}, {name: S1}]\nsteps: [{name: a, section: S1, run: x}]",
		"bad template":    "name: a\nsteps: [{name: a, run: x, description: '{{.x'}]",
	} {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
