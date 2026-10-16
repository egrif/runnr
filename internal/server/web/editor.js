"use strict";

// Runbook editor: YAML, or a structured form. The server converts between
// them (POST /api/parse, /api/format), so both views follow the same rules.

function renderEditor(el, raw, name, initialError, version, updatedAt) {
  const ed = { mode: "yaml", doc: null, open: new Set(), hasComments: /^\s*#/m.test(raw), name,
    test: { vars: {}, extra: {}, setup: true }, secretVars: {} };
  if (name) api("GET", `/api/runbooks/${enc(name)}/test-values`).then((tv) => { ed.test = tv; if (ed.mode === "struct") renderStruct(ed); }).catch(() => {});
  el.innerHTML = `
    <div class="row editor-bar">
      <button class="primary" id="save">Save</button>
      <button id="validate">Validate</button>
      <div class="seg"><button id="as-yaml" class="on">YAML</button><button id="as-struct">Structured</button></div>
      ${version ? `<span class="muted">version ${version} · saved ${new Date(updatedAt).toLocaleString()}</span>` : ""}
      <span class="spacer"></span>
      ${name ? `<button class="danger" id="delete">Delete</button>` : ""}
    </div>
    <div id="validation" class="validation"></div>
    <div id="yaml-pane">
      <textarea class="editor" id="yaml" spellcheck="false">${esc(raw)}</textarea>
      <details class="card" style="margin-top:12px"><summary>Format reference</summary><pre class="cmd">${esc(REFERENCE)}</pre></details>
    </div>
    <div id="struct-pane" hidden></div>`;
  const ta = $("#yaml");
  const show = (res) => {
    const v = $("#validation");
    v.className = "validation " + (res.ok ? "ok" : "bad");
    v.textContent = res.ok ? "✔ valid" : res.error;
  };
  if (initialError) show({ ok: false, error: initialError });

  // currentYAML returns the YAML for whichever view is showing.
  const currentYAML = async () => {
    if (ed.mode === "yaml") return { ok: true, raw: ta.value };
    return api("POST", "/api/format", cleanDoc(ed.doc));
  };
  const validate = async () => {
    const y = await currentYAML();
    show(y.ok ? await api("POST", "/api/validate", y.raw, true) : y);
  };
  const setMode = async (mode) => {
    if (mode === ed.mode) return;
    if (mode === "struct") {
      const res = await api("POST", "/api/parse", ta.value, true);
      if (!res.ok) return show({ ok: false, error: `Fix the YAML before switching:\n${res.error}` });
      ed.doc = res.runbook;
      renderStruct(ed);
    } else {
      const res = await api("POST", "/api/format", cleanDoc(ed.doc));
      if (!res.ok) return show(res);
      ta.value = res.raw;
    }
    ed.mode = mode;
    $("#yaml-pane").hidden = mode !== "yaml";
    $("#struct-pane").hidden = mode !== "struct";
    $("#as-yaml").classList.toggle("on", mode === "yaml");
    $("#as-struct").classList.toggle("on", mode === "struct");
    show({ ok: true });
  };
  $("#as-yaml").onclick = () => setMode("yaml").catch((e) => toast(e.message));
  $("#as-struct").onclick = () => setMode("struct").catch((e) => toast(e.message));

  let t;
  ta.addEventListener("input", () => { clearTimeout(t); t = setTimeout(validate, 400); });
  ta.addEventListener("keydown", (e) => {
    if (e.key === "Tab") {
      e.preventDefault();
      ta.setRangeText("  ", ta.selectionStart, ta.selectionEnd, "end");
    }
  });
  $("#struct-pane").addEventListener("input", () => { clearTimeout(t); t = setTimeout(validate, 600); });
  el.addEventListener("keydown", (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key === "s") { e.preventDefault(); $("#save").click(); }
  });
  $("#validate").onclick = validate;
  $("#save").onclick = async () => {
    try {
      const y = await currentYAML();
      if (!y.ok) return show(y);
      if (ed.mode === "struct" && ed.hasComments &&
        !confirm("Saving from the structured view rewrites the YAML, so its comments will be dropped. Save anyway?")) return;
      const rb = name
        ? await api("PUT", `/api/runbooks/${enc(name)}`, y.raw, true, { "If-Match": String(version) })
        : await api("POST", "/api/runbooks", y.raw, true);
      toast("Saved");
      await loadRunbooks();
      location.hash = `#/runbooks/${enc(rb.name)}`;
    } catch (e) {
      show({ ok: false, error: /changed since/.test(e.message)
        ? `${e.message}\nSomeone saved a newer version. Copy your changes, reload, and re-apply them.`
        : e.message });
    }
  };
  if (name) $("#delete").onclick = async () => {
    if (!confirm(`Delete runbook "${name}"?`)) return;
    await api("DELETE", `/api/runbooks/${enc(name)}`);
    toast("Deleted");
    await loadRunbooks();
    location.hash = "#/";
  };
}

