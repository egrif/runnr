"use strict";

// The run console (#/runs/{id}/console): one screen for following and
// driving a run. Across the top: the run, where it is (step, section, time
// in the section, the maintenance window) and a toolbar. On the left: the
// sections and steps. On the right, in resizable panes: the selected
// step's definition (editable while the run is live), what it's doing (or
// did, or will do), and the background steps' terminals. Terminals live in
// the panes, not the drawer.

const CX_DONE = ["succeeded", "skipped", "forced"];
const CX_ENDED = ["succeeded", "failed", "aborted"];

function cxSizes() {
  let s = {};
  try { s = JSON.parse(localStorage.getItem("runnr.console") || "{}"); } catch {}
  return { left: s.left || 340, details: s.details || 1, action: s.action || 1.4, bg: s.bg || 0.8 };
}
function cxSaveSizes(z) { try { localStorage.setItem("runnr.console", JSON.stringify(z)); } catch {} }

async function viewConsole(id) {
  const { run, live, controls } = await api("GET", `/api/runs/${enc(id)}`);
  const state = {
    run, live, controls, console: true,
    steps: run.steps.map((s) => ({ ...s, log: s.output ? s.output.replace(/\n$/, "").split("\n").map((t) => ({ stream: "stdout", text: t })) : [] })),
    question: null, open: new Set(), selected: null, follow: true, editing: null, sizes: cxSizes(),
    plan: null, versionDismissed: 0, fg: null, bgActive: null,
  };
  cxMountConsole(state);
  cxHostTerms(state);

  const loadPlan = async () => {
    try { state.plan = await api("GET", `/api/runs/${enc(id)}/plan`); cxRender(state, { details: !state.editing }); } catch {}
  };
  api("GET", `/api/runbooks/${enc(run.runbook)}`).then((d) => { state.rbSections = d.runbook?.sections; state.inputDefs = d.runbook?.inputs; cxRender(state); }).catch(() => {});
  await loadPlan();
  let pt;
  state.onState = () => { clearTimeout(pt); pt = setTimeout(loadPlan, 400); };
  // Someone may save a newer version of the runbook while the run goes on.
  const poll = setInterval(() => { if (!document.body.classList.contains("console-mode") || CX_ENDED.includes(state.run.status)) return clearInterval(poll); loadPlan(); }, 20000);
  cxRender(state);
  if (!live) return;

  stream = new EventSource(`/api/runs/${enc(id)}/events`);
  let first = true;
  stream.onmessage = (msg) => {
    const e = JSON.parse(msg.data);
    if (e.seq === 0 && !first) state.steps.forEach((s) => (s.log = []));
    first = false;
    if (e.kind.startsWith("term")) {
      termEvent(id, e);
      if (e.kind === "term") return;
    }
    const before = state.steps.findIndex((s) => s.status === "running");
    applyEvent(state, e);
    if (e.kind === "step_finished" || e.kind === "step_started") state.onState();
    // Follow the run: the step it's on (or asking about) is the selected one.
    const at = state.question?.step === -1 ? -1 : cxCurrent(state);
    const moved = at !== before || e.kind === "question";
    if (state.follow && moved && (at >= 0 || state.question?.step === -1) && at !== state.selected && !state.editing) state.selected = at;
    cxRender(state, { details: moved && !state.editing });
  };
  stream.onerror = () => { if (CX_ENDED.includes(state.run.status)) stream.close(); };
}

// cxWhere says what the run is doing when it isn't on a step: starting,
// stopping its background jobs once the steps are done, or finished.
function cxWhere(state) {
  if (CX_ENDED.includes(state.run.status)) return "Finished";
  if (state.steps.every((s) => s.status === "pending")) return "Starting…";
  const bg = state.steps.filter((s) => s.status === "background").length;
  return bg ? `All steps done · stopping ${bg} background job${bg === 1 ? "" : "s"}…` : "All steps done · finishing…";
}

// cxCurrent is the step the run is on: the one a question is about, else
// the one running, else the first not done (-1 when it's all done).
function cxCurrent(state) {
  if (state.question?.step >= 0) return state.question.step;
  const r = state.steps.findIndex((s) => s.status === "running");
  if (r >= 0) return r;
  if (CX_ENDED.includes(state.run.status)) {
    const f = state.steps.findIndex((s) => ["failed", "aborted"].includes(s.status));
    return f >= 0 ? f : -1;
  }
  return state.steps.findIndex((s) => s.status === "pending");
}

// The selection is a step's index, or -1 for the run's inputs (listed
// first, without a number).
function cxSelected(state) {
  if (state.selected == null) state.selected = state.launch ? -1 : Math.max(0, cxCurrent(state));
  return state.selected;
}

function cxRender(state, { details = true } = {}) {
  cxRenderTop(state);
  cxRenderList(state);
  if (details) cxRenderDetails(state);
  cxRenderAction(state);
  cxRenderLog(state);
  cxLayout(state);
}

// ---------- top: the run, where it is, the toolbar ----------

// cxPhase: what the run is doing, in a word (and how it looks).
function cxPhase(state) {
  const r = state.run, q = state.question;
  if (CX_ENDED.includes(r.status)) return { label: r.status, cls: `s-${r.status}` };
  if (q) {
    const what = { before: "paused before the step", after: "paused: check the result", failure: "step failed: what now?", input: "needs an input",
      confirm: "asks before running", ask: "the step asks you", ask_text: "the step asks you", danger: "dangerous: double check", typed: "type to confirm", manual: "manual step: waiting for you", choose: "which way?" }[q.type] || "waiting for you";
    return { label: what, cls: q.type === "failure" ? "s-failed" : "waiting" };
  }
  return { label: "running", cls: "s-running" };
}

function cxRenderTop(state) {
  const r = state.run, el = main.querySelector(".cx-top");
  const live = state.live && !CX_ENDED.includes(r.status);
  const cur = cxCurrent(state), st = state.steps[cur];
  const ph = cxPhase(state);
  // Time in the section: since its first step started.
  let section = "", inSection = "";
  if (st?.section) {
    section = st.section;
    const first = state.steps.find((s) => s.section === section && s.started && !s.started.startsWith("0001"));
    if (first) inSection = `<span class="cx-clock" data-since="${esc(first.started)}" ${!live ? `data-until="${esc(r.ended || "")}"` : ""}></span>`;
  }
  el.innerHTML = `
    <div class="cx-title">
      <a href="#/runs/${enc(r.id)}" class="cx-back" data-classic title="The classic run page (with the runbooks list)">☰</a>
      <h1><a href="#/runbooks/${enc(r.runbook)}">${esc(r.runbook)}</a></h1>
      <span class="muted">${r.runbook_version ? `v${r.runbook_version} · ` : ""}${esc(r.id)} · ${esc(r.mode)}${r.dry_run ? " · dry run" : ""}</span>
      <span class="spacer"></span>
      <button class="link small" data-reset-layout title="Put the panes back where they started">⊞ Reset layout</button>
      <span class="badge ${esc(ph.cls)}">${ICONS[r.status] || ""} ${esc(ph.label)}</span>
      <span class="muted" title="Since the run started"><span class="cx-clock" data-since="${esc(r.started)}" ${!live ? `data-until="${esc(r.ended || "")}"` : ""}></span></span>
    </div>
    <div class="cx-status">
      ${cur >= 0 ? `<span class="cx-at"><b>Step ${cur + 1}</b> of ${state.steps.length}: ${esc(st.name)}</span>` : `<span class="cx-at muted">${cxWhere(state)}</span>`}
      ${section ? `<span class="cx-chip" title="The section the run is in, and how long since its first step started">§ ${esc(section)} ${inSection}</span>` : ""}
      ${cxWindowChip(r)}
      ${(state.messages || []).slice(-1).map((m) => `<span class="muted small">ℹ ${esc(m)}</span>`).join("")}
    </div>
    <div class="cx-toolbar">${cxToolbar(state, live)}</div>
    ${cxBanners(state, live)}`;
  cxBindTop(state, el, live);
}

