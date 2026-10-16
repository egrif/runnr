"use strict";

const $ = (sel, el = document) => el.querySelector(sel);
const main = $("#main");
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
const ICONS = { succeeded: "✔", failed: "✘", skipped: "↷", aborted: "■", running: "▶", pending: "○", forced: "⚑" };

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

// sectionHeading returns a heading when step i starts a new section.
function sectionHeading(steps, i) {
  const s = steps[i].section;
  return s && (i === 0 || steps[i - 1].section !== s) ? `<li class="section">${esc(s)}</li>` : "";
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
  runbooks = await api("GET", "/api/runbooks");
  renderSidebar();
}

function renderSidebar() {
  const q = $("#filter").value.toLowerCase();
  const current = decodeURIComponent((location.hash.match(/^#\/runbooks\/([^/]+)/) || [])[1] || "");
  $("#runbook-list").innerHTML = runbooks
    .filter((r) => !q || r.name.includes(q) || (r.description || "").toLowerCase().includes(q) || (r.tags || []).some((t) => t.includes(q)))
    .map((r) => `<li><a href="#/runbooks/${encodeURIComponent(r.name)}" class="${r.name === current ? "active" : ""} ${r.error ? "invalid" : ""}">
        ${esc(r.name)}<span class="desc">${esc(r.error ? "invalid runbook" : r.description || `${r.steps} steps`)}</span></a></li>`)
    .join("") || `<li class="muted">No runbooks</li>`;
  $("#history-link").classList.toggle("active", location.hash.startsWith("#/history"));
}

$("#filter").addEventListener("input", renderSidebar);
$("#new-btn").addEventListener("click", () => (location.hash = "#/new"));

// ---------- router ----------

async function route() {
  if (stream) { stream.close(); stream = null; }
  resetTerm();
  const h = location.hash || "#/";
  renderSidebar();
  let m;
  try {
    if ((m = h.match(/^#\/runbooks\/([^/?]+)(?:\/(edit))?(?:\?(.*))?$/))) return await viewRunbook(decodeURIComponent(m[1]), m[2] || "run", new URLSearchParams(m[3] || ""));
    if ((m = h.match(/^#\/runs\/([^/]+)$/))) return await viewRun(decodeURIComponent(m[1]));
    if (h === "#/new") return await viewNew();
    if (h.startsWith("#/history")) return await viewHistory();
    return viewHome();
  } catch (e) {
    main.innerHTML = `<p class="error-text">${esc(e.message)}</p>`;
  }
}
window.addEventListener("hashchange", route);

function viewHome() {
  main.innerHTML = runbooks.length
    ? `<div class="empty"><h1>runnr</h1><p>Pick a runbook on the left, or create a new one.</p></div>`
    : `<div class="empty"><h1>Welcome to runnr</h1><p>You don't have any runbooks yet.</p><p><button class="primary" onclick="location.hash='#/new'">Create your first runbook</button></p></div>`;
}

// ---------- runbook: run + edit ----------

async function viewRunbook(name, tab, query) {
  const data = await api("GET", `/api/runbooks/${encodeURIComponent(name)}`);
  const rb = data.runbook;
  const href = `#/runbooks/${encodeURIComponent(name)}`;
  main.innerHTML = `
    <div class="row"><div>
      <h1>${esc(name)}</h1>
      <div class="muted">${esc(rb?.description || "")}</div>
      <div>${(rb?.tags || []).map((t) => `<span class="tag">${esc(t)}</span>`).join("")}</div>
    </div></div>
    <nav class="tabs">
      <a href="${href}" class="${tab === "run" ? "active" : ""}">Run</a>
      <a href="${href}/edit" class="${tab === "edit" ? "active" : ""}">Edit</a>
      <a href="#/history?runbook=${encodeURIComponent(name)}">History</a>
    </nav>
    <section id="tab"></section>`;
  if (tab === "edit" || !rb) return renderEditor($("#tab"), data.raw, name, data.error, data.version, data.updated_at);
  const mode = query.get("resume") ? "resume" : query.get("rerun") ? "rerun" : "run";
  await viewLaunch($("#tab"), name, mode, query.get("resume") || query.get("rerun") || "");
}

// stepDetails shows what a step does: code, expected outcome, outputs.
function stepDetails(s) {
  const outs = [...(s.outputs || []), ...(s.secret_outputs || []).map((o) => `${o} (secret)`)];
  return `
    ${s.dir ? `<div class="muted">dir: <code>${esc(s.dir)}</code></div>` : ""}
    ${s.when ? `<div class="muted">only when: <code>${esc(s.when)}</code></div>` : ""}
    <pre class="cmd">${esc(s.run || s.manual || s.command)}</pre>
    ${s.expect ? `<div class="expect"><b>Expect:</b> ${esc(s.expect)}</div>` : ""}
    ${outs.length ? `<div class="muted">publishes: ${outs.map((o) => `<code>${esc(o)}</code>`).join(" ")}</div>` : ""}`;
}

async function viewNew() {
  const { raw } = await api("GET", "/api/template?name=my-runbook");
  main.innerHTML = `<h1>New runbook</h1><p class="muted">Edit the starter below; the <code>name</code> field becomes the runbook's identifier.</p><section id="tab"></section>`;
  renderEditor($("#tab"), raw, null);
}

// ---------- live / past run ----------

async function viewRun(id) {
  const { run, live, controls } = await api("GET", `/api/runs/${encodeURIComponent(id)}`);
  const state = { run, live, controls, steps: run.steps.map((s) => ({ ...s, log: [] })), question: null, open: new Set() };
  // What steps will do (with this run's inputs and state), and the run it's compared against.
  const loadPreview = async () => {
    try { state.preview = (await api("GET", `/api/runs/${enc(id)}/preview`)).steps; renderRun(state); } catch {}
  };
  loadPreview();
  if (run.previous) api("GET", `/api/runs/${enc(run.previous)}`).then((r) => { state.prev = r.run; renderRun(state); }).catch(() => {});
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
      state.termStep = e.kind === "term_start" ? e.step : null;
      state.termShell = e.kind === "term_start" && !!e.term.shell;
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
      if (e.kind === "run_finished") { state.question = null; stream?.close(); }
      break;
    case "step_started":
      Object.assign(state.steps[e.step], { ...e.result, notes: state.steps[e.step].notes });
      state.steps[e.step].log = [];
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
      if (!state.steps[e.step].log.length && e.result.output) {
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
  }
}

function renderRun(state) {
  const { run } = state;
  const stopAt = state.steps.findIndex((s) => !["succeeded", "skipped", "forced"].includes(s.status));
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
      ${active ? `<button class="danger" id="cancel">Abort run</button>` : `${state.resumable ? `<button class="primary" id="resume">Resume…</button>` : ""}<button id="rerun">Run again…</button>`}
    </div>
    ${run.error ? `<p class="error-text">${esc(run.error)}</p>` : ""}
    ${inputs.length ? `<p class="muted">${inputs.map(([k, v]) => `<code>${esc(k)}=${esc(v)}</code>`).join(" ")}</p>` : ""}
    ${(state.messages || []).map((m) => `<p class="muted">ℹ ${esc(m)}</p>`).join("")}
    <div class="notes">${renderNotes(run.notes)}<button class="link" data-note="0">✎ Add run note</button></div>
    ${renderState(run.state, state)}
    ${run.resumes ? `<p class="muted">Resumes <a href="#/runs/${esc(run.resumes)}">${esc(run.resumes)}</a></p>` : ""}
    ${state.prev ? `<p class="muted">Compared with <a href="#/runs/${esc(state.prev.id)}">${esc(state.prev.id)}</a> (${esc(state.prev.status)}): open a step to see what it did then.</p>` : ""}
    ${live && state.controls && run.mode === "interactive" ? `<p><label><input type="checkbox" id="step-all" ${state.controls.step_mode ? "checked" : ""}> Step through the remaining steps</label>
      <span class="muted small"> · upcoming steps can be skipped or paused below</span></p>` : ""}
    ${state.question?.step === -1 ? renderQuestion(state.question, run.id) : ""}
    <ol class="steps">${state.steps.map((s, i) => `${sectionHeading(state.steps, i)}
      <li class="step ${state.question?.step === i ? "focus" : ""} ${s.note === "before start step" ? "dim" : ""}">
        <div class="step-head" data-toggle="${i}">
          <span class="icon s-${s.status}">${ICONS[s.status] || ""}</span>
          <span class="n">${i + 1}</span>
          <span class="name">${esc(s.name)} <span class="kind">${esc(s.kind)}</span>${s.owner ? ` <span class="owner">${esc(s.owner)}</span>` : ""}</span>
          <span class="meta">${[s.note, s.attempts > 1 && `${s.attempts} attempts`, s.exit_code != null && `exit ${s.exit_code}`, duration(s.started, s.ended)].filter(Boolean).map(esc).join(" · ")}</span>
          ${state.preview?.[i]?.confirm ? `<span class="badge-k warn" title="Asks before running">confirms on its own</span>` : ""}
          ${state.prev?.steps[i] && state.prev.steps[i].status !== "pending" ? `<span class="last s-${esc(state.prev.steps[i].status)}" title="Last time: ${esc(state.prev.steps[i].status)}">${ICONS[state.prev.steps[i].status]}</span>` : ""}
          ${live && s.status === "pending" && state.controls ? `
            <button class="pill ${has(ctl.skip, i) ? "on" : ""}" data-ctl="skip" data-i="${i}" title="Leave this step out">skip</button>
            <button class="pill ${has(ctl.pause_before, i) ? "on" : ""}" data-ctl="pause_before" data-i="${i}" title="Stop before this step">⏸ before</button>
            <button class="pill ${has(ctl.pause_after, i) ? "on" : ""}" data-ctl="pause_after" data-i="${i}" title="Stop after this step to check it">⏸ after</button>` : ""}
        </div>
        ${state.question?.step === i ? `<div class="step-body">${renderQuestion(state.question, run.id)}</div>` : ""}
        ${state.termStep === i ? `<div class="step-body"><p class="term-note">⌨ A terminal is open for this step below${state.termShell ? " (shell): type exit or End session to come back to the run" : ": type there when it asks"}. <button class="link" data-show-term>Show terminal</button></p></div>` : ""}
        ${s.notes?.length ? `<div class="step-body">${renderNotes(s.notes)}</div>` : ""}
        ${state.open.has(i) ? `<div class="step-body ${state.prev ? "two-col" : ""}"><div>
          ${s.status === "pending" && state.preview?.[i] ? `<h4>Will do</h4>${stepDetails(state.preview[i])}` : `
            ${state.preview?.[i]?.expect ? `<div class="expect"><b>Expect:</b> ${esc(state.preview[i].expect)}</div>` : ""}
            ${s.command || state.preview?.[i]?.command ? `<pre class="cmd">${esc(s.command || state.preview[i].command)}</pre>` : ""}
            ${s.log.length ? `<pre class="log" data-i="${i}">${s.log.map((l) => `<span class="${l.stream}">${esc(l.text)}</span>`).join("\n")}</pre>` : ""}
            ${s.error && s.status !== "succeeded" ? `<div class="error-text">${esc(s.error)}</div>` : ""}`}
          ${(s.shells || []).map((t, n) => `<details class="shell-log"><summary>Shell session ${n + 1}</summary><pre class="log">${esc(t)}</pre></details>`).join("")}
          <button class="link" data-note="${i + 1}">✎ Add note</button></div>
          ${state.prev ? `<div><h4>Last time</h4>${lastTime(state.prev.steps[i], state.prev)}</div>` : ""}
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
  main.querySelectorAll("[data-show-term]").forEach((b) => (b.onclick = (ev) => { ev.stopPropagation(); showTermDrawer(); }));
  $("#cancel") && ($("#cancel").onclick = () => api("POST", `/api/runs/${run.id}/cancel`).catch((e) => toast(e.message)));
  $("#resume") && ($("#resume").onclick = () => (location.hash = `#/runbooks/${enc(run.runbook)}?resume=${enc(run.id)}`));
  $("#rerun") && ($("#rerun").onclick = () => (location.hash = `#/runbooks/${enc(run.runbook)}?rerun=${enc(run.id)}`));
  const q = $(".question");
  if (q) bindQuestion(q, state.question, run.id);
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
};

const QLABELS = {
  run: "▶ Run", skip: "Skip", abort: "Abort run", done: "✔ Done", fail: "✘ Failed", retry: "↻ Retry", force: "⚑ Force success…",
  shell: "⌨ Open shell", continue: "Run the rest without pausing",
};

function renderQuestion(q, runId) {
  let body = "";
  if (q.type === "input") {
    const i = q.input;
    body = i.choices?.length
      ? `<select name="value">${i.choices.map((c) => `<option ${c === i.default ? "selected" : ""}>${esc(c)}</option>`).join("")}</select>`
      : `<input type="${i.secret ? "password" : "text"}" name="value" placeholder="${esc(i.default || "")}" autofocus>`;
    body += `<button class="primary" data-v="submit">Submit</button>`;
  } else {
    if (q.type === "manual") body += `<input type="text" name="note" placeholder="Note (optional)">`;
    body += q.options.map((o, idx) => `<button data-v="${esc(o)}" class="${idx === 0 ? "primary" : o === "abort" ? "danger" : ""}">${QLABELS_BY_TYPE[q.type]?.[o] || QLABELS[o] || esc(o)}</button>`).join("");
  }
  let text = ["confirm", "before"].includes(q.type) ? `<pre class="cmd">${esc(q.text)}</pre>` : q.text ? `<div class="text">${esc(q.text)}</div>` : "";
  if (q.expect) text += `<div class="expect"><b>Expect:</b> ${esc(q.expect)}</div>`;
  if (q.options?.includes("shell")) text += `<div class="muted">“Open shell” opens a terminal below, with this run's variables loaded and this step's commands beside it. The question stays here for when you're back.</div>`;
  return `<div class="question"><h3>${esc(q.title)}</h3>${text}<div class="actions">${body}</div></div>`;
}

function bindQuestion(el, q, runId) {
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
    dir: /tmp
    env: { FOO: bar }
  - name: Human step
    manual: Check the dashboard for {{.target}} looks healthy.
    unattended: pass         # fail (default) | skip | pass when nobody is there

Template helpers: default, upper, lower, trim, contains, quote`;

loadRunbooks().then(route);
