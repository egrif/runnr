"use strict";

// Importing runbook and library files (⇪ Import in the sidebar, the New
// runbook page and Libraries). Each file is checked first: what it is, and
// whether its name is taken. A taken name needs a choice: save it as the
// current version of the one there (its earlier versions are kept), import it
// under another name (its name: is changed), or skip it. Nothing is saved
// until Import.

function pickImportFiles(opts = {}) {
  const input = document.createElement("input");
  input.type = "file";
  input.multiple = true;
  input.accept = ".yaml,.yml,text/yaml";
  input.onchange = () => { if (input.files.length) openImport([...input.files], opts); };
  input.click();
}

// freeName is name with the first -2, -3… that isn't taken.
function freeName(name, taken) {
  for (let n = 2; ; n++) if (!taken.has(`${name}-${n}`)) return `${name}-${n}`;
}

async function openImport(files, opts = {}) {
  const items = await Promise.all(files.map(async (f) => {
    const raw = await f.text();
    const check = await api("POST", "/api/import/check", raw, true).catch((e) => ({ error: e.message }));
    return { file: f.name, raw, check };
  }));
  const libs = await api("GET", "/api/libraries").catch(() => []);
  const taken = { runbook: new Set(runbooks.map((r) => r.name)), library: new Set(libs.map((l) => l.name)) };
  for (const it of items) {
    const c = it.check;
    it.as = !c.name ? "" : c.exists ? freeName(c.name, taken[c.kind] || new Set()) : c.name;
    it.choice = c.error ? "skip" : c.same ? "skip" : c.exists ? "rename" : "add";
  }

  // Placeholders (@NAME@) the files name: kept, and filled in when they run.
  const phs = [...new Set(items.flatMap((it) => it.check.placeholders || []))].sort();
  const phList = phs.map((k) => `<code>@${esc(k)}@</code>`).join(", ");
  const dlg = document.createElement("dialog");
  dlg.className = "edit-step import-dialog";
  const anyRunbook = items.some((it) => it.check.kind === "runbook" && !it.check.error);
  dlg.innerHTML = `<h3>Import ${items.length === 1 ? "a file" : `${items.length} files`}</h3>
    ${phs.length ? `<p class="muted small imp-ph-note">They name ${phList} (folders): kept as written, and filled in with this machine's folders when they run. <a href="#/collections">Set the folders…</a></p>` : ""}
    <div class="import-items"></div>
    ${anyRunbook ? `<label class="row small">Add runbooks to collection ${colPicker("imp-col", "None")}</label>` : ""}
    <div class="error-text imp-error"></div>
    <div class="row"><button class="primary" data-ok>Import</button><button data-close>Cancel</button></div>`;
  document.body.append(dlg);
  const openDiffs = new Set();
  keepDiffsOpen(dlg, openDiffs);
  const col = dlg.querySelector("#imp-col");
  if (col) {
    if (opts.collection) col.value = opts.collection;
    col.onchange = (e) => { const v = pickedCol(e.target.value); if (v && ![...e.target.options].some((o) => o.value === v)) e.target.add(new Option(v, v), 1); e.target.value = v; };
  }
  const when = (t) => (t ? new Date(t).toLocaleString() : "");
  const draw = () => {
    dlg.querySelector(".import-items").innerHTML = items.map((it, k) => {
      const c = it.check, noun = c.kind || "file";
      let body;
      if (c.error) body = `<div class="error-text">${esc(c.error)}</div><div class="muted small">Skipped.</div>`;
      else if (c.same) body = `<div class="muted small">Same as ${noun} <b>${esc(c.name)}</b> version ${shortVer(c.version)}: nothing to import.</div>`;
      else if (!c.exists) body = `<div class="small">Adds ${noun} <b>${esc(c.name)}</b>.${c.builtin ? ` <span class="warn-text">It takes the place of runnr's built-in library of that name.</span>` : ""}</div>
        <label class="check small"><input type="radio" name="ch${k}" value="add" ${it.choice !== "skip" ? "checked" : ""}> Import as <input data-as="${k}" value="${esc(it.as)}" spellcheck="false"></label>
        <label class="check small"><input type="radio" name="ch${k}" value="skip" ${it.choice === "skip" ? "checked" : ""}> Skip</label>`;
      else body = `<div class="warn-text small">⚠ ${noun} <b>${esc(c.name)}</b> already exists: version ${shortVer(c.version)}, saved ${esc(when(c.updated_at))}.</div>
        ${diffHTML(c.diff, `f${k}`, openDiffs)}
        <label class="check small"><input type="radio" name="ch${k}" value="replace" ${it.choice === "replace" ? "checked" : ""}> Make it the current version of <b>${esc(c.name)}</b> (its earlier versions are kept)</label>
        <label class="check small"><input type="radio" name="ch${k}" value="rename" ${it.choice === "rename" ? "checked" : ""}> Import as <input data-as="${k}" value="${esc(it.as)}" spellcheck="false"></label>
        <label class="check small"><input type="radio" name="ch${k}" value="skip" ${it.choice === "skip" ? "checked" : ""}> Skip</label>`;
      if (c.placeholders?.length && !c.error) body += `<div class="muted small">Names ${c.placeholders.map((k) => `@${esc(k)}@`).join(", ")}.</div>`;
      return `<div class="import-item card"><div class="row"><b>${esc(it.file)}</b><span class="muted small">${esc(c.kind || "")}</span>${it.result ? `<span class="spacer"></span><span class="small ${it.result.ok ? "ok-text" : "error-text"}">${esc(it.result.text)}</span>` : ""}</div>${body}</div>`;
    }).join("");
    dlg.querySelectorAll("input[type=radio]").forEach((r) => (r.onchange = () => { items[+r.name.slice(2)].choice = r.value; }));
    dlg.querySelectorAll("[data-as]").forEach((inp) => {
      inp.oninput = () => {
        const it = items[+inp.dataset.as];
        it.as = inp.value.trim();
        it.choice = it.check.exists ? "rename" : "add";
        dlg.querySelector(`input[name=ch${inp.dataset.as}][value=${it.choice}]`).checked = true;
      };
    });
  };
  draw();
  dlg.querySelector("[data-close]").onclick = () => dlg.close();
  dlg.onclose = () => dlg.remove();
  dlg.querySelector("[data-ok]").onclick = async (e) => {
    const ok = e.currentTarget;
    ok.disabled = true;
    const collection = col ? col.value : "";
    const done = [];
    for (const it of items) {
      if (it.choice === "skip" || it.result?.ok) continue;
      const q = new URLSearchParams();
      if (it.choice === "rename" || it.choice === "add") {
        if (!it.as) { it.result = { ok: false, text: "give it a name" }; continue; }
        if (it.as !== it.check.name) q.set("as", it.as);
      }
      if (it.choice === "replace") q.set("replace", "1");
      if (collection && it.check.kind === "runbook") q.set("collection", collection);
      try {
        const res = await api("POST", `/api/import?${q}`, it.raw, true);
        it.result = { ok: true, text: res.replaced ? `saved as version ${shortVer(res.version)}` : `imported as ${res.name}` };
        done.push(res);
      } catch (err) { it.result = { ok: false, text: err.message }; }
    }
    ok.disabled = false;
    await loadRunbooks();
    if (items.some((it) => it.result && !it.result.ok)) { draw(); dlg.querySelector(".imp-error").textContent = "Some files weren't imported: change them and Import again, or Cancel."; return; }
    dlg.close();
    if (!done.length) return toast("Nothing imported");
    toast(done.length === 1 ? `Imported ${done[0].kind} ${done[0].name}` : `Imported ${done.length} files`);
    if (done.length === 1) location.hash = done[0].kind === "library" ? `#/libraries/${enc(done[0].name)}` : `#/runbooks/${enc(done[0].name)}`;
    else route();
  };
  dlg.showModal();
}

