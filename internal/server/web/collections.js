"use strict";

// Collections group runbooks that belong together, with what they're for.

let collections = [];

async function loadCollections() {
  collections = await api("GET", "/api/collections").catch(() => []);
}

// colPicker is a select of collections (minus exclude), with "+ New collection…".
function colPicker(id, first, exclude = []) {
  return `<select id="${id}" class="col-pick"><option value="">${esc(first)}</option>
    ${collections.filter((c) => !exclude.includes(c.name)).map((c) => `<option value="${esc(c.name)}">${esc(c.name)}</option>`).join("")}
    <option value="__new__">+ New collection…</option></select>`;
}

// pickedCol turns a colPicker value into a collection name ("" for none).
function pickedCol(v) {
  if (v !== "__new__") return v;
  const n = (prompt("New collection name (lowercase letters, digits, '.', '_' or '-'):") || "").trim();
  return n;
}

// membership shows which collections a runbook is in, with remove, move and add.
function membership(name) {
  const inCols = collections.filter((c) => c.collection?.runbooks.includes(name)).map((c) => c.name);
  return `<div class="membership">
    ${inCols.map((c) => `<span class="tag">in <a href="#/collections/${enc(c)}">${esc(c)}</a> <button class="link small-x" data-col-remove="${esc(c)}" title="Take it out of ${esc(c)} (the runbook stays)">✕</button></span>`).join("")}
    ${inCols.length ? colPicker("col-move", "Move to…", inCols) : ""}
    ${colPicker("col-add", inCols.length ? "Also add to…" : "Add to collection…", inCols)}
  </div>`;
}

function bindMembership(el, name, after) {
  const done = async (msg) => { toast(msg); await loadRunbooks(); after(); };
  el.querySelectorAll("[data-col-remove]").forEach((b) => (b.onclick = async () => {
    try { await api("DELETE", `/api/collections/${enc(b.dataset.colRemove)}/runbooks/${enc(name)}`); done(`Removed from ${b.dataset.colRemove}`); }
    catch (e) { toast(e.message); }
  }));
  const add = el.querySelector("#col-add");
  if (add) add.onchange = async () => {
    const c = pickedCol(add.value);
    if (!c) return (add.value = "");
    try { await api("POST", `/api/collections/${enc(c)}/runbooks`, { runbooks: [name], create: true }); done(`Added to ${c}`); }
    catch (e) { toast(e.message); add.value = ""; }
  };
  const move = el.querySelector("#col-move");
  if (move) move.onchange = async () => {
    const to = pickedCol(move.value);
    if (!to) return (move.value = "");
    const inCols = collections.filter((c) => c.collection?.runbooks.includes(name)).map((c) => c.name);
    let from = "";
    if (inCols.length > 1) {
      from = (prompt(`Move out of which collection? (${inCols.join(", ")})`, inCols[0]) || "").trim();
      if (!from) return (move.value = "");
    }
    try { const r = await api("POST", `/api/runbooks/${enc(name)}/move`, { to, from }); done(`Moved ${r.from ? `from ${r.from} ` : ""}to ${to}`); }
    catch (e) { toast(e.message); move.value = ""; }
  };
}

async function viewCollections() {
  await loadCollections();
  main.innerHTML = `
    <div class="row"><h1>Collections</h1><span class="spacer"></span><button class="primary" id="new-col">+ New collection</button></div>
    <p class="muted">Groups of runbooks that belong together (say, the prep, cutover and fallback runbooks of one migration), with what they're for. The sidebar lists runbooks by collection.</p>
    ${collections.length ? collections.map((c) => `<div class="card col-card" data-col="${esc(c.name)}">
      <h3>${esc(c.name)}</h3>
      ${c.collection ? `<p>${esc(c.collection.description || "")}</p>
        ${c.collection.expect ? `<p class="muted"><b>By the end:</b> ${esc(c.collection.expect)}</p>` : ""}
        <ol>${c.collection.runbooks.map((r) => `<li><a href="#/runbooks/${enc(r)}">${esc(r)}</a></li>`).join("")}</ol>` : `<p class="error-text">${esc(c.error)}</p>`}
    </div>`).join("") : `<div class="empty">No collections yet.</div>`}`;
  $("#new-col").onclick = () => (location.hash = "#/collections/new");
  main.querySelectorAll("[data-col] h3").forEach((h) => (h.onclick = () => (location.hash = `#/collections/${enc(h.closest("[data-col]").dataset.col)}`)));
}

