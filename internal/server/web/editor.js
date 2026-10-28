"use strict";

// Runbook editor: YAML, or a structured form. The server converts between
// them (POST /api/parse, /api/format), so both views follow the same rules.

function renderEditor(el, raw, name, initialError, version, updatedAt) {
  const ed = { mode: "yaml", doc: null, name, test: { vars: {}, extra: {}, setup: true }, secretVars: {},
    // Per-step state, keyed by the step itself so reordering can't mix it up.
    open: new Set(), adv: new Set(), runs: new Map(), wayTab: new WeakMap(), secOpen: new Set(), comments: new WeakMap() };
  if (name) api("GET", `/api/runbooks/${enc(name)}/test-values`).then((tv) => { ed.test = tv; if (ed.mode === "struct") renderStruct(ed); }).catch(() => {});
  el.innerHTML = `
    <div class="row editor-bar">
      <button class="primary" id="save">Save</button>
      <button id="validate">Validate</button>
      ${name ? `<button id="run-tests" title="Run this runbook's tests against what's in the editor, before saving">▶ Run tests</button>` : ""}
      <div class="seg"><button id="as-struct">Structured</button><button id="as-yaml" class="on">YAML</button></div>
      <button type="button" data-fnpick="#yaml" class="fn-pick-yaml" title="Insert a function at the cursor in the YAML (Ctrl+Shift+F)">ƒ Insert function</button>
      ${version ? `<span class="muted">version ${shortVer(version)} · saved ${new Date(updatedAt).toLocaleString()}</span>` : ""}
      <span class="spacer"></span>
      ${name ? `<button class="danger" id="delete">Delete</button>` : ""}
    </div>
    <div id="validation" class="validation"></div>
    <div id="draft-tests" class="draft-tests" hidden></div>
    <div id="yaml-pane">
      <textarea class="editor" id="yaml" spellcheck="false">${esc(raw)}</textarea>
      <details class="card" style="margin-top:12px"><summary>Format reference</summary><pre class="cmd">${esc(REFERENCE)}</pre></details>
    </div>
    <div id="struct-pane" hidden></div>`;
  const ta = $("#yaml");
  setFnContext(() => (ed.mode === "struct" ? ed.doc : ta.value));
  const show = (res) => {
    const v = $("#validation");
    if (!v) return; // a check that came back after the page was left
    v.className = "validation " + (res.ok ? "ok" : "bad");
    v.textContent = res.ok ? "✔ valid" : res.error;
  };
  if (initialError) show({ ok: false, error: initialError });

  // currentYAML returns the YAML for whichever view is showing.
  const currentYAML = async () => {
    if (ed.mode === "yaml") return { ok: true, raw: ta.value };
    return formatDoc(ed);
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
      ed.comments = new WeakMap();
      attachComments(ed, res.comments || {});
      renderStruct(ed);
    } else {
      const res = await formatDoc(ed);
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
  $("#run-tests") && ($("#run-tests").onclick = async () => {
    const y = await currentYAML();
    if (!y.ok) return show(y);
    runDraftTests($("#draft-tests"), name, y.raw);
  });
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
      const rb = name
        ? await api("PUT", `/api/runbooks/${enc(name)}`, y.raw, true, { "If-Match": String(version) })
        : await api("POST", `/api/runbooks${$("#new-col")?.value ? `?collection=${enc($("#new-col").value)}` : ""}`, y.raw, true);
      toast("Saved");
      await loadRunbooks();
      location.hash = `#/runbooks/${enc(rb.name)}`;
    } catch (e) {
      show({ ok: false, error: /changed since/.test(e.message)
        ? `${e.message}\nSomeone saved a newer version. Copy your changes, reload, and re-apply them.`
        : e.message });
    }
  };
  // Open in the structured view unless the YAML has to be fixed first.
  if (!initialError) setMode("struct").catch((e) => toast(e.message));
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

// ---------- comments ----------
// Comments from the YAML are kept with the object they're on (a step, a
// way, the runbook…) under the field name, or "#" for the item itself, so
// they move with it. They're turned back into YAML paths when formatting.

function attachComments(ed, comments) {
  const d = ed.doc;
  for (const [path, text] of Object.entries(comments)) {
    const segs = path ? path.split(".") : [];
    let obj = d, field = "#";
    if (segs.length) {
      const last = segs.at(-1);
      const parent = segs.length > 1 ? getPath(d, segs.slice(0, -1).join(".")) : d;
      if (/^\d+$/.test(last)) { obj = parent?.[+last]; field = "#"; } else { obj = parent; field = last; }
    }
    if (!obj || typeof obj !== "object") { obj = d; field = "#"; }
    setComment(ed, obj, field, [getComment(ed, obj, field), text].filter(Boolean).join("\n"));
  }
}

const getComment = (ed, obj, field) => (obj && ed.comments.get(obj)?.[field]) || "";
function setComment(ed, obj, field, text) {
  const c = ed.comments.get(obj) || {};
  if (text.trim()) c[field] = text.trim(); else delete c[field];
  ed.comments.set(obj, c);
}
const hasComments = (ed, obj) => Object.values(ed.comments.get(obj) || {}).some(Boolean);

// collectComments turns the comments back into YAML paths.
function collectComments(ed) {
  const out = {};
  const walk = (o, path) => {
    if (!o || typeof o !== "object") return;
    for (const [f, t] of Object.entries(ed.comments.get(o) || {})) {
      if (!t) continue;
      const key = f === "#" ? path : path ? `${path}.${f}` : f;
      out[key] = out[key] ? `${out[key]}\n${t}` : t;
    }
    if (Array.isArray(o)) o.forEach((v, i) => walk(v, path ? `${path}.${i}` : String(i)));
    else for (const [k, v] of Object.entries(o)) walk(v, path ? `${path}.${k}` : k);
  };
  walk(ed.doc, "");
  return out;
}

function formatDoc(ed) {
  // A section no step is in any more isn't written (its entry stays here,
  // in case a step moves back into it).
  const used = new Set(ed.doc.steps.map((s) => s.section).filter(Boolean));
  const doc = cleanDoc({ ...ed.doc, sections: (ed.doc.sections || []).filter((x) => used.has(x.name)) });
  const comments = collectComments(ed);
  // Comment paths for sections are by position: renumber them for the written list.
  const kept = (ed.doc.sections || []).map((x, i) => [i, used.has(x.name)]);
  const renum = {};
  for (const [path, text] of Object.entries(comments)) {
    const m = path.match(/^sections\.(\d+)(\..*)?$/);
    if (!m) { renum[path] = text; continue; }
    const k = kept.filter(([i, keep]) => keep && i < +m[1]).length;
    if (kept[+m[1]]?.[1]) renum[`sections.${k}${m[2] || ""}`] = text;
  }
  return api("POST", "/api/format", { runbook: doc, comments: renum });
}

// The editor being drawn, so row() can offer a comment on each field.
let curEd = null;

// cmtBtn is the 💬 beside a field: hover to read its comment, click to edit.
// target is [object, field], or a function returning one (for objects made
// on demand, like a section's description entry).
function cmtBtn(target) {
  const ed = curEd;
  if (!ed) return "";
  const [obj, field] = typeof target === "function" ? target(false) : target;
  const text = getComment(ed, obj, field);
  ed.cmtTargets.push(target);
  return `<button type="button" class="cmt ${text ? "has" : ""}" data-cmt="${ed.cmtTargets.length - 1}" ${text ? `data-tip="${esc(text)}"` : `title="Add a comment"`}>💬</button>`;
}

// cmtForPath finds the comment target for a control's data-path.
function cmtForPath(path) {
  const d = curEd?.doc;
  if (!d) return "";
  const segs = path.split(".");
  const field = segs.pop();
  const parent = segs.length ? getPath(d, segs.join(".")) : d;
  if (!parent || typeof parent !== "object" || /^\d+$/.test(field)) return "";
  // An empty field isn't written to the YAML, so there's nowhere to keep a comment.
  if (!isSet(parent[field]) && !getComment(curEd, parent, field)) return "";
  return cmtBtn([parent, field]);
}

