"use strict";

// Importing runbook and library files (⇪ Import in the sidebar, the New
// runbook page and Libraries). Each file is checked first: what it is, and
// whether its name is taken. A taken name needs a choice: save it as the
// next version of the one there (its earlier versions are kept), import it
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

  const dlg = document.createElement("dialog");
  dlg.className = "edit-step import-dialog";
  const anyRunbook = items.some((it) => it.check.kind === "runbook" && !it.check.error);
  dlg.innerHTML = `<h3>Import ${items.length === 1 ? "a file" : `${items.length} files`}</h3>
    <div class="import-items"></div>
    ${anyRunbook ? `<label class="row small">Add runbooks to collection ${colPicker("imp-col", "None")}</label>` : ""}
    <div class="error-text imp-error"></div>
    <div class="row"><button class="primary" data-ok>Import</button><button data-close>Cancel</button></div>`;
  document.body.append(dlg);
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
      else if (c.same) body = `<div class="muted small">Same as ${noun} <b>${esc(c.name)}</b> version ${c.version}: nothing to import.</div>`;
      else if (!c.exists) body = `<div class="small">Adds ${noun} <b>${esc(c.name)}</b>.${c.builtin ? ` <span class="warn-text">It takes the place of runnr's built-in library of that name.</span>` : ""}</div>
        <label class="check small"><input type="radio" name="ch${k}" value="add" ${it.choice !== "skip" ? "checked" : ""}> Import as <input data-as="${k}" value="${esc(it.as)}" spellcheck="false"></label>
        <label class="check small"><input type="radio" name="ch${k}" value="skip" ${it.choice === "skip" ? "checked" : ""}> Skip</label>`;
      else body = `<div class="warn-text small">⚠ ${noun} <b>${esc(c.name)}</b> already exists: version ${c.version}, saved ${esc(when(c.updated_at))}.</div>
        <label class="check small"><input type="radio" name="ch${k}" value="replace" ${it.choice === "replace" ? "checked" : ""}> Save as version ${c.version + 1} of <b>${esc(c.name)}</b> (its earlier versions are kept)</label>
        <label class="check small"><input type="radio" name="ch${k}" value="rename" ${it.choice === "rename" ? "checked" : ""}> Import as <input data-as="${k}" value="${esc(it.as)}" spellcheck="false"></label>
        <label class="check small"><input type="radio" name="ch${k}" value="skip" ${it.choice === "skip" ? "checked" : ""}> Skip</label>`;
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
        it.result = { ok: true, text: res.replaced ? `saved as version ${res.version}` : `imported as ${res.name}` };
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