function cxWindowChip(r) {
  const w = r.window;
  if (!w) return "";
  const target = parseOffset(w.target);
  if (w.opened && !w.closed) {
    return `<span class="cx-chip cx-window open" title="Production is down until the window closes">🔴 Maintenance window open <span class="mw-clock" data-since="${esc(w.opened)}" data-target="${target ?? ""}"></span>${w.target ? ` / ${esc(w.target)}` : ""}</span>`;
  }
  if (w.opened && w.closed) {
    const len = (new Date(w.closed) - new Date(w.opened)) / 1000;
    return `<span class="cx-chip cx-window ${target != null && len > target ? "over" : ""}">🟢 Window closed after ${fmtOffset(len, false)}${w.target ? ` (target ${esc(w.target)})` : ""}</span>`;
  }
  return `<span class="cx-chip cx-window muted">Window not open yet${w.open_step >= 0 ? ` (opens at step ${w.open_step + 1})` : ""}${w.target ? ` · target ${esc(w.target)}` : ""}</span>`;
}

// cxToolbar: the run's controls. Answers the run's question with buttons
// where that's all it needs (inputs, typed checks and choices are answered
// in the action pane), and the step/pause controls while it's live.
function cxToolbar(state, live) {
  const r = state.run, q = state.question, ctl = state.controls || {};
  const btn = (act, label, cls = "", title = "", on = false) => `<button data-act="${esc(act)}" class="${cls} ${on ? "on" : ""}" title="${esc(title)}">${label}</button>`;
  if (!live) {
    // Optional steps don't decide where a resume starts (engine.ResumePoint).
    const stop = state.steps.findIndex((s) => !s.optional && !CX_DONE.includes(s.status));
    return [
      ["failed", "aborted"].includes(r.status) && stop >= 0 ? btn("resume", "▶ Resume…", "primary", `Continue from step ${stop + 1}`) : "",
      btn("rerun", "↻ Run again…"),
      btn("raw", "Raw log", "", "The whole run as a plain log"),
    ].join("");
  }
  const QA = {
    before: [["run", "▶ Run step", "primary"], ["skip", "⏭ Skip"], ["continue", "⏩ Run the rest", "", "Run this and the remaining steps without pausing"]],
    after: [["continue", "▶ Continue", "primary"], ["retry", "↻ Run again"], ["fail", "✘ Mark failed…"]],
    confirm: [["run", "▶ Run", "primary"], ["skip", "⏭ Skip"]],
    ask: [["yes", "✔ Yes", "primary"], ["no", "✘ No"]],
    manual: [["done", "✔ Done", "primary"], ["skip", "⏭ Skip"], ["fail", "✘ Failed…"]],
    failure: [["retry", "↻ Retry", "primary"], ["way", "↪ Another way…"], ["skip", "⏭ Skip"], ["force", "⚑ Force success…"]],
  };
  let answers = "";
  if (q && QA[q.type]) {
    answers = QA[q.type].filter(([v]) => q.options?.includes(v)).map(([v, l, c, t]) => btn(`answer:${v}`, l, c, t)).join("");
  } else if (q) {
    answers = btn("focus-question", "↓ Answer below", "primary", "This question needs more than a button: it's in the action pane");
  }
  const cur = cxCurrent(state);
  const next = cur + 1; // 0-based step after the current one
  const has = (list, i) => (list || []).includes(i + 1);
  return `${answers}${answers ? `<span class="cx-sep"></span>` : ""}
    ${btn("step-mode", "⏯ Step through", "", "Pause before every remaining step", !!ctl.step_mode)}
    ${next < state.steps.length ? btn("pause-next", "⏸ Pause before next", "", `Pause before step ${next + 1}`, has(ctl.pause_before, next)) : ""}
    ${cur >= 0 ? btn("pause-after", "⏸ Pause after this", "", `Pause after step ${cur + 1} to check its result`, has(ctl.pause_after, cur)) : ""}
    <span class="spacer"></span>
    ${btn("abort", "■ Abort run", "danger")}`;
}

function cxBanners(state, live) {
  const r = state.run, p = state.plan;
  let out = "";
  const edits = r.edits || [];
  if (edits.length && !r.edits_saved) {
    const steps = [...new Set(edits.map((e) => e.step + 1))].sort((a, b) => a - b);
    out += `<div class="cx-banner edits">✎ ${steps.length === 1 ? `Step ${steps[0]} was` : `Steps ${steps.join(", ")} were`} changed in this run; the runbook doesn't have ${steps.length === 1 ? "the change" : "the changes"}. <button data-act="save-edits" class="primary">Save to runbook</button></div>`;
  }
  if (live && p && p.latest > p.version && p.latest !== state.versionDismissed && (p.changed?.length || p.shape)) {
    out += p.shape
      ? `<div class="cx-banner version">⚠ The runbook was saved as version ${p.latest} during this run, and ${esc(p.shape)}: this run can't take it. Carry on with version ${p.version}, or abort and run version ${p.latest}.
          <button data-act="dismiss-version">Carry on</button><button data-act="abort" class="danger">Abort run</button></div>`
      : `<div class="cx-banner version">⚠ The runbook was saved as version ${p.latest} during this run. Steps not run yet that it changes: ${p.changed.map((n) => `<a href="#" data-pick="${n - 1}">${n}</a>`).join(", ")}.
          <button data-act="adopt" class="primary">Use version ${p.latest} for the rest</button><button data-act="dismiss-version">Keep version ${p.version}</button><button data-act="abort" class="danger">Abort run</button></div>`;
  }
  return out;
}

function cxBindTop(state, el, live) {
  const r = state.run;
  el.querySelectorAll("[data-act]").forEach((b) => (b.onclick = async () => {
    const act = b.dataset.act;
    try {
      if (act.startsWith("answer:")) return cxAnswer(state, act.slice(7));
      switch (act) {
        case "focus-question": return main.querySelector(".cx-action .question input, .cx-action .question select, .cx-action .question button")?.focus();
        case "abort": return abortRun(state);
        case "resume": location.hash = `#/runbooks/${enc(r.runbook)}/console?resume=${enc(r.id)}`; return;
        case "rerun": location.hash = `#/runbooks/${enc(r.runbook)}/console?rerun=${enc(r.id)}`; return;
        case "raw": return cxRawLog(state);
        case "save-edits": {
          if (!confirm(`Save this run's step edits as a new version of “${r.runbook}”?\nThe YAML is reformatted, so comments in it are dropped.`)) return;
          const { version } = await api("POST", `/api/runs/${enc(r.id)}/save-edits`);
          r.edits_saved = version;
          toast(`Saved as version ${version}`);
          state.onState();
          return cxRender(state, { details: false });
        }
        case "dismiss-version": state.versionDismissed = state.plan.latest; return cxRender(state, { details: false });
        case "adopt": {
          const res = await api("POST", `/api/runs/${enc(r.id)}/adopt-version`, { version: state.plan.latest });
          r.runbook_version = res.version;
          toast(`On version ${res.version}: ${res.changed.length ? `steps ${res.changed.join(", ")} changed` : "no remaining step changed"}${res.restarted ? "; the current step starts again" : ""}`);
          state.onState();
          return cxRender(state);
        }
      }
      // Step and pause controls.
      const ctl = { step_mode: false, skip: [], pause_before: [], pause_after: [], ...(state.controls || {}) };
      const toggle = (k, n) => (ctl[k] = ctl[k].includes(n) ? ctl[k].filter((x) => x !== n) : [...ctl[k], n]);
      const cur = cxCurrent(state);
      if (act === "step-mode") ctl.step_mode = !ctl.step_mode;
      if (act === "pause-next") toggle("pause_before", cur + 2);
      if (act === "pause-after") toggle("pause_after", cur + 1);
      state.controls = await api("PUT", `/api/runs/${enc(r.id)}/controls`, ctl);
      cxRender(state, { details: false });
    } catch (e) { toast(e.message); }
  }));
  el.querySelector("[data-classic]").onclick = () => runView("classic");
  el.querySelector("[data-reset-layout]").onclick = () => cxResetLayout(state);
  el.querySelectorAll("[data-pick]").forEach((a) => (a.onclick = (e) => { e.preventDefault(); cxSelect(state, +a.dataset.pick); }));
}

