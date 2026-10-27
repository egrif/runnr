package timeline

import (
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/egrif/runnr/internal/engine"
)

// HTML renders the timeline as one self-contained page.
func (t *Timeline) HTML() []byte {
	r := t.Run
	var b strings.Builder
	e := html.EscapeString
	fmt.Fprintf(&b, `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Timeline · %s</title>
<style>%s</style></head><body>
<header><h1>%s <span class="muted">· run %s</span></h1>
<p class="meta"><span class="badge s-%s">%s</span> %s → %s · %s · %s run · runbook version %s</p></header>
`, e(r.Runbook+" "+r.ID), pageCSS, e(r.Runbook), e(r.ID), e(string(r.Status)), e(string(r.Status)),
		e(t.Start.Local().Format("Mon Jan 2 2006, 15:04:05")), e(clock(t.End, t.Start)), e(Dur(t.End.Sub(t.Start))), e(r.Mode), e(r.RunbookVersion.Short()))

	// What it comes to.
	count := map[engine.Status]int{}
	shells := 0
	for _, l := range t.Lanes {
		if !l.Removed {
			count[l.Status]++
		}
		for _, s := range l.Spans {
			if s.Kind == "shell" {
				shells++
			}
		}
	}
	var stopped time.Duration
	for _, bd := range t.Bands {
		if bd.Kind == "stopped" {
			stopped += bd.To.Sub(bd.From)
		}
	}
	card := func(n, label string) {
		fmt.Fprintf(&b, `<div class="card"><b>%s</b><span>%s</span></div>`, e(n), e(label))
	}
	b.WriteString(`<section class="cards">`)
	card(Dur(t.End.Sub(t.Start)), "start to end")
	card(fmt.Sprint(count[engine.StatusSucceeded]+count[engine.StatusForced]), fmt.Sprintf("of %d steps done", len(r.Steps)))
	if n := count[engine.StatusFailed] + count[engine.StatusAborted]; n > 0 {
		card(fmt.Sprint(n), "failed or aborted")
	}
	if n := count[engine.StatusSkipped]; n > 0 {
		card(fmt.Sprint(n), "skipped")
	}
	if n := len(r.Resumptions); n > 0 {
		card(fmt.Sprint(n), "times resumed")
	}
	if stopped > 0 {
		card(Dur(stopped), "stopped between")
	}
	if w := r.Window; w != nil && !w.Opened.IsZero() && !w.Closed.IsZero() {
		card(Dur(w.Closed.Sub(w.Opened)), "maintenance window")
	}
	if shells > 0 {
		card(fmt.Sprint(shells), "shell sessions")
	}
	b.WriteString("</section>\n")

	b.WriteString(`<h2>The whole run</h2>`)
	t.chart(&b, t.Axis, false)
	if w := r.Window; w != nil && !w.Opened.IsZero() {
		to := w.Closed
		if to.IsZero() {
			to = t.End
		}
		if d := to.Sub(w.Opened); d < t.End.Sub(t.Start)*3/5 {
			margin := max(time.Minute, d/10)
			from, until := w.Opened.Add(-margin), to.Add(margin)
			if from.Before(t.Start) {
				from = t.Start
			}
			if until.After(t.End) {
				until = t.End
			}
			fmt.Fprintf(&b, `<h2>The maintenance window, %s → %s</h2>`, e(w.Opened.Local().Format("15:04")), e(to.Local().Format("15:04")))
			t.chart(&b, Zoomed(from, until), true)
		}
	}
	b.WriteString(`<p class="legend">
<span><i class="sw s-succeeded"></i>succeeded</span><span><i class="sw s-failed"></i>failed / aborted</span><span><i class="sw s-forced"></i>forced</span>
<span><i class="sw k-earlier"></i>earlier try (before a resume)</span><span><i class="sw k-background"></i>background</span>
<span><i class="sw k-shell"></i>shell</span><span><i class="sw k-job"></i>job</span><span><i class="sw b-window"></i>maintenance window</span>
<span><i class="sw b-stopped"></i>stopped</span><span><i class="sw cut"></i>cut short (idle)</span>
<span>◆ note</span><span>✎ input change / edit</span><span>↻ resumed</span></p>
`)

	if len(t.Moments) > 0 {
		b.WriteString(`<h2>Key moments</h2><table class="moments">`)
		for _, m := range t.Moments {
			fmt.Fprintf(&b, `<tr class="m-%s"><td class="t">%s</td><td>%s</td></tr>`, e(m.Kind), e(clock(m.At, t.Start)), e(m.Text))
		}
		b.WriteString("</table>\n")
	}
	fmt.Fprintf(&b, `<footer class="muted">runnr · drawn %s</footer></body></html>`, e(time.Now().Local().Format("2006-01-02 15:04")))
	return []byte(b.String())
}