// ---------- structured view ----------

const H = FIELD_HELP;

function cleanDoc(doc) {
  // Drop empty values so they don't show up in the YAML.
  const clean = (o) => {
    if (Array.isArray(o)) return o.map(clean);
    if (o && typeof o === "object") {
      const out = {};
      for (const [k, v] of Object.entries(o)) {
        const c = clean(v);
        if (c === "" || c === false || c === 0 || c == null || (Array.isArray(c) && !c.length && k !== "steps") || (typeof c === "object" && !Array.isArray(c) && !Object.keys(c).length)) continue;
        out[k] = c;
      }
      return out;
    }
    return o;
  };
  return clean(doc);
}

function getPath(obj, path) { return path.split(".").reduce((o, k) => (o == null ? o : o[k]), obj); }
function setPath(obj, path, v) {
  const ks = path.split(".");
  const last = ks.pop();
  const parent = ks.reduce((o, k) => (o[k] ??= {}), obj);
  if (v === undefined) delete parent[last]; else parent[last] = v;
}

// control renders an input for a field. Kinds: text (default), area, code,
// int, bool, list (comma separated), kv (KEY=value per line), select.
function control(path, kind, val, opts = {}) {
  const p = esc(path);
  const ph = opts.placeholder ? ` placeholder="${esc(opts.placeholder)}"` : "";
  switch (kind) {
    case "area": return `<textarea data-path="${p}" rows="${opts.rows || 2}"${ph}>${esc(val || "")}</textarea>`;
    case "code": return `<textarea class="code" data-path="${p}" rows="${opts.rows || Math.min(24, Math.max(6, String(val || "").split("\n").length + 2))}" spellcheck="false"${ph}>${esc(val || "")}</textarea>`;
    case "int": return `<input type="number" min="0" data-path="${p}" data-kind="int" value="${esc(val ?? "")}"${ph}>`;
    case "bool": return `<input type="checkbox" data-path="${p}" ${val ? "checked" : ""}>`;
    case "list": return `<input data-path="${p}" data-kind="list" value="${esc((val || []).join(", "))}"${ph}>`;
    case "kv": return `<textarea data-path="${p}" data-kind="kv" rows="2" spellcheck="false"${ph}>${esc(Object.entries(val || {}).map(([k, v]) => `${k}=${v}`).join("\n"))}</textarea>`;
    case "select": return `<select data-path="${p}">${opts.choices.map(([v, label]) => `<option value="${esc(v)}" ${v === (val || "") ? "selected" : ""}>${esc(label)}</option>`).join("")}</select>`;
  }
  return `<input data-path="${p}" value="${esc(val ?? "")}"${ph}>`;
}

// row is a label (with its ⓘ) beside its input; rows in a group align.
function row(label, help, ctl) {
  return `<label class="frow"><span class="flabel">${esc(label)}${helpIcon(help)}</span>${ctl}</label>`;
}

// group is a set of rows; collapsible groups start open if they hold values.
function group(title, rows, { collapsible = false, open = false, id = "" } = {}) {
  const body = `<div class="fgroup-rows">${rows.filter(Boolean).join("")}</div>`;
  if (!collapsible) return `<div class="fgroup"><div class="fgroup-title">${esc(title)}</div>${body}</div>`;
  return `<details class="fgroup" data-group="${esc(id)}" ${open ? "open" : ""}><summary class="fgroup-title">${esc(title)}</summary>${body}</details>`;
}

function readField(el) {
  const v = el.type === "checkbox" ? el.checked : el.value;
  switch (el.dataset.kind) {
    case "int": return v === "" ? undefined : Math.max(0, parseInt(v, 10) || 0);
    case "list": return v.split(",").map((x) => x.trim()).filter(Boolean);
    case "kv": {
      const o = {};
      v.split("\n").forEach((l) => { const i = l.indexOf("="); if (i > 0) o[l.slice(0, i).trim()] = l.slice(i + 1); });
      return o;
    }
  }
  return v === "" ? undefined : v;
}

const hasAny = (o, keys) => keys.some((k) => { const v = o[k]; return v !== undefined && v !== "" && !(Array.isArray(v) && !v.length) && !(v && typeof v === "object" && !Array.isArray(v) && !Object.keys(v).length); });

function kindOf(s) {
  return s.manual !== undefined && s.run === undefined ? "manual" : s.checks ? "checks" : s.alternatives ? "alternatives" : "run";
}