async function cxAnswer(state, value) {
  const q = state.question;
  if (!q) return;
  let note = "";
  if (value === "force") {
    note = (prompt("Why accept this failure as success? (required)") || "").trim();
    if (!note) return toast("Forcing a step needs a reason");
  }
  if (value === "fail") {
    const why = prompt(q.type === "after" ? "What's wrong with the result?" : "Why did this step fail?");
    if (why === null) return;
    note = why;
  }
  try { await api("POST", `/api/runs/${enc(state.run.id)}/answer`, { question_id: q.id, value, note }); }
  catch (e) { toast(e.message); }
}

async function cxRawLog(state) {
  let text;
  try { text = await (await fetch(`/api/runs/${enc(state.run.id)}/log`, { headers: { "X-Runnr": "1" } })).text(); } catch (e) { return toast(e.message); }
  const dlg = document.createElement("dialog");
  dlg.className = "edit-step cx-raw";
  dlg.innerHTML = `<div class="row"><h3>Raw log</h3><span class="spacer"></span><a class="button" href="/api/runs/${enc(state.run.id)}/log?download=1">Download</a><button data-close>Close</button></div><pre class="log">${esc(text)}</pre>`;
  document.body.append(dlg);
  dlg.querySelector("[data-close]").onclick = () => dlg.close();
  dlg.onclose = () => dlg.remove();
  dlg.showModal();
}

// ---------- left: sections and steps ----------

function cxRenderList(state) {
  const el = main.querySelector(".cx-list");
  const keep = el.scrollTop;
  const sel = cxSelected(state), cur = cxCurrent(state);
  const changed = new Set((state.plan?.latest > state.plan?.version && state.plan.latest !== state.versionDismissed) ? state.plan.changed || [] : []);
  const w = state.run.window;
  const inWin = (i) => w && w.open_step >= 0 && i >= w.open_step && (w.close_step < 0 || i <= w.close_step);
  let html = `<div class="cx-list-head"><b>Steps</b><span class="spacer"></span>
    ${state.launch ? `<span class="muted small">${state.include.filter(Boolean).length} of ${state.steps.length} included</span>`
      : `<label class="small" title="Keep the step the run is on selected"><input type="checkbox" data-follow ${state.follow ? "checked" : ""}> follow the run</label>`}</div><ol>`;
  {
    const ins = cxInputs(state);
    const missing = ins.filter((x) => x.required && !x.def && !x.secret && !x.value).length;
    const asking = state.question?.step === -1;
    const started = !state.launch && state.steps.some((x) => x.status !== "pending");
    const cls = ["cx-inputs-item", sel === -1 && "sel", asking && "waiting", started && "st-succeeded", missing && "needs"].filter(Boolean).join(" ");
    html += `<li class="cx-step ${cls}" data-i="-1" title="The run's inputs and the values they have">
      <span class="ic">${asking ? "⏸" : missing ? "!" : started ? "✔" : "≡"}</span><span class="n"></span><span class="nm">Inputs</span>
      <span class="mk"></span><span class="tm">${missing ? `${missing} to fill in` : ins.length ? `${ins.length} value${ins.length === 1 ? "" : "s"}` : "none"}</span></li>`;
  }
  state.steps.forEach((s, i) => {
    if (s.section && (i === 0 || state.steps[i - 1].section !== s.section)) {
      const members = state.steps.filter((x) => x.section === s.section);
      const done = members.every((x) => CX_DONE.includes(x.status));
      const info = (state.rbSections || []).find((x) => x.name === s.section);
      const at = parseOffset(info?.window_offset);
      html += `<li class="cx-sec ${done ? "done" : ""}" title="${esc(info?.description || s.section)}"><span>${done ? "✔ " : ""}${esc(s.section)}</span>${at != null ? `<span class="cx-when">T${fmtOffset(at)}</span>` : ""}</li>`;
    }
    const v = state.plan?.steps?.[i] || {};
    const waiting = state.question?.step === i;
    const cls = [`st-${s.status}`, state.include && !state.include[i] && "excluded", waiting && "waiting", i === sel && "sel", i === cur && "cur", inWin(i) && "in-window", s.note === "before start step" && "dim"].filter(Boolean).join(" ");
    const marks = [
      s.status === "background" || v.background ? `<span title="Runs in the background">◐</span>` : "",
      v.confirm_typing ? `<span class="cx-tag" title="Confirmed by typing ${esc(v.confirm_typing)}">type</span>` : v.dangerous ? `<span title="Dangerous">⚠</span>` : "",
      s.optional ? `<span title="Optional">◇</span>` : "",
      s.edited ? `<span title="Changed in this run">✎</span>` : "",
      changed.has(i + 1) ? `<span class="cx-new" title="The runbook's newer version changes this step">new</span>` : "",
      state.before?.has(i) ? `<span title="Pauses before this step">⏸↑</span>` : "",
      state.after?.has(i) ? `<span title="Pauses after this step">⏸↓</span>` : "",
    ].join("");
    const right = s.started && !s.started.startsWith("0001") ? (duration(s.started, s.ended) || "") : v.offset ? `T${esc(v.offset)}` : "";
    html += `<li class="cx-step ${cls}" data-i="${i}" title="${esc(`${i + 1}. ${s.name}${s.note ? ` (${s.note})` : ""}`)}">
      <span class="ic">${waiting ? "⏸" : ICONS[s.status] || ""}</span><span class="n">${i + 1}</span><span class="nm">${esc(s.name)}</span>
      <span class="mk">${marks}</span><span class="tm">${right}</span></li>`;
  });
  el.innerHTML = html + "</ol>";
  el.scrollTop = keep;
  el.querySelectorAll(".cx-step").forEach((li) => (li.onclick = () => cxSelect(state, +li.dataset.i)));
  el.querySelector("[data-follow]")?.addEventListener("change", (e) => {
    state.follow = e.target.checked;
    if (state.follow) cxSelect(state, Math.max(0, cxCurrent(state)), true);
  });
  // Keep the run's step in view while following.
  if (state.follow && cur >= 0) el.querySelector(`.cx-step[data-i="${cur}"]`)?.scrollIntoView({ block: "nearest" });
}

function cxSelect(state, i, follow = false) {
  if (state.editing && state.editing.i !== i && !confirm("Discard your unapplied edit?")) return;
  state.editing = null;
  state.selected = i;
  state.follow = follow || i === cxCurrent(state);
  (state.render || cxRender)(state);
}

// ---------- details: the step as defined (editable while live) ----------

function cxEditable(state, i) {
  if (!state.live || CX_ENDED.includes(state.run.status) || !state.plan?.live) return false;
  if (state.question?.step === i) return true;
  const cur = state.steps.findIndex((s) => ["pending", "running"].includes(s.status));
  return cur >= 0 && i >= cur && ["pending", "running"].includes(state.steps[i].status);
}

// cxInputs are the runbook's inputs with the values they have: as typed
// before the run, else the run's (secrets masked).
function cxInputs(state) {
  const defs = state.rb?.inputs || state.inputDefs || [];
  const vals = state.launch ? state.vars : state.run.inputs || {};
  const names = [...new Set([...defs.map((d) => d.name), ...Object.keys(vals)])];
  return names.map((n) => {
    const d = defs.find((x) => x.name === n) || {};
    return { name: n, value: vals[n] ?? "", def: d.default || "", required: !!d.required, secret: !!d.secret, choices: d.choices || [], description: d.description || "" };
  });
}

// cxEditTarget: what "Edit this step" changes. In a live run, the step for
// this run (the current step or a later one; null once it's run); before a
// run, or after one, the saved runbook.
function cxEditTarget(state, i) {
  if (i < 0) return null;
  if (state.launch) return "runbook";
  if (state.live && !CX_ENDED.includes(state.run.status)) return cxEditable(state, i) ? "run" : null;
  return "runbook";
}

