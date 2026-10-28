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

// membership shows which collections a runbook is in (✕ takes it out) and
// lets you add it to another. Moving and reordering are on the Collections page.
function membership(name) {
  const inCols = collections.filter((c) => c.collection?.runbooks.includes(name)).map((c) => c.name);
  return `<div class="membership"><span class="muted small">Collections:</span>
    ${inCols.length ? inCols.map((c) => `<span class="tag"><a href="#/collections">${esc(c)}</a> <button class="link small-x" data-col-remove="${esc(c)}" title="Take it out of ${esc(c)} (the runbook stays)">✕</button></span>`).join("") : `<span class="muted small">none</span>`}
    ${colPicker("col-add", "+ Add to…", inCols)}
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

// viewCollections is a board: a card per collection with its runbooks,
// plus the runbooks in none. Drag a runbook to another card to move it
// (hold Alt/Option to add it there as well), or within a card to reorder.
// folderRows are a scope's folders on this machine ("": for everything,
// else a collection's): the values placeholders stand for, as set (one can
// name others: @REPOS_ROOT@/x) and what that comes to here. names are
// always shown.
function folderRows(scope, vals, names) {
  const all = [...new Set([...names, ...Object.keys(vals || {}).sort()])];
  return `<table class="kv folder-rows">${all.map((n) => {
    const v = vals?.[n];
    const note = !v ? "" : v.why ? `✘ ${v.value} ${v.why}` : v.value !== v.raw ? `→ ${v.value}` : "";
    return `<tr><td><code>@${esc(n)}@</code></td><td>${withPathPick({ type: "dir" }, `<input data-folder="${esc(n)}" data-scope="${esc(scope)}" value="${esc(v?.raw || "")}" placeholder="not set" spellcheck="false" autocomplete="off">`)}${note ? `<div class="hint ${v.why ? "error-text" : "muted"}">${esc(note)}</div>` : ""}</td></tr>`;
  }).join("")}<tr><td colspan="2"><input class="folder-new" data-scope="${esc(scope)}" placeholder="+ another: NAME=FOLDER" spellcheck="false" autocomplete="off"></td></tr></table>`;
}

async function viewCollections() {
  await loadCollections();
  const scopes = (await api("GET", "/api/folders").catch(() => ({}))).scopes || {};
  const inAny = new Set(collections.flatMap((c) => c.collection?.runbooks || []));
  const byName = new Map(runbooks.map((r) => [r.name, r]));
  const item = (r, col) => {
    const rb = byName.get(r);
    return `<li draggable="true" data-rb="${esc(r)}" data-from="${esc(col)}">
      <span class="grip">⠿</span><a href="#/runbooks/${enc(r)}" draggable="false">${esc(r)}</a>
      <span class="muted small rb-line">${esc(rb ? (rb.description || "").split("\n")[0] : "(no such runbook)")}</span>
      ${col ? `<button class="icon-btn small-x" data-remove="${esc(r)}" data-col="${esc(col)}" title="Take it out of ${esc(col)} (the runbook stays)">✕</button>` : ""}
    </li>`;
  };
  const card = (c) => {
    const col = c.collection;
    const others = runbooks.filter((r) => !col?.runbooks.includes(r.name));
    return `<div class="card col-card" data-card="${esc(c.name)}">
      <div class="row col-head-row">
        <input class="col-name" data-col-name="${esc(c.name)}" value="${esc(c.name)}" size="${Math.max(8, c.name.length + 1)}" title="Rename">
        <span class="muted small">${col ? `${col.runbooks.length} runbook${col.runbooks.length === 1 ? "" : "s"}` : ""}</span>
        <span class="spacer"></span>
        <a class="small" href="#/collections/${enc(c.name)}" title="Edit the YAML">YAML</a>
        <button class="icon-btn danger" data-delete-col="${esc(c.name)}" title="Delete the collection (its runbooks stay)">🗑</button>
      </div>
      ${col ? `<textarea class="col-field" rows="2" data-col-field="description" data-col="${esc(c.name)}" placeholder="What this group of runbooks is for">${esc(col.description || "")}</textarea>
        <textarea class="col-field" rows="1" data-col-field="expect" data-col="${esc(c.name)}" placeholder="By the end: how things stand once they've all run">${esc(col.expect || "")}</textarea>
        <ol class="col-list" data-list="${esc(c.name)}">${col.runbooks.map((r) => item(r, c.name)).join("")}</ol>
        <div class="col-drop" data-list-end="${esc(c.name)}">Drop a runbook here</div>
        ${others.length ? `<select data-add-to="${esc(c.name)}"><option value="">+ Add a runbook…</option>${others.map((r) => `<option>${esc(r.name)}</option>`).join("")}</select>` : ""}
        <details class="col-folders" ${Object.keys(scopes[c.name] || {}).length ? "open" : ""}><summary class="small">Folders on this machine</summary>
          <p class="muted small">What @REPO_ROOT@, @PROJECT_DIR@ and others stand for in these runbooks, here: kept on this machine, not in the collection.</p>
          ${folderRows(c.name, scopes[c.name], ["REPO_ROOT", "PROJECT_DIR"])}</details>`
        : `<p class="error-text">${esc(c.error)}</p>`}
    </div>`;
  };
  const loose = runbooks.filter((r) => !inAny.has(r.name));
  main.innerHTML = `
    <div class="row page-head"><h1>Collections${helpIcon("Groups of runbooks that belong together (say, the prep, cutover and fallback runbooks of one migration). Drag a runbook to another collection to move it, hold Alt/⌥ as you drop to add it there too, or drag within a collection to reorder. Names and descriptions save as you edit them.")}</h1><span class="spacer"></span><button id="new-col">+ New collection</button></div>
    <p class="muted small page-sub">Drag runbooks between collections to move them (<kbd>Alt</kbd>/<kbd>⌥</kbd> to add); edits save as you type.</p>
    <details class="card col-folders global-folders" ${Object.keys(scopes[""] || {}).length ? "" : "open"}><summary><b>Folders on this machine, for everything</b></summary>
      <p class="muted small">Runbooks name folders by placeholder (<code>@REPO_ROOT@/tools</code>), kept as written; a run fills them in with these and its collection's, and doesn't start while one it names isn't a folder here. A value can name another: a collection's <code>@REPO_ROOT@</code> can be <code>@REPOS_ROOT@/ops-runbooks</code>.</p>
      ${folderRows("", scopes[""], ["REPOS_ROOT"])}</details>
    <div class="col-board">
      ${collections.map(card).join("")}
      <div class="card col-card loose" data-card="">
        <div class="row col-head-row"><b>Not in a collection</b><span class="muted small">${loose.length}</span></div>
        <p class="muted small">Drop a runbook here to take it out of its collection.</p>
        <ol class="col-list" data-list="">${loose.map((r) => item(r.name, "")).join("")}</ol>
        <div class="col-drop" data-list-end="">Drop here</div>
      </div>
    </div>`;
  const reload = async (msg) => { if (msg) toast(msg); await loadRunbooks(); viewCollections(); };
  const fail = (e) => { toast(e.message); reload(); };
  const setFolder = (scope, name, value) =>
    api("PUT", "/api/folders", { collection: scope, name, value }).then(() => reload(value ? `@${name}@ set${scope ? ` for ${scope}` : ""}` : `@${name}@ removed`)).catch(fail);
  main.querySelectorAll("[data-folder]").forEach((el) => (el.onchange = () => setFolder(el.dataset.scope, el.dataset.folder, el.value.trim())));
  main.querySelectorAll(".folder-new").forEach((el) => (el.onchange = () => {
    const [n, ...v] = el.value.split("=");
    if (!n.trim() || !v.join("=").trim()) return toast("Give it as NAME=FOLDER");
    setFolder(el.dataset.scope, n.trim().replace(/^@|@$/g, ""), v.join("=").trim());
  }));

  $("#new-col").onclick = async () => {
    const n = (prompt("Name for the new collection (lowercase letters, digits, '.', '_' or '-'):") || "").trim();
    if (!n) return;
    try { await api("POST", "/api/collections", `name: ${n}\nrunbooks: []\n`, true); reload(`Created ${n}`); } catch (e) { toast(e.message); }
  };
  main.querySelectorAll("[data-col-name]").forEach((el) => (el.onchange = () => {
    const nw = el.value.trim();
    api("PATCH", `/api/collections/${enc(el.dataset.colName)}`, { name: nw }).then(() => reload(`Renamed to ${nw}`)).catch(fail);
  }));
  main.querySelectorAll("[data-col-field]").forEach((el) => (el.onchange = () => {
    api("PATCH", `/api/collections/${enc(el.dataset.col)}`, { [el.dataset.colField]: el.value }).then(() => toast("Saved")).catch(fail);
  }));
  main.querySelectorAll("[data-delete-col]").forEach((b) => (b.onclick = async () => {
    if (!confirm(`Delete collection “${b.dataset.deleteCol}”? Its runbooks stay.`)) return;
    api("DELETE", `/api/collections/${enc(b.dataset.deleteCol)}`).then(() => reload("Deleted")).catch(fail);
  }));
  main.querySelectorAll("[data-remove]").forEach((b) => (b.onclick = () => {
    api("DELETE", `/api/collections/${enc(b.dataset.col)}/runbooks/${enc(b.dataset.remove)}`).then(() => reload(`Took ${b.dataset.remove} out of ${b.dataset.col}`)).catch(fail);
  }));
  main.querySelectorAll("[data-add-to]").forEach((sel) => (sel.onchange = () => {
    if (!sel.value) return;
    api("POST", `/api/collections/${enc(sel.dataset.addTo)}/runbooks`, { runbooks: [sel.value] }).then(() => reload(`Added ${sel.value}`)).catch(fail);
  }));

  // Drag and drop.
  let drag = null, mark = null;
  const clear = () => { mark?.classList.remove("drop-before", "drop-after", "drop-on"); mark = null; };
  const board = $(".col-board");
  const target = (e) => {
    const li = e.target.closest(".col-list li");
    if (li && li.dataset.rb !== drag?.rb) {
      const r = li.getBoundingClientRect();
      return { el: li, list: li.closest("[data-list]").dataset.list, before: li.dataset.rb, after: e.clientY > r.top + r.height / 2 };
    }
    const end = e.target.closest("[data-list-end]") || e.target.closest(".col-card")?.querySelector("[data-list-end]");
    return end ? { el: end, list: end.dataset.listEnd } : null;
  };
  board.ondragstart = (e) => {
    const li = e.target.closest?.("li[data-rb]");
    if (!li) return;
    drag = { rb: li.dataset.rb, from: li.dataset.from };
    e.dataTransfer.effectAllowed = "copyMove";
    e.dataTransfer.setData("text/plain", drag.rb);
    board.classList.add("dragging");
  };
  board.ondragover = (e) => {
    if (!drag) return;
    const t = target(e);
    clear();
    if (!t) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = e.altKey && t.list && t.list !== drag.from ? "copy" : "move";
    mark = t.el;
    mark.classList.add(t.before ? (t.after ? "drop-after" : "drop-before") : "drop-on");
  };
  board.ondragend = () => { clear(); drag = null; board.classList.remove("dragging"); };
  board.ondrop = async (e) => {
    const t = drag && target(e);
    clear();
    board.classList.remove("dragging");
    if (!t) return;
    e.preventDefault();
    const { rb, from } = drag;
    drag = null;
    try {
      if (t.list === "") {
        if (from) { await api("DELETE", `/api/collections/${enc(from)}/runbooks/${enc(rb)}`); return reload(`Took ${rb} out of ${from}`); }
        return;
      }
      const cur = collections.find((c) => c.name === t.list)?.collection?.runbooks || [];
      const list = cur.filter((x) => x !== rb);
      let at = t.before ? list.indexOf(t.before) + (t.after ? 1 : 0) : list.length;
      if (at < 0) at = list.length;
      list.splice(at, 0, rb);
      await api("PUT", `/api/collections/${enc(t.list)}/runbooks`, { runbooks: list });
      const copy = e.altKey && from && from !== t.list;
      if (from && from !== t.list && !copy) await api("DELETE", `/api/collections/${enc(from)}/runbooks/${enc(rb)}`);
      reload(from === t.list ? "Reordered" : copy ? `Added ${rb} to ${t.list}` : from ? `Moved ${rb} to ${t.list}` : `Added ${rb} to ${t.list}`);
    } catch (err) { fail(err); }
  };
}

async function viewCollection(name) {
  const isNew = name === "new";
  const d = isNew ? { raw: `name: my-collection\ndescription: What this group of runbooks is for.\nexpect: How things stand once they've all run.\nrunbooks: [${runbooks.slice(0, 2).map((r) => r.name).join(", ")}]\n` }
    : await api("GET", `/api/collections/${enc(name)}`);
  const c = d.collection;
  main.innerHTML = `
    <p><a href="#/collections">← Collections</a></p>
    <h1>${isNew ? "New collection" : esc(name)} <span class="muted small">YAML</span></h1>
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
      ${d.version ? `<span class="muted">version ${shortVer(d.version)}</span>` : ""}
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
