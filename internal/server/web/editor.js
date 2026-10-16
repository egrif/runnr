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

// stepRight holds the step's settings, grouped: common things first, related
// things together, rarely used groups folded away.
function stepRight(d, s, i) {
  const kind = kindOf(s);
  const P = `steps.${i}`;
  const isRun = kind === "run";
  const later = d.steps.slice(i + 1).map((o, k) => [o.id || o.name, `${i + k + 2}. ${o.name}`]);
  const flags = [
    row("Confirm first", H.step.confirm, control(`${P}.confirm`, "bool", s.confirm)),
    row("Always run", H.step.always, control(`${P}.always`, "bool", s.always)),
    row("Keep going if it fails", H.step.continue_on_error, control(`${P}.continue_on_error`, "bool", s.continue_on_error)),
    isRun && row("Needs a terminal", H.step.tty, control(`${P}.tty`, "bool", s.tty)),
    isRun && row("Run in background", H.step.background, control(`${P}.background`, "bool", s.background)),
    isRun && s.background && row("Stop after", H.step.stop_after, control(`${P}.stop_after`, "select", s.stop_after, { choices: [["", "when the run ends"], ...later] })),
  ];
  return [
    group("Step", [
      row("Name", H.step.name, control(`${P}.name`, "text", s.name)),
      row("Section", H.step.section, control(`${P}.section`, "text", s.section)),
      row("Owner", H.step.owner, control(`${P}.owner`, "text", s.owner)),
      row("Expected", H.step.expect, control(`${P}.expect`, "area", s.expect, { placeholder: "what success looks like" })),
    ]),
    group("Behaviour", flags),
    kind === "checks" && group("Repeat until all pass", [
      row("Every", H.step.repeat_every, control(`${P}.repeat.every`, "text", s.repeat?.every, { placeholder: "e.g. 20s (blank: don't repeat)" })),
      row("Give up after", H.step.repeat_timeout, control(`${P}.repeat.timeout`, "text", s.repeat?.timeout, { placeholder: "e.g. 10m" })),
    ]),
    group("Conditions & timing", [
      row("Only when", H.step.when, control(`${P}.when`, "text", s.when, { placeholder: '{{eq .env "production"}}' })),
      kind !== "manual" && row("Timeout", H.step.timeout, control(`${P}.timeout`, "text", s.timeout, { placeholder: "e.g. 10m" })),
      isRun && row("Retries", H.step.retries, control(`${P}.retries`, "int", s.retries)),
      isRun && row("Retry delay", H.step.retry_delay, control(`${P}.retry_delay`, "text", s.retry_delay, { placeholder: "e.g. 10s" })),
    ], { collapsible: true, id: "timing", open: hasAny(s, ["when", "timeout", "retries", "retry_delay"]) }),
    kind !== "manual" && group("Where it runs", [
      row("Directory", H.step.dir, control(`${P}.dir`, "text", s.dir)),
      row("Shell", H.step.step_shell, control(`${P}.shell`, "text", s.shell, { placeholder: d.shell || "sh" })),
      row("Environment", H.step.step_env, control(`${P}.env`, "kv", s.env, { placeholder: "KEY=value" })),
    ], { collapsible: true, id: "where", open: hasAny(s, ["dir", "shell", "env"]) }),
    (isRun || kind === "alternatives") && !s.background && group("Publishes to later steps", [
      row("Values", H.step.outputs, control(`${P}.outputs`, "list", s.outputs, { placeholder: "var1, var2" })),
      row("Secret values", H.step.secret_outputs, control(`${P}.secret_outputs`, "list", s.secret_outputs)),
      isRun && row("Capture stdout as", H.step.capture, control(`${P}.capture`, "text", s.capture)),
    ], { collapsible: true, id: "outputs", open: hasAny(s, ["outputs", "secret_outputs", "capture"]) }),
    group("Advanced", [
      row("ID", H.step.id, control(`${P}.id`, "text", s.id)),
      row("When unattended", H.step.unattended, control(`${P}.unattended`, "select", s.unattended, { choices: [["", "fail (default)"], ["fail", "fail"], ["skip", "skip"], ["pass", "pass"]] })),
    ], { collapsible: true, id: "advanced", open: hasAny(s, ["id", "unattended"]) }),
  ].filter(Boolean).join("");
}