// stepLeft is the main body of a step: its command, instructions, checks
// or alternatives.
function stepLeft(s, i) {
  const kind = kindOf(s);
  const P = `steps.${i}`;
  const seg = `<div class="seg kind-seg">${[["run", "Command"], ["manual", "Manual"], ["checks", "Checks"], ["alternatives", "Alternatives"]].map(([k, l]) =>
    `<button data-act="kind" data-kind="${k}" data-i="${i}" class="${kind === k ? "on" : ""}">${l}</button>`).join("")}${helpIcon(H.step.kind)}</div>`;
  let body = "";
  if (kind === "run") body = `<div class="flabel">Command${helpIcon(H.step.run)}</div>${control(`${P}.run`, "code", s.run)}`;
  if (kind === "manual") body = `<div class="flabel">Instructions${helpIcon(H.step.manual)}</div>${control(`${P}.manual`, "code", s.manual)}`;
  if (kind === "checks") {
    body = `<div class="flabel">Checks${helpIcon(H.step.checks)}</div>${(s.checks || []).map((c, j) => `
      <div class="sub-card">
        <div class="row">${control(`${P}.checks.${j}.name`, "text", c.name, { placeholder: "what it confirms" })}${helpIcon(H.check.name)}
          <button class="icon-btn danger" data-act="del-check" data-i="${i}" data-j="${j}" title="Remove check">✕</button></div>
        ${control(`${P}.checks.${j}.run`, "code", c.run, { rows: 3, placeholder: "command that exits 0 when it passes" })}
      </div>`).join("")}<button data-act="add-check" data-i="${i}">+ Add check</button>`;
  }
  if (kind === "alternatives") {
    body = `<div class="flabel">Alternatives${helpIcon(H.step.alternatives)}</div>${(s.alternatives || []).map((a, j) => {
      const A = `${P}.alternatives.${j}`;
      const withSteps = !!a.steps;
      return `<div class="sub-card">
        <div class="row">${control(`${A}.name`, "text", a.name, { placeholder: "name" })}
          <button class="icon-btn danger" data-act="del-alt" data-i="${i}" data-j="${j}" title="Remove alternative">✕</button></div>
        <div class="fgroup-rows">${row("Only when", H.alternative.when, control(`${A}.when`, "text", a.when, { placeholder: "optional, for unattended runs" }))}</div>
        <div class="seg small-seg"><button data-act="alt-kind" data-i="${i}" data-j="${j}" data-kind="run" class="${withSteps ? "" : "on"}">Command</button><button data-act="alt-kind" data-i="${i}" data-j="${j}" data-kind="steps" class="${withSteps ? "on" : ""}">Steps</button>${helpIcon(H.alternative.steps)}</div>
        ${withSteps ? `${(a.steps || []).map((st, k) => {
          const S = `${A}.steps.${k}`;
          const manual = st.manual !== undefined && st.run === undefined;
          return `<div class="mini-step">
            <div class="row"><span class="n">${k + 1}</span>${control(`${S}.name`, "text", st.name, { placeholder: "step name" })}
              <select data-act-change="sub-kind" data-i="${i}" data-j="${j}" data-k="${k}"><option value="run" ${manual ? "" : "selected"}>command</option><option value="manual" ${manual ? "selected" : ""}>manual</option></select>
              <label class="small">${control(`${S}.confirm`, "bool", st.confirm)} confirm</label>
              <label class="small">${control(`${S}.tty`, "bool", st.tty)} terminal</label>
              <button class="icon-btn danger" data-act="del-sub" data-i="${i}" data-j="${j}" data-k="${k}" title="Remove">✕</button></div>
            ${control(`${S}.${manual ? "manual" : "run"}`, "code", manual ? st.manual : st.run, { rows: 2 })}
          </div>`;
        }).join("")}<button data-act="add-sub" data-i="${i}" data-j="${j}">+ Add step</button>`
        : `${control(`${A}.run`, "code", a.run, { rows: 4 })}<label class="small">${control(`${A}.tty`, "bool", a.tty)} needs a terminal${helpIcon(H.alternative.tty)}</label>`}
      </div>`;
    }).join("")}<button data-act="add-alt" data-i="${i}">+ Add alternative</button>`;
  }
  return seg + body;
}

// Less-used step fields, under Advanced. [key, label, kind, help, applies(step kind), group]
const MORE_FIELDS = [
  ["always", "Always run", "bool", H.step.always, () => true, "Behaviour"],
  ["continue_on_error", "Keep going if it fails", "bool", H.step.continue_on_error, () => true, "Behaviour"],
  ["tty", "Needs a terminal", "bool", H.step.tty, (k) => k === "run", "Behaviour"],
  ["background", "Run in background", "bool", H.step.background, (k) => k === "run", "Behaviour"],
  ["when", "Only when", "text", H.step.when, () => true, "Conditions & timing"],
  ["timeout", "Timeout", "text", H.step.timeout, (k) => k !== "manual", "Conditions & timing"],
  ["retries", "Retries", "int", H.step.retries, (k) => k === "run", "Conditions & timing"],
  ["retry_delay", "Retry delay", "text", H.step.retry_delay, (k) => k === "run", "Conditions & timing"],
  ["repeat", "Repeat until all pass", "repeat", H.step.repeat_every, (k) => k === "checks", "Conditions & timing"],
  ["dir", "Directory", "text", H.step.dir, (k) => k !== "manual", "Where it runs"],
  ["shell", "Shell", "text", H.step.step_shell, (k) => k !== "manual", "Where it runs"],
  ["env", "Environment", "kv", H.step.step_env, (k) => k !== "manual", "Where it runs"],
  ["outputs", "Publishes", "list", H.step.outputs, (k) => k === "run" || k === "alternatives", "Publishes to later steps"],
  ["secret_outputs", "Publishes (secret)", "list", H.step.secret_outputs, (k) => k === "run" || k === "alternatives", "Publishes to later steps"],
  ["capture", "Capture stdout as", "text", H.step.capture, (k) => k === "run", "Publishes to later steps"],
  ["id", "ID", "text", H.step.id, () => true, "Other"],
  ["unattended", "When unattended", "select", H.step.unattended, () => true, "Other"],
];