// chart draws the lanes on axis a; zoomed leaves out lanes with nothing in
// view.
func (t *Timeline) chart(b *strings.Builder, a Axis, zoomed bool) {
	e := html.EscapeString
	pct := func(x float64) string { return fmt.Sprintf("%.3f%%", x*100) }
	visible := func(from, to time.Time) bool { return !to.Before(a.Start) && !from.After(a.End) }
	ticks := a.Ticks(10, 0.07)
	// Under every row: the grid, the cuts and the window.
	var under strings.Builder
	for _, tk := range ticks {
		fmt.Fprintf(&under, `<i class="grid" style="left:%s"></i>`, pct(tk.X))
	}
	for _, s := range a.Segs {
		if s.Cut {
			fmt.Fprintf(&under, `<i class="cut" style="left:%s;width:%s" title="%s idle, cut short"></i>`, pct(s.X0), pct(s.X1-s.X0), e(Dur(s.To.Sub(s.From))))
		}
	}
	for _, bd := range t.Bands {
		if bd.Kind == "window" && visible(bd.From, bd.To) {
			fmt.Fprintf(&under, `<i class="b-window" style="left:%s;width:%s"></i>`, pct(a.X(bd.From)), pct(a.X(bd.To)-a.X(bd.From)))
		}
	}
	bar := func(from, to time.Time, class, title string) {
		if !visible(from, to) {
			return
		}
		x0, x1 := a.X(from), a.X(to)
		fmt.Fprintf(b, `<i class="bar %s" style="left:%s;width:%s" title="%s"></i>`, class, pct(x0), pct(x1-x0), e(title))
	}
	mark := func(m Mark) {
		if !visible(m.At, m.At) {
			return
		}
		sym := map[string]string{"note": "◆", "resume": "↻"}[m.Kind]
		if sym == "" {
			sym = "✎"
		}
		fmt.Fprintf(b, `<span class="mark k-%s" style="left:%s" title="%s · %s">%s</span>`, e(m.Kind), pct(a.X(m.At)), e(clock(m.At, t.Start)), e(m.Text), sym)
	}
	span := func(from, to time.Time) string {
		return fmt.Sprintf("%s → %s (%s)", clock(from, t.Start), clock(to, t.Start), Dur(to.Sub(from)))
	}

	b.WriteString(`<div class="tl"><div class="row head"><div class="lab"></div><div class="track">`)
	for _, tk := range ticks {
		fmt.Fprintf(b, `<span class="tick" style="left:%s">%s</span>`, pct(tk.X), e(tk.Label))
	}
	for _, s := range a.Segs {
		if s.Cut {
			fmt.Fprintf(b, `<span class="tick cutlab" style="left:%s">%s</span>`, pct((s.X0+s.X1)/2), e(Dur(s.To.Sub(s.From))))
		}
	}
	b.WriteString(`</div></div>`)

	// The run.
	fmt.Fprintf(b, `<div class="row run"><div class="lab">run</div><div class="track">%s`, under.String())
	bar(t.Start, t.End, "k-run", fmt.Sprintf("%s · %s", t.Run.Status, span(t.Start, t.End)))
	for _, bd := range t.Bands {
		if bd.Kind == "stopped" {
			bar(bd.From, bd.To, "b-stopped", bd.Label+" · "+span(bd.From, bd.To))
		}
	}
	for _, m := range t.Marks {
		if m.Lane < 0 {
			mark(m)
		}
	}
	b.WriteString(`</div></div>`)

	section, removed := "", false
	for i, l := range t.Lanes {
		if zoomed {
			in := false
			for _, s := range l.Spans {
				in = in || visible(s.From, s.To)
			}
			if !in {
				continue
			}
		}
		if l.Removed && !removed {
			removed = true
			b.WriteString(`<div class="row sect"><div class="lab">No longer in the runbook</div></div>`)
		} else if !l.Removed && l.Section != section {
			section = l.Section
			if section != "" {
				fmt.Fprintf(b, `<div class="row sect"><div class="lab">%s</div></div>`, e(section))
			}
		}
		dur := ""
		for _, s := range l.Spans {
			if s.Kind == "try" || s.Kind == "background" {
				dur = Dur(s.To.Sub(s.From))
			}
		}
		cls := "row"
		if l.Removed {
			cls += " removed"
		}
		fmt.Fprintf(b, `<div class="%s"><div class="lab" title="%s"><span class="num">%d</span>%s<span class="d">%s</span></div><div class="track">%s`,
			cls, e(fmt.Sprintf("step %d: %s · %s", l.Num, l.Name, l.Status)), l.Num, e(l.Name), e(dur), under.String())
		for _, s := range l.Spans {
			title := fmt.Sprintf("%d. %s · ", l.Num, l.Name)
			switch s.Kind {
			case "shell", "job":
				title += s.Label + " · " + span(s.From, s.To)
				bar(s.From, s.To, "k-"+s.Kind, title)
			}
		}
		for _, s := range l.Spans {
			title := fmt.Sprintf("%d. %s · %s · %s", l.Num, l.Name, s.Status, span(s.From, s.To))
			if s.Label != "" {
				title += " · " + s.Label
			}
			switch s.Kind {
			case "earlier":
				bar(s.From, s.To, "k-earlier s-"+string(s.Status), title)
			case "try", "background":
				cls := "k-" + s.Kind + " s-" + string(s.Status)
				if s.Open {
					cls += " open"
				}
				bar(s.From, s.To, cls, title)
			}
		}
		for _, m := range t.Marks {
			if m.Lane == i {
				mark(m)
			}
		}
		b.WriteString(`</div></div>`)
	}
	b.WriteString("</div>\n")
}