function cxRenderDetails(state) {
  const pane = main.querySelector(".cx-details");
  const i = cxSelected(state);
  const head = pane.querySelector(".cx-pane-head"), body = pane.querySelector(".cx-pane-body");
  if (i === -1) return cxRenderInputsDetails(state, head, body);
  const s = state.steps[i];
  if (!s) return;
  const v = state.plan?.steps?.[i] || {};
  const target = cxEditTarget(state, i);
  const editingThis = state.editing?.i === i;
  head.innerHTML = `<b>Step ${i + 1}</b><span class="cx-head-name" title="${esc(s.name)}">${esc(s.name)}</span><span class="spacer"></span>
    ${editingThis ? `<button data-edit-show title="Go to the editor">✎ Editing in ${cxPlaced(state, "edit") ? "Step edit" : "Step activity"}</button>`
      : target ? `<button data-edit title="${target === "run" ? "Change this step for this run" : "Change this step in the runbook (saved as a new version)"}">✎ Edit this step</button>`
      : `<span class="muted small">already run</span>`}`;
  head.querySelector("[data-edit-show]")?.addEventListener("click", () => {
    if (!cxPlaced(state, "edit")) { state.actionTab = "edit"; (state.launch ? cxLaunchRenderAction : cxRenderAction)(state); }
    cxEditHostEl(state)?.scrollIntoView({ block: "nearest" });
  });
  const row = (k, val) => (val ? `<tr><td>${k}</td><td>${val}</td></tr>` : "");
  const code = (t) => `<code>${esc(t)}</code>`;
  const flags = [v.always && "setup (always)", v.optional && "optional", v.dangerous && "dangerous", v.confirm && "confirm first", v.background && "background", v.choose && "asks which way"].filter(Boolean);
  const text = `
    ${v.description ? `<div class="doing">${esc(v.description)}</div>` : ""}
    ${v.expect ? `<div class="expect"><b>Expect:</b> ${esc(v.expect)}</div>` : ""}
    <table class="kv cx-kv">
      ${row("kind", esc(v.kind || s.kind) + (flags.length ? ` · ${esc(flags.join(" · "))}` : ""))}
      ${row("section", esc(v.section || ""))}${row("owner", esc(v.owner || ""))}
      ${row("only when", v.when && code(v.when))}${row("skip if", v.skip_if && code(v.skip_if))}
      ${row("confirm by typing", v.confirm_typing && code(v.confirm_typing))}
      ${row("waits for", (v.wait_for || []).map(code).join(", "))}${row("stops after", v.stop_after && code(v.stop_after))}
      ${row("ok if output matches", v.ok_if && code(v.ok_if))}${row("fails if output matches", v.fail_if && code(v.fail_if))}
      ${row("verified on resume by", v.verify && code(v.verify))}
      ${row("dir", v.dir && code(v.dir))}${row("timeout", esc(v.timeout || ""))}${row("retries", v.retries ? String(v.retries) : "")}
      ${row("repeat", esc(v.repeat || ""))}
      ${row("planned", v.offset ? `T${esc(v.offset)} (relative to the window opening)` : "")}
      ${row("publishes", [...(v.outputs || []), ...(v.secret_outputs || []).map((o) => `${o} (secret)`)].map(code).join(" "))}
    </table>
    ${s.notes?.length ? renderNotes(s.notes) : ""}`;
  const codeHTML = v.ways?.length ? v.ways.map((w) => `<h4>${esc(w.name)}${w.preferred ? " <span class='muted small'>(preferred)</span>" : ""}</h4><pre class="cmd">${esc(w.command)}</pre>`).join("")
    : v.command ? `<pre class="cmd">${esc(v.command)}</pre>` : `<p class="muted">No code.</p>`;
  body.innerHTML = `<div class="cx-split" style="--cx-split:${state.sizes.split || 0.45}">
    <div class="cx-split-a">${text}</div><div class="cx-gutter-v" data-split title="Drag to resize"></div><div class="cx-split-b">${codeHTML}</div></div>`;
  cxBindSplit(state, body.querySelector(".cx-split"));
  head.querySelector("[data-edit]")?.addEventListener("click", () => cxStartEdit(state, i));
}

// The details of the inputs: each one's definition, and its value.
function cxRenderInputsDetails(state, head, body) {
  const ins = cxInputs(state);
  head.innerHTML = `<b>Inputs</b><span class="muted small">${state.launch ? "the values this run will start with" : "the values this run started with"}</span>`;
  body.innerHTML = ins.length ? `<table class="cx-inputs-table">
      <tr><th>Input</th><th>Value</th><th>Default</th><th></th></tr>
      ${ins.map((x) => `<tr class="${x.required && !x.def && !x.secret && !x.value ? "needs" : ""}">
        <td><code>${esc(x.name)}</code>${x.required ? " *" : ""}${x.description ? `<div class="muted small">${esc(x.description)}</div>` : ""}</td>
        <td>${x.secret ? (x.value ? "••••" : `<span class="muted">${state.launch ? "asked for when needed" : "not stored"}</span>`) : x.value ? `<code>${esc(x.value)}</code>` : x.def ? `<span class="muted">default</span>` : `<span class="muted">—</span>`}</td>
        <td>${x.def ? `<code>${esc(x.def)}</code>` : ""}</td>
        <td class="muted small">${[x.secret && "secret", x.choices.length && `one of ${x.choices.join(", ")}`].filter(Boolean).map(esc).join(" · ")}</td></tr>`).join("")}
    </table>${state.launch ? `<p class="muted small">Fill them in in the pane below.</p>` : ""}` : `<p class="muted">This runbook takes no inputs.</p>`;
}

// cxStartEdit opens the editor's own form for step i: the runbook as saved
// (for the sections, later steps and functions the form offers), with the
// step as this run has it when editing it for the run.
async function cxStartEdit(state, i) {
  const target = cxEditTarget(state, i);
  try {
    const data = await api("GET", `/api/runbooks/${enc(state.run.runbook)}`);
    const parsed = await api("POST", "/api/parse", data.raw, true);
    if (!parsed.ok) throw new Error(`The saved runbook doesn't parse: ${parsed.error}`);
    const doc = parsed.runbook;
    if (target === "run") {
      const def = await api("GET", `/api/runs/${enc(state.run.id)}/steps/${i + 1}`);
      const st = await api("POST", "/api/parse-step", def.yaml, true);
      if (!st.ok) throw new Error(st.error);
      if (i >= doc.steps.length) throw new Error("the saved runbook has fewer steps than this run; edit the step in the classic run page");
      doc.steps[i] = st.step;
    }
    if (i >= doc.steps.length) throw new Error(`the runbook has no step ${i + 1} now`);
    if (state.editing && state.editing.i !== i && state.editing.handle?.dirty() && !confirm(`Discard your change to step ${state.editing.i + 1}?`)) return;
    state.editing = { i, target, doc, comments: target === "run" ? {} : parsed.comments, version: data.version, error: "" };
    state.follow = false;
    // Without a Step edit pane, the editor is a tab of the step's activity.
    if (!cxPlaced(state, "edit")) state.actionTab = "edit";
    (state.render || cxRender)(state);
  } catch (e) { toast(e.message); }
}