const isSet = (v) => v !== undefined && v !== "" && v !== false && !(Array.isArray(v) && !v.length) && !(v && typeof v === "object" && !Array.isArray(v) && !Object.keys(v).length);

// checkRow puts checkboxes side by side, in columns that line up.
function checkRow(boxes, cls = "") {
  return `<div class="check-grid">${boxes.map(([label, help, ctl, set]) =>
    `<label class="check ${cls} ${set ? "" : "unset"}">${ctl}<span>${esc(label)}</span>${helpIcon(help)}</label>`).join("")}</div>`;
}

// detailsPane: the common fields, then Advanced. Collapsed, Advanced still
// shows the fields that are set; open, it shows them all.
function detailsPane(ed, d, s, i) {
  const kind = kindOf(s);
  const P = `steps.${i}`;
  const later = d.steps.slice(i + 1).map((o, k) => [o.id || o.name, `${i + k + 2}. ${o.name}`]);
  const main = [
    row("Name", H.step.name, control(`${P}.name`, "text", s.name)),
    row("What we're doing", H.step.description, control(`${P}.description`, "area", s.description, { placeholder: "what this step does, and why" })),
    row("By the end", H.step.expect, control(`${P}.expect`, "area", s.expect, { placeholder: "how things should stand when it's done" })),
    row("Section", H.step.section, control(`${P}.section`, "text", s.section)),
    row("Owner", H.step.owner, control(`${P}.owner`, "text", s.owner)),
  ];
  const fields = MORE_FIELDS.filter(([, , , , applies]) => applies(kind));
  const open = ed.adv.has(i);
  const nSet = fields.filter(([k]) => isSet(s[k])).length;
  const groups = [...new Set(fields.map((f) => f[5]))].map((g) => {
    const fs = fields.filter((f) => f[5] === g);
    const bools = fs.filter((f) => f[2] === "bool");
    const others = fs.filter((f) => f[2] !== "bool");
    const rows = others.map(([k, label, fkind, help]) => {
      let ctl;
      if (fkind === "repeat") ctl = `<div class="row tight">${control(`${P}.repeat.every`, "text", s.repeat?.every, { placeholder: "every, e.g. 20s" })}${control(`${P}.repeat.timeout`, "text", s.repeat?.timeout, { placeholder: "give up after, e.g. 10m" })}</div>`;
      else if (fkind === "select") ctl = control(`${P}.${k}`, "select", s[k], { choices: [["", "fail (default)"], ["skip", "skip"], ["pass", "pass"]] });
      else ctl = control(`${P}.${k}`, fkind, s[k]);
      return `<label class="frow ${isSet(s[k]) ? "" : "unset"}"><span class="flabel">${esc(label)}${helpIcon(help)}</span>${ctl}</label>`;
    });
    if (g === "Behaviour" && s.background) {
      rows.push(`<label class="frow">${`<span class="flabel">Stop after${helpIcon(H.step.stop_after)}</span>`}${control(`${P}.stop_after`, "select", s.stop_after, { choices: [["", "when the run ends"], ...later] })}</label>`);
    }
    const anySet = fs.some(([k]) => isSet(s[k]));
    return `<div class="adv-title ${anySet ? "" : "unset"}">${esc(g)}</div>
      ${bools.length ? checkRow(bools.map(([k, label, , help]) => [label, help, control(`${P}.${k}`, "bool", s[k]), isSet(s[k])])) : ""}
      ${rows.join("")}`;
  });
  // One grid, so every input lines up, Advanced included.
  return `<div class="fgroup-rows ${open ? "" : "adv-closed"}">${main.join("")}
    ${checkRow([["Confirm first", H.step.confirm, control(`${P}.confirm`, "bool", s.confirm), true]])}
    <button class="link adv-toggle" data-act="adv" data-i="${i}">${open ? "▾" : "▸"} Advanced${!open && nSet ? ` <span class="muted">(${nSet} set)</span>` : ""}</button>
    ${groups.join("")}</div>`;
}