function editComment(ed, target) {
  const [obj, field] = typeof target === "function" ? target(true) : target;
  const dlg = document.createElement("dialog");
  dlg.className = "edit-step cmt-dialog";
  dlg.innerHTML = `<h3>Comment</h3><p class="muted small">Saved as a YAML comment above ${field === "#" ? "this item" : `<code>${esc(field)}</code>`}.</p>
    <textarea rows="5">${esc(getComment(ed, obj, field))}</textarea>
    <div class="row"><button class="primary" data-ok>Save</button><button data-cancel>Cancel</button><span class="spacer"></span>
      ${getComment(ed, obj, field) ? `<button class="danger" data-rm>Delete</button>` : ""}</div>`;
  document.body.append(dlg);
  dlg.onclose = () => dlg.remove();
  const done = (text) => { setComment(ed, obj, field, text); dlg.close(); renderStruct(ed); $("#struct-pane").dispatchEvent(new Event("input")); };
  dlg.querySelector("[data-ok]").onclick = () => done(dlg.querySelector("textarea").value);
  dlg.querySelector("[data-cancel]").onclick = () => dlg.close();
  dlg.querySelector("[data-rm]")?.addEventListener("click", () => done(""));
  dlg.showModal();
  dlg.querySelector("textarea").focus();
}

// Each step gets a stable id for the page (the run pane is found by it).
const stepIds = new WeakMap();
let stepSeq = 0;
const uidOf = (s) => { if (!stepIds.has(s)) stepIds.set(s, ++stepSeq); return stepIds.get(s); };