function cxRenderEditor(state, head, body) {
  const ed = state.editing, i = ed.i;
  const waiting = state.question?.step === i;
  const run = ed.target === "run";
  head.innerHTML = `<b>Edit step ${i + 1}</b><span class="cx-head-name" title="${esc(state.steps[i]?.name || "")}">${esc(state.steps[i]?.name || "")}</span><span class="muted small">${run ? "for this run" : `in the runbook (version ${ed.version})`}</span><span class="spacer"></span>
    <button class="primary" data-apply>${run ? (waiting ? "Apply and run it again" : "Apply") : "Save to runbook"}</button>
    <button data-view title="Close the editor">✕ Close editor</button>`;
  // The form is built once, and moves with its host; re-rendering the head
  // leaves what's typed alone.
  if (ed.wrap && ed.wrap.parentElement !== body) body.replaceChildren(ed.wrap);
  if (!(ed.mounted && body.contains(ed.mounted))) {
    const note = run
      ? `${waiting ? "The run is waiting on this step: applying starts it again with the new version."
          : state.steps[i].status === "running" ? "This step is running now; the change applies if it runs again (retry)." : "The run uses the new version when it gets here."}
         Save it to the runbook afterwards (the banner above), or the change is only in this run.`
      : state.launch ? "Saving makes a new version of the runbook with just this step changed. The run uses it when you start."
      : `Saving makes a new version of the runbook with just this step changed; this run isn't changed.${state.run.runbook_version && ed.version !== state.run.runbook_version ? ` This run was on version ${state.run.runbook_version}; the runbook is at ${ed.version}: check it's the same step.` : ""}`;
    body.innerHTML = `<div class="cx-edit-wrap"><p class="muted small cx-edit-note">${note}</p><div class="cx-step-editor"></div><div class="error-text"></div></div>`;
    ed.wrap = body.querySelector(".cx-edit-wrap");
    ed.mounted = body.querySelector(".cx-step-editor");
    ed.handle = renderStepEditor(ed.mounted, { doc: ed.doc, comments: ed.comments, i });
  }
  const err = body.querySelector(".error-text");
  head.querySelector("[data-view]").onclick = () => {
    if (ed.handle.dirty() && !confirm("Discard your change?")) return;
    state.editing = null;
    if (state.actionTab === "edit") state.actionTab = "now";
    (state.render || cxRender)(state);
  };
  head.querySelector("[data-apply]").onclick = async () => {
    try {
      const step = await ed.handle.step();
      if (!step) return;
      const yaml = JSON.stringify(step); // JSON is YAML
      if (run) {
        const res = await api("PUT", `/api/runs/${enc(state.run.id)}/steps/${i + 1}`, yaml, true);
        state.run.edits = res.edits;
        state.run.edits_saved = 0;
        Object.assign(state.steps[i], { edited: true, name: res.view.name, kind: res.view.kind });
        toast(res.restarted ? "Step changed; running it again" : "Step changed for this run");
      } else {
        const res = await api("PUT", `/api/runbooks/${enc(state.run.runbook)}/steps/${i + 1}`, yaml, true, { "If-Match": String(ed.version) });
        toast(`Saved to the runbook as version ${res.version}`);
        if (state.launch) {
          state.rb = res.runbook;
          state.version = res.version;
          Object.assign(state.steps[i], { name: res.runbook.steps[i].name, section: res.runbook.steps[i].section });
        }
      }
      state.editing = null;
      if (state.actionTab === "edit") state.actionTab = "now";
      if (state.launch) { state.refresh(); return cxLaunchRender(state); }
      state.onState?.();
      cxRender(state);
    } catch (e) { err.textContent = e.message; }
  };
}

// cxBindSplit: the text | code divider in the details pane.
function cxBindSplit(state, el) {
  const g = el.querySelector("[data-split]");
  g.onpointerdown = (e) => {
    e.preventDefault();
    g.setPointerCapture(e.pointerId);
    const box = el.getBoundingClientRect();
    document.body.classList.add("cx-dragging");
    g.onpointermove = (ev) => {
      state.sizes.split = Math.max(0.15, Math.min(0.85, (ev.clientX - box.left) / box.width));
      el.style.setProperty("--cx-split", state.sizes.split);
    };
    g.onpointerup = () => { g.onpointermove = g.onpointerup = null; document.body.classList.remove("cx-dragging"); cxSaveSizes(state.sizes); };
  };
}

// ---------- action: what the step is doing, did, or will do ----------

function cxRenderAction(state) {
  const pane = main.querySelector(".cx-action");
  const i = cxSelected(state), s = state.steps[i], q = state.question;
  if (i === -1) return cxRenderInputsAction(state, pane);
  const v = state.plan?.steps?.[i] || {};
  const head = pane.querySelector(".cx-pane-head"), body = pane.querySelector(".cx-action-body");
  const scroll = body.querySelector("pre.log");
  const atEnd = !scroll || scroll.scrollHeight - scroll.scrollTop - scroll.clientHeight < 8;
  const cur = cxCurrent(state);
  // A step's terminal (a tty step, or a shell opened at it) is a tab beside Now.
  const showFg = !cxPlaced(state, "terminal") && state.fg && !state.fg.ended && (state.fg.step === i || state.fg.step === cur);
  const tabs = cxActionTabs(state, showFg);
  const onTerm = tabs.on === "term";
  head.innerHTML = `<b>${s?.status === "pending" ? "Will do" : s?.status === "running" || q?.step === i ? "Now" : "What happened"}</b>
    ${q?.step === i ? `<span class="badge waiting">⏸ waiting for you</span>` : s ? `<span class="badge s-${esc(s.status)}">${ICONS[s.status] || ""} ${esc(s.status)}</span>` : ""}
    ${tabs.html}
    <span class="spacer"></span>
    ${i !== cur && cur >= 0 ? `<button class="link" data-goto="${cur}">→ step ${cur + 1}, where the run is</button>` : ""}
    ${state.live ? `<button class="link" data-note="${i + 1}">✎ note</button>` : ""}`;
  let html = "";
  // An input the run is asking for, whatever's selected.
  if (q?.step === -1) html += renderQuestion(q, state.run.id);
  if (q?.step === i) html += renderQuestion(q, state.run.id);
  if (s) {
    if (s.status === "pending" && !s.log.length) {
      const where = state.fg?.step === i ? "" : v.background ? " It will start in its own terminal (the background pane) and the run will go on." : v.kind === "tty" ? " It will need a terminal: it opens here." : "";
      html += `<p class="muted">Not reached yet.${where}</p>${v.command ? `<pre class="cmd">${esc(v.command)}</pre>` : ""}`;
    } else {
      // A step on a terminal shows it below; its log is the recorded session once it ends.
      const asking = q?.step === i && ["before", "confirm", "danger", "typed", "choose"].includes(q.type) && !s.log.length;
      html += asking || (state.fg?.step === i && !state.fg.ended && s.status === "running") ? "" : thisRun(s, i);
    }
  }
  body.innerHTML = html;
  const log = body.querySelector("pre.log");
  if (log && atEnd) log.scrollTop = log.scrollHeight;
  body.querySelectorAll(".question").forEach((el) => bindQuestion(el, q, state.run.id));
  head.querySelector("[data-goto]")?.addEventListener("click", () => cxSelect(state, cur, true));
  head.querySelector("[data-note]")?.addEventListener("click", async () => {
    const text = (prompt(`Note on step ${i + 1}:`) || "").trim();
    if (!text) return;
    try { pushNote(state, i, await api("POST", `/api/runs/${enc(state.run.id)}/notes`, { step: i + 1, text, author: author() })); cxRender(state); } catch (e) { toast(e.message); }
  });
  // A step's terminal shows in this pane when that step is selected (or it's the run's step).
  cxBindActionTabs(state, head, () => cxRenderAction(state));
  const fg = main.querySelector(".cx-fg");
  body.hidden = tabs.on !== "now";
  fg.hidden = !onTerm;
  if (onTerm) {
    const s = state.fg;
    const bar = fg.querySelector(".cx-tabs");
    if (bar && bar.dataset.for !== s.id) {
      bar.dataset.for = s.id;
      bar.innerHTML = `<span class="cx-fg-title">⌨ ${esc(s.title)}</span><span class="muted small cx-fg-hint">${s.shell ? "the run's variables are loaded · type exit to go back" : "type here when it asks"}</span>
        <span class="spacer"></span><button class="link" data-fg-end title="Hang up, like closing the terminal">End session</button>`;
      bar.querySelector("[data-fg-end]").onclick = () => { if (confirm(`End “${s.title}”? Whatever's running in it is hung up.`)) sendTerm(s, { end: true }); };
    }
    cxShowSession(s, fg.querySelector(".cx-term"));
  }
  cxRenderTermPane(state);
}

// cxActionTabs are the step activity pane's tabs: Now, and the step's
// terminal when there's no Terminal target pane, and the step editor when
// there's no Step edit pane. on is the one showing.
function cxActionTabs(state, showFg) {
  const editTab = !!state.editing && !cxPlaced(state, "edit");
  const want = state.actionTab || "now";
  const on = want === "term" && showFg ? "term" : want === "edit" && editTab ? "edit" : "now";
  if (!showFg && !editTab) return { on, html: "" };
  const q = state.question, i = cxSelected(state);
  const b = (k, label, title) => `<button data-atab="${k}" class="${on === k ? "on" : ""}" title="${esc(title)}">${label}</button>`;
  return { on, html: `<div class="seg cx-act-tabs">${b("now", `Now${q && q.step === i ? " ●" : ""}`, "The step: its question and output")}${showFg ? b("term", `⌨ ${state.fg.shell ? "Shell" : "Terminal"}`, state.fg.title) : ""}${editTab ? b("edit", `✎ Edit step ${state.editing.i + 1}`, "The step's editor") : ""}</div>` };
}

