"use strict";

// Runbook editor: YAML, or a structured form. The server converts between
// them (POST /api/parse, /api/format), so both views follow the same rules.

function renderEditor(el, raw, name, initialError, version, updatedAt) {
  const ed = { mode: "yaml", doc: null, open: new Set(), hasComments: /^\s*#/m.test(raw) };
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

// Field kinds: text (default), area, code, int, bool, list (comma
// separated), kv (KEY=value per line), select.
const STEP_FIELDS = [
  ["name", "Name", "text"], ["id", "ID", "text", "Optional; for --from and references"],
  ["section", "Section", "text", "Heading this step is grouped under"], ["owner", "Owner", "text", "Who does it (display only)"],
  ["expect", "Expected outcome", "area", "What success looks like"],
  ["dir", "Directory", "text"], ["when", "Only when", "text", `Template, e.g. {{eq .env "production"}}`],
  ["timeout", "Timeout", "text", "e.g. 30s, 10m"], ["retries", "Retries", "int"], ["retry_delay", "Retry delay", "text", "e.g. 10s"],
  ["shell", "Shell", "text", "Default: the runbook's"], ["capture", "Capture stdout as", "text"],
  ["outputs", "Publishes", "list", "Shell variables to add to the run's state"],
  ["secret_outputs", "Publishes (secret)", "list", "Same, masked and never stored"],
  ["env", "Environment", "kv", "KEY=value per line"],
  ["unattended", "When unattended", "select", "Manual and terminal steps with nobody there", ["", "fail", "skip", "pass"]],
];
const STEP_FLAGS = [
  ["confirm", "Confirm before running"], ["always", "Always run (setup)"],
  ["tty", "Needs a terminal"], ["continue_on_error", "Continue on error"],
];

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

function fieldHTML(path, label, kind, val, help, choices) {
  const p = esc(path);
  let ctl;
  switch (kind) {
    case "area": ctl = `<textarea data-path="${p}" rows="2">${esc(val || "")}</textarea>`; break;
    case "code": ctl = `<textarea class="code" data-path="${p}" rows="${Math.min(16, Math.max(3, String(val || "").split("\n").length + 1))}" spellcheck="false">${esc(val || "")}</textarea>`; break;
    case "int": ctl = `<input type="number" min="0" data-path="${p}" data-kind="int" value="${esc(val ?? "")}">`; break;
    case "list": ctl = `<input data-path="${p}" data-kind="list" value="${esc((val || []).join(", "))}">`; break;
    case "kv": ctl = `<textarea data-path="${p}" data-kind="kv" rows="2" spellcheck="false">${esc(Object.entries(val || {}).map(([k, v]) => `${k}=${v}`).join("\n"))}</textarea>`; break;
    case "select": ctl = `<select data-path="${p}">${choices.map((c) => `<option value="${esc(c)}" ${c === (val || "") ? "selected" : ""}>${esc(c || "(default)")}</option>`).join("")}</select>`; break;
    default: ctl = `<input data-path="${p}" value="${esc(val ?? "")}">`;
  }
  return `<label class="f"><span>${esc(label)}</span>${ctl}${help ? `<small>${esc(help)}</small>` : ""}</label>`;
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

function renderStruct(ed) {
  const d = ed.doc;
  d.inputs ||= [];
  d.steps ||= [];
  const pane = $("#struct-pane");
  pane.innerHTML = `
    <div class="card">
      <h3>Runbook</h3>
      <div class="fgrid">
        ${fieldHTML("name", "Name", "text", d.name, "Lowercase identifier")}
        ${fieldHTML("shell", "Shell", "text", d.shell, "Default: sh")}
        ${fieldHTML("tags", "Tags", "list", d.tags)}
        ${fieldHTML("profile", "Profile", "list", d.profile, "Files sourced before every step, e.g. ~/.zshrc (aliases, functions)")}
      </div>
      ${fieldHTML("description", "Description", "area", d.description)}
      ${fieldHTML("env", "Environment for every step", "kv", d.env, "KEY=value per line; values may use {{.input}}")}
    </div>
    <div class="card">
      <h3>Inputs</h3>
      <table class="inputs"><thead><tr><th>Name</th><th>Description</th><th>Default</th><th>Choices</th><th>Required</th><th>Secret</th><th></th></tr></thead><tbody>
      ${d.inputs.map((inp, i) => `<tr>
        <td><input data-path="inputs.${i}.name" value="${esc(inp.name || "")}"></td>
        <td><input data-path="inputs.${i}.description" value="${esc(inp.description || "")}"></td>
        <td><input data-path="inputs.${i}.default" value="${esc(inp.default || "")}"></td>
        <td><input data-path="inputs.${i}.choices" data-kind="list" value="${esc((inp.choices || []).join(", "))}" placeholder="a, b"></td>
        <td><input type="checkbox" data-path="inputs.${i}.required" ${inp.required ? "checked" : ""}></td>
        <td><input type="checkbox" data-path="inputs.${i}.secret" ${inp.secret ? "checked" : ""}></td>
        <td><button class="link" data-act="del-input" data-i="${i}">remove</button></td></tr>`).join("")}
      </tbody></table>
      <button data-act="add-input">+ Add input</button>
    </div>
    <h3>Steps</h3>
    <ol class="steps struct-steps">${d.steps.map((s, i) => {
      const manual = s.manual !== undefined && s.run === undefined;
      return `${sectionHeading(d.steps, i)}<li class="step">
        <div class="step-head" data-act="open" data-i="${i}">
          <span class="n">${i + 1}</span><span class="name">${esc(s.name || "(unnamed)")} ${stepBadges(s)}</span>
          <span class="spacer"></span>
          <button class="icon-btn" data-act="up" data-i="${i}" title="Move up" ${i === 0 ? "disabled" : ""}>↑</button>
          <button class="icon-btn" data-act="down" data-i="${i}" title="Move down" ${i === d.steps.length - 1 ? "disabled" : ""}>↓</button>
          <button class="icon-btn" data-act="dup" data-i="${i}" title="Duplicate">⧉</button>
          <button class="icon-btn danger" data-act="del" data-i="${i}" title="Delete">✕</button>
        </div>
        ${ed.open.has(i) ? `<div class="step-body">
          <div class="seg kind-seg">
            <button data-act="kind" data-kind="run" data-i="${i}" class="${manual ? "" : "on"}">Command</button>
            <button data-act="kind" data-kind="manual" data-i="${i}" class="${manual ? "on" : ""}">Manual instructions</button>
          </div>
          ${fieldHTML(`steps.${i}.${manual ? "manual" : "run"}`, manual ? "Instructions" : "Command", "code", manual ? s.manual : s.run, manual ? "What the operator should do; templates allowed" : "Shell script; templates like {{.input}} allowed")}
          <div class="flags">${STEP_FLAGS.map(([k, label]) => `<label><input type="checkbox" data-path="steps.${i}.${k}" ${s[k] ? "checked" : ""}> ${label}</label>`).join("")}</div>
          <div class="fgrid">${STEP_FIELDS.map(([k, label, kind, help, choices]) => fieldHTML(`steps.${i}.${k}`, label, kind, s[k], help, choices)).join("")}</div>
        </div>` : ""}
      </li>`;
    }).join("")}</ol>
    <button data-act="add-step">+ Add step</button>`;

  pane.oninput = pane.onchange = (e) => {
    const el = e.target.closest("[data-path]");
    if (!el) return;
    setPath(d, el.dataset.path, readField(el));
    if (/\.name$/.test(el.dataset.path) && e.type === "change") renderStruct(ed);
  };
  pane.onclick = (e) => {
    const b = e.target.closest("[data-act]");
    if (!b) return;
    e.preventDefault();
    const i = +b.dataset.i;
    const swap = (a, c) => {
      [d.steps[a], d.steps[c]] = [d.steps[c], d.steps[a]];
      const oa = ed.open.has(a), oc = ed.open.has(c);
      ed.open.delete(a); ed.open.delete(c);
      if (oa) ed.open.add(c);
      if (oc) ed.open.add(a);
    };
    switch (b.dataset.act) {
      case "open": toggle(ed.open, i); break;
      case "up": swap(i, i - 1); break;
      case "down": swap(i, i + 1); break;
      case "dup": d.steps.splice(i + 1, 0, structuredClone({ ...d.steps[i], id: undefined, name: `${d.steps[i].name} (copy)` })); break;
      case "del": if (!confirm(`Delete step ${i + 1} (${d.steps[i].name})?`)) return; d.steps.splice(i, 1); ed.open.clear(); break;
      case "kind": {
        const s = d.steps[i];
        const text = s.run ?? s.manual ?? "";
        delete s.run; delete s.manual;
        s[b.dataset.kind] = text;
        break;
      }
      case "add-step":
        d.steps.push({ name: "New step", run: "echo hello", section: d.steps.at(-1)?.section });
        ed.open.add(d.steps.length - 1);
        break;
      case "add-input": d.inputs.push({ name: "" }); break;
      case "del-input": d.inputs.splice(i, 1); break;
    }
    renderStruct(ed);
  };
}