function renderStruct(ed) {
  const d = ed.doc;
  d.inputs ||= [];
  d.steps ||= [];
  const pane = $("#struct-pane");
  pane.innerHTML = `
    <div class="card">
      <h3>Runbook</h3>
      <div class="fgroup-rows">
        ${row("Name", H.runbook.name, control("name", "text", d.name))}
        ${row("Description", H.runbook.description, control("description", "area", d.description))}
        ${row("Tags", H.runbook.tags, control("tags", "list", d.tags, { placeholder: "ops, deploy" }))}
        ${row("Shell", H.runbook.shell, control("shell", "text", d.shell, { placeholder: "sh" }))}
      </div>
      ${group("Environment, profile & masking", [
        row("Environment", H.runbook.env, control("env", "kv", d.env, { placeholder: "KEY=value" })),
        row("Profile", H.runbook.profile, control("profile", "list", d.profile, { placeholder: "~/.zshrc" })),
        row("Sensitive patterns", H.runbook.sensitive, control("sensitive", "kv", d.sensitive, { placeholder: "api_key=X-Api-Key: {{SENSITIVE}}" })),
      ], { collapsible: true, id: "rb-more", open: hasAny(d, ["env", "profile", "sensitive"]) })}
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
    <h3>Steps${helpIcon(H.runbook.steps)}</h3>
    <ol class="steps struct-steps">${d.steps.map((s, i) => `${sectionHeading(d.steps, i)}<li class="step">
        <div class="step-head" data-act="open" data-i="${i}">
          <span class="n">${i + 1}</span><span class="name">${esc(s.name || "(unnamed)")} ${stepBadges(s)}</span>
          <span class="spacer"></span>
          <button class="icon-btn" data-act="up" data-i="${i}" title="Move up" ${i === 0 ? "disabled" : ""}>↑</button>
          <button class="icon-btn" data-act="down" data-i="${i}" title="Move down" ${i === d.steps.length - 1 ? "disabled" : ""}>↓</button>
          <button class="icon-btn" data-act="dup" data-i="${i}" title="Duplicate">⧉</button>
          <button class="icon-btn danger" data-act="del" data-i="${i}" title="Delete">✕</button>
        </div>
        ${ed.open.has(i) ? `<div class="step-body step-edit">
          <div class="se-left" style="${ed.leftWidth ? `width:${ed.leftWidth}px` : ""}">${stepLeft(s, i)}</div>
          <div class="se-right">${stepRight(d, s, i)}</div>
        </div>` : ""}
      </li>`).join("")}</ol>
    <button data-act="add-step">+ Add step</button>`;

  // Remember the command pane's width when it's resized.
  pane.querySelectorAll(".se-left").forEach((el) => new ResizeObserver(() => {
    if (el.offsetWidth) ed.leftWidth = el.offsetWidth;
  }).observe(el));

  pane.oninput = pane.onchange = (e) => {
    const k = e.target.closest("[data-act-change]");
    if (k && e.type === "change") {
      const sub = d.steps[+k.dataset.i].alternatives[+k.dataset.j].steps[+k.dataset.k];
      const text = sub.run ?? sub.manual ?? "";
      delete sub.run; delete sub.manual;
      sub[k.value] = text;
      return renderStruct(ed);
    }
    const el = e.target.closest("[data-path]");
    if (!el) return;
    setPath(d, el.dataset.path, readField(el));
    // Structure-changing fields redraw the form.
    if (e.type === "change" && /\.(name|background)$/.test(el.dataset.path)) renderStruct(ed);
  };
  pane.onclick = (e) => {
    const b = e.target.closest("[data-act]");
    if (!b || b.tagName === "SELECT") return;
    e.preventDefault();
    const i = +b.dataset.i, j = +b.dataset.j, k = +b.dataset.k;
    const s = d.steps[i];
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
      case "dup": d.steps.splice(i + 1, 0, structuredClone({ ...s, id: undefined, name: `${s.name} (copy)` })); break;
      case "del": if (!confirm(`Delete step ${i + 1} (${s.name})?`)) return; d.steps.splice(i, 1); ed.open.clear(); break;
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
    }
    renderStruct(ed);
  };
}
