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
  const h = location.hash || "#/";
  renderSidebar();
  let m;
  try {
    if ((m = h.match(/^#\/runbooks\/([^/]+)(?:\/(edit))?$/))) return await viewRunbook(decodeURIComponent(m[1]), m[2] || "run");
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

async function viewRunbook(name, tab) {
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
  renderRunForm($("#tab"), rb);
}

function renderRunForm(el, rb) {
  const inputs = rb.inputs || [];
  const field = (i) => {
    const id = `in-${i.name}`;
    let ctl;
    if (i.choices?.length) {
      ctl = `<select id="${id}" name="${esc(i.name)}">${i.required && !i.default ? `<option value=""></option>` : ""}${i.choices.map((c) => `<option ${c === i.default ? "selected" : ""}>${esc(c)}</option>`).join("")}</select>`;
    } else {
      ctl = `<input id="${id}" name="${esc(i.name)}" type="${i.secret ? "password" : "text"}" placeholder="${esc(i.default || "")}" ${i.required && !i.default ? "required" : ""}>`;
    }
    return `<label for="${id}">${esc(i.name)}${i.required ? " *" : ""}</label>${ctl}${i.description ? `<div class="hint">${esc(i.description)}</div>` : ""}`;
  };
  el.innerHTML = `
    <form id="run-form" class="card">
      ${inputs.length ? `<div class="form-grid">${inputs.map(field).join("")}</div>` : `<div class="muted">This runbook takes no inputs.</div>`}
      <div class="options">
        <label><input type="radio" name="mode" value="interactive" checked> Interactive</label>
        <label><input type="radio" name="mode" value="unattended"> Unattended</label>
        <label title="Approve confirm-gated steps automatically in unattended mode"><input type="checkbox" name="yes"> Auto-approve confirmations</label>
        <label><input type="checkbox" name="dry"> Dry run</label>
        <label>Start at <select name="from"><option value="">step 1</option>${rb.steps.slice(1).map((s, i) => `<option value="${i + 2}">${i + 2}. ${esc(s.name)}</option>`).join("")}</select></label>
      </div>
      <button class="primary" type="submit">▶ Start run</button>
    </form>
    <h2>Steps</h2>
    <ol class="steps">${rb.steps.map((s, i) => `${sectionHeading(rb.steps, i)}
      <li class="step"><div class="step-head"><span class="n">${i + 1}</span><span class="name">${esc(s.name)}${s.owner ? ` <span class="owner">${esc(s.owner)}</span>` : ""}</span>
        <span class="meta">${[s.manual ? "manual" : s.tty ? "terminal" : "run", s.always && "always", s.confirm && "confirm", s.when && "conditional", s.continue_on_error && "continue on error", s.retries && `retries ${s.retries}`, s.timeout && `timeout ${s.timeout}`].filter(Boolean).join(" · ")}</span></div>
        <div class="step-body"><pre class="cmd">${esc(s.run || s.manual)}</pre></div></li>`).join("")}
    </ol>`;
  const form = $("#run-form");
  const syncYes = () => (form.yes.disabled = form.mode.value !== "unattended");
  form.addEventListener("change", syncYes);
  syncYes();
  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const vars = {};
    inputs.forEach((i) => (vars[i.name] = form.elements[i.name].value));
    try {
      const { id } = await api("POST", "/api/runs", {
        runbook: rb.name, vars, unattended: form.mode.value === "unattended", yes: form.yes.checked, dry_run: form.dry.checked, from: form.from.value,
      });
      location.hash = `#/runs/${id}`;
    } catch (e) { toast(e.message); }
  });
}

function renderEditor(el, raw, name, initialError, version, updatedAt) {
  el.innerHTML = `
    <div class="row" style="margin-bottom:8px">
      <button class="primary" id="save">Save</button>
      <button id="validate">Validate</button>
      ${version ? `<span class="muted">version ${version} · saved ${new Date(updatedAt).toLocaleString()}</span>` : ""}
      <span class="spacer"></span>
      ${name ? `<button class="danger" id="delete">Delete</button>` : ""}
    </div>
    <div id="validation" class="validation"></div>
    <textarea class="editor" id="yaml" spellcheck="false">${esc(raw)}</textarea>
    <details class="card" style="margin-top:12px"><summary>Format reference</summary><pre class="cmd">${esc(REFERENCE)}</pre></details>`;
  const ta = $("#yaml");
  const show = (res) => {
    const v = $("#validation");
    v.className = "validation " + (res.ok ? "ok" : "bad");
    v.textContent = res.ok ? "✔ valid" : res.error;
  };
  if (initialError) show({ ok: false, error: initialError });
  const validate = async () => show(await api("POST", "/api/validate", ta.value, true));
  let t;
  ta.addEventListener("input", () => { clearTimeout(t); t = setTimeout(validate, 400); });
  ta.addEventListener("keydown", (e) => {
    if (e.key === "Tab") {
      e.preventDefault();
      ta.setRangeText("  ", ta.selectionStart, ta.selectionEnd, "end");
    } else if ((e.metaKey || e.ctrlKey) && e.key === "s") {
      e.preventDefault();
      $("#save").click();
    }
  });
  $("#validate").onclick = validate;
  $("#save").onclick = async () => {
    try {
      const rb = name
        ? await api("PUT", `/api/runbooks/${encodeURIComponent(name)}`, ta.value, true, { "If-Match": String(version) })
        : await api("POST", "/api/runbooks", ta.value, true);
      toast("Saved");
      await loadRunbooks();
      location.hash = `#/runbooks/${encodeURIComponent(rb.name)}`;
    } catch (e) {
      show({ ok: false, error: /changed since/.test(e.message)
        ? `${e.message}\nSomeone saved a newer version. Copy your changes, reload, and re-apply them.`
        : e.message });
    }
  };
  if (name) $("#delete").onclick = async () => {
    if (!confirm(`Delete runbook "${name}"?`)) return;
    await api("DELETE", `/api/runbooks/${encodeURIComponent(name)}`);
    toast("Deleted");
    await loadRunbooks();
    location.hash = "#/";
  };
}

async function viewNew() {
  const { raw } = await api("GET", "/api/template?name=my-runbook");
  main.innerHTML = `<h1>New runbook</h1><p class="muted">Edit the starter below; the <code>name</code> field becomes the runbook's identifier.</p><section id="tab"></section>`;
  renderEditor($("#tab"), raw, null);
}

// ---------- live / past run ----------

async function viewRun(id) {
  const { run, live } = await api("GET", `/api/runs/${encodeURIComponent(id)}`);
  const state = { run, steps: run.steps.map((s) => ({ ...s, log: [] })), question: null, open: new Set() };
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
      state.steps[e.step].log.push({ stream: e.stream, text: e.text });
      break;
    case "step_finished":
      Object.assign(state.steps[e.step], { ...e.result, notes: state.steps[e.step].notes });
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
  }
}

function renderRun(state) {
  const { run } = state;
  const stopAt = state.steps.findIndex((s) => !["succeeded", "skipped", "forced"].includes(s.status));
  state.resumable = ["failed", "aborted"].includes(run.status) && stopAt >= 0 ? stopAt + 1 : 0;
  const scroll = {};
  main.querySelectorAll("pre.log").forEach((p) => (scroll[p.dataset.i] = p.scrollHeight - p.scrollTop - p.clientHeight < 8));
  const active = !["succeeded", "failed", "aborted"].includes(run.status);
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
      ${active ? `<button class="danger" id="cancel">Abort run</button>` : `${state.resumable ? `<button class="primary" id="resume">Resume from step ${state.resumable}</button>` : ""}<button id="rerun">Run again</button>`}
    </div>
    ${run.error ? `<p class="error-text">${esc(run.error)}</p>` : ""}
    ${inputs.length ? `<p class="muted">${inputs.map(([k, v]) => `<code>${esc(k)}=${esc(v)}</code>`).join(" ")}</p>` : ""}
    <div class="notes">${renderNotes(run.notes)}<button class="link" data-note="0">✎ Add run note</button></div>
    ${run.resumes ? `<p class="muted">Resumes <a href="#/runs/${esc(run.resumes)}">${esc(run.resumes)}</a></p>` : ""}
    ${state.question?.step === -1 ? renderQuestion(state.question, run.id) : ""}
    <ol class="steps">${state.steps.map((s, i) => `${sectionHeading(state.steps, i)}
      <li class="step ${state.question?.step === i ? "focus" : ""} ${s.note === "before start step" ? "dim" : ""}">
        <div class="step-head" data-toggle="${i}">
          <span class="icon s-${s.status}">${ICONS[s.status] || ""}</span>
          <span class="n">${i + 1}</span>
          <span class="name">${esc(s.name)} <span class="kind">${esc(s.kind)}</span>${s.owner ? ` <span class="owner">${esc(s.owner)}</span>` : ""}</span>
          <span class="meta">${[s.note, s.attempts > 1 && `${s.attempts} attempts`, s.exit_code != null && `exit ${s.exit_code}`, duration(s.started, s.ended)].filter(Boolean).map(esc).join(" · ")}</span>
        </div>
        ${state.question?.step === i ? `<div class="step-body">${renderQuestion(state.question, run.id)}</div>` : ""}
        ${s.notes?.length ? `<div class="step-body">${renderNotes(s.notes)}</div>` : ""}
        ${state.open.has(i) ? `<div class="step-body">
          ${s.command ? `<pre class="cmd">${esc(s.command)}</pre>` : ""}
          ${s.log.length ? `<pre class="log" data-i="${i}">${s.log.map((l) => `<span class="${l.stream}">${esc(l.text)}</span>`).join("\n")}</pre>` : ""}
          ${s.error && s.status !== "succeeded" ? `<div class="error-text">${esc(s.error)}</div>` : ""}
          <button class="link" data-note="${i + 1}">✎ Add note</button>
        </div>` : ""}
      </li>`).join("")}
    </ol>`;

  main.querySelectorAll("pre.log").forEach((p) => { if (scroll[p.dataset.i] !== false) p.scrollTop = p.scrollHeight; });
  main.querySelectorAll("[data-note]").forEach((b) => (b.onclick = (ev) => {
    ev.stopPropagation();
    addNote(run.id, +b.dataset.note, state);
  }));
  main.querySelectorAll("[data-toggle]").forEach((h) => (h.onclick = () => {
    const i = +h.dataset.toggle;
    state.open.has(i) ? state.open.delete(i) : state.open.add(i);
    renderRun(state);
  }));
  $("#cancel") && ($("#cancel").onclick = () => api("POST", `/api/runs/${run.id}/cancel`).catch((e) => toast(e.message)));
  $("#resume") && ($("#resume").onclick = async () => {
    try {
      const { id } = await api("POST", `/api/runs/${run.id}/resume`, {});
      location.hash = `#/runs/${id}`;
    } catch (e) { toast(e.message); }
  });
  $("#rerun") && ($("#rerun").onclick = () => (location.hash = `#/runbooks/${encodeURIComponent(run.runbook)}`));
  const q = $(".question");
  if (q) bindQuestion(q, state.question, run.id);
}