// The Run this pane: runs just this step (with the edits as they are now),
// optionally after the setup steps, with the runbook's test values.
function runPane(ed, i) {
  const r = ed.runs.get(i);
  if (!r) return "";
  const lines = r.lines.map((l) => `<span class="${esc(l.stream)}">${esc(l.text)}</span>`).join("\n");
  return `<div class="run-pane">
    <div class="row run-pane-head"><b>Run this</b>
      <span class="badge s-${esc(r.status)}">${ICONS[r.status] || ""} ${esc(r.status)}</span>
      <span class="spacer"></span>
      ${r.status === "running" ? `<button class="link danger" data-act="stop-run" data-i="${i}">Stop</button>` : `<button class="link" data-act="run-this" data-i="${i}">Run again</button>`}
      <button class="link" data-act="close-run" data-i="${i}">Close</button></div>
    ${r.error ? `<div class="error-text">${esc(r.error)}</div>` : ""}
    ${r.question ? renderQuestion(r.question, r.id) : ""}
    <pre class="log run-log">${lines || '<span class="info">starting…</span>'}</pre>
  </div>`;
}

async function runThis(ed, i) {
  const old = ed.runs.get(i);
  old?.es?.close();
  const y = await api("POST", "/api/format", cleanDoc(ed.doc));
  const r = { id: null, lines: [], status: "running", question: null, error: y.ok ? "" : y.error };
  ed.runs.set(i, r);
  if (!y.ok) { r.status = "failed"; return renderStruct(ed); }
  try {
    // Inputs without a test value use their default (blank secrets are asked for).
    const vars = {};
    for (const inp of ed.doc.inputs || []) {
      const v = (inp.secret ? ed.secretVars : ed.test.vars)[inp.name];
      if (v || !inp.secret) vars[inp.name] = v || "";
    }
    const res = await api("POST", "/api/test-run", {
      raw: y.raw, step: i + 1, vars, extra: ed.test.extra, setup: ed.test.setup,
    });
    r.id = res.id;
  } catch (e) { r.status = "failed"; r.error = e.message; return renderStruct(ed); }
  renderStruct(ed);
  r.es = new EventSource(`/api/runs/${enc(r.id)}/events`);
  r.es.onmessage = (msg) => {
    const e = JSON.parse(msg.data);
    if (e.kind.startsWith("term")) { termEvent(r.id, e); return; }
    const name = ed.doc.steps[e.step]?.name;
    const tag = e.step >= 0 && e.step !== i ? `[setup: ${name}] ` : "";
    switch (e.kind) {
      case "output": r.lines.push({ stream: e.stream, text: tag + e.text }); break;
      case "step_started": if (e.step !== i) r.lines.push({ stream: "info", text: `── setup: ${name}` }); break;
      case "step_finished":
        if (e.result.status !== "skipped" || e.step === i) r.lines.push({ stream: "info", text: `${tag}${ICONS[e.result.status] || ""} ${e.result.status}${e.result.note ? " — " + e.result.note : ""}${e.result.error ? " — " + e.result.error : ""}` });
        break;
      case "question": r.question = e.question; break;
      case "answered": if (r.question?.id === e.answered) r.question = null; break;
      case "run_finished":
        r.status = e.run.status;
        if (e.run.error) r.error = e.run.error;
        r.question = null;
        r.es.close();
        break;
    }
    updateRunPane(ed, i);
  };
}

// updateRunPane redraws just the run pane, so typing elsewhere isn't disturbed.
function updateRunPane(ed, i) {
  const el = $(`#struct-pane .run-slot[data-i="${i}"]`);
  if (!el) return;
  el.innerHTML = runPane(ed, i);
  const log = el.querySelector(".run-log");
  if (log) log.scrollTop = log.scrollHeight;
  const q = el.querySelector(".question");
  if (q) bindQuestion(q, ed.runs.get(i).question, ed.runs.get(i).id);
}

// testValuesCard: inputs and extra variables used by Run this.
function testValuesCard(ed, d) {
  const t = ed.test;
  return `<details class="card test-values" ${ed.testOpen ? "open" : ""}><summary><b>Test values</b> <span class="muted small">used by “Run this”${ed.name ? "" : " (save the runbook to keep them)"}</span>${helpIcon("Inputs and extra variables for trying steps out while editing. Saved with this runbook, except secret inputs, which are kept only in this browser tab.")}</summary>
    <div class="fgroup-rows">
      ${(d.inputs || []).filter((x) => x.name).map((x) => row(x.name + (x.secret ? " (secret)" : ""), x.description || "",
        `<input data-test-var="${esc(x.name)}" ${x.secret ? 'type="password"' : ""} value="${esc((x.secret ? ed.secretVars : t.vars)[x.name] ?? "")}" placeholder="${esc(x.default || "")}">`)).join("")}
      ${row("Extra variables", "Variables an earlier step would normally publish (KEY=value per line), so a step can run on its own.",
        `<textarea data-test-extra rows="3" spellcheck="false" placeholder="region=use1">${esc(Object.entries(t.extra || {}).map(([k, v]) => `${k}=${v}`).join("\n"))}</textarea>`)}
      ${row("Run setup first", "Run the steps marked “always” before the step, to build its environment.", `<input type="checkbox" data-test-setup ${t.setup ? "checked" : ""}>`)}
    </div>
    ${ed.name ? `<button data-act="save-test">Save test values</button>` : ""}
  </details>`;
}

