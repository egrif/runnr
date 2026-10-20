// Package htmlview writes a runbook as one self-contained HTML page to read
// (or share): sections and steps that expand and collapse, each step's code
// as written or exactly as runnr would run it, and ways as tabs.
package htmlview

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/egrif/runnr/internal/engine"
	"github.com/egrif/runnr/internal/runbook"
)

//go:embed page.html
var pageHTML string

// markPNG is runnr's mark (the sprout, the hub and its conduits, cropped
// from the logo), for the page's icon and header.
//
//go:embed mark.png
var markPNG []byte

var markURL = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(markPNG))

var page = template.Must(template.New("page").Funcs(template.FuncMap{
	"join": strings.Join,
}).Parse(pageHTML))

// Options shape the page.
type Options struct {
	// Vars fills in inputs (others use their default).
	Vars map[string]string
	// Libraries are the function libraries the runbook uses.
	Libraries []*runbook.Library
	// Now is when the page was made (default: now).
	Now time.Time
}

type kv struct{ K, V string }

type badge struct{ Text, Class, Title string }

type input struct {
	Name, Description, Value, Source string
	Secret                           bool
}

type way struct {
	Name, Kind string
	Preferred  bool
	// Written is the code as written (checks one after another); Script
	// is the code as runnr runs it, to paste after the setup.
	Written, Script string
	// Manual instructions, as written and filled in.
	Manual, ManualRendered string
}

type step struct {
	N                   int
	Anchor, Name        string
	Badges              []badge
	Description, Expect string
	Meta                []kv
	Ways                []way
	Choose              bool
	// Setup is what to paste before this step on its own: the common
	// setup and the setup steps before it.
	Setup string
}

type section struct {
	Name, Anchor, Description, Expect, Offset string
	Steps                                     []step
	Window                                    bool // the maintenance window is open during some of it
}

type library struct {
	Name, Description string
	Functions         []string
	Code              string
}

type data struct {
	Name, Description, Expect string
	Overview                  template.HTML
	// Setup is everything runnr gives the steps, with every setup step.
	Setup     string
	Facts     []kv
	Inputs    []input
	Sections  []section
	Libraries []library
	Generated string
	Steps     int
	Mark      template.URL
	Shell     string
}

