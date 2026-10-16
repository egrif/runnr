"use strict";

// Collections group runbooks that belong together, with what they're for.

let collections = [];

async function loadCollections() {
  collections = await api("GET", "/api/collections").catch(() => []);
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
        return `<li><a href="#/runbooks/${enc(r)}">${esc(r)}</a> <span class="muted">${esc(rb ? (rb.description || "").split("\n")[0] : "(no such runbook yet)")}</span></li>`;
      }).join("")}</ol>` : ""}
    <div class="row" style="margin:12px 0 8px">
      <button class="primary" id="save">Save</button>
      ${d.version ? `<span class="muted">version ${d.version}</span>` : ""}
      <span class="spacer"></span>${isNew ? "" : `<button class="danger" id="delete">Delete</button>`}
    </div>
    <div id="validation" class="validation">${d.error ? esc(d.error) : ""}</div>
    <textarea class="editor" id="yaml" spellcheck="false" style="min-height:30vh">${esc(d.raw)}</textarea>
    <p class="muted small">Fields: name, description, expect (how things stand once they've all run), runbooks (in the order they're usually run).</p>`;
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