const pageCSS = `
:root{--bg:#fafaf8;--fg:#1d1f23;--muted:#6b7079;--line:#e3e3df;--card:#fff;--ok:#2e8b4a;--bad:#c93a3a;--warn:#c99a1c;--run:#3b6fd6;--bgk:#2a9fae;--shell:#9a4fc9;--win:#f9f0d2;--stop:#00000010}
@media (prefers-color-scheme: dark){:root:not([data-theme="light"]){--bg:#15171b;--fg:#e4e6eb;--muted:#9097a3;--line:#2c2f36;--card:#1d2026;--ok:#46b06a;--bad:#e05c5c;--warn:#d9b13c;--run:#5b8ef0;--bgk:#3fbccb;--shell:#b77be0;--win:#3a3420;--stop:#ffffff12}}
:root[data-theme="dark"]{--bg:#15171b;--fg:#e4e6eb;--muted:#9097a3;--line:#2c2f36;--card:#1d2026;--ok:#46b06a;--bad:#e05c5c;--warn:#d9b13c;--run:#5b8ef0;--bgk:#3fbccb;--shell:#b77be0;--win:#3a3420;--stop:#ffffff12}
*{box-sizing:border-box}
body{margin:0;padding:24px clamp(16px,3vw,40px);background:var(--bg);color:var(--fg);font:14px/1.45 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif}
h1{font-size:22px;margin:0 0 4px}h2{font-size:15px;margin:28px 0 10px}
.muted{color:var(--muted);font-weight:normal}.meta{margin:0;color:var(--muted)}
.badge{display:inline-block;padding:1px 8px;border-radius:10px;border:1px solid currentColor;font-size:12px}
.badge.s-succeeded{color:var(--ok)}.badge.s-failed,.badge.s-aborted{color:var(--bad)}.badge.s-running{color:var(--run)}
.cards{display:flex;flex-wrap:wrap;gap:10px;margin-top:18px}
.card{background:var(--card);border:1px solid var(--line);border-radius:8px;padding:10px 14px;min-width:120px}
.card b{display:block;font-size:20px}.card span{color:var(--muted);font-size:12px}
.tl{background:var(--card);border:1px solid var(--line);border-radius:8px;padding:8px 12px 10px;overflow:hidden}
.row{display:flex;align-items:stretch;min-height:22px}
.row .lab{flex:0 0 clamp(120px,24%,260px);display:flex;align-items:center;gap:6px;padding-right:10px;white-space:nowrap;overflow:hidden;font-size:13px}
.lab .num{color:var(--muted);min-width:1.6em;text-align:right;font-variant-numeric:tabular-nums}
.lab .d{margin-left:auto;color:var(--muted);font-size:11px;font-variant-numeric:tabular-nums}
.track{position:relative;flex:1;min-width:0;border-left:1px solid var(--line)}
.row.head{min-height:20px}.row.head .track{border-left-color:transparent}
.tick{position:absolute;top:2px;transform:translateX(-50%);font-size:11px;color:var(--muted);white-space:nowrap}
.tick.cutlab{font-style:italic}
.row.sect .lab{font-size:11px;text-transform:uppercase;letter-spacing:.05em;color:var(--muted);padding-top:8px;flex-basis:100%}
.row.removed{opacity:.55}
.row.run{border-bottom:1px solid var(--line);margin-bottom:2px}
.track .grid{position:absolute;top:0;bottom:0;width:1px;background:var(--line)}
.track .cut{position:absolute;top:0;bottom:0;background:repeating-linear-gradient(135deg,var(--line) 0 3px,transparent 3px 7px)}
.track .b-window{position:absolute;top:0;bottom:0;background:var(--win)}
.bar{position:absolute;top:5px;height:12px;min-width:2px;border-radius:2px;background:var(--muted)}
.bar.k-run{top:9px;height:4px;background:var(--muted);opacity:.5}
.bar.b-stopped{top:3px;height:16px;background:var(--stop);border:1px dashed var(--muted)}
.bar.s-succeeded{background:var(--ok)}.bar.s-failed,.bar.s-aborted{background:var(--bad)}.bar.s-forced{background:var(--warn)}
.bar.s-running{background:var(--run)}.bar.s-suspended{background:var(--shell);opacity:.6}.badge.s-suspended{color:var(--shell)}.bar.s-skipped{background:transparent;border:1px dashed var(--muted)}
.bar.k-earlier{opacity:.45;background-image:repeating-linear-gradient(135deg,#ffffff55 0 2px,transparent 2px 5px)}
.bar.k-background,.bar.s-background{background:var(--bgk);background-image:repeating-linear-gradient(90deg,#ffffff40 0 2px,transparent 2px 6px)}
.bar.k-shell{top:15px;height:4px;background:var(--shell)}.bar.k-job{top:15px;height:4px;background:var(--bgk)}
.bar.open{border-right:2px solid var(--fg)}
.mark{position:absolute;top:1px;transform:translateX(-50%);font-size:13px;line-height:20px;cursor:default}
.mark.k-note{color:var(--shell)}.mark.k-resume{color:var(--run);font-weight:bold}.mark.k-input,.mark.k-edit{color:var(--warn)}
.legend{display:flex;flex-wrap:wrap;gap:6px 16px;color:var(--muted);font-size:12px;margin:10px 0 0}
.legend span{display:inline-flex;align-items:center;gap:6px}
.sw{display:inline-block;width:18px;height:10px;border-radius:2px;background:var(--muted)}
.sw.s-succeeded{background:var(--ok)}.sw.s-failed{background:var(--bad)}.sw.s-forced{background:var(--warn)}
.sw.k-earlier{background:var(--ok);opacity:.45}.sw.k-background{background:var(--bgk)}.sw.k-shell{background:var(--shell);height:4px}.sw.k-job{background:var(--bgk);height:4px}
.sw.b-window{background:var(--win)}.sw.b-stopped{background:var(--stop);border:1px dashed var(--muted)}.sw.cut{background:repeating-linear-gradient(135deg,var(--line) 0 3px,transparent 3px 7px)}
.moments{border-collapse:collapse;width:100%;max-width:1100px}
.moments td{padding:4px 10px 4px 0;border-bottom:1px solid var(--line);vertical-align:top}
.moments .t{white-space:nowrap;color:var(--muted);font-variant-numeric:tabular-nums;width:1%}
.moments .m-failed td:last-child{color:var(--bad)}.moments .m-resume td:last-child{color:var(--run)}.moments .m-window td:last-child{color:var(--warn)}
footer{margin-top:28px;font-size:12px}
@media (max-width:640px){.row .lab{flex-basis:110px}.lab .d{display:none}}
`
