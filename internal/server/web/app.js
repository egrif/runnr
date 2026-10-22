"use strict";

const $ = (sel, el = document) => el.querySelector(sel);
const main = $("#main");
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
const ICONS = { succeeded: "✔", failed: "✘", skipped: "↷", aborted: "■", running: "▶", pending: "○", forced: "⚑", background: "◐" };

let runbooks = [];
let stream = null; // active EventSource

async function api(method, path, body, raw = false, headers = {}) {
  const opts = { method, headers: { "X-Runnr": "1", ...headers } };
  if (body !== undefined) {
    opts.body = raw ? body : JSON.stringify(body);
    opts.headers["Content-Type"] = raw ? "application/yaml" : "application/json";
  }
  const res = await fetch(path, opts);
  if (res.status === 401 && path !== "/api/login") {
    await login();
    return api(method, path, body, raw, headers);
  }
  if (res.status === 204) return null;
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

async function login() {
  for (;;) {
    const token = prompt("This runnr server requires an API token:");
    if (token === null) throw new Error("token required");
    const res = await fetch("/api/login", { method: "POST", headers: { "X-Runnr": "1", "Content-Type": "application/json" }, body: JSON.stringify({ token }) });
    if (res.ok) return;
  }
}

function author() {
  let name = "";
  try { name = localStorage.getItem("runnr.author") || ""; } catch {}
  if (!name) {
    name = (prompt("Your name, for notes:") || "").trim();
    if (name) try { localStorage.setItem("runnr.author", name); } catch {}
  }
  return name;
}

async function addNote(runId, step, state) {
  const text = (prompt(step ? `Note on step ${step}:` : "Note on this run:") || "").trim();
  if (!text) return;
  try {
    const n = await api("POST", `/api/runs/${runId}/notes`, { step, text, author: author() });
    pushNote(state, step - 1, n);
    renderRun(state);
  } catch (e) { toast(e.message); }
}

// pushNote adds a note unless it's already shown (live runs get notes both
// from the initial load or our own POST, and from the event stream).
function pushNote(state, idx, n) {
  const list = idx < 0 ? (state.run.notes ||= []) : (state.steps[idx].notes ||= []);
  if (!list.some((m) => m.time === n.time && m.text === n.text)) list.push(n);
}

function renderState(vars, state) {
  const keys = Object.keys(vars || {}).sort();
  if (!keys.length) return "";
  return `<details class="card state" ${state.stateOpen ? "open" : ""}><summary>State · ${keys.length} variables</summary>
    <table class="kv">${keys.map((k) => `<tr><td><code>${esc(k)}</code></td><td><code>${esc(vars[k])}</code></td></tr>`).join("")}</table></details>`;
}

function renderNotes(notes) {
  return (notes || []).map((n) => `<div class="note">✎ <span class="muted">${new Date(n.time).toLocaleString()} · ${esc(n.author || "")}</span> ${esc(n.text)}</div>`).join("");
}

// sectionHeading returns a heading when step i starts a new section, with
// the section's description and outcome if the runbook gives them.
function sectionHeading(steps, i, sections) {
  const s = steps[i].section;
  if (!s || (i > 0 && steps[i - 1].section === s)) return "";
  const info = (sections || []).find((x) => x.name === s);
  const at = parseOffset(info?.window_offset);
  return `<li class="section">${esc(s)}${at != null ? ` <span class="badge-k planned" title="Planned start, relative to the maintenance window opening">T${fmtOffset(at)}</span>` : ""}${info?.description ? `<div class="section-desc">${esc(info.description)}</div>` : ""}${info?.expect ? `<div class="section-desc"><b>By the end:</b> ${esc(info.expect)}</div>` : ""}</li>`;
}

function toast(msg) {
  let t = $(".toast");
  if (!t) { t = document.createElement("div"); t.className = "toast"; document.body.append(t); }
  t.textContent = msg;
  t.classList.add("show");
  clearTimeout(t._h);
  t._h = setTimeout(() => t.classList.remove("show"), 2200);
}

function duration(a, b) {
  if (!a || !b || b.startsWith("0001")) return "";
  const ms = new Date(b) - new Date(a);
  return ms < 1000 ? `${ms}ms` : ms < 60000 ? `${(ms / 1000).toFixed(1)}s` : `${Math.floor(ms / 60000)}m${Math.round((ms % 60000) / 1000)}s`;
}

// ---------- sidebar ----------

async function loadRunbooks() {
  [runbooks] = await Promise.all([api("GET", "/api/runbooks"), loadCollections()]);
  renderSidebar();
}

// renderSidebar lists runbooks grouped by collection (others at the end).
function renderSidebar() {
  const q = $("#filter").value.toLowerCase();
  const current = decodeURIComponent((location.hash.match(/^#\/runbooks\/([^/?]+)/) || [])[1] || "");
  const matches = (r) => !q || r.name.includes(q) || (r.description || "").toLowerCase().includes(q) || (r.tags || []).some((t) => t.includes(q));
  const item = (r) => `<li><a href="#/runbooks/${encodeURIComponent(r.name)}" class="${r.name === current ? "active" : ""} ${r.error ? "invalid" : ""}">
      ${esc(r.name)}<span class="desc">${esc(r.error ? "invalid runbook" : (r.description || "").split("\n")[0] || `${r.steps} steps`)}</span></a></li>`;
  const byName = new Map(runbooks.map((r) => [r.name, r]));
  const grouped = new Set();
  let html = "";
  for (const c of collections) {
    const members = (c.collection?.runbooks || []).map((n) => byName.get(n)).filter(Boolean);
    members.forEach((r) => grouped.add(r.name));
    const shown = members.filter(matches);
    if (!shown.length && q) continue;
    html += `<li class="col-head"><a href="#/collections" title="${esc(c.collection?.description || "")}">${esc(c.name)}</a></li>${shown.map(item).join("")}`;
  }
  const rest = runbooks.filter((r) => !grouped.has(r.name) && matches(r));
  if (rest.length) html += (collections.length ? `<li class="col-head muted">Other runbooks</li>` : "") + rest.map(item).join("");
  $("#runbook-list").innerHTML = html || `<li class="muted">No runbooks</li>`;
  $("#history-link").classList.toggle("active", location.hash.startsWith("#/history"));
  $("#help-link").classList.toggle("active", location.hash === "#/help");
  $("#libs-link").classList.toggle("active", location.hash.startsWith("#/libraries"));
  $("#cols-link").classList.toggle("active", location.hash.startsWith("#/collections"));
  $("#sched-link").classList.toggle("active", location.hash.startsWith("#/schedule"));
  $("#manage-link").classList.toggle("active", location.hash === "#/manage");
}

$("#filter").addEventListener("input", renderSidebar);
$("#new-btn").addEventListener("click", () => (location.hash = "#/new"));
$("#import-btn").addEventListener("click", () => openFolderImport());
$("#copy-on-select").checked = copySel.on;
$("#copy-on-select").addEventListener("change", (e) => setCopyOnSelect(e.target.checked));

// ---------- router ----------

async function route() {
  if (stream) { stream.close(); stream = null; }
  cxLeaveShells(/\/console(\?|$)/.test(location.hash));
  resetTerm();
  resetSide();
  document.body.classList.remove("console-mode");
  const h = location.hash || "#/";
  renderSidebar();
  let m;
  try {
    if ((m = h.match(/^#\/runbooks\/([^/?]+)\/console(?:\?(.*))?$/))) { runView("console"); return await viewConsoleLaunch(decodeURIComponent(m[1]), new URLSearchParams(m[2] || "")); }
    if ((m = h.match(/^#\/runbooks\/([^/?]+)(?:\/(edit|tests))?(?:\?(.*))?$/))) return await viewRunbook(decodeURIComponent(m[1]), m[2] || "run", new URLSearchParams(m[3] || ""));
    if ((m = h.match(/^#\/runs\/([^/]+)\/console$/))) { runView("console"); return await viewConsole(decodeURIComponent(m[1])); }
    if ((m = h.match(/^#\/runs\/([^/]+)$/))) {
      if (runView() === "console") return location.replace(`#/runs/${m[1]}/console`);
      return await viewRun(decodeURIComponent(m[1]));
    }
    if (h === "#/new") return await viewNew();
    if (h.startsWith("#/history")) return await viewHistory();
    if (h === "#/help") return viewHelp();
    if (h === "#/libraries") return await viewLibraries();
    if (h === "#/manage") return await viewManage();
    if (h === "#/collections") return await viewCollections();
    if (h === "#/schedule") return await viewSchedule();
    if ((m = h.match(/^#\/collections\/([^/]+)$/))) return await viewCollection(decodeURIComponent(m[1]));
    if ((m = h.match(/^#\/libraries\/([^/]+)$/))) return await viewLibrary(decodeURIComponent(m[1]));
    return viewHome();
  } catch (e) {
    main.innerHTML = `<p class="error-text">${esc(e.message)}</p>`;
  }
}
window.addEventListener("hashchange", route);

function viewHome() {
  const logo = `<img class="logo" src="brand/logo.png" alt="">`;
  main.innerHTML = runbooks.length
    ? `<div class="empty">${logo}<h1>runnr</h1><p>Pick a runbook on the left, or create a new one.</p></div>`
    : `<div class="empty">${logo}<h1>Welcome to runnr</h1><p>You don't have any runbooks yet.</p><p><button class="primary" onclick="location.hash='#/new'">Create your first runbook</button></p></div>`;
}

// ---------- runbook: run + edit ----------

async function viewRunbook(name, tab, query) {
  const data = await api("GET", `/api/runbooks/${encodeURIComponent(name)}`);
  const rb = data.runbook;
  const href = `#/runbooks/${encodeURIComponent(name)}`;
  main.innerHTML = `
    <div class="row"><div>
      <h1>${esc(name)}</h1>
      ${rb?.description ? `<div class="rb-desc">${esc(rb.description)}</div>` : ""}
      ${rb?.expect ? `<div class="rb-desc muted"><b>By the end:</b> ${esc(rb.expect)}</div>` : ""}
      ${rb?.overview ? `<details class="rb-overview"><summary>Overview: the process from a high level</summary><div class="md">${md(rb.overview)}</div></details>` : ""}
      ${rb ? membership(name) : ""}
      ${rb ? `<div class="claude-only">${explainBtn(0, "✨ Review this runbook with Claude")}</div>` : ""}
      <div>${(rb?.tags || []).map((t) => `<span class="tag">${esc(t)}</span>`).join("")}</div>
    </div><span class="spacer"></span>${rb ? `<button id="export-rb" title="Write the runbook, and the libraries it uses, to files">⇩ Export…</button>` : ""}</div>
    <nav class="tabs">
      <a href="${href}" class="${tab === "run" ? "active" : ""}">Run</a>
      <a href="${href}/edit" class="${tab === "edit" ? "active" : ""}">Edit</a>
      <a href="${href}/tests" class="${tab === "tests" ? "active" : ""}">Tests</a>
      <a href="#/history?runbook=${encodeURIComponent(name)}">History</a>
      <a href="${href}/console" class="tab-console" title="Set up a run and run it in the console: steps, definitions, what's happening and background steps on one screen">▣ Console</a>
    </nav>
    <section id="tab"></section>`;
  bindMembership(main, name, () => route());
  $("#export-rb") && ($("#export-rb").onclick = () => exportRunbook(name));
  main.querySelector('.claude-only [data-explain="0"]')?.addEventListener("click", () => openExplain({ runbook: name }, 0, `Review: ${name}`));
  if (tab === "tests" && rb) return viewTests($("#tab"), name);
  if (tab === "edit" || !rb) return renderEditor($("#tab"), data.raw, name, data.error, data.version, data.updated_at);
  const mode = query.get("resume") ? "resume" : query.get("rerun") ? "rerun" : "run";
  await viewLaunch($("#tab"), name, mode, query.get("resume") || query.get("rerun") || "", query.get("schedule") || "");
}

// stepDetails shows what a step does: code, expected outcome, outputs.
// stepCode is all the code a step (or its view) runs: script, checks, ways.
function stepCode(s) {
  if (!s) return "";
  const checks = (cs) => (cs || []).map((c) => c.run || c.command || "").join("\n");
  return [s.command, s.run, checks(s.checks), ...(s.ways || []).map((w) => [w.run, w.command, checks(w.checks)].join("\n"))].filter(Boolean).join("\n");
}

function stepDetails(s, fns) {
  const outs = [...(s.outputs || []), ...(s.secret_outputs || []).map((o) => `${o} (secret)`)];
  const fi = fnIcon(stepCode(s), fns);
  return `
    ${s.description ? `<div class="doing">${esc(s.description)}${fi}</div>` : fi ? `<div>${fi}</div>` : ""}
    ${s.dir ? `<div class="muted">dir: <code>${esc(s.dir)}</code></div>` : ""}
    ${s.when ? `<div class="muted">only when: <code>${esc(s.when)}</code></div>` : ""}
    <pre class="cmd">${esc(s.run || s.manual || s.command)}</pre>
    ${s.expect ? `<div class="expect"><b>By the end:</b> ${esc(s.expect)}</div>` : ""}
    ${outs.length ? `<div class="muted">publishes: ${outs.map((o) => `<code>${esc(o)}</code>`).join(" ")}</div>` : ""}`;
}

async function viewNew() {
  const { raw } = await api("GET", "/api/template?name=my-runbook");
  main.innerHTML = `<h1>New runbook</h1>
    <p class="muted">Edit the starter below, or import a file; the <code>name</code> field becomes the runbook's identifier.</p>
    <div class="row new-opts"><label>Collection ${colPicker("new-col", "None")}</label>
      <button class="link" id="import-file">Import files…</button></div>
    <section id="tab"></section>`;
  $("#new-col").onchange = (e) => { const v = pickedCol(e.target.value); if (v && !collections.some((c) => c.name === v)) e.target.add(new Option(v, v), 1); e.target.value = v; };
  $("#import-file").onclick = () => pickImportFiles({ collection: $("#new-col").value });
  renderEditor($("#tab"), raw, null);
}

// ---------- live / past run ----------

// runView is the run page people last chose: "console" or "classic" (and
// sets it, given one).
function runView(set) {
  try {
    if (set) localStorage.setItem("runnr.runView", set);
    return localStorage.getItem("runnr.runView") || "classic";
  } catch { return set || "classic"; }
}

async function viewRun(id) {
  const { run, live, controls } = await api("GET", `/api/runs/${encodeURIComponent(id)}`);
  const state = { run, live, controls, steps: run.steps.map((s) => ({ ...s, log: [] })), question: null, open: new Set() };
  // What steps will do (with this run's inputs and state), and the run it's compared against.
  const loadPreview = async () => {
    try { state.preview = (await api("GET", `/api/runs/${enc(id)}/preview`)).steps; renderRun(state); } catch {}
  };
  loadPreview();
  if (run.previous) api("GET", `/api/runs/${enc(run.previous)}`).then((r) => { state.prev = r.run; renderRun(state); }).catch(() => {});
  // Earlier runs (for History) and the runbook's configuration (for Details).
  state.history = [];
  api("GET", `/api/runs?runbook=${enc(run.runbook)}&limit=30`).then((rs) => { state.history = rs.filter((r) => r.id !== run.id); renderRun(state); }).catch(() => {});
  api("GET", `/api/runbooks/${enc(run.runbook)}`).then(async (d) => {
    state.cfg = d.runbook?.steps; state.rbSections = d.runbook?.sections; state.inputDefs = d.runbook?.inputs;
    await loadLibs(d.runbook?.libraries);
    state.fns = fnsOf(d.runbook);
    renderRun(state);
  }).catch(() => {});
  let pt;
  state.onState = () => { clearTimeout(pt); pt = setTimeout(loadPreview, 300); };
  // Past runs: show stored output; expand failures.
  state.steps.forEach((s, i) => {
    if (s.output) s.log = s.output.replace(/\n$/, "").split("\n").map((t) => ({ stream: "stdout", text: t }));
    if (s.status === "failed") state.open.add(i);
  });
  renderRun(state);
  if (!live) return;

  stream = new EventSource(`/api/runs/${encodeURIComponent(id)}/events`);
  let first = true;
  stream.onmessage = (msg) => {
    const e = JSON.parse(msg.data);
    if (e.seq === 0 && !first) { // reconnected: backlog replays from scratch
      state.steps.forEach((s) => (s.log = []));
    }
    first = false;
    if (e.kind.startsWith("term")) {
      termEvent(id, e);
      if (e.kind === "term") return; // terminal output doesn't change the page
    }
    applyEvent(state, e);
    renderRun(state);
  };
  stream.onerror = () => { if (["succeeded", "failed", "aborted"].includes(state.run.status)) stream.close(); };
}

function applyEvent(state, e) {
  switch (e.kind) {
    case "run_started":
    case "run_finished":
      state.run = e.run;
      if (e.kind === "run_started") state.steps.forEach((s) => (s.log = []));
      e.run.steps.forEach((r, i) => Object.assign(state.steps[i], { ...r, notes: state.steps[i].notes }));
      e.run.notes = state.run.notes;
      if (e.kind === "run_finished") {
        state.question = null; stream?.close();
        if (e.run.edits?.length && !e.run.edits_saved) toast("Run finished. Your step edits aren't saved to the runbook yet");
      }
      break;
    case "step_started":
      // Replaced, not merged: a step run again (re-opened, edited) starts afresh.
      state.steps[e.step] = { ...e.result, notes: state.steps[e.step].notes, log: [] };
      state.current = e.step;
      state.open.add(e.step);
      break;
    case "output":
      if (e.step < 0) (state.messages ||= []).push(e.text);
      else state.steps[e.step].log.push({ stream: e.stream, text: e.text });
      break;
    case "step_finished":
      Object.assign(state.steps[e.step], { ...e.result, notes: state.steps[e.step].notes });
      if (e.state) { state.run.state = e.state; state.onState?.(); }
      // Terminal sessions aren't streamed into the log; show the recorded transcript.
      if ((!state.steps[e.step].log.length || e.result.kind === "background") && e.result.output) {
        state.steps[e.step].log = e.result.output.replace(/\n$/, "").split("\n").map((t) => ({ stream: "stdout", text: t }));
      }
      if (e.result.status === "succeeded" && !e.result.output) state.open.delete(e.step);
      break;
    case "question":
      state.question = e.question;
      if (e.step >= 0) state.open.add(e.step);
      break;
    case "answered":
      if (state.question?.id === e.answered) state.question = null;
      break;
    case "note":
      pushNote(state, e.step, e.note);
      break;
    case "controls":
      state.controls = e.controls;
      break;
    case "window":
      state.run.window = e.run.window;
      break;
    case "inputs":
      Object.assign(state.run, { inputs: e.run.inputs, input_changes: e.run.input_changes, state: e.run.state, window: e.run.window });
      state.onState?.();
      break;
    case "step_progress":
      // A re-opened step (and the one after it) starts afresh.
      if (e.result.status === "pending") state.steps[e.step] = { ...e.result, notes: state.steps[e.step].notes, log: [] };
      else Object.assign(state.steps[e.step], { ...e.result, notes: state.steps[e.step].notes, log: state.steps[e.step].log });
      break;
  }
}

function renderRun(state) {
  const { run } = state;
  // Optional steps don't decide where a resume starts (as engine.ResumePoint).
  const stopAt = state.steps.findIndex((s) => !s.optional && !["succeeded", "skipped", "forced"].includes(s.status));
  state.resumable = ["failed", "aborted"].includes(run.status) && stopAt >= 0 ? stopAt + 1 : 0;
  const scroll = {};
  main.querySelectorAll("pre.log").forEach((p) => (scroll[p.dataset.i] = p.scrollHeight - p.scrollTop - p.clientHeight < 8));
  const active = !["succeeded", "failed", "aborted"].includes(run.status);
  const live = active && state.live;
  const ctl = state.controls || { skip: [], pause_before: [], pause_after: [] };
  const has = (list, i) => (list || []).includes(i + 1);
  const inputs = Object.entries(run.inputs || {});
  main.innerHTML = `
    <div class="row">
      <div>
        <h1><a href="#/runbooks/${encodeURIComponent(run.runbook)}">${esc(run.runbook)}</a></h1>
        <div class="muted">${esc(run.id)}${run.runbook_version ? ` · v${run.runbook_version}` : ""} · ${esc(run.mode)}${run.dry_run ? " · dry run" : ""} · started ${new Date(run.started).toLocaleString()}
          ${duration(run.started, run.ended) ? ` · took ${duration(run.started, run.ended)}` : ""}</div>
      </div>
      <span class="spacer"></span>
      <span class="badge s-${esc(run.status)}">${ICONS[run.status] || ""} ${esc(run.status)}</span>
      <a class="button" href="#/runs/${enc(run.id)}/console" title="One screen: the steps, the selected step's definition, what it's doing, and the background steps">▣ Console</a>
      ${active ? `<button class="danger" id="cancel">Abort run</button>` : `<button id="delete-run" class="danger" title="Delete this run's record">Delete run</button><button id="export-logs" title="Its logs, with the runs linked to it by resuming: to files, or a download">⇩ Export logs…</button><button id="raw-log" class="${state.raw ? "on" : ""}" title="The whole run as a plain log: each step's command, output and result, in order">${state.raw ? "Steps" : "Raw log"}</button>${state.resumable ? `<button class="primary" id="resume">Resume…</button>` : ""}<button id="rerun">Run again…</button>`}
    </div>
    ${run.error ? `<p class="error-text">${esc(run.error)}</p>` : ""}
    ${editsBanner(run, state)}
    ${windowBanner(run)}
    ${inputs.length ? `<p class="muted">${inputs.map(([k, v]) => `<code>${esc(k)}=${esc(v)}</code>`).join(" ")}${canChangeInputs(state) ? ` <button class="link" id="change-inputs" title="Change them for the rest of the run">✎ Change inputs</button>` : ""}</p>` : ""}
    ${inputChangesHTML(run)}
    ${(state.messages || []).map((m) => `<p class="muted">ℹ ${esc(m)}</p>`).join("")}
    ${runningBackground().length ? `<div class="bg-list">${runningBackground().map((s) => `<span class="bg-item">◐ ${esc(s.title.replace(/^background · /, ""))} <button class="link" data-show-bg="${esc(s.id)}" title="Go to its terminal, in its step">Show</button></span>`).join("")}</div>` : ""}
    <div class="notes">${renderNotes(run.notes)}<button class="link" data-note="0">✎ Add run note</button></div>
    ${renderState(run.state, state)}
    ${run.resumes ? `<p class="muted">Resumes <a href="#/runs/${esc(run.resumes)}">${esc(run.resumes)}</a></p>` : ""}
    ${resumedHTML(run)}
    ${removedHTML(run)}
    ${state.prev ? `<p class="muted">Compared with <a href="#/runs/${esc(state.prev.id)}">${esc(state.prev.id)}</a> (${esc(state.prev.status)}): open a step to see what it did then.</p>` : ""}
    ${live && state.controls && run.mode === "interactive" ? `<p><label><input type="checkbox" id="step-all" ${state.controls.step_mode ? "checked" : ""}> Step through the remaining steps</label>
      <span class="muted small"> · upcoming steps can be skipped or paused below</span></p>` : ""}
    ${state.question?.step === -1 ? renderQuestion(state.question, run.id) : ""}
    <ol class="steps">${state.steps.map((s, i) => `${sectionHeading(state.steps, i, state.rbSections)}
      <li class="step ${state.question?.step === i ? "focus" : ""} ${s.note === "before start step" ? "dim" : ""} ${state.cfg && inWindow(state.cfg, i) ? "in-window" : ""}">
        <div class="step-head" data-toggle="${i}">
          <span class="icon s-${s.status}">${ICONS[s.status] || ""}</span>
          <span class="n">${i + 1}</span>
          <span class="name">${esc(s.name)} <span class="kind">${esc(s.kind)}</span>${s.optional ? ` <span class="badge-k opt" title="Not required: the run asks before it, and can carry on without it">optional</span>` : ""}${s.edited ? ` <span class="badge-k warn" title="Changed during this run">edited</span>` : ""}${s.owner ? ` <span class="owner">${esc(s.owner)}</span>` : ""}</span>
          <span class="meta">${[s.way && `way: ${s.way}`, s.note, s.attempts > 1 && `${s.attempts} attempts`, s.exit_code != null && `exit ${s.exit_code}`, duration(s.started, s.ended)].filter(Boolean).map(esc).join(" · ")}${stepTiming(state, i)}</span>
          ${state.preview?.[i]?.confirm ? `<span class="badge-k warn" title="Asks before running">confirms on its own</span>` : ""}
          ${state.prev?.steps[i] && state.prev.steps[i].status !== "pending" ? `<span class="last s-${esc(state.prev.steps[i].status)}" title="Last time: ${esc(state.prev.steps[i].status)}">${ICONS[state.prev.steps[i].status]}</span>` : ""}
          ${live && s.status === "running" && s.kind !== "background" && state.question?.step !== i ? `<button class="pill danger" data-stop-step="${i}" title="Interrupt its command: the step fails and you choose what's next (the run goes on)">■ Stop step</button>` : ""}
          ${live && state.question?.reopenable === i + 1 ? `<button class="pill" data-reopen="${i}" title="Go back to this step: it waits for you before running again, then the run carries on from there">↺ re-open</button>` : ""}
          ${live && editable(state, i) ? `<button class="pill" data-edit-step="${i}" title="Change this step for this run${state.question?.step === i ? " and run it again" : ""}">✎ edit</button>` : ""}
          ${live && s.status === "pending" && state.controls ? `
            <button class="pill ${has(ctl.skip, i) ? "on" : ""}" data-ctl="skip" data-i="${i}" title="Leave this step out">skip</button>
            <button class="pill ${has(ctl.pause_before, i) ? "on" : ""}" data-ctl="pause_before" data-i="${i}" title="Stop before this step">⏸ before</button>
            <button class="pill ${has(ctl.pause_after, i) ? "on" : ""}" data-ctl="pause_after" data-i="${i}" title="Stop after this step to check it">⏸ after</button>` : ""}
        </div>
        ${state.question?.step === i ? `<div class="step-body">${renderQuestion(state.question, run.id)}</div>` : ""}
        ${hasTerm(run.id, i) ? `<div class="step-body term-slot" data-term-slot="${esc(termSlotKey(run.id, i))}"></div>` : ""}
        ${s.notes?.length ? `<div class="step-body">${renderNotes(s.notes)}</div>` : ""}
        ${state.open.has(i) ? `<div class="step-body two-col"><div>
          ${s.status === "pending" && state.preview?.[i] ? `<h4>Will do</h4>${stepDetails(state.preview[i], state.fns)}` : `
            ${state.preview?.[i]?.description || fnIcon(stepCode(state.cfg?.[i]) || s.command, state.fns) ? `<div class="doing">${esc(state.preview?.[i]?.description || "")}${fnIcon(stepCode(state.cfg?.[i]) || s.command, state.fns)}</div>` : ""}
            ${state.preview?.[i]?.expect ? `<div class="expect"><b>Expect:</b> ${esc(state.preview[i].expect)}</div>` : ""}
            ${s.command || state.preview?.[i]?.command ? `<pre class="cmd">${esc(s.command || state.preview[i].command)}</pre>` : ""}`}
          <button class="link" data-note="${i + 1}">✎ Add note</button> ${explainBtn(i + 1)}</div>
          <div>${sidePanel(`run:${i}`, i, state.cfg?.[i], state.history || [], state.prev, { label: "This run", html: thisRun(s, i) })}</div>
        </div>` : ""}
      </li>`).join("")}
    </ol>`;

  main.querySelectorAll("pre.log").forEach((p) => { if (scroll[p.dataset.i] !== false) p.scrollTop = p.scrollHeight; });
  const st = $("details.state");
  if (st) st.ontoggle = () => (state.stateOpen = st.open);
  main.querySelectorAll("[data-note]").forEach((b) => (b.onclick = (ev) => {
    ev.stopPropagation();
    addNote(run.id, +b.dataset.note, state);
  }));
  main.querySelectorAll("[data-toggle]").forEach((h) => (h.onclick = () => {
    const i = +h.dataset.toggle;
    state.open.has(i) ? state.open.delete(i) : state.open.add(i);
    renderRun(state);
  }));
  main.querySelectorAll("[data-ctl]").forEach((b) => (b.onclick = (ev) => {
    ev.stopPropagation();
    const k = b.dataset.ctl, n = +b.dataset.i + 1;
    const next = { ...ctl, step_mode: !!ctl.step_mode };
    next[k] = has(ctl[k], n - 1) ? ctl[k].filter((x) => x !== n) : [...(ctl[k] || []), n];
    setControls(run.id, next, state);
  }));
  $("#step-all") && ($("#step-all").onchange = (e) => setControls(run.id, { ...ctl, step_mode: e.target.checked }, state));
  bindSide(main, () => renderRun(state));
  main.querySelectorAll("[data-explain]").forEach((b) => (b.onclick = (ev) => {
    ev.stopPropagation();
    const n = +b.dataset.explain;
    openExplain({ runbook: run.runbook }, n, n ? `${n}. ${state.steps[n - 1].name}` : run.runbook);
  }));
  main.querySelectorAll("[data-show-bg]").forEach((b) => (b.onclick = () => showTerm(b.dataset.showBg)));
  main.querySelectorAll("[data-stop-step]").forEach((b) => (b.onclick = (ev) => { ev.stopPropagation(); stopRunningStep(run.id, +b.dataset.stopStep, state.steps[+b.dataset.stopStep]?.name); }));
  main.querySelectorAll("[data-reopen]").forEach((b) => (b.onclick = async (ev) => {
    ev.stopPropagation();
    b.disabled = true;
    try {
      await api("POST", `/api/runs/${enc(run.id)}/steps/${+b.dataset.reopen + 1}/reopen`);
      side.tab.set(`run:${b.dataset.reopen}`, "run");
      toast(`Step ${+b.dataset.reopen + 1} re-opened: it waits for you before running again`);
    } catch (e) { b.disabled = false; toast(e.message); }
  }));
  main.querySelectorAll("[data-edit-step]").forEach((b) => (b.onclick = (ev) => { ev.stopPropagation(); editRunStep(state, +b.dataset.editStep); }));
  $("#save-edits") && ($("#save-edits").onclick = () => saveRunEdits(state));
  $("#cancel") && ($("#cancel").onclick = () => abortRun(state));
  $("#change-inputs") && ($("#change-inputs").onclick = () => changeInputs(state));
  $("#export-logs") && ($("#export-logs").onclick = () => exportLogs(run.id));
  $("#delete-run") && ($("#delete-run").onclick = async () => {
    if (!confirm(`Delete run ${run.id}? Its record, output and notes are removed for good.`)) return;
    try { await api("DELETE", `/api/runs/${enc(run.id)}`); toast("Run deleted"); location.hash = `#/runbooks/${enc(run.runbook)}`; }
    catch (e) { toast(e.message); }
  });
  $("#raw-log") && ($("#raw-log").onclick = async () => {
    state.raw = !state.raw;
    if (state.raw && state.rawText == null) {
      try { state.rawText = await (await fetch(`/api/runs/${enc(run.id)}/log`, { headers: { "X-Runnr": "1" } })).text(); }
      catch (e) { state.raw = false; return toast(e.message); }
    }
    renderRun(state);
  });
  if (state.raw && state.rawText != null) {
    const panel = document.createElement("div");
    panel.className = "raw-log";
    panel.innerHTML = `<div class="row"><span class="muted small">The run as a plain log, credentials masked.</span><span class="spacer"></span>
        <button type="button" data-raw-copy>Copy</button><a class="button" href="/api/runs/${enc(run.id)}/log?download=1">Download</a></div>
      <pre class="log">${esc(state.rawText)}</pre>`;
    panel.querySelector("[data-raw-copy]").onclick = async (e) => {
      try { await navigator.clipboard.writeText(state.rawText); e.target.textContent = "Copied"; } catch { toast("Select the text and copy it"); }
      setTimeout(() => (e.target.textContent = "Copy"), 1400);
    };
    main.querySelector("ol.steps")?.replaceWith(panel);
  }
  $("#resume") && ($("#resume").onclick = () => (location.hash = `#/runbooks/${enc(run.runbook)}?resume=${enc(run.id)}`));
  $("#rerun") && ($("#rerun").onclick = () => (location.hash = `#/runbooks/${enc(run.runbook)}?rerun=${enc(run.id)}`));
  const q = $(".question");
  if (q) bindQuestion(q, state.question, run.id);
  termMountAll(); // terminals go back in their steps
}

// ---------- maintenance window ----------

// windowBanner: where the run stands against the maintenance window. While
// it's open the clock ticks (see the timer below).
function windowBanner(run) {
  const w = run.window;
  if (!w) return "";
  const target = parseOffset(w.target);
  const tgt = w.target ? ` of ${esc(w.target)} target` : "";
  if (w.opened && !w.closed) {
    const active = !["succeeded", "failed", "aborted"].includes(run.status);
    return `<div class="mw-banner open"><b>🔴 Maintenance window open: production is down</b>
      <span class="mw-clock" data-since="${esc(w.opened)}" data-target="${target ?? ""}"></span><span class="muted">${tgt}</span>
      ${active ? "" : `<b>The run has stopped with the window still open.</b>`}</div>`;
  }
  if (w.opened && w.closed) {
    const len = (new Date(w.closed) - new Date(w.opened)) / 1000;
    const over = target != null && len > target;
    return `<div class="mw-banner ${over ? "over" : ""}">Maintenance window: <b>${fmtOffset(len, false)}</b>${tgt}${over ? ` <span class="mw-over">(${fmtOffset(len - target, false)} over)</span>` : ""}</div>`;
  }
  const span = [w.open_step, w.close_step].filter((x) => x >= 0).map((x) => x + 1).join("–");
  return `<p class="muted">Maintenance window${span ? ` (steps ${span})` : ""} not open yet${w.target ? ` · target ${esc(w.target)}` : ""}.</p>`;
}

// stepTiming: a step's planned time against the window, and when it
// actually started.
function stepTiming(state, i) {
  if (!state.cfg) return "";
  // The preview has the run's inputs applied (they can override offsets).
  const pv = state.preview?.[i];
  const planned = pv ? pv.offset_sec ?? null : plannedOffset(state.cfg, state.rbSections, i);
  const w = state.run.window, s = state.steps[i];
  if (planned == null) return "";
  let out = ` · planned T${fmtOffset(planned)}`;
  if (w?.opened && s.started) {
    const actual = (new Date(s.started) - new Date(w.opened)) / 1000;
    const drift = actual - planned;
    out += ` · started T${fmtOffset(actual)}`;
    if (Math.abs(drift) >= 60) out += ` <span class="${drift > 0 ? "late" : "early"}">(${fmtOffset(Math.abs(drift), false)} ${drift > 0 ? "late" : "early"})</span>`;
  }
  return out;
}

// One timer ticks every open-window clock on the page.
setInterval(() => {
  document.querySelectorAll(".mw-clock[data-since]").forEach((el) => {
    const sec = (Date.now() - new Date(el.dataset.since)) / 1000;
    const target = el.dataset.target === "" ? null : +el.dataset.target;
    el.textContent = `T${fmtOffset(sec)}`;
    el.classList.toggle("mw-over", target != null && sec > target);
  });
}, 1000);

// ---------- editing steps during a run ----------

// editable: the current step and later ones can be changed while the run is live.
function editable(state, i) {
  if (state.question?.step === i) return true; // waiting on it: edit and run it again
  const cur = state.steps.findIndex((s) => ["pending", "running"].includes(s.status));
  return cur >= 0 && i >= cur && ["pending", "running"].includes(state.steps[i].status);
}

function editsBanner(run, state) {
  const edits = run.edits || [];
  if (!edits.length) return "";
  const steps = [...new Set(edits.map((e) => e.step))].sort((a, b) => a - b);
  const names = steps.map((i) => `${i + 1}. ${esc(state.steps[i]?.name || "")}`).join(", ");
  if (run.edits_saved) return `<p class="muted">✎ Step edits (${names}) saved to the runbook as version ${run.edits_saved}.</p>`;
  return `<div class="edits-banner">✎ You changed ${steps.length === 1 ? "a step" : `${steps.length} steps`} in this run (${names}). The runbook doesn't have ${steps.length === 1 ? "this change" : "these changes"} yet.
    <button class="primary" id="save-edits">Save to runbook</button></div>`;
}

async function editRunStep(state, i) {
  const run = state.run;
  let def;
  try { def = await api("GET", `/api/runs/${enc(run.id)}/steps/${i + 1}`); } catch (e) { return toast(e.message); }
  const waiting = state.question?.step === i;
  const dlg = document.createElement("dialog");
  dlg.className = "edit-step";
  dlg.innerHTML = `<h3>Edit step ${i + 1} for this run</h3>
    <p class="muted small">${waiting ? "Applying starts this step again with the new version." : i + 1 === def.current ? "This step is running now; the change applies if it runs again (retry)." : "The run uses the new version when it gets here."}
      Save the change to the runbook afterwards, or it's only in this run.</p>
    <textarea class="editor" spellcheck="false">${esc(def.yaml)}</textarea>
    <div class="error-text"></div>
    <div class="row"><button class="primary" data-apply>${waiting ? "Apply and run it again" : "Apply"}</button><button data-close>Cancel</button></div>`;
  document.body.append(dlg);
  const ta = dlg.querySelector("textarea");
  ta.onkeydown = (e) => { if (e.key === "Tab") { e.preventDefault(); ta.setRangeText("  ", ta.selectionStart, ta.selectionEnd, "end"); } };
  dlg.querySelector("[data-close]").onclick = () => dlg.close();
  dlg.onclose = () => dlg.remove();
  dlg.querySelector("[data-apply]").onclick = async () => {
    try {
      const res = await api("PUT", `/api/runs/${enc(run.id)}/steps/${i + 1}`, ta.value, true);
      state.run.edits = res.edits;
      state.run.edits_saved = 0;
      Object.assign(state.steps[i], { edited: true, name: res.view.name, kind: res.view.kind });
      dlg.close();
      toast(res.restarted ? "Step changed; running it again" : "Step changed for this run");
      state.onState?.();
      renderRun(state);
    } catch (e) { dlg.querySelector(".error-text").textContent = e.message; }
  };
  dlg.showModal();
  ta.focus();
}

// canChangeInputs: a live run's inputs can change once its steps have
// started, until they're done.
function canChangeInputs(state) {
  const r = state.run;
  return !!state.live && !["succeeded", "failed", "aborted"].includes(r.status) && !!r.begun && !r.begun.startsWith("0001")
    && (state.inputDefs || []).length > 0 && state.steps.some((s) => ["pending", "running"].includes(s.status));
}

// inputChangesHTML lists the changes to a run's inputs.
function inputChangesHTML(run) {
  const cs = run.input_changes || [];
  if (!cs.length) return "";
  const at = (c) => (c.step < 0 ? "before the first step" : c.step >= run.steps.length ? "after the last step" : `from step ${c.step + 1}`);
  return `<details class="input-changes small"><summary>Inputs changed during the run (${cs.length})</summary><ul>${cs.map((c) =>
    `<li><code>${esc(c.name)}</code>: <code>${esc(c.old || "—")}</code> → <code>${esc(c.new || "—")}</code> <span class="muted">${at(c)} · ${new Date(c.at).toLocaleTimeString()}${c.by ? ` · ${esc(c.by)}` : ""}</span></li>`).join("")}</ul></details>`;
}

// changeInputs changes a live run's inputs: they take effect before its
// next command (a step waiting to run its command starts again).
function changeInputs(state) {
  const run = state.run, defs = state.inputDefs || [];
  let saved = "";
  try { saved = localStorage.getItem("runnr.author") || ""; } catch {}
  const dlg = document.createElement("dialog");
  dlg.className = "edit-step abort-dialog inputs-dialog";
  const field = (d) => {
    const v = run.inputs?.[d.name] ?? "";
    if (d.choices?.length) return `<select name="${esc(d.name)}">${(d.required ? [] : [""]).concat(d.choices).map((c) => `<option ${c === v ? "selected" : ""} value="${esc(c)}">${esc(c || "(default)")}</option>`).join("")}</select>`;
    if (d.secret) return `<input name="${esc(d.name)}" type="password" placeholder="unchanged" autocomplete="off">`;
    return withPathPick(d, `<input name="${esc(d.name)}" value="${esc(v)}" placeholder="${esc(d.default || "")}" spellcheck="false" autocomplete="off">`);
  };
  dlg.innerHTML = `<h3>Change inputs</h3>
    <p class="muted small">New values take effect before the run's next command: the next step, or the one waiting now if it hasn't run its command yet (it starts again). A command that's running keeps the values it has. The change is recorded with the run, and a resume uses the new values.</p>
    <table class="kv">${defs.map((d) => `<tr><td><code>${esc(d.name)}</code>${d.required ? " *" : ""}${d.description ? `<div class="muted small">${esc(d.description)}</div>` : ""}</td><td>${field(d)}</td></tr>`).join("")}</table>
    <label>Your name <input name="__by" value="${esc(saved)}" autocomplete="name"></label>
    <div class="inputs-result small"></div>
    <div class="error-text"></div>
    <div class="row"><button class="primary" data-ok>Change</button><button data-close>Cancel</button></div>`;
  document.body.append(dlg);
  dlg.querySelector("[data-close]").onclick = () => dlg.close();
  dlg.onclose = () => dlg.remove();
  dlg.querySelector("[data-ok]").onclick = async (e) => {
    const values = {};
    for (const d of defs) {
      const f = dlg.querySelector(`[name="${CSS.escape(d.name)}"]`);
      if (d.secret ? f.value !== "" : f.value !== (run.inputs?.[d.name] ?? "")) values[d.name] = f.value;
    }
    if (!Object.keys(values).length) return dlg.close();
    const by = dlg.querySelector("[name=__by]").value.trim();
    if (by) try { localStorage.setItem("runnr.author", by); } catch {}
    e.currentTarget.disabled = true;
    try {
      const res = await api("PUT", `/api/runs/${enc(run.id)}/inputs`, { values, by });
      const stale = Object.entries(res.stale || {}).map(([k, steps]) => `<li><code>${esc(k)}</code>: step${steps.length === 1 ? "" : "s"} ${steps.join(", ")} already ran with the old value</li>`);
      const repl = (res.replaces || []).map((k) => `<li><code>${esc(k)}</code>: a step had published a value for it; the new one takes its place</li>`);
      const msg = !res.changed?.length ? "Nothing changed." : res.restarted ? "Changed: the step waiting now starts again with the new values." : "Changed: they take effect before the next command.";
      if (stale.length || repl.length) {
        dlg.querySelector(".inputs-result").innerHTML = `<p><b>${esc(msg)}</b></p><ul>${[...stale, ...repl].join("")}</ul>`;
        dlg.querySelector("[data-ok]").hidden = true;
        dlg.querySelector("[data-close]").textContent = "OK";
      } else { dlg.close(); toast(msg); }
    } catch (x) { dlg.querySelector(".error-text").textContent = x.message; e.currentTarget.disabled = false; }
  };
  dlg.showModal();
  dlg.querySelector("input, select")?.focus();
}

// abortRun asks first, with an optional reason. A run this server isn't
// running (it's in a terminal, or its process is gone) is asked to stop,
// or marked aborted when its process is gone (or with "now").
function abortRun(state) {
  const run = state.run, o = run.owner;
  const elsewhere = !state.live;
  let saved = "";
  try { saved = localStorage.getItem("runnr.author") || ""; } catch {}
  const where = !elsewhere ? "The step running now is stopped and the run ends aborted; it can be resumed."
    : o ? `It's running ${o.via === "cli" ? "in a terminal" : "in another runnr server"} on ${esc(o.host)} (pid ${o.pid}). runnr asks that process to stop it, or marks the run aborted if the process is gone.`
    : "This server isn't running it. runnr asks whichever runnr is to stop it, and marks it aborted if none does.";
  const dlg = document.createElement("dialog");
  dlg.className = "edit-step abort-dialog";
  dlg.innerHTML = `<h3>Abort this run?</h3>
    <p class="muted small">${where}</p>
    <label>Your name <input name="by" value="${esc(saved)}" autocomplete="name"></label>
    <label>Why (optional, recorded with the run) <input name="reason" autocomplete="off"></label>
    ${elsewhere ? `<label class="check"><input type="checkbox" name="force"> Mark it aborted now, without waiting (its process is gone)</label>` : ""}
    <div class="error-text"></div>
    <div class="row"><button class="danger" data-ok>Abort run</button><button data-close>Keep running</button></div>`;
  document.body.append(dlg);
  const f = (n) => dlg.querySelector(`[name=${n}]`);
  dlg.querySelector("[data-close]").onclick = () => dlg.close();
  dlg.onclose = () => dlg.remove();
  dlg.querySelector("[data-ok]").onclick = async (e) => {
    const ok = e.currentTarget;
    const by = f("by").value.trim();
    if (by) try { localStorage.setItem("runnr.author", by); } catch {}
    ok.disabled = true;
    if (elsewhere) ok.textContent = "Waiting for it to stop…";
    try {
      const res = await api("POST", `/api/runs/${enc(run.id)}/cancel`, { author: by, reason: f("reason").value.trim(), force: !!f("force")?.checked });
      if (!res.aborted) { dlg.querySelector(".error-text").textContent = res.message; ok.disabled = false; ok.textContent = "Abort run"; return; }
      dlg.close();
      toast(elsewhere ? `Run aborted: ${res.message}` : "Aborting…");
      if (elsewhere) viewRun(run.id);
    } catch (err) { dlg.querySelector(".error-text").textContent = err.message; ok.disabled = false; ok.textContent = "Abort run"; }
  };
  dlg.showModal();
  (saved ? f("reason") : f("by")).focus();
}

// thisRun is what step i has done in this run: its output as it arrives,
// errors, checks and shell sessions (the "This run" tab).
function thisRun(s, i) {
  if (s.status === "pending" && !s.log.length) return `<p class="muted">Not reached yet in this run.</p>`;
  const meta = [s.way && `way: ${s.way}`, s.note, s.attempts > 1 && `${s.attempts} attempts`, s.exit_code != null && `exit ${s.exit_code}`, duration(s.started, s.ended)].filter(Boolean);
  return `<p><span class="s-${esc(s.status)}">${ICONS[s.status] || ""} ${esc(s.status)}</span>${meta.length ? ` <span class="muted">${meta.map(esc).join(" · ")}</span>` : ""}</p>
    ${s.log.length ? `<pre class="log" data-i="${i}">${s.log.map((l) => `<span class="${l.stream}">${esc(l.text)}</span>`).join("\n")}</pre>` : s.status === "running" ? `<p class="muted">No output yet.</p>` : ""}
    ${s.error && s.status !== "succeeded" ? `<div class="error-text">${esc(s.error)}</div>` : ""}
    ${renderChecks(s.checks)}${renderSub(s)}
    ${(s.shells || []).map((t, n) => `<details class="shell-log"><summary>Shell session ${n + 1}</summary><pre class="log">${esc(t)}</pre></details>`).join("")}
    ${earlierTries(s)}`;
}

// alignedRun is run with its steps matched to rb's by name (in order), as a
// resume does when the runbook has changed since: a step it doesn't have
// is pending. changed says the runbook's steps differ; dropped names the
// steps it ran that the runbook no longer has.
function alignedRun(run, rb) {
  if (!run || !rb?.steps) return run;
  const used = new Set();
  const steps = rb.steps.map((s, j) => {
    const i = run.steps.findIndex((r, k) => !used.has(k) && r.name === s.name);
    if (i < 0) return { index: j, name: s.name, status: "pending" };
    used.add(i);
    return run.steps[i];
  });
  const changed = steps.length !== run.steps.length || steps.some((s, i) => s.name !== run.steps[i].name);
  const dropped = run.steps.filter((s, k) => !used.has(k) && s.status !== "pending").map((s) => s.name);
  return { ...run, steps, changed, dropped };
}

// changedNote says a resume carries the run on with a changed runbook.
function changedNote(prev, version) {
  if (!prev?.changed) return "";
  const d = prev.dropped || [];
  return ` The runbook has changed since this run (it ran version ${prev.runbook_version || "?"}, now ${version}): it carries on with this version, its steps matched by name${d.length ? `; ${d.length === 1 ? "a step it ran is" : `${d.length} steps it ran are`} no longer in it (${d.map(esc).join(", ")}) and ${d.length === 1 ? "stays" : "stay"} in the record` : ""}.`;
}

// earlierTries are a step's tries before the run was resumed.
function earlierTries(s) {
  return (s.before || []).map((b, n) => `<details class="shell-log"><summary>Earlier try${(s.before.length > 1) ? ` ${n + 1}` : ""}, before the run was resumed: <span class="s-${esc(b.status)}">${ICONS[b.status] || ""} ${esc(b.status)}</span>${b.started ? ` <span class="muted">${new Date(b.started).toLocaleString()}</span>` : ""}</summary>
    ${b.output ? `<pre class="log">${esc(b.output)}</pre>` : ""}${b.error ? `<div class="error-text">${esc(b.error)}</div>` : ""}</details>`).join("");
}

// resumedHTML lists the times a run was resumed (it carried on in the same record).
function resumedHTML(run) {
  const rs = run.resumptions || [];
  if (!rs.length) return "";
  return `<p class="muted">${rs.map((r) => `↻ Resumed ${new Date(r.at).toLocaleString()} from step ${r.from + 1} (it had ${esc(r.status)}${r.error ? `: ${esc(r.error)}` : ""})${r.was_version ? ` with runbook version ${r.runbook_version} (it had run version ${r.was_version})` : ""}`).join("<br>")}</p>`;
}

// removedHTML lists steps the run did that its runbook no longer has.
function removedHTML(run) {
  const rm = run.removed || [];
  if (!rm.length) return "";
  return `<details class="card removed-steps"><summary>${rm.length} step${rm.length === 1 ? "" : "s"} it ran that the runbook no longer has</summary>
    ${rm.map((s) => `<div class="removed-step"><b>${esc(s.name)}</b> <span class="s-${esc(s.status)}">${ICONS[s.status] || ""} ${esc(s.status)}</span>${s.output ? `<pre class="log">${esc(s.output)}</pre>` : ""}</div>`).join("")}</details>`;
}

async function saveRunEdits(state) {
  if (!confirm(`Save this run's step edits as a new version of “${state.run.runbook}”?\nThe YAML is reformatted, so comments in it are dropped.`)) return;
  try {
    const { version } = await api("POST", `/api/runs/${enc(state.run.id)}/save-edits`);
    state.run.edits_saved = version;
    toast(`Saved as version ${version}`);
    api("GET", `/api/runbooks/${enc(state.run.runbook)}`).then((d) => { state.cfg = d.runbook?.steps; renderRun(state); }).catch(() => {});
    renderRun(state);
  } catch (e) { toast(e.message); }
}

// renderChecks shows the latest attempt of a group of checks.
function renderChecks(checks) {
  if (!checks?.length) return "";
  const latest = Math.max(0, ...checks.map((c) => c.attempt || 0));
  return `<table class="checks">${checks.map((c) => `<tr>
    <td>${c.not_run ? "⏸" : c.passed ? "✅" : "❌"}</td><td>${esc(c.name)}</td>
    <td class="muted">${c.not_run ? "waits for the one above to pass" : c.passed ? (c.attempt && c.attempt < latest ? `passed in attempt ${c.attempt}` : "") : esc((c.output || c.error || "").split("\n").filter(Boolean).pop() || "")}</td></tr>`).join("")}</table>`;
}

// renderSub shows which alternative ran and its steps.
function renderSub(s) {
  if (!s.alternative) return "";
  return `<p class="muted">Done by: <b>${esc(s.alternative)}</b></p>${(s.sub || []).length ? `<ol class="sub-steps">${s.sub.map((r) =>
    `<li><span class="s-${esc(r.status)}">${ICONS[r.status] || ""}</span> ${esc(r.name)} <span class="muted">${esc([r.note, r.error].filter(Boolean).join(" · "))}</span></li>`).join("")}</ol>` : ""}`;
}

async function setControls(id, c, state) {
  try {
    state.controls = await api("PUT", `/api/runs/${enc(id)}/controls`, {
      step_mode: c.step_mode, skip: c.skip || [], pause_before: c.pause_before || [], pause_after: c.pause_after || [],
    });
    renderRun(state);
  } catch (e) { toast(e.message); }
}

// Some answers read differently depending on the question.
const QLABELS_BY_TYPE = {
  after: { continue: "✔ Looks right — continue", retry: "↻ Run it again", fail: "✘ Mark it failed…" },
  danger: { run: "⚠ Run it anyway" },
  typed: { run: "⚠ Run it" },
};

const QLABELS = {
  yes: "✔ Yes", no: "✘ No", run: "▶ Run", skip: "Skip", abort: "Abort run", done: "✔ Done", fail: "✘ Failed", retry: "↻ Retry", force: "⚑ Force success…", way: "↪ Try another way…",
  shell: "⌨ Open shell", continue: "Run the rest without pausing",
};

function renderQuestion(q, runId) {
  let body = "";
  if (q.type === "input" || q.type === "ask_text") {
    const i = q.input;
    body = i.choices?.length
      ? `<select name="value">${i.choices.map((c) => `<option ${c === i.default ? "selected" : ""}>${esc(c)}</option>`).join("")}</select>`
      : withPathPick(i, `<input type="${i.secret ? "password" : "text"}" name="value" placeholder="${esc(i.default || "")}" autofocus>`);
    body += `<button class="primary" data-v="submit">Submit</button>`;
  } else {
    if (q.type === "manual") body += `<input type="text" name="note" placeholder="Note (optional)">`;
    const typedQ = q.type === "danger" || q.type === "typed";
    if (typedQ) body += `<input type="text" name="note" class="danger-confirm" placeholder="type ${esc(q.confirm)}" autocomplete="off" size="${Math.max(10, q.confirm.length + 2)}">`;
    body += q.options.map((o, idx) => `<button data-v="${esc(o)}" ${typedQ && o === "run" ? "disabled" : ""} class="${typedQ && o === "run" ? "danger" : idx === 0 ? "primary" : o === "abort" ? "danger" : ""}">${QLABELS_BY_TYPE[q.type]?.[o] || QLABELS[o] || esc(o)}</button>`).join("");
  }
  let text = q.type === "choose" ? `<pre class="cmd">${esc(q.text)}</pre>` : q.type === "danger" ? `<p class="danger-why">Type <b>${esc(q.confirm)}</b> (the step number) to run it anyway.</p><pre class="cmd">${esc(q.text)}</pre>`
    : q.type === "typed" ? `<p class="danger-why">Type <b>${esc(q.confirm)}</b> to run it.</p><pre class="cmd">${esc(q.text)}</pre>`
    : ["confirm", "before"].includes(q.type) ? `<pre class="cmd">${esc(q.text)}</pre>` : q.text ? `<div class="text">${esc(q.text)}</div>` : "";
  if (q.expect) text += `<div class="expect"><b>Expect:</b> ${esc(q.expect)}</div>`;
  if (q.optional) text = `<p class="optional-note">◇ <b>Optional:</b> this step isn't required. Run it, or skip it and carry on.</p>` + text;
  if (q.options?.includes("shell")) text += `<div class="muted">“Open shell” opens a terminal below, with this run's variables loaded and this step's commands beside it. The question stays here for when you're back.</div>`;
  // Going elsewhere: back a step, or to any step by number (it waits there before running).
  const nav = q.goto ? `<div class="q-goto small">${q.step > 0 ? `<button class="link" data-goto="${q.step}" title="Go back to step ${q.step}: it and the steps after it run again (it waits for you first)">↺ Back to step ${q.step}</button> · ` : ""}
    <label>Go to step <input type="number" min="1" name="goto" size="4" placeholder="${q.step + 1}"></label><button class="link" data-goto-n>Go</button>
    <span class="muted">back runs the steps from there again; ahead skips the ones between; it waits for you before running</span></div>` : "";
  return `<div class="question ${q.type === "danger" || q.type === "typed" ? "q-danger" : ""}"><h3>${esc(q.title)}</h3>${text}<div class="actions">${body}</div>${nav}</div>`;
}

// gotoStep goes to step n (1-based) while the run waits on the operator.
async function gotoStep(runId, n, force = false) {
  try {
    await api("POST", `/api/runs/${enc(runId)}/goto`, { step: n, force });
    toast(`Going to step ${n}: it waits for you before running`);
  } catch (e) {
    if (!force && /point of no return/.test(e.message) && confirm(`${e.message}.\n\nGo back anyway?`)) return gotoStep(runId, n, true);
    toast(e.message);
  }
}

// stopRunningStep interrupts the step whose command is running; it fails,
// and the run asks what to do.
async function stopRunningStep(runId, i, name) {
  if (!confirm(`Stop step ${i + 1}${name ? ` (${name})` : ""}? Its command is interrupted (Ctrl-C, then terminated if it won't stop). The step fails and you choose what's next: retry, skip, or go to another step. The run goes on.`)) return;
  let by = "";
  try { by = localStorage.getItem("runnr.author") || ""; } catch {}
  try { await api("POST", `/api/runs/${enc(runId)}/stop`, { step: i + 1, by }); toast(`Stopping step ${i + 1}…`); }
  catch (e) { toast(e.message); }
}

function bindQuestion(el, q, runId) {
  el.querySelector("[data-goto]")?.addEventListener("click", (e) => gotoStep(runId, +e.currentTarget.dataset.goto));
  const gn = el.querySelector("[name=goto]");
  if (gn) {
    const go = () => { const n = parseInt(gn.value, 10); if (n > 0) gotoStep(runId, n); };
    el.querySelector("[data-goto-n]").onclick = go;
    gn.onkeydown = (e) => { if (e.key === "Enter") { e.preventDefault(); go(); } };
  }
  el.querySelectorAll("button[data-v]").forEach((b) => (b.onclick = async () => {
    let value = b.dataset.v;
    if (value === "submit") value = el.querySelector("[name=value]").value;
    const note = el.querySelector("[name=note]")?.value || "";
    if (value === "force") {
      const reason = (prompt("Why accept this failure as success? (required)") || "").trim();
      if (!reason) return toast("Forcing a step needs a reason");
      return send(value, reason);
    }
    if (value === "fail" && !note && (q.type === "manual" || q.type === "after")) {
      const reason = prompt(q.type === "after" ? "What's wrong with the result?" : "Why did this step fail?");
      if (reason === null) return;
      return send(value, reason);
    }
    send(value, note);
  }));
  const typed = el.querySelector(".danger-confirm");
  if (typed) {
    const run = el.querySelector("[data-v=run]");
    typed.oninput = () => (run.disabled = typed.value.trim() !== q.confirm);
    typed.onkeydown = (e) => { if (e.key === "Enter" && !run.disabled) run.click(); };
    typed.focus();
  }
  const input = el.querySelector("input[name=value]");
  if (input) {
    input.focus();
    input.onkeydown = (e) => { if (e.key === "Enter") el.querySelector("[data-v=submit]").click(); };
  }
  async function send(value, note) {
    el.querySelectorAll("button").forEach((b) => (b.disabled = true));
    try { await api("POST", `/api/runs/${runId}/answer`, { question_id: q.id, value, note }); }
    catch (e) { toast(e.message); el.querySelectorAll("button").forEach((b) => (b.disabled = false)); }
  }
}

// ---------- history ----------

async function viewHistory() {
  const filter = new URLSearchParams(location.hash.split("?")[1] || "").get("runbook") || "";
  const runs = await api("GET", `/api/runs${filter ? `?runbook=${encodeURIComponent(filter)}` : ""}`);
  main.innerHTML = `
    <h1>Run history${filter ? ` · ${esc(filter)}` : ""}</h1>
    ${filter ? `<p><a href="#/history">Show all</a></p>` : `<p class="muted">Most recent first.</p>`}
    ${runs.length ? `<table><thead><tr><th>Status</th><th>Runbook</th><th>Mode</th><th>Started</th><th>Duration</th><th>Run ID</th></tr></thead><tbody>
      ${runs.map((r) => `<tr data-id="${esc(r.id)}">
        <td><span class="s-${esc(r.status)}">${ICONS[r.status] || ""} ${esc(r.status)}</span></td>
        <td>${esc(r.runbook)}</td><td>${esc(r.mode)}${r.dry_run ? " (dry)" : ""}</td>
        <td>${new Date(r.started).toLocaleString()}</td><td>${duration(r.started, r.ended)}</td>
        <td class="muted"><code>${esc(r.id)}</code></td></tr>`).join("")}
    </tbody></table>` : `<div class="empty">No runs yet.</div>`}`;
  main.querySelectorAll("tr[data-id]").forEach((tr) => (tr.onclick = () => (location.hash = `#/runs/${tr.dataset.id}`)));
}

const REFERENCE = `name: my-runbook            # lowercase id, used as the filename
description: What it does
tags: [ops]
shell: bash                  # default: sh
profile: [~/.zshrc]          # sourced before every step: aliases/functions work (slower)
sensitive:                   # extra credential patterns to hide; {{SENSITIVE}} is the hidden part
  api_key: 'X-Api-Key: {{SENSITIVE}}' 
env: { REGION: us-east-1 }   # values may use {{.input}} templates
inputs:
  - name: target
    description: Where to run
    default: staging
    required: true
    choices: [staging, production]
    secret: false            # masked in logs and history
steps:
  - name: Automated step
    run: echo "hello {{.target}}"
    id: hello                # optional; usable with "start at" / --from
    section: Deploy          # heading shown above a group of steps
    owner: Datastores        # who does it (display only)
    always: true             # still runs when starting at a later step
    when: '{{eq .target "production"}}'  # skip unless truthy
    confirm: true            # ask before running (unattended needs --yes)
    timeout: 30s
    retries: 2
    retry_delay: 10s
    capture: greeting        # stdout saved as {{.greeting}} for later steps
  - name: Publish values
    run: |
      region=us-east-1
      pw=$(fetch-password)
    outputs: [region]        # joins the run's state: {{.region}} / $region later
    secret_outputs: [pw]     # same, masked and never stored
    expect: what success looks like (shown when reviewing / running by hand)
  - name: Needs a terminal
    run: terraform apply
    tty: true                # needs a keyboard: runs on a terminal you type into; output still recorded
    continue_on_error: true
    optional: true           # not required: interactive runs ask first; unattended runs carry on if it fails
    dir: /tmp
    env: { FOO: bar }
  - name: Human step
    manual: Check the dashboard for {{.target}} looks healthy.
    unattended: pass         # fail (default) | skip | pass when nobody is there

Template helpers: default, upper, lower, trim, contains, quote`;

loadRunbooks().then(route);