// Render writes the page.
func Render(rb *runbook.Runbook, o Options) ([]byte, error) {
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	hasFuncs := len(rb.Functions) > 0
	for _, l := range o.Libraries {
		hasFuncs = hasFuncs || len(l.Functions) > 0
	}
	p := engine.NewPreviewer(rb, o.Vars, nil, hasFuncs)
	plan := p.Plan()
	shellName := rb.Shell
	if shellName == "" {
		shellName = "sh"
	}
	sc := &scripter{rb: rb, p: p, vars: o.Vars, libs: o.Libraries, shell: shellName}
	base := sc.base()
	d := data{Mark: markURL, Shell: shellName, Setup: sc.setup(base, len(rb.Steps)), Name: rb.Name, Description: strings.TrimSpace(p.Render(rb.Description)), Expect: strings.TrimSpace(p.Render(rb.Expect)), Steps: len(rb.Steps), Generated: o.Now.Format("2 Jan 2006 15:04 MST")}

	if ov := strings.TrimSpace(p.Render(rb.Overview)); ov != "" {
		d.Overview = markdown(ov)
	}
	d.Facts = append(d.Facts, kv{"Shell", shellName})
	if len(rb.Libraries) > 0 {
		d.Facts = append(d.Facts, kv{"Libraries", strings.Join(rb.Libraries, ", ")})
	}
	if len(rb.Profile) > 0 {
		d.Facts = append(d.Facts, kv{"Sourced before every step", strings.Join(rb.Profile, ", ")})
	}
	for _, k := range slices.Sorted(maps.Keys(rb.Env)) {
		d.Facts = append(d.Facts, kv{"Environment", k + "=" + p.Render(rb.Env[k])})
	}
	if mw := rb.MaintenanceWindow; mw != nil {
		var parts []string
		if mw.Target != "" {
			parts = append(parts, "target "+mw.Target)
		}
		if o, c := rb.WindowSteps(); o >= 0 {
			if c >= 0 {
				parts = append(parts, fmt.Sprintf("steps %d–%d", o+1, c+1))
			} else {
				parts = append(parts, fmt.Sprintf("from step %d (no step closes it)", o+1))
			}
		}
		if lead, ok := mw.NotifyLead(); ok {
			parts = append(parts, "announced "+runbook.FormatSpan(lead)+" ahead")
		}
		if desc := strings.TrimSpace(mw.Description); desc != "" {
			parts = append(parts, desc)
		}
		d.Facts = append(d.Facts, kv{"Maintenance window (production down)", strings.Join(parts, " · ")})
	}

	for _, in := range rb.Inputs {
		i := input{Name: in.Name, Description: in.Description, Secret: in.Secret}
		switch v, given := o.Vars[in.Name]; {
		case given && v != "":
			i.Value, i.Source = v, "given"
		case in.Default != "":
			i.Value, i.Source = in.Default, "default"
		default:
			i.Source = "asked when the run starts"
			if !in.Required {
				i.Source = "empty unless given"
			}
		}
		if len(in.Choices) > 0 {
			i.Description = strings.TrimSpace(i.Description + " (" + strings.Join(in.Choices, " | ") + ")")
		}
		if in.Secret && i.Value != "" {
			i.Value = "****"
		}
		d.Inputs = append(d.Inputs, i)
	}

	sectionOffset := map[string]string{}
	for _, pt := range rb.PlanPoints(o.Vars) {
		if pt.Section != "" && !pt.Ignored {
			sectionOffset[pt.Section] = "T" + engine.FormatOffset(pt.Offset)
		}
	}
	sections := map[string]runbook.Section{}
	for _, s := range rb.Sections {
		sections[s.Name] = s
	}
	byID := map[string]int{}
	for i, st := range rb.Steps {
		if st.ID != "" {
			byID[st.ID] = i
		}
	}
	open, closing := rb.WindowSteps()
	for i, st := range rb.Steps {
		if len(d.Sections) == 0 || d.Sections[len(d.Sections)-1].Name != st.Section {
			sec := sections[st.Section]
			d.Sections = append(d.Sections, section{
				Name: st.Section, Anchor: anchor("section", st.Section),
				Description: strings.TrimSpace(p.Render(sec.Description)), Expect: strings.TrimSpace(p.Render(sec.Expect)),
				Offset: sectionOffset[st.Section],
			})
		}
		sec := &d.Sections[len(d.Sections)-1]
		s := buildStep(p, sc, rb, i, st, plan[i], open, closing, byID)
		s.Setup = sc.setup(base, i)
		sec.Steps = append(sec.Steps, s)
		if open >= 0 && i >= open && (closing < 0 || i <= closing) {
			sec.Window = true
		}
	}

	add := func(name, desc string, fns map[string]runbook.Function) {
		if len(fns) == 0 {
			return
		}
		d.Libraries = append(d.Libraries, library{Name: name, Description: desc, Functions: slices.Sorted(maps.Keys(fns)), Code: strings.TrimRight(runbook.FunctionScript(fns), "\n")})
	}
	for _, l := range o.Libraries {
		add(l.Name, l.Description, l.Functions)
	}
	add("This runbook's own functions", "", rb.Functions)

	var b bytes.Buffer
	if err := page.Execute(&b, d); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func buildStep(p *engine.Previewer, sc *scripter, rb *runbook.Runbook, i int, st runbook.Step, v engine.StepView, open, closing int, byID map[string]int) step {
	s := step{N: i + 1, Anchor: fmt.Sprintf("step-%d", i+1), Name: st.Name, Description: v.Description, Expect: strings.TrimSpace(v.Expect), Choose: st.Choose}
	b := func(text, class, title string) { s.Badges = append(s.Badges, badge{text, class, title}) }
	ways := st.WayList()
	if len(ways) > 1 {
		b(fmt.Sprintf("%d ways", len(ways)), "kind", "Alternative ways to do this step")
	} else {
		b(v.Kind, "kind", "")
	}
	if st.Always {
		b("always", "setup", "Runs on every run, resume and later start, to set up values later steps use")
	}
	if st.Optional {
		b("optional", "optional", "Not required: an interactive run asks before it, and the run can carry on without it")
	}
	if i == open {
		b("opens the window", "window", "Production is down from here")
	}
	if i == closing {
		b("closes the window", "window", "")
	}
	if v.Offset != "" {
		b("T"+v.Offset, "time", "Planned start, relative to the maintenance window opening")
	} else if v.IgnoredOffset != "" {
		b("unscheduled", "muted", "Its planned time (T"+v.IgnoredOffset+") is out of order with the steps around it")
	}
	if st.TTY && v.Kind != "tty" {
		b("tty", "", "Needs a terminal: runnr connects you to it")
	}
	if st.Background && v.Kind != "background" {
		b("background", "", "Keeps running alongside the next steps")
	}
	if st.Confirm {
		b("confirm", "confirm", "Asks before it runs")
	}
	if st.Dangerous {
		b("dangerous", "danger", "Out of sequence, it needs a typed double check")
	}
	if st.When != "" {
		b("conditional", "", "Skipped unless: "+st.When)
	}

	m := func(k, v string) {
		if v != "" {
			s.Meta = append(s.Meta, kv{k, v})
		}
	}
	m("Owner", st.Owner)
	if st.When != "" {
		m("Runs only when", st.When+"  →  "+p.Render(st.When))
	}
	m("Directory", v.Dir)
	m("Timeout", st.Timeout)
	if st.Retries > 0 {
		r := fmt.Sprintf("%d", st.Retries)
		if st.RetryDelay != "" {
			r += ", " + st.RetryDelay + " apart"
		}
		m("Retries", r)
	}
	var pub []string
	pub = append(pub, st.Outputs...)
	for _, n := range st.SecretOutputs {
		pub = append(pub, n+" (secret)")
	}
	if st.Capture != "" {
		pub = append(pub, st.Capture+" (its output)")
	}
	m("Publishes", strings.Join(pub, ", "))
	if st.Background {
		until := "the run ends"
		if st.StopAfter != "" {
			until = "step " + st.StopAfter
			if j, ok := byID[st.StopAfter]; ok {
				until = fmt.Sprintf("step %d (%s) finishes", j+1, rb.Steps[j].Name)
			}
		}
		m("In the background until", until)
	}
	m("Repeats", v.Repeat)
	if st.RetryChecks != "" {
		m("A retry reruns", st.RetryChecks+" checks")
	}
	if st.ContinueOnError {
		m("If it fails", "the run carries on")
	}
	if st.Unattended != "" {
		m("Unattended", st.Unattended)
	}
	for _, k := range slices.Sorted(maps.Keys(st.Env)) {
		m("Environment", k+"="+p.Render(st.Env[k]))
	}

	pref := st.PreferredWay()
	for j, w := range ways {
		wv := way{Name: w.Name, Kind: w.Kind(), Preferred: len(ways) > 1 && j == pref}
		switch w.Kind() {
		case "manual":
			wv.Manual = strings.TrimSpace(w.Manual)
			wv.ManualRendered = strings.TrimSpace(p.Render(w.Manual))
		case "checks":
			var b strings.Builder
			for k, c := range w.Checks {
				if k > 0 {
					b.WriteString("\n\n")
				}
				b.WriteString("# ✓ " + c.Name + " (passes on exit 0)\n" + strings.TrimSpace(c.Run))
			}
			wv.Written = b.String()
			wv.Script = sc.stepScript(i, st, w)
		default:
			wv.Written = strings.TrimSpace(w.Run)
			wv.Script = sc.stepScript(i, st, w)
		}
		if st.Always && wv.Script != "" {
			wv.Script = "# A setup step: the setup at the top runs it already.\n" + wv.Script
		}
		s.Ways = append(s.Ways, wv)
	}
	return s
}

// anchor makes an id for a link target.
func anchor(prefix, name string) string {
	var b strings.Builder
	b.WriteString(prefix + "-")
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			if !strings.HasSuffix(b.String(), "-") {
				b.WriteByte('-')
			}
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}