async function viewCollection(name) {
  const isNew = name === "new";
  const d = isNew ? { raw: `name: my-collection\ndescription: What this group of runbooks is for.\nexpect: How things stand once they've all run.\nrunbooks: [${runbooks.slice(0, 2).map((r) => r.name).join(", ")}]\n` }
    : await api("GET", `/api/collections/${enc(name)}`);
  const c = d.collection;
  main.innerHTML = `
    <h1>${isNew ? "New collection" : esc(name)}</h1>
    ${c ? `<p>${esc(c.description || "")}</p>${c.expect ? `<p class="muted"><b>By the end:</b> ${esc(c.expect)}</p>` : ""}
      <ol class="col-runbooks">${c.runbooks.map((r) => {
        const rb = runbooks.find((x) => x.name === r);
        return `<li><a href="#/runbooks/${enc(r)}">${esc(r)}</a> <span class="muted">${esc(rb ? (rb.description || "").split("\n")[0] : "(no such runbook yet)")}</span>
          <span class="col-row-actions"><select data-move-rb="${esc(r)}"><option value="">Move to…</option>${collections.filter((x) => x.name !== name).map((x) => `<option value="${esc(x.name)}">${esc(x.name)}</option>`).join("")}<option value="__new__">+ New collection…</option></select>
          <button class="link" data-remove-rb="${esc(r)}" title="Take it out of this collection (the runbook stays)">remove</button></span></li>`;
      }).join("")}</ol>
      ${runbooks.some((r) => !c.runbooks.includes(r.name)) ? `<select id="add-rb"><option value="">+ Add a runbook…</option>${runbooks.filter((r) => !c.runbooks.includes(r.name)).map((r) => `<option value="${esc(r.name)}">${esc(r.name)}</option>`).join("")}</select>` : ""}` : ""}
    <div class="row" style="margin:12px 0 8px">
      <button class="primary" id="save">Save</button>
      ${d.version ? `<span class="muted">version ${d.version}</span>` : ""}
      <span class="spacer"></span>${isNew ? "" : `<button class="danger" id="delete">Delete</button>`}
    </div>
    <div id="validation" class="validation">${d.error ? esc(d.error) : ""}</div>
    <textarea class="editor" id="yaml" spellcheck="false" style="min-height:30vh">${esc(d.raw)}</textarea>
    <p class="muted small">Fields: name, description, expect (how things stand once they've all run), runbooks (in the order they're usually run).</p>`;
  const reload = async (msg) => { toast(msg); await loadRunbooks(); route(); };
  main.querySelectorAll("[data-move-rb]").forEach((sel) => (sel.onchange = async () => {
    const to = pickedCol(sel.value);
    if (!to) return (sel.value = "");
    try { await api("POST", `/api/runbooks/${enc(sel.dataset.moveRb)}/move`, { to, from: name }); reload(`Moved ${sel.dataset.moveRb} to ${to}`); }
    catch (e) { toast(e.message); sel.value = ""; }
  }));
  main.querySelectorAll("[data-remove-rb]").forEach((b) => (b.onclick = async () => {
    try { await api("DELETE", `/api/collections/${enc(name)}/runbooks/${enc(b.dataset.removeRb)}`); reload(`Removed ${b.dataset.removeRb}`); }
    catch (e) { toast(e.message); }
  }));
  const addRb = $("#add-rb");
  if (addRb) addRb.onchange = async () => {
    if (!addRb.value) return;
    try { await api("POST", `/api/collections/${enc(name)}/runbooks`, { runbooks: [addRb.value] }); reload(`Added ${addRb.value}`); }
    catch (e) { toast(e.message); }
  };
  const ta = $("#yaml");
  ta.addEventListener("input", () => {
    clearTimeout(ta._t);
    ta._t = setTimeout(async () => {
      const r = await api("POST", "/api/collections/validate", ta.value, true);
      const v = $("#validation");
      v.className = "validation " + (r.ok ? "ok" : "bad");
      v.textContent = r.ok ? "✔ valid" : r.error;
    }, 400);
  });
  $("#save").onclick = async () => {
    try {
      const res = isNew ? await api("POST", "/api/collections", ta.value, true)
        : await api("PUT", `/api/collections/${enc(name)}`, ta.value, true, { "If-Match": String(d.version) });
      toast("Saved");
      await loadCollections();
      renderSidebar();
      location.hash = `#/collections/${enc(res.name)}`;
      if (res.name === name) route();
    } catch (e) { const v = $("#validation"); v.className = "validation bad"; v.textContent = e.message; }
  };
  if (!isNew) $("#delete").onclick = async () => {
    if (!confirm(`Delete collection "${name}"? Its runbooks stay.`)) return;
    await api("DELETE", `/api/collections/${enc(name)}`);
    await loadCollections();
    renderSidebar();
    location.hash = "#/collections";
  };
}