function cleanDoc(doc) {
  // Drop empty values so they don't show up in the YAML.
  const clean = (o) => {
    if (Array.isArray(o)) return o.map(clean);
    if (o && typeof o === "object") {
      const out = {};
      for (const [k, v] of Object.entries(o)) {
        const c = clean(v);
        if (c === "" || c === false || c === 0 || c == null || (Array.isArray(c) && !c.length && k !== "steps") || (typeof c === "object" && !Array.isArray(c) && !Object.keys(c).length && k !== "maintenance_window")) continue;
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
    case "code": return `<div class="code-wrap"><textarea class="code" data-path="${p}" rows="${opts.rows || Math.min(24, Math.max(6, String(val || "").split("\n").length + 2))}" spellcheck="false"${ph}>${esc(val || "")}</textarea>${fnPickBtn()}</div>`;
    case "int": return `<input type="number" min="0" data-path="${p}" data-kind="int" value="${esc(val ?? "")}"${ph}>`;
    case "bool": return `<input type="checkbox" data-path="${p}" ${val ? "checked" : ""}>`;
    case "list": return `<input data-path="${p}" data-kind="list" value="${esc((val || []).join(", "))}"${ph}>`;
    case "kv": return `<textarea data-path="${p}" data-kind="kv" rows="2" spellcheck="false"${ph}>${esc(Object.entries(val || {}).map(([k, v]) => `${k}=${v}`).join("\n"))}</textarea>`;
    case "select": return `<select data-path="${p}">${opts.choices.map(([v, label]) => `<option value="${esc(v)}" ${v === (val || "") ? "selected" : ""}>${esc(label)}</option>`).join("")}</select>`;
  }
  return `<input data-path="${p}" value="${esc(val ?? "")}"${ph}>`;
}

// row is a label (with its ⓘ, and in the editor its 💬) beside its input;
// rows in a group align.
function row(label, help, ctl) {
  const path = curEd && (ctl.match(/data-path="([^"]+)"/) || [])[1];
  return `<label class="frow"><span class="flabel">${esc(label)}${helpIcon(help)}${path ? cmtForPath(path.replace(/&quot;/g, '"')) : ""}</span>${ctl}</label>`;
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

const WAY_KINDS = [["run", "Scripted"], ["manual", "Manual"], ["checks", "Checks"]];
const wayKind = (w) => (w.manual !== undefined && w.run === undefined ? "manual" : w.checks ? "checks" : "run");

function kindOf(s) {
  if (s.ways) return "ways";
  return wayKind(s);
}

// hasKind: the step (or one of its ways) is a script, manual or checks.
const hasKind = (s, k) => (s.ways ? s.ways.some((w) => wayKind(w) === k) : wayKind(s) === k);

// wayBody edits a script, instructions or checks; P is the path of the step
// (one way) or of one of its ways (w is its index).
function wayBody(o, P, kind, i, w) {
  const wa = w === undefined ? "" : ` data-w="${w}"`;
  if (kind === "run") return control(`${P}.run`, "code", o.run, w === undefined ? {} : { rows: 5 });
  if (kind === "manual") return control(`${P}.manual`, "code", o.manual, w === undefined ? {} : { rows: 4 });
  return `${(o.checks || []).map((c, j) => `
      <div class="sub-card">
        <div class="row">${control(`${P}.checks.${j}.name`, "text", c.name, { placeholder: "what it confirms" })}${helpIcon(H.check.name)}
          <button class="icon-btn danger" data-act="del-check" data-i="${i}" data-j="${j}"${wa} title="Remove check">✕</button></div>
        ${control(`${P}.checks.${j}.run`, "code", c.run, { rows: 3, placeholder: "command that exits 0 when it passes" })}
      </div>`).join("")}<button data-act="add-check" data-i="${i}"${wa}>+ Add check</button>`;
}

// stepLeft is the main body of a step: its script, instructions or checks,
// or its ways (as tabs) when there are several.
function stepLeft(ed, s, i) {
  const P = `steps.${i}`;
  const label = (o, kind) => {
    const [l, help] = { run: ["Script", H.step.run], manual: ["Instructions", H.step.manual], checks: ["Checks", H.step.checks] }[kind];
    const code = kind === "checks" ? (o.checks || []).map((c) => c.run || "").join("\n") : kind === "run" ? o.run : "";
    return `<div class="flabel">${l}${helpIcon(help)}${kind !== "checks" ? cmtBtn([o, kind]) : ""}${fnIcon(code, fnsOf(ed.doc))}</div>`;
  };
  if (!s.ways) {
    const kind = kindOf(s);
    const seg = `<div class="seg kind-seg">${WAY_KINDS.map(([k, l]) =>
      `<button data-act="kind" data-kind="${k}" data-i="${i}" class="${kind === k ? "on" : ""}">${l}</button>`).join("")}${helpIcon(H.step.kind)}</div>`;
    return `<div class="row way-bar">${seg}<span class="spacer"></span><button class="link" data-act="add-way" data-i="${i}">+ Add another way</button>${helpIcon(H.step.ways)}</div>
      ${label(s, kind)}${wayBody(s, P, kind, i)}`;
  }
  const pref = Math.max(0, s.ways.findIndex((w) => w.preferred));
  const tab = Math.min(ed.wayTab.get(s) ?? pref, s.ways.length - 1);
  const w = s.ways[tab], W = `${P}.ways.${tab}`, k = wayKind(w);
  return `<div class="way-tabs">
      ${s.ways.map((x, j) => `<button class="way-tab ${j === tab ? "on" : ""}" data-act="way-tab" data-i="${i}" data-j="${j}" title="${j === pref ? "Preferred: used unless the step asks which" : ""}">${esc(x.name || "(unnamed)")}${j === pref ? " ★" : ""}</button>`).join("")}
      <button class="way-tab add" data-act="add-way" data-i="${i}" title="${esc(H.step.ways)}">＋</button>
      <span class="spacer"></span>
      <label class="check">${control(`${P}.choose`, "bool", s.choose)}<span>Ask which way each time</span>${helpIcon(H.step.choose)}</label>
    </div>
    <div class="way-panel">
      <div class="row way-props">${control(`${W}.name`, "text", w.name, { placeholder: "name, e.g. Scripted" })}${cmtBtn([w, "#"])}
        <select data-way-kind data-i="${i}" data-j="${tab}">${WAY_KINDS.map(([v, l]) => `<option value="${v}" ${v === k ? "selected" : ""}>${l}</option>`).join("")}</select>
        <label class="check" title="${esc(H.way.preferred)}"><input type="checkbox" data-pref data-i="${i}" data-j="${tab}" ${tab === pref ? "checked disabled" : ""}><span>preferred</span></label>
        ${k === "run" ? `<label class="check">${control(`${W}.tty`, "bool", w.tty)}<span>terminal</span></label>` : ""}
        <span class="spacer"></span>
        <button class="icon-btn danger" data-act="del-way" data-i="${i}" data-j="${tab}" title="Remove this way">✕</button></div>
      ${label(w, k)}${wayBody(w, W, k, i, tab)}
    </div>`;
}

// uniqueWayName returns base, or base 2, base 3… not used by the step's ways.
function uniqueWayName(ways, base) {
  let n = 1, name = base;
  while (ways.some((w) => w.name === name)) name = `${base} ${++n}`;
  return name;
}

// sectionPick moves a step to another section (to the end of it), or
// starts a new section at this step.
function sectionPick(d, i) {
  const s = d.steps[i];
  return `<select data-sec-pick data-i="${i}" title="Move this step to another section">
    ${s.section ? "" : `<option value="" selected>(none)</option>`}
    ${sectionNames(d).map((n) => `<option value="${esc(n)}" ${n === s.section ? "selected" : ""}>${esc(n)}</option>`).join("")}
    <option value="__new__">+ New section from this step…</option></select>`;
}

// offsetPair is a planned-time field plus the input (if any) that overrides it.
function offsetPair(d, path, value, inputPath, input, placeholder) {
  const names = (d.inputs || []).map((x) => x.name).filter(Boolean);
  return `<span class="offset-pair">${control(path, "text", value, { placeholder })}
    <select data-path="${esc(inputPath)}" title="${esc(H.step.offset_input)}"><option value="">no override</option>
      ${names.map((n) => `<option value="${esc(n)}" ${n === input ? "selected" : ""}>input: ${esc(n)}</option>`).join("")}</select></span>`;
}

// plannedHint: what a step's planned time is without its own (from its section).
function plannedHint(d, i) {
  const p = plannedOffset(d.steps.map((s, k) => (k === i ? { ...s, window_offset: "" } : s)), d.sections, i);
  return p == null ? "e.g. -30m, 0, +12m" : `T${fmtOffset(p)} (from the section)`;
}

// sectionNames: the sections steps use, in order, then any only described.
function sectionNames(d) {
  return [...new Set([...d.steps.map((s) => s.section), ...(d.sections || []).map((x) => x.name)].filter(Boolean))];
}

// Less-used step fields, under Advanced. [key, label, kind, help, applies(step kind), group]
const MORE_FIELDS = [
  ["owner", "Owner", "text", H.step.owner, () => true, "Placement"],
  ["section", "Section", "section", H.step.section, () => true, "Placement"],
  ["window", "Window", "window", H.step.window, (k, s, d) => !!d?.maintenance_window, "Placement"],
  ["window_offset", "Planned at", "planned", H.step.window_offset, (k, s, d) => !!d?.maintenance_window, "Placement"],
  ["always", "Always run", "bool", H.step.always, () => true, "Behaviour"],
  ["optional", "Optional (not required)", "bool", H.step.optional, () => true, "Behaviour"],
  ["continue_on_error", "Keep going if it fails", "bool", H.step.continue_on_error, () => true, "Behaviour"],
  ["tty", "Needs a terminal", "bool", H.step.tty, (k) => k === "run", "Behaviour"],
  ["background", "Run in background", "bool", H.step.background, (k, s) => k === "run" || (s.ways?.length > 0 && s.ways.every((w) => w.run)), "Behaviour"],
  ["close_on_exit", "Close its terminal when done", "bool", H.step.close_on_exit, (k, s) => !!s.background, "Behaviour"],
  ["confirm_typing", "Confirm by typing", "text", H.step.confirm_typing, () => true, "Behaviour"],
  ["when", "Only when", "text", H.step.when, () => true, "Conditions & timing"],
  ["skip_if", "Skip if (command)", "text", H.step.skip_if, () => true, "Conditions & timing"],
  ["ok_if", "Output must match", "text", H.step.ok_if, (k, s) => hasKind(s, "run"), "Conditions & timing"],
  ["fail_if", "Fail if output matches", "text", H.step.fail_if, (k, s) => hasKind(s, "run"), "Conditions & timing"],
  ["verify", "Verify (on resume)", "text", H.step.verify, () => true, "Conditions & timing"],
  ["wait_for", "Wait for (background steps)", "list", H.step.wait_for, () => true, "Conditions & timing"],
  ["timeout", "Timeout", "text", H.step.timeout, (k, s) => hasKind(s, "run") || hasKind(s, "checks"), "Conditions & timing"],
  ["retries", "Retries", "int", H.step.retries, (k, s) => hasKind(s, "run"), "Conditions & timing"],
  ["retry_delay", "Retry delay", "text", H.step.retry_delay, (k, s) => hasKind(s, "run"), "Conditions & timing"],
  ["repeat", "Repeat until all pass", "repeat", H.step.repeat_every, (k, s) => hasKind(s, "checks"), "Conditions & timing"],
  ["retry_checks", "Retry checks", "retry", H.step.retry_checks, (k, s) => hasKind(s, "checks"), "Conditions & timing"],
  ["parallel_checks", "Run checks at once", "bool", H.step.parallel_checks, (k, s) => hasKind(s, "checks"), "Conditions & timing"],
  ["dir", "Directory", "text", H.step.dir, (k) => k !== "manual", "Where it runs"],
  ["shell", "Shell", "text", H.step.step_shell, (k) => k !== "manual", "Where it runs"],
  ["env", "Environment", "kv", H.step.step_env, (k) => k !== "manual", "Where it runs"],
  ["outputs", "Publishes", "list", H.step.outputs, (k, s) => hasKind(s, "run"), "Publishes to later steps"],
  ["secret_outputs", "Publishes (secret)", "list", H.step.secret_outputs, (k, s) => hasKind(s, "run"), "Publishes to later steps"],
  ["capture", "Capture stdout as", "text", H.step.capture, (k, s) => hasKind(s, "run"), "Publishes to later steps"],
  ["id", "ID", "text", H.step.id, () => true, "Other"],
  ["unattended", "When unattended", "select", H.step.unattended, () => true, "Other"],
];

// fieldSet: whether a step's field holds something other than its default
// (set ones show above the fold). A step's section is shown by where it
// sits in the outline, so it stays under Advanced.
function fieldSet(s, k) {
  if (k === "section") return false;
  if (k === "window_offset") return isSet(s.window_offset) || isSet(s.offset_input);
  return isSet(s[k]);
}

const isSet = (v) => v !== undefined && v !== "" && v !== false && !(Array.isArray(v) && !v.length) && !(v && typeof v === "object" && !Array.isArray(v) && !Object.keys(v).length);

// checkRow puts checkboxes side by side, in columns that line up.
function checkRow(boxes) {
  return `<div class="check-grid">${boxes.map(([label, help, ctl]) =>
    `<label class="check">${ctl}<span>${esc(label)}</span>${helpIcon(help)}</label>`).join("")}</div>`;
}

// detailsPane: the common fields and any advanced ones that are set, then
// Advanced, which opens to offer the rest.
function detailsPane(ed, d, s, i) {
  const kind = kindOf(s);
  const P = `steps.${i}`;
  const later = d.steps.slice(i + 1).map((o, k) => [o.id || o.name, `${i + k + 2}. ${o.name}`]);
  const fieldRow = ([k, label, fkind, help]) => {
    let ctl;
    if (fkind === "repeat") {
      // None (run once) until chosen; then how often and for how long.
      ctl = s.repeat
        ? `<div class="row tight">${control(`${P}.repeat.every`, "text", s.repeat.every, { placeholder: "every, e.g. 20s" })}${control(`${P}.repeat.timeout`, "text", s.repeat.timeout, { placeholder: "give up after, e.g. 10m" })}<button class="icon-btn small-x" data-act="repeat-off" data-i="${i}" title="Don't repeat (run once)">✕</button></div>`
        : `<select data-repeat-on data-i="${i}"><option value="">none (run once)</option><option value="on">every 20s, for up to 10m…</option></select>`;
    }
    else if (fkind === "section") ctl = sectionPick(d, i);
    else if (fkind === "window") ctl = control(`${P}.window`, "select", s.window, { choices: [["", "—"], ["open", "opens here (production goes down)"], ["close", "closes here (production is back)"]] });
    else if (fkind === "planned") ctl = offsetPair(d, `${P}.window_offset`, s.window_offset, `${P}.offset_input`, s.offset_input, plannedHint(d, i));
    else if (fkind === "select") ctl = control(`${P}.${k}`, "select", s[k], { choices: [["", "fail (default)"], ["skip", "skip"], ["pass", "pass"]] });
    else if (fkind === "retry") ctl = control(`${P}.${k}`, "select", s[k], { choices: [["", "all of them (default)"], ["failed", "only the failed ones"], ["each", "each in turn, until it passes"]] });
    else ctl = control(`${P}.${k}`, fkind, s[k]);
    return row(label, help, ctl);
  };
  const box = ([k, label, , help]) => [label, help, control(`${P}.${k}`, "bool", s[k])];
  // fields renders a set of fields: checkboxes together, then the rest.
  const fields = (fs) => {
    const bools = fs.filter((f) => f[2] === "bool");
    return (bools.length ? checkRow(bools.map(box)) : "") + fs.filter((f) => f[2] !== "bool").map(fieldRow).join("");
  };
  const all = MORE_FIELDS.filter(([, , , , applies]) => applies(kind, s, d));
  const set = all.filter(([k]) => fieldSet(s, k));
  const unset = all.filter(([k]) => !fieldSet(s, k));
  const open = ed.adv.has(s);
  const rest = open ? [...new Set(unset.map((f) => f[5]))].map((g) =>
    `<div class="adv-title">${esc(g)}</div>${fields(unset.filter((f) => f[5] === g))}`).join("") : "";
  // One grid, so every input lines up.
  return `<div class="fgroup-rows">
    ${row("Name", H.step.name, control(`${P}.name`, "text", s.name))}
    ${row("What we're doing", H.step.description, control(`${P}.description`, "area", s.description, { placeholder: "what this step does, and why" }))}
    ${row("By the end", H.step.expect, control(`${P}.expect`, "area", s.expect, { placeholder: "how things should stand when it's done" }))}
    ${checkRow([["Confirm first", H.step.confirm, control(`${P}.confirm`, "bool", s.confirm)], ["Dangerous", H.step.dangerous, control(`${P}.dangerous`, "bool", s.dangerous)], ...set.filter((f) => f[2] === "bool").map(box)])}
    ${s.background ? row("Stop after", H.step.stop_after, control(`${P}.stop_after`, "select", s.stop_after, { choices: [["", "when the run ends"], ...later] })) : ""}
    ${set.filter((f) => f[2] !== "bool").map(fieldRow).join("")}
    ${unset.length ? `<button class="link adv-toggle" data-act="adv" data-i="${i}">${open ? "▾" : "▸"} Advanced <span class="muted">(${unset.length} more)</span></button>` : ""}
    <div class="adv-rest">${rest}</div></div>`;
}

// The Run this pane: runs just this step (with the edits as they are now),
// optionally after the setup steps, with the runbook's test values.
function runPane(ed, s, i) {
  const r = ed.runs.get(s);
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
    ${r.id ? `<div class="term-slot" data-term-slot="${esc(r.id)}:*"></div>` : ""}
    <pre class="log run-log">${lines || '<span class="info">starting…</span>'}</pre>
  </div>`;
}

// runInputs asks for the inputs to Run this with, all at once, filled
// with the last ones used (or the test values). What's entered is kept in
// this tab for the next Run this, and saved as the test values if asked
// (secrets never are). "Don't ask again" runs with them from then on
// (until a required one is blank, or Test values says to ask). Resolves
// to the values, or null if cancelled.
function runInputs(ed) {
  const defs = (ed.doc.inputs || []).filter((x) => x.name);
  const val = (d) => (d.secret ? ed.secretVars : ed.test.vars)[d.name] ?? "";
  const needed = (d) => d.required && !d.default;
  if (!defs.length || (ed.noAsk && !defs.some((d) => needed(d) && !val(d))))
    return Promise.resolve(Object.fromEntries(defs.map((d) => [d.name, val(d)])));
  const field = (d) => {
    const v = val(d), tip = d.description ? ` title="${esc(d.description)}"` : "";
    if (d.choices?.length) return `<select name="${esc(d.name)}"${tip}>${(needed(d) ? [] : [""]).concat(d.choices).map((c) => `<option ${c === v ? "selected" : ""} value="${esc(c)}">${esc(c || `(default${d.default ? ": " + d.default : ""})`)}</option>`).join("")}</select>`;
    return withPathPick(d, `<input name="${esc(d.name)}"${tip} ${d.secret ? 'type="password"' : ""} value="${esc(v)}" placeholder="${esc(d.default || "")}" spellcheck="false" autocomplete="off">`);
  };
  const dlg = document.createElement("dialog");
  dlg.className = "edit-step run-inputs";
  dlg.innerHTML = `<h3>Run this with</h3>
    <div class="ri-grid">${defs.map((d) => `<label for="ri-${esc(d.name)}"${d.description ? ` title="${esc(d.description)}"` : ""}>${esc(d.name)}${needed(d) ? " *" : ""}:</label>${field(d).replace(`name="${esc(d.name)}"`, `name="${esc(d.name)}" id="ri-${esc(d.name)}"`)}`).join("")}</div>
    ${ed.name ? `<label class="check small"><input type="checkbox" name="__save"> Save as the runbook's test values</label>` : ""}
    <label class="check small"><input type="checkbox" name="__noask"> Don't ask again (change them in Test values)</label>
    <div class="error-text"></div>
    <div class="row"><button class="primary" data-ok>Run</button><button data-close>Cancel</button></div>`;
  document.body.append(dlg);
  return new Promise((done) => {
    let out = null;
    dlg.querySelector("[data-close]").onclick = () => dlg.close();
    dlg.onclose = () => { dlg.remove(); done(out); };
    const ok = () => {
      const vars = {}, missing = [];
      for (const d of defs) {
        const v = dlg.querySelector(`[name="${CSS.escape(d.name)}"]`).value;
        if (!v && needed(d)) missing.push(d.name);
        (d.secret ? ed.secretVars : ed.test.vars)[d.name] = v;
        vars[d.name] = v;
      }
      if (missing.length) return (dlg.querySelector(".error-text").textContent = `Needs ${missing.join(", ")}`);
      if (dlg.querySelector("[name=__save]")?.checked)
        api("PUT", `/api/runbooks/${enc(ed.name)}/test-values`, ed.test).then(() => toast("Saved the test values (not secrets)")).catch((e) => toast(e.message));
      if (dlg.querySelector("[name=__noask]").checked) ed.noAsk = true;
      out = vars;
      dlg.close();
    };
    dlg.querySelector("[data-ok]").onclick = ok;
    dlg.onkeydown = (e) => { if (e.key === "Enter" && e.target.matches("input:not([type=checkbox])")) { e.preventDefault(); ok(); } };
    dlg.showModal();
    dlg.querySelector(".ri-grid input, .ri-grid select")?.focus();
  });
}

async function runThis(ed, i) {
  const s = ed.doc.steps[i];
  const vars = await runInputs(ed);
  if (!vars) return;
  const old = ed.runs.get(s);
  old?.es?.close();
  const y = await formatDoc(ed);
  const r = { id: null, lines: [], status: "running", question: null, error: y.ok ? "" : y.error };
  ed.runs.set(s, r);
  if (!y.ok) { r.status = "failed"; return renderStruct(ed); }
  try {
    const res = await api("POST", "/api/test-run", {
      raw: y.raw, step: i + 1, vars, extra: ed.test.extra, setup: ed.test.setup,
    });
    r.id = res.id;
  } catch (e) { r.status = "failed"; r.error = e.message; return renderStruct(ed); }
  renderStruct(ed);
  r.es = new EventSource(`/api/runs/${enc(r.id)}/events`);
  r.es.onmessage = (msg) => {
    const e = JSON.parse(msg.data);
    if (e.kind.startsWith("term")) {
      termEvent(r.id, e);
      // A terminal (this step's, or a setup step's) shows in the Run this pane.
      if (e.kind !== "term") { r.term = e.kind === "term_start"; updateRunPane(ed, s); }
      return;
    }
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
    updateRunPane(ed, s);
  };
}

// updateRunPane redraws just the run pane, so typing elsewhere isn't disturbed.
function updateRunPane(ed, s) {
  const el = $(`#struct-pane .run-slot[data-uid="${uidOf(s)}"]`);
  if (!el) return;
  const i = ed.doc.steps.indexOf(s);
  el.innerHTML = runPane(ed, s, i);
  const log = el.querySelector(".run-log");
  if (log) log.scrollTop = log.scrollHeight;
  const q = el.querySelector(".question");
  const r = ed.runs.get(s);
  if (q) bindQuestion(q, r.question, r.id);
  termMountAll();
}

// testValuesCard: inputs and extra variables used by Run this.
function testValuesCard(ed, d) {
  const t = ed.test;
  return `<details class="card test-values" ${ed.testOpen ? "open" : ""}><summary><b>Test values</b> <span class="muted small">used by “Run this”${ed.name ? "" : " (save the runbook to keep them)"}</span>${helpIcon("Inputs and extra variables for trying steps out while editing. Saved with this runbook, except secret inputs, which are kept only in this browser tab. Run this asks for all of them at once, filled with these (unless you told it not to ask); what you enter there fills in here too.")}</summary>
    <div class="fgroup-rows">
      ${(d.inputs || []).filter((x) => x.name).map((x) => row(x.name + (x.secret ? " (secret)" : ""), x.description || "",
        withPathPick(x, `<input data-test-var="${esc(x.name)}" ${x.secret ? 'type="password"' : ""} value="${esc((x.secret ? ed.secretVars : t.vars)[x.name] ?? "")}" placeholder="${esc(x.default || "")}">`))).join("")}
      ${row("Extra variables", "Variables an earlier step would normally publish (KEY=value per line), so a step can run on its own.",
        `<textarea data-test-extra rows="3" spellcheck="false" placeholder="region=use1">${esc(Object.entries(t.extra || {}).map(([k, v]) => `${k}=${v}`).join("\n"))}</textarea>`)}
      ${row("Ask before Run this", "Ask for the inputs each time, filled with these.", `<input type="checkbox" data-test-ask ${ed.noAsk ? "" : "checked"}>`)}
      ${row("Run setup first", "Run the steps marked “always” before the step, to build its environment.", `<input type="checkbox" data-test-setup ${t.setup ? "checked" : ""}>`)}
    </div>
    ${ed.name ? `<button data-act="save-test">Save test values</button>` : ""}
  </details>`;
}

// secEntry is a section's sections: entry (description, expect, planned
// time, comments), made when first needed.
function secEntry(d, name, create) {
  let e = (d.sections || []).find((x) => x.name === name);
  if (!e && create) (d.sections ||= []).push((e = { name }));
  return e;
}

// blocksOf splits the steps into runs that share a section.
function blocksOf(d) {
  const blocks = [];
  d.steps.forEach((s, i) => {
    const sec = s.section || "";
    if (blocks.length && blocks.at(-1).section === sec) blocks.at(-1).end = i;
    else blocks.push({ section: sec, start: i, end: i });
  });
  return blocks;
}

// outline is the steps, grouped under their sections' headings. Steps and
// sections can be dragged by their ⠿ handles.
function outline(ed, d) {
  const mw = !!d.maintenance_window;
  const inputs = (d.inputs || []).filter((x) => x.name);
  return `<div class="outline" id="outline">
    <div class="drop-zone" data-drop="top">Drop here: before every section (no section)</div>
    ${blocksOf(d).map((b) => {
      const steps = d.steps.slice(b.start, b.end + 1).map((s, k) => stepItem(ed, d, s, b.start + k)).join("");
      if (!b.section) return `<div class="sec-block nosec"><ol class="steps struct-steps">${steps}</ol></div>`;
      const n = b.section, info = secEntry(d, n, false) || {};
      const open = ed.secOpen.has(n);
      const target = (field) => (create) => [secEntry(d, n, create) || {}, field];
      return `<div class="sec-block" data-block="${b.start}">
        <div class="sec-head" data-head="${b.start}">
          <span class="grip" draggable="true" data-grip="sec" data-start="${b.start}" data-end="${b.end}" title="Drag to move the section">⠿</span>
          <input class="sec-name" data-sec-rename="${esc(n)}" value="${esc(n)}" size="${Math.max(10, n.length + 2)}" title="${esc(H.section.name)}">${cmtBtn(target("#"))}
          ${mw ? `<span class="sec-at" title="${esc(H.section.window_offset)}">T <input data-sec="${esc(n)}" data-sec-field="window_offset" value="${esc(info.window_offset || "")}" placeholder="e.g. 0">
            <select data-sec="${esc(n)}" data-sec-field="offset_input" title="${esc(H.step.offset_input)}"><option value="">no override</option>${inputs.map((x) => `<option value="${esc(x.name)}" ${x.name === info.offset_input ? "selected" : ""}>input: ${esc(x.name)}</option>`).join("")}</select></span>` : ""}
          <button class="link sec-toggle" data-act="sec-toggle" data-sec="${esc(n)}">${open ? "▾" : "▸"} ${info.description || info.expect ? "About" : "+ About"}</button>
          ${!open && info.description ? `<span class="muted small sec-preview">${esc(info.description.split("\n")[0])}</span>` : ""}
          <span class="spacer"></span>
          <span class="muted small">${b.end - b.start + 1} step${b.end > b.start ? "s" : ""}</span>
          <button class="icon-btn" data-act="sec-add-step" data-i="${b.end}" title="Add a step at the end of this section">＋</button>
          <button class="icon-btn danger" data-act="sec-ungroup" data-i="${b.start}" title="Remove this heading (its steps join the section above)">✕</button>
        </div>
        ${open ? `<div class="sec-details">
          <label><span class="flabel">What it does${helpIcon(H.section.description)}${cmtBtn(target("description"))}</span><textarea rows="2" data-sec="${esc(n)}" data-sec-field="description">${esc(info.description || "")}</textarea></label>
          <label><span class="flabel">By the end${helpIcon(H.section.expect)}${cmtBtn(target("expect"))}</span><textarea rows="2" data-sec="${esc(n)}" data-sec-field="expect">${esc(info.expect || "")}</textarea></label>
        </div>` : ""}
        <ol class="steps struct-steps">${steps}</ol>
        <div class="drop-zone small" data-drop-after="${b.end}">Drop here: end of ${esc(n)}</div>
      </div>`;
    }).join("")}
    <div class="drop-zone" data-drop="end">Drop here: at the end</div>
  </div>
  <div class="row outline-actions"><button data-act="add-step">+ Add step</button><button data-act="add-section">+ Add section</button>
    <span class="muted small">Drag ⠿ to reorder steps and sections, or to move a step into another section.</span></div>`;
}

function stepItem(ed, d, s, i) {
  const r = ed.runs.get(s);
  return `<li class="step ${inWindow(d.steps, i) ? "in-window" : ""}" data-step="${i}">
    <div class="step-head" data-act="open" data-i="${i}">
      <span class="grip" draggable="true" data-grip="step" data-i="${i}" title="Drag to move the step">⠿</span>
      <span class="n">${i + 1}</span><span class="name">${esc(s.name || "(unnamed)")} ${stepBadges(s)} ${plannedBadge(d.steps, d.sections, i)}</span>${cmtBtn([s, "#"])}
      <span class="spacer"></span>
      <button class="icon-btn claude-only" data-act="explain" data-i="${i}" title="Ask Claude about this step, as edited (the whole runbook goes along as context)">✨</button>
      <button class="icon-btn" data-act="run-this" data-i="${i}" title="Run just this step with the test values">▶ Run this</button>
      <button class="icon-btn" data-act="up" data-i="${i}" title="Move up (into the section above, from the top of a section)" ${i === 0 ? "disabled" : ""}>↑</button>
      <button class="icon-btn" data-act="down" data-i="${i}" title="Move down (into the section below, from the end of a section)" ${i === d.steps.length - 1 ? "disabled" : ""}>↓</button>
      <button class="icon-btn" data-act="dup" data-i="${i}" title="Duplicate">⧉</button>
      <button class="icon-btn danger" data-act="del" data-i="${i}" title="Delete">✕</button>
    </div>
    ${ed.open.has(s) ? `<div class="step-body step-edit">
      <div class="se-details" style="width:${ed.widths[0] ? ed.widths[0] + "px" : r ? "30%" : "38%"}">${detailsPane(ed, d, s, i)}</div>
      <div class="split" data-split="0" title="Drag to resize"></div>
      <div class="se-command">${stepLeft(ed, s, i)}</div>
      ${r ? `<div class="split" data-split="1" title="Drag to resize"></div>
        <div class="se-run run-slot" data-uid="${uidOf(s)}" style="width:${ed.widths[1] ? ed.widths[1] + "px" : "34%"}">${runPane(ed, s, i)}</div>` : ""}
    </div>` : ""}
  </li>`;
}

// moveStep takes step from to just before index to (in the list without
// it), into section sec.
function moveStep(d, from, to, sec) {
  const [s] = d.steps.splice(from, 1);
  s.section = sec || undefined;
  d.steps.splice(Math.max(0, Math.min(to, d.steps.length)), 0, s);
}

function renderStruct(ed) {
  const d = ed.doc;
  d.inputs ||= [];
  d.steps ||= [];
  ed.widths ||= [null, null]; // px once dragged; proportional until then
  ed.cmtTargets = [];
  // Library functions load once; draw again when they arrive.
  loadLibs(d.libraries).then((loaded) => { if (loaded && ed.mode === "struct") renderStruct(ed); });
  curEd = ed;
  const pane = ed.pane || $("#struct-pane");
  // One step only (the run console's Edit this step): its form, as in the outline.
  const single = ed.only != null && d.steps[ed.only];
  const html = single ? `<div class="step-edit single-step">
      <div class="se-details" style="width:${ed.widths[0] ? ed.widths[0] + "px" : "42%"}">${detailsPane(ed, d, single, ed.only)}</div>
      <div class="split" data-split="0" title="Drag to resize"></div>
      <div class="se-command">${stepLeft(ed, single, ed.only)}</div></div>` : `
    <div class="card">
      <h3>Runbook${cmtBtn([d, "#"])}</h3>
      <div class="fgroup-rows">
        ${row("Name", H.runbook.name, control("name", "text", d.name))}
        ${row("What it's for", H.runbook.description, control("description", "area", d.description, { rows: 3 }))}
        ${row("By the end", H.runbook.expect, control("expect", "area", d.expect))}
        ${row("Overview", H.runbook.overview, control("overview", "area", d.overview, { rows: 6, placeholder: "the strategy: the phases, why in this order, what can go wrong (Markdown)" }))}
        ${row("Tags", H.runbook.tags, control("tags", "list", d.tags, { placeholder: "ops, deploy" }))}
        ${row("Libraries", H.runbook.libraries, control("libraries", "list", d.libraries, { placeholder: "db-helpers" }))}
      </div>
      ${group(`Maintenance window${d.maintenance_window ? "" : " (off)"}`, [
        `<p class="muted small" style="grid-column: 1 / -1">${esc(H.runbook.maintenance_window)}</p>`,
        `<label class="check" style="grid-column: 1 / -1"><input type="checkbox" data-mw-toggle ${d.maintenance_window ? "checked" : ""}><span>Use a maintenance window</span></label>`,
        d.maintenance_window ? row("Target length", H.runbook.window_target, offsetPair(d, "maintenance_window.target", d.maintenance_window?.target, "maintenance_window.target_input", d.maintenance_window?.target_input, "e.g. 45m")) : "",
        d.maintenance_window ? row("Description", H.runbook.window_description, control("maintenance_window.description", "text", d.maintenance_window?.description)) : "",
        d.maintenance_window ? row("Notification", H.runbook.notification_required, `<span class="offset-pair"><label class="check">${control("maintenance_window.notification_required", "bool", d.maintenance_window.notification_required)}<span>required</span></label>
          ${d.maintenance_window.notification_required ? `${control("maintenance_window.notify_before", "text", d.maintenance_window.notify_before, { placeholder: "2w (default)" })}<span class="muted small">before</span>` : ""}</span>`) : "",
      ], { collapsible: true, id: "rb-window", open: ed.windowOpen ?? !!d.maintenance_window })}
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
        <th>Type${helpIcon(H.input.type)}</th><th>Choices${helpIcon(H.input.choices)}</th><th>Required${helpIcon(H.input.required)}</th><th>Secret${helpIcon(H.input.secret)}</th><th>Identifier${helpIcon(H.input.identifier)}</th><th></th></tr></thead><tbody>
      ${d.inputs.map((inp, i) => `<tr>
        <td>${control(`inputs.${i}.name`, "text", inp.name)}</td>
        <td>${control(`inputs.${i}.description`, "text", inp.description)}</td>
        <td>${withPathPick(inp, control(`inputs.${i}.default`, "text", inp.default))}</td>
        <td>${control(`inputs.${i}.type`, "select", inp.type, { choices: [["", "text"], ["dir", "folder"], ["file", "file"]] })}</td>
        <td>${control(`inputs.${i}.choices`, "list", inp.choices, { placeholder: "a, b" })}</td>
        <td>${control(`inputs.${i}.required`, "bool", inp.required)}</td>
        <td>${control(`inputs.${i}.secret`, "bool", inp.secret)}</td>
        <td>${control(`inputs.${i}.identifier`, "bool", inp.identifier)}</td>
        <td><button class="link" data-act="del-input" data-i="${i}">remove</button></td></tr>`).join("")}
      </tbody></table>
      <button data-act="add-input">+ Add input</button>
    </div>
    ${testValuesCard(ed, d)}
    <h3>Steps${helpIcon(H.runbook.steps)}</h3>
    ${outline(ed, d)}`;
  curEd = null;
  pane.innerHTML = html;

  pane.querySelectorAll(".run-slot").forEach((el) => {
    const log = el.querySelector(".run-log");
    if (log) log.scrollTop = log.scrollHeight;
    const q = el.querySelector(".question");
    const r = ed.runs.get(d.steps.find((x) => uidOf(x) === +el.dataset.uid));
    if (q && r?.question) bindQuestion(q, r.question, r.id);
  });
  bindDrag(ed, pane);
  termMountAll();
  const mw = pane.querySelector('[data-group="rb-window"]');
  if (mw) mw.ontoggle = () => (ed.windowOpen = mw.open);
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
    if (t.matches("[data-test-ask]")) { ed.noAsk = !t.checked; return; }
    if (t.matches("[data-sec]")) {
      d.sections ||= [];
      let sec = d.sections.find((x) => x.name === t.dataset.sec);
      if (!sec) d.sections.push((sec = { name: t.dataset.sec }));
      sec[t.dataset.secField] = t.value || undefined;
      d.sections = d.sections.filter((x) => x.description || x.expect || x.window_offset || x.offset_input || hasComments(ed, x));
      return;
    }
    if (t.matches("[data-mw-toggle]") && e.type === "change") {
      ed.windowOpen = true;
      if (t.checked) {
        d.maintenance_window = {};
        return renderStruct(ed);
      }
      // Off: window marks and planned times only mean something with a window.
      const fields = ["window", "window_offset", "offset_input"];
      const marked = d.steps.filter((st) => fields.some((f) => st[f])).length + (d.sections || []).filter((x) => x.window_offset || x.offset_input).length;
      if (marked && !confirm(`Turning the maintenance window off also clears the window marks and planned times on ${marked} step${marked === 1 ? "" : "s"}/section${marked === 1 ? "" : "s"}. Go ahead?`)) {
        t.checked = true;
        return;
      }
      delete d.maintenance_window;
      d.steps.forEach((st) => fields.forEach((f) => delete st[f]));
      (d.sections || []).forEach((x) => { delete x.window_offset; delete x.offset_input; });
      d.sections = (d.sections || []).filter((x) => x.description || x.expect || hasComments(ed, x));
      return renderStruct(ed);
    }
    if (t.matches("[data-sec-rename]") && e.type === "change") {
      const old = t.dataset.secRename, nw = t.value.trim();
      if (!nw || nw === old) return renderStruct(ed);
      if (sectionNames(d).includes(nw) && !confirm(`There's already a section called “${nw}”. Merge “${old}” into it?`)) return renderStruct(ed);
      // Merging into a section that's already described keeps that description.
      const merge = (d.sections || []).some((x) => x.name === nw);
      d.steps.forEach((st) => { if (st.section === old) st.section = nw; });
      d.sections = (d.sections || []).filter((x) => !(merge && x.name === old));
      for (const sec of d.sections) if (sec.name === old) sec.name = nw;
      return renderStruct(ed);
    }
    if (t.matches("[data-repeat-on]") && e.type === "change") {
      if (t.value) d.steps[+t.dataset.i].repeat = { every: "20s", timeout: "10m" };
      return renderStruct(ed);
    }
    if (t.matches("[data-sec-pick]") && e.type === "change") {
      const i = +t.dataset.i;
      if (ed.only != null) {
        // Editing one step in place: it keeps its position, only its section changes.
        const v = t.value === "__new__" ? (prompt("New section name:") || "").trim() : t.value;
        if (v || t.value === "") d.steps[i].section = v || undefined;
        return renderStruct(ed);
      }
      if (t.value === "__new__") {
        const v = (prompt("New section name (it starts at this step):") || "").trim();
        if (v) d.steps[i].section = v;
        return renderStruct(ed);
      }
      // Move it to the end of the (last block of the) chosen section.
      const last = d.steps.map((x) => x.section || "").lastIndexOf(t.value);
      const to = last < 0 ? d.steps.length : last >= i ? last : last + 1;
      moveStep(d, i, to, t.value);
      return renderStruct(ed);
    }
    if (t.matches("[data-way-kind]") && e.type === "change") {
      const w = d.steps[+t.dataset.i].ways[+t.dataset.j];
      const text = w.run ?? w.manual ?? w.checks?.[0]?.run ?? "";
      delete w.run; delete w.manual; delete w.checks;
      if (t.value === "run") w.run = text || "echo TODO";
      if (t.value === "manual") { w.manual = text || "TODO: how to do it by hand"; delete w.tty; }
      if (t.value === "checks") { w.checks = [{ name: "check 1", run: text || "true" }]; delete w.tty; }
      return renderStruct(ed);
    }
    if (t.matches("[data-pref]") && e.type === "change") {
      ed.wayTab.set(d.steps[+t.dataset.i], +t.dataset.j);
      d.steps[+t.dataset.i].ways.forEach((w, k) => { if (k === +t.dataset.j) w.preferred = true; else delete w.preferred; });
      return renderStruct(ed);
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
    const el = t.closest("[data-path]");
    if (!el) return;
    setPath(d, el.dataset.path, readField(el));
    // Structure-changing fields redraw the form. So does an advanced field
    // that's now set (or cleared): it moves above the fold (or back).
    const m = el.dataset.path.match(/^steps\.(\d+)\.([a-z_]+)/);
    const fk = m && (m[2] === "offset_input" ? "window_offset" : m[2]);
    const moved = m && MORE_FIELDS.some(([k]) => k === fk) && !!el.closest(".adv-rest") === fieldSet(d.steps[+m[1]], fk);
    if (e.type === "change" && (moved || /\.(name|section|confirm|dangerous|background|window|window_offset|offset_input|choose|tty|notification_required)$|^maintenance_window\./.test(el.dataset.path))) {
      const focus = document.activeElement?.dataset?.path;
      renderStruct(ed);
      if (focus) pane.querySelector(`[data-path="${CSS.escape(focus)}"]`)?.focus();
    }
  };
  pane.onclick = (e) => {
    const c = e.target.closest("[data-cmt]");
    if (c) {
      e.preventDefault();
      e.stopPropagation();
      return editComment(ed, ed.cmtTargets[+c.dataset.cmt]);
    }
    const b = e.target.closest("[data-act]");
    if (!b || b.tagName === "SELECT" || e.target.closest(".question")) return;
    e.preventDefault();
    e.stopPropagation();
    const i = +b.dataset.i, j = +b.dataset.j, k = +b.dataset.k;
    const s = d.steps[i];
    // up/down move within a section; from its top (or bottom) a step moves
    // into the section above (or below).
    const move = (dir) => {
      const nb = d.steps[i + dir];
      if (!nb) return;
      if ((nb.section || "") === (s.section || "")) [d.steps[i], d.steps[i + dir]] = [nb, s];
      else s.section = nb.section;
    };
    switch (b.dataset.act) {
      case "open": toggle(ed.open, s); break;
      case "up": move(-1); break;
      case "down": move(1); break;
      case "dup": d.steps.splice(i + 1, 0, structuredClone({ ...s, id: undefined, name: `${s.name} (copy)` })); break;
      case "del": if (!confirm(`Delete step ${i + 1} (${s.name})?`)) return; ed.runs.get(s)?.es?.close(); d.steps.splice(i, 1); break;
      case "adv": toggle(ed.adv, s); break;
      case "run-this": ed.open.add(s); return runThis(ed, i);
      case "stop-run": { const r = ed.runs.get(s); if (r?.id) api("POST", `/api/runs/${enc(r.id)}/cancel`).catch(() => {}); return; }
      case "close-run": ed.runs.get(s)?.es?.close(); ed.runs.delete(s); break;
      case "way-tab": ed.wayTab.set(s, j); break;
      case "explain":
        formatDoc(ed).then((y) => y.ok ? openExplain({ raw: y.raw }, i + 1, `${i + 1}. ${s.name} (as edited)`) : toast(y.error));
        return;
      case "repeat-off": delete s.repeat; break;
      case "sec-toggle": toggle(ed.secOpen, b.dataset.sec); break;
      case "sec-add-step": {
        const ns = { name: "New step", run: "echo hello", section: s.section };
        d.steps.splice(i + 1, 0, ns);
        ed.open.add(ns);
        break;
      }
      case "sec-ungroup": {
        if (!confirm(`Remove the heading “${s.section}”? Its steps join the section above.`)) return;
        const name = s.section, above = i > 0 ? d.steps[i - 1].section : undefined;
        for (let k = i; k < d.steps.length && d.steps[k].section === name; k++) d.steps[k].section = above;
        break;
      }
      case "add-section": {
        const name = (prompt("New section name:") || "").trim();
        if (!name) return;
        const ns = { name: "New step", run: "echo hello", section: name };
        d.steps.push(ns);
        ed.open.add(ns);
        break;
      }
      case "save-test":
        api("PUT", `/api/runbooks/${enc(ed.name)}/test-values`, ed.test).then(() => toast("Test values saved")).catch((err) => toast(err.message));
        return;
      case "kind": {
        // Carry the text over where it makes sense.
        const text = s.run ?? s.manual ?? s.checks?.[0]?.run ?? "";
        for (const f of ["run", "manual", "checks", "repeat"]) delete s[f];
        if (b.dataset.kind === "run") s.run = text;
        if (b.dataset.kind === "manual") s.manual = text;
        if (b.dataset.kind === "checks") s.checks = [{ name: "check 1", run: text }];
        if (b.dataset.kind !== "run") { delete s.background; delete s.stop_after; delete s.tty; }
        break;
      }
      case "add-check":
      case "del-check": {
        const o = b.dataset.w !== undefined ? s.ways[+b.dataset.w] : s;
        if (b.dataset.act === "add-check") (o.checks ||= []).push({ name: `check ${o.checks.length + 1}`, run: "" });
        else o.checks.splice(j, 1);
        break;
      }
      case "add-way": {
        if (!s.ways) {
          // The step's own script, instructions or checks become its first (preferred) way.
          const k = kindOf(s);
          const first = { name: WAY_KINDS.find(([v]) => v === k)[1], preferred: true };
          if (k === "run") { first.run = s.run ?? ""; if (s.tty) first.tty = true; }
          if (k === "manual") first.manual = s.manual ?? "";
          if (k === "checks") first.checks = s.checks;
          for (const f of ["run", "manual", "checks", "tty", "background", "stop_after"]) delete s[f];
          s.ways = [first, k === "run" ? { name: "Manual", manual: "TODO: how to do it by hand" } : { name: "Scripted", run: "echo TODO" }];
        } else {
          s.ways.push({ name: uniqueWayName(s.ways, "Another way"), run: "echo TODO" });
        }
        ed.wayTab.set(s, s.ways.length - 1);
        break;
      }
      case "del-way": {
        if (!confirm(`Remove the way “${s.ways[j].name}”?`)) return;
        s.ways.splice(j, 1);
        ed.wayTab.set(s, 0);
        if (s.ways.length === 1) {
          // One way left: it's just the step's script, instructions or checks again.
          const w = s.ways[0];
          delete s.ways; delete s.choose;
          if (w.run !== undefined) s.run = w.run;
          if (w.manual !== undefined) s.manual = w.manual;
          if (w.checks) s.checks = w.checks;
          if (w.tty) s.tty = true;
        }
        break;
      }
      case "add-step": {
        const ns = { name: "New step", run: "echo hello", section: d.steps.at(-1)?.section };
        d.steps.push(ns);
        ed.open.add(ns);
        break;
      }
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

// ---------- drag and drop ----------
// Steps drop before or after another step (joining its section), on a
// section heading (first in that section), at the end of a section, or at
// the very top (no section) or end. Sections drop on another heading
// (before it) or at the end.

function bindDrag(ed, pane) {
  const d = ed.doc;
  let drag = null, mark = null;
  const clear = () => { mark?.classList.remove("drop-before", "drop-after", "drop-on"); mark = null; };
  const target = (e) => {
    if (!drag) return null;
    const zone = e.target.closest("[data-drop],[data-drop-after]");
    if (zone) {
      if (drag.type === "sec" && !zone.dataset.drop) return null;
      if (drag.type === "sec" && zone.dataset.drop === "top") return null;
      return { el: zone, cls: "drop-on", zone };
    }
    const head = e.target.closest("[data-head]");
    if (head) {
      if (drag.type === "sec" && +head.dataset.head === drag.start) return null;
      return { el: head, cls: drag.type === "sec" ? "drop-before" : "drop-on", head: +head.dataset.head };
    }
    const li = e.target.closest("li[data-step]");
    if (li && drag.type === "step") {
      const i = +li.dataset.step;
      if (i === drag.i) return null;
      const r = li.getBoundingClientRect();
      const after = e.clientY > r.top + Math.min(r.height, 44) / 2;
      return { el: li, cls: after ? "drop-after" : "drop-before", step: i, after };
    }
    return null;
  };
  pane.ondragstart = (e) => {
    const g = e.target.closest?.("[data-grip]");
    if (!g) return;
    drag = g.dataset.grip === "sec" ? { type: "sec", start: +g.dataset.start, end: +g.dataset.end } : { type: "step", i: +g.dataset.i };
    e.dataTransfer.effectAllowed = "move";
    e.dataTransfer.setData("text/plain", "");
    pane.classList.add("dragging-" + drag.type);
    const li = g.closest(drag.type === "sec" ? ".sec-block" : "li");
    if (li) e.dataTransfer.setDragImage(li, 20, 16);
  };
  pane.ondragover = (e) => {
    const t = target(e);
    clear();
    if (!t) return;
    e.preventDefault();
    mark = t.el;
    mark.classList.add(t.cls);
  };
  pane.ondragleave = (e) => { if (!pane.contains(e.relatedTarget)) clear(); };
  pane.ondragend = () => { clear(); drag = null; pane.classList.remove("dragging-step", "dragging-sec"); };
  pane.ondrop = (e) => {
    const t = target(e);
    clear();
    pane.classList.remove("dragging-step", "dragging-sec");
    if (!t) return;
    e.preventDefault();
    const steps = d.steps;
    if (drag.type === "step") {
      const from = drag.i, moving = steps[from];
      let anchor, before = true, sec;
      if (t.step !== undefined) { anchor = steps[t.step]; before = !t.after; sec = anchor.section; }
      else if (t.head !== undefined) { anchor = steps[t.head]; sec = anchor.section; }
      else if (t.zone.dataset.dropAfter !== undefined) { anchor = steps[+t.zone.dataset.dropAfter]; before = false; sec = anchor.section; }
      else if (t.zone.dataset.drop === "top") { anchor = steps[0]; sec = undefined; }
      else { anchor = steps.at(-1); before = false; sec = anchor.section; }
      if (anchor === moving) return renderStruct(ed);
      steps.splice(from, 1);
      const at = steps.indexOf(anchor) + (before ? 0 : 1);
      moving.section = sec || undefined;
      steps.splice(at, 0, moving);
    } else {
      const block = steps.slice(drag.start, drag.end + 1);
      const anchor = t.head !== undefined ? steps[t.head] : null;
      steps.splice(drag.start, block.length);
      steps.splice(anchor ? steps.indexOf(anchor) : steps.length, 0, ...block);
    }
    drag = null;
    renderStruct(ed);
    pane.dispatchEvent(new Event("input"));
  };
}

// ---------- one step (the run console's Edit this step) ----------

// renderStepEditor puts the editor's form for step i of doc (the runbook,
// as /api/parse gives it) in el, with the same Structured / YAML views and
// validation as the editor page. step() resolves to the step as edited
// (null if the YAML doesn't parse), dirty() says whether it changed.
function renderStepEditor(el, { doc, comments, i, yaml }) {
  const ed = { mode: "struct", doc, only: i, test: { vars: {}, extra: {}, setup: true }, secretVars: {},
    open: new Set(), adv: new Set(), runs: new Map(), wayTab: new WeakMap(), secOpen: new Set(), comments: new WeakMap() };
  attachComments(ed, comments || {});
  const orig = JSON.stringify(cleanDoc(doc.steps[i]));
  el.innerHTML = `
    <div class="row editor-bar se1-bar">
      <div class="seg"><button data-m="struct" class="on">Structured</button><button data-m="yaml">YAML</button></div>
      <button data-validate title="Check the runbook with this step as edited">Validate</button>
      <button type="button" class="fn-pick-yaml" data-fnpick-yaml title="Insert a function at the cursor (Ctrl+Shift+F)">ƒ Insert function</button>
      <span class="validation se1-v"></span>
    </div>
    <div class="se1-struct struct-pane"></div>
    <div class="se1-yaml" hidden><textarea class="editor cx-yaml" spellcheck="false"></textarea></div>`;
  ed.pane = el.querySelector(".se1-struct");
  const ta = el.querySelector("textarea"), v = el.querySelector(".se1-v");
  setFnContext(() => doc);
  // In the YAML view, its textarea; in the form, the code field last typed in.
  el.querySelector("[data-fnpick-yaml]").onclick = () => {
    const t = ed.mode === "yaml" ? ta : (fnPick.last && el.contains(fnPick.last) ? fnPick.last : el.querySelector(".se1-struct textarea.code"));
    if (t) openFnPicker(t);
  };
  const show = (res) => { v.className = "validation se1-v " + (res.ok ? "ok" : "bad"); v.textContent = res.ok ? "✔ valid" : res.error; };
  ta.onkeydown = (e) => { if (e.key === "Tab") { e.preventDefault(); ta.setRangeText("  ", ta.selectionStart, ta.selectionEnd, "end"); } };
  // fromYAML takes the YAML view's text into the form's step.
  const fromYAML = async () => {
    const res = await api("POST", "/api/parse-step", ta.value, true);
    if (!res.ok) { show(res); return false; }
    doc.steps[i] = res.step;
    return true;
  };
  const setMode = async (mode) => {
    if (mode === ed.mode) return;
    if (mode === "yaml") {
      const res = await api("POST", "/api/format-step", cleanDoc(doc.steps[i]));
      if (!res.ok) return show(res);
      ta.value = res.raw;
    } else if (!(await fromYAML())) return;
    ed.mode = mode;
    el.querySelector(".se1-struct").hidden = mode !== "struct";
    el.querySelector(".se1-yaml").hidden = mode !== "yaml";
    el.querySelectorAll("[data-m]").forEach((b) => b.classList.toggle("on", b.dataset.m === mode));
    if (mode === "struct") renderStruct(ed);
    else ta.focus();
  };
  el.querySelectorAll("[data-m]").forEach((b) => (b.onclick = () => setMode(b.dataset.m)));
  el.querySelector("[data-validate]").onclick = async () => {
    if (ed.mode === "yaml" && !(await fromYAML())) return;
    const y = await formatDoc(ed);
    show(y.ok ? await api("POST", "/api/validate", y.raw, true) : y);
  };
  renderStruct(ed);
  if (yaml) { ta.value = yaml; }
  return {
    async step() {
      if (ed.mode === "yaml" && !(await fromYAML())) return null;
      return cleanDoc(doc.steps[i]);
    },
    dirty: () => (ed.mode === "yaml" ? ta.value !== (yaml || ta.value) || JSON.stringify(cleanDoc(doc.steps[i])) !== orig : JSON.stringify(cleanDoc(doc.steps[i])) !== orig),
  };
}