const QLABELS = { run: "▶ Run", skip: "Skip", abort: "Abort run", done: "✔ Done", fail: "✘ Failed", retry: "↻ Retry", force: "⚑ Force success…" };

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
    body += q.options.map((o, idx) => `<button data-v="${esc(o)}" class="${idx === 0 ? "primary" : o === "abort" ? "danger" : ""}">${QLABELS[o] || esc(o)}</button>`).join("");
  }
  const text = q.type === "confirm" ? `<pre class="cmd">${esc(q.text)}</pre>` : q.text ? `<div class="text">${esc(q.text)}</div>` : "";
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
    if (value === "fail" && !note && q.type === "manual") {
      const reason = prompt("Why did this step fail?") ?? "";
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
      echo "region=us-east-1" >> "$RUNNR_OUTPUT"  # {{.region}} / $region later
      echo "pw=..." >> "$RUNNR_SECRET"            # same, masked
  - name: Needs a terminal
    run: terraform apply
    tty: true                # CLI: attached; GUI/unattended: becomes a manual step
    continue_on_error: true
    dir: /tmp
    env: { FOO: bar }
  - name: Human step
    manual: Check the dashboard for {{.target}} looks healthy.
    unattended: pass         # fail (default) | skip | pass when nobody is there

Template helpers: default, upper, lower, trim, contains, quote`;

loadRunbooks().then(route);