function cxBindActionTabs(state, head, redraw) {
  head.querySelectorAll("[data-atab]").forEach((b) => (b.onclick = () => {
    state.actionTab = b.dataset.atab;
    redraw();
    cxRenderEditHost(state);
    if (state.actionTab === "term") state.fg?.xt.focus();
  }));
}

// cxEditHostEl is where the step editor goes: the Step edit pane, else the
// step activity pane (as a tab).
function cxEditHostEl(state) {
  const frame = cxPlaced(state, "edit") ? state.frames.edit : state.frames.action;
  return frame?.querySelector(".cx-edit-host");
}

function cxRenderEditHost(state) {
  const host = cxEditHostEl(state);
  for (const h of main.querySelectorAll(".cx-edit-host")) if (h !== host) h.hidden = true;
  const empty = state.frames.edit?.querySelector(".cx-edit-empty");
  if (empty) empty.hidden = !!state.editing;
  if (!host) return;
  const inAction = !cxPlaced(state, "edit");
  host.hidden = !state.editing || (inAction && state.actionTab !== "edit");
  if (state.editing) cxRenderEditor(state, host.querySelector(".cx-edit-head"), host.querySelector(".cx-edit-body"));
}

// cxRenderTermPane shows the terminal a step has open (a tty step, or a
// shell) in the Terminal pane, when one is on screen.
function cxRenderTermPane(state) {
  if (!cxPlaced(state, "terminal")) return;
  const pane = main.querySelector(".cx-termpane");
  const s = state.fg && !state.fg.ended ? state.fg : null;
  pane.querySelector(".cx-termpane-empty").hidden = !!s;
  pane.querySelector(".cx-termpane-title").textContent = s ? `⌨ ${s.title}` : "";
  const end = pane.querySelector("[data-tp-end]");
  end.hidden = !s;
  end.onclick = () => { if (s && confirm(`End “${s.title}”? Whatever's running in it is hung up.`)) sendTerm(s, { end: true }); };
  if (s) cxShowSession(s, pane.querySelector(".cx-term"));
}

// In a run, the inputs' action: the question asking for one, or the values
// it started with.
function cxRenderInputsAction(state, pane) {
  const q = state.question;
  const head = pane.querySelector(".cx-pane-head"), body = pane.querySelector(".cx-action-body");
  const cur = cxCurrent(state);
  head.innerHTML = `<b>${q?.step === -1 ? "Now" : "What happened"}</b>${q?.step === -1 ? `<span class="badge waiting">⏸ waiting for you</span>` : ""}<span class="spacer"></span>
    ${cur >= 0 ? `<button class="link" data-goto="${cur}">→ step ${cur + 1}, where the run is</button>` : ""}`;
  const ins = cxInputs(state);
  body.innerHTML = (q?.step === -1 ? renderQuestion(q, state.run.id) : "")
    + (ins.length ? `<p class="muted">${q?.step === -1 ? "Asked for as the run starts; the others:" : "The run started with:"}</p>
      <table class="kv">${ins.map((x) => `<tr><td><code>${esc(x.name)}</code></td><td>${x.secret ? "••••" : `<code>${esc(x.value || "")}</code>`}</td></tr>`).join("")}</table>` : `<p class="muted">No inputs.</p>`);
  body.querySelectorAll(".question").forEach((el) => bindQuestion(el, q, state.run.id));
  head.querySelector("[data-goto]")?.addEventListener("click", () => cxSelect(state, cur, true));
  main.querySelector(".cx-fg").hidden = true;
}

// ---------- terminals in the panes ----------

function cxHostTerms(state) {
  const aside = (s) => s.shell && /^shell alongside the run/.test(s.title);
  term.host = {
    started(s) {
      if (s.background) {
        cxBgAssign(state, s);
        (state.bgActiveBy ||= {})[s.bgPane] = s.id;
        return cxRenderBg(state);
      }
      if (aside(s)) {
        // The Shell pane that asked for it (or another; with none on screen, it's the step's terminal).
        const panes = cxLeafIds(state, "shell");
        let id = (state.shellQueue || []).shift();
        if (!panes.includes(id)) id = panes.find((p) => ![...term.sessions.values()].some((x) => x.shellPane === p && !x.ended && !x.gone));
        if (id) { s.shellPane = id; cxRenderShells(state); s.xt.focus(); return; }
      }
      state.fg = s; state.actionTab = "term"; cxRenderAction(state); cxLayout(state); s.xt.focus();
    },
    ended(s) {
      if (s.background) {
        // It stays, saying how it ended, until closed (✕), unless its step
        // says to close it when done.
        if (s.closeOnExit) cxRemoveBg(state, s);
        else cxRenderBg(state);
      } else if (s.shellPane) {
        cxRenderShells(state);
        setTimeout(() => { s.gone = true; s.el.remove(); s.xt.dispose(); term.sessions.delete(s.id); cxRenderShells(state); }, 1500);
      } else if (state.fg === s) {
        // Recorded with the step: the log shows it. Leave it a moment, then back to Now.
        setTimeout(() => { if (state.fg === s) { state.fg = null; state.actionTab = "now"; cxRenderAction(state); cxLayout(state); } }, 1500);
      }
    },
    show(s) {
      if (s.background) { cxBgAssign(state, s); (state.bgActiveBy ||= {})[s.bgPane] = s.id; cxRenderBg(state); }
      else if (s.shellPane) { cxRenderShells(state); s.xt.focus(); }
      else { state.fg = s; state.actionTab = "term"; cxRenderAction(state); s.xt.focus(); }
    },
  };
}

function cxShowSession(s, container) {
  if (s.el.parentElement !== container) {
    container.querySelectorAll(".term-pane").forEach((p) => p.remove());
    container.append(s.el);
  }
  s.el.hidden = false;
  if (!s.opened) { s.xt.open(s.el); s.opened = true; }
  requestAnimationFrame(() => { try { s.fit.fit(); } catch {} });
  if (!s.observed) {
    s.observed = true;
    new ResizeObserver(() => { if (s.el.offsetParent) try { s.fit.fit(); } catch {} }).observe(s.el);
  }
}

// The background targets on screen (else the first, off screen).
function cxBgPanes(state) {
  const ids = cxLeafIds(state, "background");
  return ids.length ? ids : ["background"];
}

// cxBgAssign gives a background session a target: the one with the fewest
// running jobs, taking turns among equals.
function cxBgAssign(state, s) {
  const panes = cxBgPanes(state);
  if (s.bgPane && panes.includes(s.bgPane)) return;
  const load = (id) => [...term.sessions.values()].filter((x) => x.background && !x.ended && x.bgPane === id && x !== s).length;
  const min = Math.min(...panes.map(load));
  const ties = panes.filter((id) => load(id) === min);
  const turn = state.bgTurn || 0;
  state.bgTurn = turn + 1;
  s.bgPane = ties[turn % ties.length];
}