function sectionsGroup(d) {
  const names = [...new Set(d.steps.map((s) => s.section).filter(Boolean))];
  if (!names.length) return "";
  const info = (n) => (d.sections || []).find((x) => x.name === n) || {};
  return group("Sections", names.map((n) => `<div class="sec-edit" style="grid-column: 1 / -1">
      <div class="flabel">${esc(n)}</div>
      <div class="fgroup-rows">
        ${row("What it does", H.section.description, `<textarea rows="2" data-sec="${esc(n)}" data-sec-field="description">${esc(info(n).description || "")}</textarea>`)}
        ${row("By the end", H.section.expect, `<textarea rows="2" data-sec="${esc(n)}" data-sec-field="expect">${esc(info(n).expect || "")}</textarea>`)}
      </div></div>`), { collapsible: true, id: "rb-sections", open: (d.sections || []).length > 0 });
}

function renderStruct(ed) {
  const d = ed.doc;
  d.inputs ||= [];
  d.steps ||= [];
  ed.adv ||= new Set();
  ed.runs ||= new Map();
  ed.widths ||= [null, null]; // px once dragged; proportional until then
  const pane = $("#struct-pane");
  pane.innerHTML = `
    <div class="card">
      <h3>Runbook</h3>
      <div class="fgroup-rows">
        ${row("Name", H.runbook.name, control("name", "text", d.name))}
        ${row("What it's for", H.runbook.description, control("description", "area", d.description, { rows: 3 }))}
        ${row("By the end", H.runbook.expect, control("expect", "area", d.expect))}
        ${row("Tags", H.runbook.tags, control("tags", "list", d.tags, { placeholder: "ops, deploy" }))}
        ${row("Libraries", H.runbook.libraries, control("libraries", "list", d.libraries, { placeholder: "aurora-helpers" }))}
      </div>
      ${sectionsGroup(d)}
      ${group("Shell, environment & masking", [
        row("Shell", H.runbook.shell, control("shell", "text", d.shell, { placeholder: "sh" })),
        row("Environment", H.runbook.env, control("env", "kv", d.env, { placeholder: "KEY=value" })),
        row("Profile", H.runbook.profile, control("profile", "list", d.profile, { placeholder: "~/.zshrc" })),
        row("Sensitive patterns", H.runbook.sensitive, control("sensitive", "kv", d.sensitive, { placeholder: "api_key=X-Api-Key: {{SENSITIVE}}" })),
      ], { collapsible: true, id: "rb-more", open: hasAny(d, ["env", "profile", "sensitive", "shell"]) })}
      ${group("Functions", [
        `<div class="fn-list" style="grid-column: 1 / -1">${Object.entries(d.functions || {}).map(([n, f]) => `
          <div class="sub-card">
            <div class="row"><input data-fn-rename="${esc(n)}" value="${esc(n)}" placeholder="name">
              <input data-path="functions.${esc(n)}.description" value="${esc(f.description || "")}" placeholder="what it does / usage">
              <button class="icon-btn danger" data-act="del-fn" data-fn="${esc(n)}" title="Remove">✕</button></div>
            ${control(`functions.${n}.body`, "code", f.body, { rows: 3 })}
          </div>`).join("")}<button data-act="add-fn">+ Add function</button></div>`,
      ], { collapsible: true, id: "rb-fns", open: Object.keys(d.functions || {}).length > 0 })}
    </div>
    <div class="card">
      <h3>Inputs${helpIcon(H.runbook.inputs)}</h3>
      <table class="inputs"><thead><tr>
        <th>Name${helpIcon(H.input.name)}</th><th>Description${helpIcon(H.input.description)}</th><th>Default${helpIcon(H.input.default)}</th>
        <th>Choices${helpIcon(H.input.choices)}</th><th>Required${helpIcon(H.input.required)}</th><th>Secret${helpIcon(H.input.secret)}</th><th></th></tr></thead><tbody>
      ${d.inputs.map((inp, i) => `<tr>
        <td>${control(`inputs.${i}.name`, "text", inp.name)}</td>
        <td>${control(`inputs.${i}.description`, "text", inp.description)}</td>
        <td>${control(`inputs.${i}.default`, "text", inp.default)}</td>
        <td>${control(`inputs.${i}.choices`, "list", inp.choices, { placeholder: "a, b" })}</td>
        <td>${control(`inputs.${i}.required`, "bool", inp.required)}</td>
        <td>${control(`inputs.${i}.secret`, "bool", inp.secret)}</td>
        <td><button class="link" data-act="del-input" data-i="${i}">remove</button></td></tr>`).join("")}
      </tbody></table>
      <button data-act="add-input">+ Add input</button>
    </div>
    ${testValuesCard(ed, d)}
    <h3>Steps${helpIcon(H.runbook.steps)}</h3>
    <ol class="steps struct-steps">${d.steps.map((s, i) => `${sectionHeading(d.steps, i, d.sections)}<li class="step">
        <div class="step-head" data-act="open" data-i="${i}">
          <span class="n">${i + 1}</span><span class="name">${esc(s.name || "(unnamed)")} ${stepBadges(s)}</span>
          <span class="spacer"></span>
          <button class="icon-btn" data-act="run-this" data-i="${i}" title="Run just this step with the test values">▶ Run this</button>
          <button class="icon-btn" data-act="up" data-i="${i}" title="Move up" ${i === 0 ? "disabled" : ""}>↑</button>
          <button class="icon-btn" data-act="down" data-i="${i}" title="Move down" ${i === d.steps.length - 1 ? "disabled" : ""}>↓</button>
          <button class="icon-btn" data-act="dup" data-i="${i}" title="Duplicate">⧉</button>
          <button class="icon-btn danger" data-act="del" data-i="${i}" title="Delete">✕</button>
        </div>
        ${ed.open.has(i) ? `<div class="step-body step-edit">
          <div class="se-details" style="width:${ed.widths[0] ? ed.widths[0] + "px" : ed.runs.has(i) ? "30%" : "38%"}">${detailsPane(ed, d, s, i)}</div>
          <div class="split" data-split="0" title="Drag to resize"></div>
          <div class="se-command">${stepLeft(s, i)}</div>
          ${ed.runs.has(i) ? `<div class="split" data-split="1" title="Drag to resize"></div>
            <div class="se-run run-slot" data-i="${i}" style="width:${ed.widths[1] ? ed.widths[1] + "px" : "34%"}">${runPane(ed, i)}</div>` : ""}
        </div>` : ""}
      </li>`).join("")}</ol>
    <button data-act="add-step">+ Add step</button>`;

  pane.querySelectorAll(".run-slot").forEach((el) => {
    const log = el.querySelector(".run-log");
    if (log) log.scrollTop = log.scrollHeight;
    const q = el.querySelector(".question");
    const r = ed.runs.get(+el.dataset.i);
    if (q && r?.question) bindQuestion(q, r.question, r.id);
  });
  const tv = pane.querySelector("details.test-values");
  if (tv) tv.ontoggle = () => (ed.testOpen = tv.open);

  // Draggable dividers: the first sizes the details, the second the run pane.
  pane.querySelectorAll("[data-split]").forEach((h) => (h.onmousedown = (e) => {
    e.preventDefault();
    const which = +h.dataset.split;
    const target = which === 0 ? h.previousElementSibling : h.nextElementSibling;
    const start = e.clientX, w0 = target.offsetWidth;
    const move = (ev) => {
      const w = Math.max(200, Math.min(h.parentElement.offsetWidth * 0.6, w0 + (which === 0 ? ev.clientX - start : start - ev.clientX)));
      target.style.width = w + "px";
      ed.widths[which] = w;
    };
    const up = () => { removeEventListener("mousemove", move); removeEventListener("mouseup", up); document.body.classList.remove("dragging"); };
    addEventListener("mousemove", move);
    addEventListener("mouseup", up);
    document.body.classList.add("dragging");
  }));

  pane.oninput = pane.onchange = (e) => {
    const t = e.target;
    if (t.matches("[data-test-var]")) {
      const n = t.dataset.testVar;
      const secret = (d.inputs.find((x) => x.name === n) || {}).secret;
      (secret ? ed.secretVars : ed.test.vars)[n] = t.value;
      return;
    }
    if (t.matches("[data-test-extra]")) {
      ed.test.extra = {};
      t.value.split("\n").forEach((l) => { const at = l.indexOf("="); if (at > 0) ed.test.extra[l.slice(0, at).trim()] = l.slice(at + 1); });
      return;
    }
    if (t.matches("[data-test-setup]")) { ed.test.setup = t.checked; return; }
    if (t.matches("[data-sec]")) {
      d.sections ||= [];
      let sec = d.sections.find((x) => x.name === t.dataset.sec);
      if (!sec) d.sections.push((sec = { name: t.dataset.sec }));
      sec[t.dataset.secField] = t.value || undefined;
      d.sections = d.sections.filter((x) => x.description || x.expect);
      return;
    }
    const rn = t.closest("[data-fn-rename]");
    if (rn) {
      if (e.type !== "change") return;
      const old = rn.dataset.fnRename, nw = rn.value.trim();
      if (nw && nw !== old && !d.functions[nw]) {
        d.functions = Object.fromEntries(Object.entries(d.functions).map(([k2, v]) => [k2 === old ? nw : k2, v]));
      }
      return renderStruct(ed);
    }
    const k = t.closest("[data-act-change]");
    if (k && e.type === "change") {
      const sub = d.steps[+k.dataset.i].alternatives[+k.dataset.j].steps[+k.dataset.k];
      const text = sub.run ?? sub.manual ?? "";
      delete sub.run; delete sub.manual;
      sub[k.value] = text;
      return renderStruct(ed);
    }
    const el = t.closest("[data-path]");
    if (!el) return;
    setPath(d, el.dataset.path, readField(el));
    // Structure-changing fields redraw the form.
    if (e.type === "change" && /\.(name|background|section|confirm|always|continue_on_error|tty)$/.test(el.dataset.path)) renderStruct(ed);
  };
  pane.onclick = (e) => {
    const b = e.target.closest("[data-act]");
    if (!b || b.tagName === "SELECT" || e.target.closest(".question")) return;
    e.preventDefault();
    e.stopPropagation();
    const i = +b.dataset.i, j = +b.dataset.j, k = +b.dataset.k;
    const s = d.steps[i];
    const swap = (a, c) => {
      [d.steps[a], d.steps[c]] = [d.steps[c], d.steps[a]];
      for (const m of [ed.runs]) {
        const va = m.get(a), vc = m.get(c);
        m.delete(a); m.delete(c);
        if (va) m.set(c, va);
        if (vc) m.set(a, vc);
      }
      for (const set of [ed.open, ed.adv]) {
        const oa = set.has(a), oc = set.has(c);
        set.delete(a); set.delete(c);
        if (oa) set.add(c);
        if (oc) set.add(a);
      }
    };
    switch (b.dataset.act) {
      case "open": toggle(ed.open, i); break;
      case "up": swap(i, i - 1); break;
      case "down": swap(i, i + 1); break;
      case "dup": d.steps.splice(i + 1, 0, structuredClone({ ...s, id: undefined, name: `${s.name} (copy)` })); break;
      case "del": if (!confirm(`Delete step ${i + 1} (${s.name})?`)) return; d.steps.splice(i, 1); ed.open.clear(); ed.runs.clear(); ed.adv.clear(); break;
      case "adv": toggle(ed.adv, i); break;
      case "run-this": ed.open.add(i); return runThis(ed, i);
      case "stop-run": { const r = ed.runs.get(i); if (r?.id) api("POST", `/api/runs/${enc(r.id)}/cancel`).catch(() => {}); return; }
      case "close-run": ed.runs.get(i)?.es?.close(); ed.runs.delete(i); break;
      case "save-test":
        api("PUT", `/api/runbooks/${enc(ed.name)}/test-values`, ed.test).then(() => toast("Test values saved")).catch((err) => toast(err.message));
        return;
      case "kind": {
        // Carry the text over where it makes sense.
        const text = s.run ?? s.manual ?? s.checks?.[0]?.run ?? s.alternatives?.[0]?.run ?? "";
        for (const f of ["run", "manual", "checks", "alternatives", "repeat"]) delete s[f];
        if (b.dataset.kind === "run") s.run = text;
        if (b.dataset.kind === "manual") s.manual = text;
        if (b.dataset.kind === "checks") s.checks = [{ name: "check 1", run: text }];
        if (b.dataset.kind === "alternatives") s.alternatives = [{ name: "option 1", run: text }, { name: "option 2", run: "" }];
        if (b.dataset.kind !== "run") { delete s.background; delete s.stop_after; delete s.tty; }
        break;
      }
      case "add-check": (s.checks ||= []).push({ name: `check ${s.checks.length + 1}`, run: "" }); break;
      case "del-check": s.checks.splice(j, 1); break;
      case "add-alt": (s.alternatives ||= []).push({ name: `option ${s.alternatives.length + 1}`, run: "" }); break;
      case "del-alt": s.alternatives.splice(j, 1); break;
      case "alt-kind": {
        const a = s.alternatives[j];
        if (b.dataset.kind === "steps") { a.steps = [{ name: "step 1", run: a.run || "" }]; delete a.run; delete a.tty; }
        else { a.run = a.steps?.[0]?.run || ""; delete a.steps; }
        break;
      }
      case "add-sub": s.alternatives[j].steps.push({ name: `step ${s.alternatives[j].steps.length + 1}`, run: "" }); break;
      case "del-sub": s.alternatives[j].steps.splice(k, 1); break;
      case "add-step":
        d.steps.push({ name: "New step", run: "echo hello", section: d.steps.at(-1)?.section });
        ed.open.add(d.steps.length - 1);
        break;
      case "add-input": d.inputs.push({ name: "" }); break;
      case "del-input": d.inputs.splice(i, 1); break;
      case "add-fn": {
        d.functions ||= {};
        let n = 1;
        while (d.functions[`fn${n}`]) n++;
        d.functions[`fn${n}`] = { description: "", body: "echo hello" };
        break;
      }
      case "del-fn": delete d.functions[b.dataset.fn]; break;
    }
    renderStruct(ed);
  };
}