// ---------- a project folder ----------

// The folder import reads a folder on the machine runnr runs on (see
// package project): runbooks, libraries, fakes and test suites, with
// placeholders kept as written and references checked. Every change rescans, so
// what's shown is what Import will do.

const IMP_DIRS = "runnr.importDirs";
const impLoad = (k, d) => { try { return JSON.parse(localStorage.getItem(k) || "null") ?? d; } catch { return d; } };
const impSave = (k, v) => { try { localStorage.setItem(k, JSON.stringify(v)); } catch {} };
const KIND_LABEL = { runbook: "runbook", library: "library", fakes: "fake library", suite: "test suite", run: "run" };

function openFolderImport(dir = "") {
  const recent = impLoad(IMP_DIRS, []);
  const st = { dir: dir || recent[0] || "", renames: {}, include: [], skip: [], replace: new Set(), plan: null, results: null, autoIncluded: false, openDiffs: new Set() };
  const dlg = document.createElement("dialog");
  dlg.className = "edit-step import-dialog folder-import";
  dlg.innerHTML = `<h3>Import a project folder</h3>
    <p class="muted small">Its runbooks, libraries, fake libraries and test suites (in subfolders too), from the machine runnr runs on. Placeholders (<code>@NAME@</code>) are kept as written (a run fills them in with this machine's folders), and references checked. <button class="link" data-upload>Upload files instead…</button></p>
    <div class="row"><input class="imp-dir" placeholder="~/dev/my-repo/ops/my-project" spellcheck="false" value="${esc(st.dir)}"><button data-scan class="primary">Scan</button></div>
    <div class="row imp-recent small">${recent.map((d) => `<button class="link" data-recent="${esc(d)}" title="${esc(d)}">${esc(d.split("/").slice(-2).join("/"))}</button>`).join(" · ")}</div>
    <div class="imp-plan"></div>
    <div class="error-text imp-error"></div>
    <div class="imp-extra"><h4>Also import <span class="muted small">run records or other files from elsewhere (e.g. logs exported as records)</span></h4>
      <ul class="imp-extra-list small"></ul>
      <div class="row"><button type="button" data-add-file>📄 Add a file…</button><button type="button" data-add-dir>📁 Add a folder…</button></div></div>
    <div class="row imp-actions" hidden>${colPicker("imp-col2", "No collection")}<span class="spacer"></span><button class="primary" data-ok>Import</button><button data-close>Cancel</button></div>`;
  document.body.append(dlg);
  keepDiffsOpen(dlg, st.openDiffs);
  const $d = (s) => dlg.querySelector(s);
  const err = (m) => ($d(".imp-error").textContent = m || "");
  $d("[data-upload]").onclick = () => { dlg.close(); pickImportFiles(); };
  $d("[data-close]").onclick = () => dlg.close();
  dlg.onclose = () => dlg.remove();
  $d("#imp-col2").onchange = (e) => { const v = pickedCol(e.target.value); if (v && ![...e.target.options].some((o) => o.value === v)) e.target.add(new Option(v, v), 1); e.target.value = v; };
  dlg.querySelectorAll("[data-recent]").forEach((b) => (b.onclick = () => { $d(".imp-dir").value = b.dataset.recent; scan(true); }));
  $d("[data-scan]").onclick = () => scan(true);
  $d(".imp-dir").onkeydown = (e) => { if (e.key === "Enter") scan(true); };

  const body = () => ({ dir: st.dir, renames: st.renames, include: st.include, skip: st.skip });
  let seq = 0;
  async function scan(fresh) {
    if (fresh) {
      st.dir = $d(".imp-dir").value.trim();
      // Files added from elsewhere stay; libraries found for the last folder go.
      const extra = st.include.filter((p) => !st.autoFound?.has(p));
      Object.assign(st, { renames: {}, include: extra, autoFound: new Set(), skip: [], replace: new Set(), results: null, autoIncluded: false });
    }
    if (!st.dir && !st.include.length) return err("Give a folder");
    const mine = ++seq;
    err("");
    try {
      const plan = await api("POST", "/api/import/scan", body());
      if (mine !== seq) return;
      st.plan = plan;
      if (fresh) impSave(IMP_DIRS, [st.dir, ...recent.filter((d) => d !== st.dir)].slice(0, 6));
      // A library that's missing but defined once elsewhere in the repository comes along (it can be left out).
      if (!st.autoIncluded) {
        st.autoIncluded = true;
        const found = plan.docs.flatMap((d) => d.refs || []).filter((r) => !r.found && r.candidates?.length === 1).map((r) => r.candidates[0]);
        const add = [...new Set(found)].filter((c) => !st.include.includes(c));
        if (add.length) { st.include.push(...add); (st.autoFound ||= new Set()); add.forEach((a) => st.autoFound.add(a)); return scan(false); }
      }
      draw();
    } catch (e) { if (mine === seq) { st.plan = null; draw(); err(e.message); } }
  }

  const drawExtra = () => {
    const extra = st.include.filter((p) => !st.autoFound?.has(p));
    $d(".imp-extra-list").innerHTML = extra.map((p) => `<li><code>${esc(p)}</code> <button class="link" data-unadd="${esc(p)}">✕</button></li>`).join("") || `<li class="muted">Nothing yet</li>`;
    dlg.querySelectorAll("[data-unadd]").forEach((b) => (b.onclick = () => { st.include = st.include.filter((x) => x !== b.dataset.unadd); drawExtra(); scan(false); }));
  };
  const addExtra = async (kind) => {
    const p = await pickPath({ kind, start: impLoad("runnr.importExtraDir", st.dir || "~"), title: kind === "dir" ? "Add a folder to import" : "Add a file to import" });
    if (!p) return;
    impSave("runnr.importExtraDir", kind === "dir" ? p : p.replace(/\/[^/]*$/, ""));
    if (!st.include.includes(p)) st.include.push(p);
    drawExtra();
    scan(false);
  };
  $d("[data-add-file]").onclick = () => addExtra("file");
  $d("[data-add-dir]").onclick = () => addExtra("dir");
  drawExtra();

  function draw() {
    const p = st.plan;
    $d(".imp-actions").hidden = !p;
    if (!p) return ($d(".imp-plan").innerHTML = "");
    const used = [...new Set(p.docs.flatMap((d) => d.placeholders || []))].sort();
    const res = Object.fromEntries((st.results || []).map((r) => [r.rel, r]));
    const neededBy = {};
    for (const d of p.docs) for (const r of d.refs || []) (neededBy[`${r.kind}:${r.name}`] ||= []).push(d.name);
    const row = (d) => {
      const key = `${d.kind}:${d.original}`, skipped = st.skip.includes(d.path), r = res[d.rel];
      const replaceBox = `<label class="check"><input type="checkbox" data-replace="${esc(d.path)}" ${st.replace.has(d.path) ? "checked" : ""}> make it the current version (earlier versions are kept)</label>`;
      let state;
      if (d.error) state = `<span class="error-text">${esc(d.error)}</span>`;
      else if (d.same) state = `<span class="muted">${d.kind === "run" ? "already in the history" : `unchanged (version ${shortVer(d.version)})`}</span>`;
      else if (d.exists) state = `<span class="warn-text">⚠ name taken: version ${shortVer(d.version)}, saved ${esc(new Date(d.updated_at).toLocaleString())}</span> ${replaceBox}`;
      else state = `<span class="ok-text">new</span>${d.builtin ? ` <span class="warn-text">(takes the place of runnr's built-in library)</span>` : ""}`;
      if (d.placeholders?.length && !d.error) state += `<div class="small muted">names ${d.placeholders.map((k) => `@${esc(k)}@`).join(", ")}</div>`;
      const missing = (d.refs || []).filter((x) => !x.found);
      const refs = missing.map((x) => `<div class="small error-text">needs ${esc(KIND_LABEL[x.kind])} ${esc(x.name)}${x.candidates?.length ? `: ${x.candidates.map((c) => `<label class="check"><input type="checkbox" data-include="${esc(c)}" ${st.include.includes(c) ? "checked" : ""}> import ${esc(c.replace(p.repo_root + "/", ""))}</label>`).join(" ")}` : " (not in this folder, the repository or the store)"}</div>`).join("");
      return `<tr class="${skipped ? "skipped" : ""}">
        <td><input type="checkbox" data-take="${esc(d.path)}" ${skipped ? "" : "checked"} ${d.error ? "disabled" : ""} title="Import it"></td>
        <td class="small">${esc(KIND_LABEL[d.kind] || "")}</td>
        <td>${d.kind === "run" ? `<code>${esc(d.name)}</code>` : d.error && !d.name ? "" : `<input class="imp-name" data-rename="${esc(key)}" value="${esc(st.renames[key] ?? d.name)}" spellcheck="false" ${d.kind === "suite" && !d.original ? "" : ""}>`}
          <div class="muted small">${esc(d.rel)}${d.included ? (neededBy[`${d.kind}:${d.name}`] ? ` · from the repository, needed by ${esc(neededBy[`${d.kind}:${d.name}`].join(", "))}` : " · added from elsewhere") : ""}</div>${refs}</td>
        <td class="small">${r ? `<b class="${["failed", "exists"].includes(r.status) ? "error-text" : "ok-text"}">${esc(r.status)}${r.version ? ` · ${shortVer(r.version)}` : ""}</b>${r.error ? `<div class="error-text">${esc(r.error)}</div>` : ""}${r.status === "exists" ? `<div>${replaceBox}</div>` : ""}` : state}</td></tr>
        ${d.diff && !skipped && (!r || r.status === "exists") ? `<tr class="imp-diff-row"><td></td><td colspan="3">${diffHTML(d.diff, d.path, st.openDiffs)}</td></tr>` : ""}`;
    };
    const phList = used.map((k) => `<code>@${esc(k)}@</code>`).join(", ");
    $d(".imp-plan").innerHTML = `
      <div class="muted small">Folder <code>${esc(p.dir)}</code>${p.repo_root ? ` in repository <code>${esc(p.repo_root)}</code>` : ""}</div>
      ${used.length ? `<p class="muted small imp-ph-note">They name ${phList} (folders): kept as written, and filled in with this machine's folders when they run. <a href="#/collections">Set the folders…</a></p>` : ""}
      <h4>Files</h4>
      ${p.docs.length ? `<table class="imp-docs">${p.docs.map(row).join("")}</table>` : `<p class="muted">No runnr files here.</p>`}
      ${p.ignored?.length ? `<details class="small muted"><summary>${p.ignored.length} other YAML file${p.ignored.length === 1 ? "" : "s"} left alone</summary>${p.ignored.map(esc).join("<br>")}</details>` : ""}`;
    dlg.querySelectorAll("[data-rename]").forEach((i) => (i.onchange = () => { const v = i.value.trim(), k = i.dataset.rename; if (!v || v === k.split(":")[1]) delete st.renames[k]; else st.renames[k] = v; scan(false); }));
    dlg.querySelectorAll("[data-take]").forEach((c) => (c.onchange = () => { st.skip = c.checked ? st.skip.filter((x) => x !== c.dataset.take) : [...st.skip, c.dataset.take]; scan(false); }));
    dlg.querySelectorAll("[data-include]").forEach((c) => (c.onchange = () => { st.include = c.checked ? [...st.include, c.dataset.include] : st.include.filter((x) => x !== c.dataset.include); scan(false); }));
    dlg.querySelectorAll("[data-replace]").forEach((c) => (c.onchange = () => { c.checked ? st.replace.add(c.dataset.replace) : st.replace.delete(c.dataset.replace); }));
    const todo = p.docs.filter((d) => !d.error && !d.same && !st.skip.includes(d.path));
    $d("[data-ok]").textContent = st.results ? "Import again" : `Import${todo.length ? ` ${todo.length} file${todo.length === 1 ? "" : "s"}` : ""}`;
  }

  $d("[data-ok]").onclick = async (e) => {
    const ok = e.currentTarget;
    ok.disabled = true;
    try {
      const out = await api("POST", "/api/import/project", { ...body(), replace: [...st.replace], collection: $d("#imp-col2").value });
      st.plan = out.plan;
      st.results = out.results;
      await loadRunbooks();
      const bad = out.results.filter((r) => ["failed", "exists"].includes(r.status));
      const n = out.results.filter((r) => ["imported", "replaced"].includes(r.status)).length;
      draw();
      err(bad.length ? `${bad.length} file${bad.length === 1 ? "" : "s"} not imported: see why above, change it and Import again.` : "");
      toast(n ? `Imported ${n} file${n === 1 ? "" : "s"}` : "Nothing new to import");
      if (!bad.length) { $d("[data-close]").textContent = "Done"; }
    } catch (x) { err(x.message); }
    ok.disabled = false;
  };
  dlg.showModal();
  if (st.dir) scan(true); else $d(".imp-dir").focus();
}

// diffHTML shows what saving over a stored runbook (or library) would
// change: a unified diff, folded until asked for. open keeps which are
// unfolded across redraws (keyed by key).
function diffHTML(diff, key, open) {
  if (!diff) return "";
  const lines = diff.replace(/\n$/, "").split("\n");
  const add = lines.filter((l) => l.startsWith("+") && !l.startsWith("+++")).length;
  const rm = lines.filter((l) => l.startsWith("-") && !l.startsWith("---")).length;
  const cls = (l) => l.startsWith("+++") || l.startsWith("---") ? "d-file" : l.startsWith("@@") ? "d-hunk" : l.startsWith("+") ? "d-add" : l.startsWith("-") ? "d-rm" : "";
  return `<details class="imp-diff" data-diff="${esc(key)}" ${open?.has(key) ? "open" : ""}><summary>Show what it changes <span class="d-add">+${add}</span> <span class="d-rm">−${rm}</span></summary><pre class="diff">${lines.map((l) => `<span class="${cls(l)}">${esc(l) || " "}</span>`).join("\n")}</pre></details>`;
}

// keepDiffsOpen remembers which diffs in root are unfolded.
function keepDiffsOpen(root, open) {
  root.addEventListener("toggle", (e) => {
    const k = e.target.dataset?.diff;
    if (k) e.target.open ? open.add(k) : open.delete(k);
  }, true);
}

// importRunsDialog imports runs another runnr exported: records (.json),
// raw logs (.log), a zip of them, or a folder of them, picked on this
// machine or sent from the browser's. They're checked first: a run already
// here is left as it is, one exported while still going comes in aborted,
// and one from a raw log is rebuilt from its text (close, not exact).
function importRunsDialog(onDone) {
  const dlg = document.createElement("dialog");
  dlg.className = "edit-step run-import";
  dlg.innerHTML = `<h3>Import runs</h3>
    <p class="muted small">Runs exported from another runnr (or a backup): records (<code>.json</code>), raw logs (<code>.log</code>), the zip the export downloads, or a folder of them. A run from a raw log is rebuilt from its text: close, not exact (times to the second; no sections or state). Where there's both for a run, the record is used.</p>
    <div class="row"><button data-files>Choose files…</button><button data-folder>Choose a folder…</button><button data-upload title="Files on the computer this browser runs on">From this computer…</button></div>
    <div class="ri-found"></div>
    <div class="error-text"></div>
    <div class="row"><button class="primary" data-ok disabled>Import</button><button data-close>Cancel</button></div>`;
  document.body.append(dlg);
  const $d = (q) => dlg.querySelector(q);
  const src = { paths: [], uploads: [] };
  let found = [];
  const sel = new Set();
  let imported = false;
  const check = async () => {
    $d(".error-text").textContent = "";
    try {
      const res = await api("POST", "/api/runs/import", { ...src, check: true });
      found = res.runs;
      sel.clear();
      found.forEach((r) => !r.exists && sel.add(r.id));
      draw(res.errors);
    } catch (e) { $d(".error-text").textContent = e.message; }
  };
  const draw = (errors = []) => {
    const where = [...src.paths, ...src.uploads.map((u) => u.name)];
    $d(".ri-found").innerHTML = `${where.length ? `<p class="small muted">From ${where.map((p) => `<code title="${esc(p)}">${esc(p.split("/").pop() || p)}${src.paths.includes(p) && !/\.(json|log|txt|zip)$/i.test(p) ? "/" : ""}</code>`).join(", ")}</p>` : ""}
      ${found.length ? `<table class="history ri-table"><thead><tr><th><input type="checkbox" data-all ${sel.size && sel.size === found.filter((r) => !r.exists).length ? "checked" : ""}></th><th>Status</th><th>Runbook</th><th>Started</th><th>Run ID</th><th></th></tr></thead><tbody>
        ${found.map((r) => `<tr class="${r.exists ? "muted" : ""}"><td><input type="checkbox" data-id="${esc(r.id)}" ${sel.has(r.id) ? "checked" : ""} ${r.exists ? "disabled" : ""}></td>
          <td><span class="s-${esc(r.status)}">${ICONS[r.status] || ""} ${esc(r.status)}</span></td><td>${esc(r.runbook)}</td><td>${new Date(r.started).toLocaleString()}</td>
          <td><code>${esc(r.id)}</code></td><td class="small">${r.exists ? "already here" : [r.unfinished && "was still going: comes in aborted", r.from_log && "from a raw log"].filter(Boolean).join("; ")}</td></tr>`).join("")}</tbody></table>` : where.length ? `<p class="muted">No runs to import.</p>` : ""}
      ${errors.length ? `<ul class="error-text small">${errors.map((e) => `<li><code>${esc(e.from)}</code>: ${esc(e.error)}</li>`).join("")}</ul>` : ""}`;
    $d("[data-ok]").disabled = !sel.size;
    $d("[data-ok]").textContent = sel.size ? `Import ${sel.size} run${sel.size === 1 ? "" : "s"}` : "Import";
  };
  dlg.addEventListener("change", (e) => {
    const t = e.target;
    if (t.matches("[data-all]")) { found.forEach((r) => !r.exists && (t.checked ? sel.add(r.id) : sel.delete(r.id))); draw(); }
    else if (t.matches("[data-id]")) { t.checked ? sel.add(t.dataset.id) : sel.delete(t.dataset.id); draw(); }
  });
  const start = () => impLoad("runnr.importRunsDir", "~");
  const remember = (p) => { try { localStorage.setItem("runnr.importRunsDir", JSON.stringify(p.replace(/\/[^/]*$/, "") || "/")); } catch {} };
  $d("[data-files]").onclick = async () => {
    const ps = await pickPath({ kind: "file", multi: true, accept: [".json", ".log", ".txt", ".zip"], start: start(), title: "Choose runs to import" });
    if (!ps?.length) return;
    remember(ps[0]);
    src.paths.push(...ps.filter((p) => !src.paths.includes(p)));
    check();
  };
  $d("[data-folder]").onclick = async () => {
    const p = await pickPath({ kind: "dir", start: start(), title: "Choose a folder of exported runs" });
    if (!p) return;
    try { localStorage.setItem("runnr.importRunsDir", JSON.stringify(p)); } catch {}
    if (!src.paths.includes(p)) src.paths.push(p);
    check();
  };
  $d("[data-upload]").onclick = () => {
    const input = document.createElement("input");
    input.type = "file";
    input.multiple = true;
    input.accept = ".json,.log,.txt,.zip,application/json,application/zip,text/plain";
    input.onchange = async () => {
      for (const f of input.files) {
        const b = new Uint8Array(await f.arrayBuffer());
        let bin = "";
        for (let i = 0; i < b.length; i += 0x8000) bin += String.fromCharCode(...b.subarray(i, i + 0x8000));
        src.uploads.push({ name: f.name, data: btoa(bin) });
      }
      check();
    };
    input.click();
  };
  $d("[data-ok]").onclick = async (e) => {
    e.currentTarget.disabled = true;
    try {
      const res = await api("POST", "/api/runs/import", { ...src, ids: [...sel] });
      const n = res.runs.filter((r) => r.imported).length;
      imported = n > 0;
      toast(`Imported ${n} run${n === 1 ? "" : "s"}`);
      dlg.close();
    } catch (x) { $d(".error-text").textContent = x.message; e.currentTarget.disabled = false; }
  };
  $d("[data-close]").onclick = () => dlg.close();
  dlg.onclose = () => { dlg.remove(); if (imported) onDone?.(); };
  dlg.showModal();
}

// scrubDialog masks credentials in the stored history (runnr history
// --scrub): it looks first and lists the runs it would change, then masks
// them and compacts the database.
async function scrubDialog(onDone) {
  const dlg = document.createElement("dialog");
  dlg.className = "edit-step abort-dialog scrub-dialog";
  dlg.innerHTML = `<h3>Scrub credentials from the history</h3>
    <p class="muted small">Masks passwords, tokens and keys in every stored run (its output, commands, notes and reasons), as exports are masked: the built-in patterns, each runbook's own, and values held under credential names. Then the database is compacted so the old text isn't left in it. Runs still going are left alone. This can't be undone.</p>
    <div class="scrub-found muted">Looking…</div>
    <div class="error-text"></div>
    <div class="row"><button class="danger" data-ok disabled>Scrub</button><button data-close>Close</button></div>`;
  document.body.append(dlg);
  const $d = (q) => dlg.querySelector(q);
  let changed = false;
  const list = (res) => res.runs.map((r) => `<li><code>${esc(r.id)}</code> ${esc(r.runbook)}: ${r.running ? "still running, left alone"
    : [r.record && "output and record", r.notes && `${r.notes} note${r.notes === 1 ? "" : "s"}`, r.asks && `${r.asks} reason${r.asks === 1 ? "" : "s"}`].filter(Boolean).join(", ")}</li>`).join("");
  $d("[data-close]").onclick = () => dlg.close();
  dlg.onclose = () => { dlg.remove(); if (changed) onDone?.(); };
  dlg.showModal();
  try {
    const res = await api("POST", "/api/runs/scrub", { check: true });
    const n = res.runs.filter((r) => !r.running).length;
    $d(".scrub-found").innerHTML = n ? `<p><b>${n} of ${res.total} run${res.total === 1 ? "" : "s"}</b> have credentials to mask:</p><ul class="small">${list(res)}</ul>`
      : `<p>Nothing to mask in ${res.total} run${res.total === 1 ? "" : "s"}.</p>${res.runs.length ? `<ul class="small">${list(res)}</ul>` : ""}`;
    $d("[data-ok]").disabled = !n;
    $d("[data-ok]").textContent = n ? `Scrub ${n} run${n === 1 ? "" : "s"}` : "Scrub";
  } catch (e) { $d(".error-text").textContent = e.message; }
  $d("[data-ok]").onclick = async (e) => {
    e.currentTarget.disabled = true;
    try {
      const res = await api("POST", "/api/runs/scrub", {});
      changed = true;
      const n = res.runs.filter((r) => !r.running).length;
      toast(`Masked credentials in ${n} run${n === 1 ? "" : "s"}${res.compacted ? "; database compacted" : ""}`);
      dlg.close();
    } catch (x) { $d(".error-text").textContent = x.message; e.currentTarget.disabled = false; }
  };
}
