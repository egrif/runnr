"use strict";

// The right-hand side of a step, on the launch screen and the run page:
// History (what it did in an earlier run; last time by default, any earlier
// run selectable) or Details (how it's configured).

const side = { tab: new Map(), pick: new Map(), runs: new Map() };

// resetSide forgets per-page choices (called when the page changes).
function resetSide() { side.pick.clear(); }

function sidePanel(key, i, cfg, history, defaultRun) {
  const tab = side.tab.get(key) || "history";
  const tabs = `<div class="side-tabs">${[["history", "History"], ["details", "Details"]].map(([t, l]) =>
    `<button type="button" class="${tab === t ? "on" : ""}" data-side-tab="${t}" data-key="${esc(key)}">${l}</button>`).join("")}</div>`;
  if (tab === "details") return `<div class="side">${tabs}${stepConfig(cfg)}</div>`;

  const runs = [...history];
  if (defaultRun && !runs.some((r) => r.id === defaultRun.id)) runs.unshift(defaultRun);
  if (!runs.length) return `<div class="side">${tabs}<p class="muted">No earlier runs of this runbook yet.</p></div>`;
  const picked = side.pick.get(key) || defaultRun?.id || runs[0].id;
  const run = picked === defaultRun?.id ? defaultRun : side.runs.get(picked);
  const options = runs.map((r) => `<option value="${esc(r.id)}" ${r.id === picked ? "selected" : ""}>
    ${esc(new Date(r.started).toLocaleString())} · ${esc(r.status)}${r.id === defaultRun?.id ? " · last time" : ""}</option>`).join("");
  return `<div class="side">${tabs}
    <div class="side-pick"><select data-side-pick="${esc(key)}" title="Show what this step did in another run">${options}</select>
      <a href="#/runs/${enc(picked)}" class="small">open run</a></div>
    ${run ? lastTime(run.steps[i], run) : `<p class="muted">Loading…</p>`}</div>`;
}

// bindSide wires the tabs and run picker; rerender redraws the page.
function bindSide(root, rerender) {
  root._sideRerender = rerender;
  if (root._sideBound) return;
  root._sideBound = true;
  root.addEventListener("click", (e) => {
    const b = e.target.closest("[data-side-tab]");
    if (!b) return;
    e.preventDefault();
    e.stopPropagation();
    side.tab.set(b.dataset.key, b.dataset.sideTab);
    root._sideRerender();
  });
  root.addEventListener("change", async (e) => {
    const sel = e.target.closest("[data-side-pick]");
    if (!sel) return;
    side.pick.set(sel.dataset.sidePick, sel.value);
    if (!side.runs.has(sel.value)) {
      root._sideRerender(); // shows "Loading…"
      try { side.runs.set(sel.value, (await api("GET", `/api/runs/${enc(sel.value)}`)).run); }
      catch (err) { toast(err.message); }
    }
    root._sideRerender();
  });
}

// stepConfig lists how a step is set up: everything that isn't a default.
function stepConfig(s) {
  if (!s) return `<p class="muted">Configuration not available (the runbook has changed or is invalid).</p>`;
  const H = FIELD_HELP.step;
  const yes = "yes";
  const kv = (o) => Object.entries(o || {}).map(([k, v]) => `${k}=${v}`).join("\n");
  const rows = [
    ["Kind", H.kind, stepKindName(s)],
    ["Section", H.section, s.section], ["Owner", H.owner, s.owner], ["ID", H.id, s.id],
    ["Confirm first", H.confirm, s.confirm && yes],
    ["Dangerous", H.dangerous, s.dangerous && yes],
    ["Always run", H.always, s.always && yes],
    ["Keep going if it fails", H.continue_on_error, s.continue_on_error && yes],
    ["Needs a terminal", H.tty, s.tty && yes],
    ["Background", H.background, s.background && (s.stop_after ? `until ${s.stop_after} finishes` : "until the run ends")],
    ["Only when", H.when, s.when],
    ["Timeout", H.timeout, s.timeout], ["Retries", H.retries, s.retries], ["Retry delay", H.retry_delay, s.retry_delay],
    ["Repeat", H.repeat_every, s.repeat && `every ${s.repeat.every}, give up after ${s.repeat.timeout}`],
    ["Checks", H.checks, s.checks?.map((c) => c.name).join("\n")],
    ["Alternatives", H.alternatives, s.alternatives?.map((a) => a.name + (a.when ? ` (when ${a.when})` : "")).join("\n")],
    ["Directory", H.dir, s.dir], ["Shell", H.step_shell, s.shell], ["Environment", H.step_env, kv(s.env)],
    ["Publishes", H.outputs, s.outputs?.join(", ")], ["Publishes (secret)", H.secret_outputs, s.secret_outputs?.join(", ")],
    ["Capture stdout as", H.capture, s.capture],
    ["When unattended", H.unattended, s.unattended],
    ["Expected", H.expect, s.expect],
  ].filter(([, , v]) => v !== undefined && v !== "" && v !== false && v !== 0 && v != null);
  return `<table class="kv config">${rows.map(([label, help, v]) =>
    `<tr><td class="flabel">${esc(label)}${helpIcon(help)}</td><td><code>${esc(v)}</code></td></tr>`).join("")}</table>
    ${rows.length === 1 ? `<p class="muted small">Everything else is the default.</p>` : ""}`;
}