function cxRenderBg(state) {
  const bgs = [...term.sessions.values()].filter((s) => s.background);
  bgs.forEach((s) => cxBgAssign(state, s));
  state.bgActiveBy ||= {};
  for (const id of new Set([...cxBgPanes(state), "background"])) {
    const pane = state.frames[id]?.querySelector(".cx-bg");
    if (!pane) continue;
    const mine = bgs.filter((s) => s.bgPane === id);
    pane.querySelector(".cx-bg-empty").hidden = mine.length > 0;
    pane.querySelector(".cx-tabs").innerHTML = mine.map((s) => {
      const [result, kind] = termResult(s);
      return `<span class="term-tab ${s.id === state.bgActiveBy[id] ? "on" : ""} ${s.ended ? "ended" : ""}" data-result="${kind}" data-bg="${esc(s.id)}" title="${esc(s.title)}${s.ended ? ` · ${esc(result)}` : " · running"}"><span class="term-dot"></span>${s.ended ? `<b class="term-res">${esc(result.split(" ")[0])}</b>` : ""}${esc(s.title.replace(/^background · /, ""))}
        <button class="term-x" data-close-bg="${esc(s.id)}" title="${s.ended ? "Close" : "End it (hang up) and close"}">✕</button></span>`;
    }).join("");
    pane.querySelectorAll("[data-bg]").forEach((b) => (b.onclick = (e) => {
      if (e.target.closest("[data-close-bg]")) return;
      state.bgActiveBy[id] = b.dataset.bg;
      cxRenderBg(state);
    }));
    pane.querySelectorAll("[data-close-bg]").forEach((b) => (b.onclick = (e) => {
      e.stopPropagation();
      const s = term.sessions.get(b.dataset.closeBg);
      if (!s) return;
      if (s.ended) return cxRemoveBg(state, s);
      if (confirm(`End “${s.title}”? It's hung up, like closing its terminal, and the run goes on without it.`)) sendTerm(s, { end: true });
    }));
    const s = mine.find((x) => x.id === state.bgActiveBy[id]) || mine[mine.length - 1];
    if (s) { state.bgActiveBy[id] = s.id; cxShowSession(s, pane.querySelector(".cx-term")); }
    const status = pane.querySelector(".cx-bg-status");
    const [result, kind] = s ? termResult(s) : ["", ""];
    status.textContent = s?.ended ? result : s ? "running" : "";
    status.dataset.result = s?.ended ? kind : "running";
  }
}

// ---------- shells on demand ----------

// cxRenderShells draws each Shell pane: its shell (opened alongside the
// run, at the step it's on), or the button to open one.
function cxRenderShells(state) {
  // Once the steps are done, the run is only finishing: no more shells.
  const live = state.live && !state.launch && !CX_ENDED.includes(state.run.status) && (cxCurrent(state) >= 0 || state.steps.every((s) => s.status === "pending"));
  for (const id of cxLeafIds(state, "shell")) {
    const pane = state.frames[id]?.querySelector(".cx-shellpane");
    if (!pane) continue;
    const s = [...term.sessions.values()].find((x) => x.shellPane === id && !x.gone);
    const open = pane.querySelector("[data-sh-open]"), end = pane.querySelector("[data-sh-end]");
    const cur = cxCurrent(state);
    open.hidden = !!s && !s.ended;
    open.disabled = !live;
    open.textContent = cur >= 0 ? `⌨ Open shell at step ${cur + 1}` : "⌨ Open shell";
    open.title = live ? "A shell in the step's shell, with the run's variables and functions, alongside the run" : "Shells open once the run has started";
    end.hidden = !s || s.ended;
    pane.querySelector(".cx-shell-title").textContent = s ? `${s.title.replace(/^shell alongside the run · /, "")}${s.ended ? " · ended" : ""}` : "";
    pane.querySelector(".cx-shell-empty").hidden = !!s;
    pane.querySelector(".cx-shell-empty").textContent = live
      ? "Open a shell at the step the run is on: its shell, with the run's variables and functions. It runs alongside the run; values it publishes reach the run when the next step starts."
      : state.launch ? "Shells open here once the run has started." : CX_ENDED.includes(state.run.status) ? "The run has ended." : "The run's steps are done.";
    open.onclick = async () => {
      open.disabled = true;
      (state.shellQueue ||= []).push(id);
      try { await api("POST", `/api/runs/${enc(state.run.id)}/shell`, {}); }
      catch (e) { state.shellQueue = state.shellQueue.filter((x) => x !== id); toast(e.message); open.disabled = false; }
    };
    end.onclick = () => { if (s && confirm(`End “${s.title}”?`)) sendTerm(s, { end: true }); };
    if (s) cxShowSession(s, pane.querySelector(".cx-term"));
  }
}

// cxRemoveBg takes an ended background session out of the pane (its output
// is recorded with its step).
function cxRemoveBg(state, s) {
  if (!term.sessions.has(s.id)) return;
  term.sessions.delete(s.id);
  s.el.remove();
  s.xt.dispose();
  cxRenderBg(state);
}

// ---------- layout: resizable panes ----------

function cxLayout(state) {
  cxApplySizes(state);
  cxRenderBg(state);
  cxRenderShells(state);
  cxRenderEditHost(state);
}

// The section and run clocks tick with the window's (app.js has one for .mw-clock).
setInterval(() => {
  document.querySelectorAll(".cx-clock[data-since]").forEach((el) => {
    const end = el.dataset.until ? new Date(el.dataset.until) : new Date();
    const sec = Math.max(0, (end - new Date(el.dataset.since)) / 1000);
    el.textContent = fmtOffset(sec, false);
  });
}, 1000);

// ---------- before the run: the launch console ----------

// viewConsoleLaunch (#/runbooks/{name}/console, ?resume= or ?rerun= a run)
// is the console before a run exists: the inputs and how to run it, which
// steps to include and where to pause, and each step as it will run with
// these inputs. Nothing runs until Start.
async function viewConsoleLaunch(name, query) {
  const mode = query.get("resume") ? "resume" : query.get("rerun") ? "rerun" : "run";
  const fromId = query.get("resume") || query.get("rerun") || "";
  const data = await api("GET", `/api/runbooks/${enc(name)}`);
  const rb = data.runbook;
  if (!rb) throw new Error(`${name} isn't valid: ${data.error || "fix it in the editor"}`);
  const prev = fromId ? (await api("GET", `/api/runs/${enc(fromId)}`)).run : null;
  const state = {
    launch: true, mode, prev, rb, version: data.version, console: true,
    run: { runbook: name, status: "pending", window: rb.maintenance_window ? { target: rb.maintenance_window.target } : null },
    steps: rb.steps.map((s) => ({ name: s.name, section: s.section, status: "pending", kind: s.ways ? "ways" : s.checks ? "checks" : s.manual ? "manual" : "run", optional: s.optional, log: [] })),
    include: rb.steps.map((s, i) => mode !== "resume" || !!s.always || !CX_DONE.includes(prev?.steps[i]?.status)),
    before: new Set(), after: new Set(), selected: -1, follow: false, sizes: cxSizes(),
    opts: { unattended: false, step: false, yes: false, dry: false }, vars: {},
  };
  // Prefill: the run being resumed or rerun has the inputs (secrets aside).
  for (const i of rb.inputs || []) {
    const v = prev?.inputs?.[i.name];
    state.vars[i.name] = v && v !== "****" ? v : "";
  }
  state.render = cxLaunchRender;
  cxMountConsole(state);
  let t;
  state.refresh = () => {
    clearTimeout(t);
    t = setTimeout(async () => {
      try {
        const steps = (await api("POST", "/api/preview", { runbook: name, vars: cxLaunchVars(state), from_run: mode === "resume" ? fromId : "" })).steps;
        state.plan = { steps };
        cxRenderList(state);
        cxRenderDetails(state);
      } catch (e) { toast(e.message); }
    }, 300);
  };
  cxLaunchRender(state);
  state.refresh();
}

function cxLaunchVars(state) {
  const out = {};
  for (const i of state.rb.inputs || []) {
    const v = state.vars[i.name] ?? "";
    if (!(i.secret && v === "")) out[i.name] = v; // blank secrets: asked for when the run gets to them
  }
  return out;
}

function cxLaunchRender(state) {
  cxLaunchRenderTop(state);
  cxRenderList(state);
  cxRenderDetails(state);
  cxLaunchRenderAction(state);
  cxRenderLog(state);
  cxLayout(state);
}

function cxLaunchRenderTop(state) {
  const { rb, mode, prev, opts } = state;
  const n = state.include.filter(Boolean).length;
  const what = { run: "Start run", rerun: "Run again", resume: "Resume" }[mode];
  const el = main.querySelector(".cx-top");
  const tog = (k, label, title, dis = false) => `<button data-opt="${k}" class="${opts[k] ? "on" : ""}" title="${esc(title)}" ${dis ? "disabled" : ""}>${label}</button>`;
  el.innerHTML = `
    <div class="cx-title">
      <a href="#/runbooks/${enc(rb.name)}" class="cx-back" title="The runbook's page">☰</a>
      <h1><a href="#/runbooks/${enc(rb.name)}">${esc(rb.name)}</a></h1>
      <span class="muted">v${state.version} · ${esc((rb.description || "").split("\n")[0])}</span>
      <span class="spacer"></span>
      <button class="link small" data-reset-layout title="Put the panes back where they started">⊞ Reset layout</button>
      <span class="badge">○ not started</span>
    </div>
    <div class="cx-status">
      <span class="cx-at">${mode === "run" ? "A new run" : `${mode === "resume" ? "Resuming" : "Running again"} <a href="#/runs/${enc(prev.id)}/console">${esc(prev.id)}</a> (${esc(prev.status)})${mode === "resume" ? ": steps that finished are left out" : ""}`}</span>
      <span class="cx-chip">${n} of ${state.steps.length} steps</span>
      ${rb.maintenance_window ? `<span class="cx-chip cx-window muted">Maintenance window${rb.maintenance_window.target ? `, target ${esc(rb.maintenance_window.target)}` : ""}</span>` : ""}
    </div>
    <div class="cx-toolbar">
      <button class="primary" data-start>▶ ${what}</button>
      <span class="cx-sep"></span>
      ${tog("unattended", "Unattended", "Nobody answers: inputs come from here or defaults, and any failure ends the run")}
      ${tog("step", "⏯ Step through", "Pause before every step", opts.unattended)}
      ${tog("yes", "Auto-approve", "Approve confirm-gated steps (unattended runs)", !opts.unattended)}
      ${tog("dry", "Dry run", "Show what each step would run, without running it")}
    </div>`;
  el.querySelectorAll("[data-opt]").forEach((b) => (b.onclick = () => { opts[b.dataset.opt] = !opts[b.dataset.opt]; cxLaunchRenderTop(state); }));
  el.querySelector("[data-reset-layout]").onclick = () => cxResetLayout(state);
  el.querySelector("[data-start]").onclick = () => cxLaunchStart(state);
}

// The action pane, before the run: under Inputs, the form to fill them in;
// under a step, whether it's included and where to pause.
function cxLaunchRenderAction(state) {
  const pane = main.querySelector(".cx-action");
  const head = pane.querySelector(".cx-pane-head"), body = pane.querySelector(".cx-action-body");
  const i = state.selected, s = state.steps[i];
  const what = { run: "Start run", rerun: "Run again", resume: "Resume" }[state.mode];
  const tabs = cxActionTabs(state, false);
  head.innerHTML = `<b>${i === -1 ? "Fill in the inputs" : `Step ${i + 1} in this run`}</b>${tabs.html}<span class="spacer"></span><span class="muted small">nothing runs until ${esc(what)}</span>`;
  cxBindActionTabs(state, head, () => cxLaunchRenderAction(state));
  body.hidden = tabs.on !== "now";
  if (i === -1) {
    // Kept while it's showing, so re-rendering doesn't take the focus away.
    if (body.querySelector(".cx-inputs")) return;
    const inputs = state.rb.inputs || [];
    body.innerHTML = `<div class="card cx-inputs">
        ${inputs.length ? `<div class="form-grid">${inputs.map((inp) => {
          const v = state.vars[inp.name] || "";
          const ctl = inp.choices?.length
            ? `<select name="${esc(inp.name)}">${!inp.default ? `<option value=""></option>` : ""}${inp.choices.map((c) => `<option ${c === (v || inp.default) ? "selected" : ""}>${esc(c)}</option>`).join("")}</select>`
            : `<input name="${esc(inp.name)}" type="${inp.secret ? "password" : "text"}" value="${esc(v)}" placeholder="${esc(inp.default || "")}" autocomplete="off">`;
          const hint = [inp.description, inp.secret && "secret: not stored; leave blank to be asked when it's needed"].filter(Boolean).join(" · ");
          return `<label>${esc(inp.name)}${inp.required ? " *" : ""}</label>${ctl}${hint ? `<div class="hint">${esc(hint)}</div>` : ""}`;
        }).join("")}</div>` : `<p class="muted">This runbook takes no inputs.</p>`}
      </div>`;
    body.querySelectorAll(".cx-inputs [name]").forEach((f) => (f.oninput = f.onchange = () => {
      state.vars[f.name] = f.value;
      state.refresh();
      cxRenderInputsDetails(state, main.querySelector(".cx-details .cx-pane-head"), main.querySelector(".cx-details .cx-pane-body"));
      cxRenderList(state);
    }));
    return;
  }
  if (!s) return;
  body.innerHTML = `<div class="card cx-stepopts">
    <label class="check"><input type="checkbox" data-o="include" ${state.include[i] ? "checked" : ""}> Include it in the run</label>
    <label class="check"><input type="checkbox" data-o="before" ${state.before.has(i) ? "checked" : ""}> Pause before it</label>
    <label class="check"><input type="checkbox" data-o="after" ${state.after.has(i) ? "checked" : ""}> Pause after it, to check the result</label>
    <div class="row small"><button data-all>Include all steps</button>${state.prev ? `<button data-unfinished>Only what didn't finish last time</button>` : ""}<button data-nopause>Clear all pauses</button></div>
    ${state.prev?.steps[i] && state.prev.steps[i].status !== "pending" ? `<p class="muted small">In ${esc(state.prev.id)}: ${ICONS[state.prev.steps[i].status] || ""} ${esc(state.prev.steps[i].status)}</p>` : ""}
    </div>`;
  body.querySelectorAll("[data-o]").forEach((c) => (c.onchange = () => {
    const k = c.dataset.o;
    if (k === "include") state.include[i] = c.checked;
    else c.checked ? state[k].add(i) : state[k].delete(i);
    cxRenderList(state);
    cxLaunchRenderTop(state);
  }));
  body.querySelector("[data-all]").onclick = () => { state.include = state.steps.map(() => true); cxLaunchRender(state); };
  body.querySelector("[data-nopause]").onclick = () => { state.before.clear(); state.after.clear(); cxLaunchRender(state); };
  body.querySelector("[data-unfinished]")?.addEventListener("click", () => {
    state.include = state.rb.steps.map((st, k) => !!st.always || !CX_DONE.includes(state.prev.steps[k]?.status));
    cxLaunchRender(state);
  });
}

async function cxLaunchStart(state) {
  const { rb, opts, mode, prev } = state;
  const missing = (rb.inputs || []).filter((i) => i.required && !i.default && !i.secret && !(state.vars[i.name] || "").trim());
  if (missing.length) {
    toast(`Fill in ${missing.map((i) => i.name).join(", ")} first`);
    if (state.selected !== -1) { state.selected = -1; cxLaunchRender(state); }
    return main.querySelector(`.cx-inputs [name="${missing[0].name}"]`)?.focus();
  }
  const nums = (pred) => rb.steps.map((_, i) => i).filter(pred).map((i) => i + 1);
  const body = {
    vars: cxLaunchVars(state), unattended: opts.unattended, yes: opts.yes, dry_run: opts.dry, step_mode: opts.step && !opts.unattended,
    skip: nums((i) => !state.include[i]), pause_before: nums((i) => state.before.has(i)), pause_after: nums((i) => state.after.has(i)),
  };
  if (opts.unattended) {
    const ch = rb.steps.map((s, i) => i).filter((i) => state.include[i] && rb.steps[i].choose && (rb.steps[i].ways || []).length > 1);
    if (ch.length) {
      if (!confirm(`Steps ${ch.map((i) => i + 1).join(", ")} normally ask which way to do them. Unattended, they'll use their preferred way. Start anyway?`)) return;
      body.use_preferred = true;
    }
  }
  try {
    const { id } = mode === "resume"
      ? await api("POST", `/api/runs/${enc(prev.id)}/resume`, body)
      : await api("POST", "/api/runs", { ...body, runbook: rb.name, previous: prev?.id || "" });
    runView("console");
    location.hash = `#/runs/${enc(id)}/console`;
  } catch (e) { toast(e.message); }
}
