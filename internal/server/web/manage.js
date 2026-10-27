"use strict";

// Manage: everything stored, by kind, with what uses each. Select and
// delete (with what depends on it spelled out first), or restore what was
// deleted: deleting keeps the versions.

const MANAGE_KINDS = [
  ["runbook", "Runbooks", (n) => `#/runbooks/${enc(n)}`],
  ["library", "Libraries", (n) => `#/libraries/${enc(n)}`],
  ["fakes", "Fake libraries", null],
  ["suite", "Test suites", null],
  ["collection", "Collections", (n) => `#/collections/${enc(n)}`],
];
const MANAGE_NOUN = { runbook: "runbook", library: "library", fakes: "fake library", suite: "test suite", collection: "collection" };

async function viewManage() {
  const data = await api("GET", "/api/manage");
  const st = { sel: new Set(), delSel: new Set(), q: "" };
  const key = (it) => `${it.kind}:${it.name}`;
  const draw = () => {
    const q = st.q.toLowerCase();
    const show = (it) => !q || it.name.toLowerCase().includes(q);
    const ago = (t) => (t && !t.startsWith("0001") ? new Date(t).toLocaleString() : "");
    main.innerHTML = `
      <div class="row"><h1>Manage</h1><span class="spacer"></span><input type="search" class="manage-filter" placeholder="Filter by name…" value="${esc(st.q)}"></div>
      <p class="muted">Everything stored. Deleting keeps the versions, so it can be restored below. Runs are deleted from their page (or <code>runnr prune</code>).</p>
      <div class="manage-bar row"><button class="danger" data-del ${st.sel.size ? "" : "disabled"}>Delete selected${st.sel.size ? ` (${st.sel.size})` : ""}…</button><span class="muted small">${st.sel.size ? [...st.sel].join(", ").replace(/(^|, )\w+:/g, "$1") : "Select items to delete them"}</span></div>
      ${MANAGE_KINDS.map(([kind, title, link]) => {
        const rows = data.items.filter((it) => it.kind === kind && show(it));
        if (!rows.length) return "";
        return `<h3>${title} <span class="muted small">${rows.length}</span></h3>
          <table class="manage-table"><tr><th></th><th>Name</th><th>Version</th><th>Saved</th><th>Used by</th></tr>
          ${rows.map((it) => `<tr class="${st.sel.has(key(it)) ? "sel" : ""}">
            <td><input type="checkbox" data-sel="${esc(key(it))}" ${st.sel.has(key(it)) ? "checked" : ""} ${it.builtin ? "disabled title=\"Built into runnr\"" : ""}></td>
            <td>${link ? `<a href="${link(it.name)}">${esc(it.name)}</a>` : esc(it.name)}${it.builtin ? ` <span class="badge-k">built in</span>` : ""}${it.error ? ` <span class="error-text small">${esc(it.error)}</span>` : ""}</td>
            <td class="muted">${it.version ? shortVer(it.version) : ""}</td>
            <td class="muted small">${esc(ago(it.updated_at))}</td>
            <td class="small">${(it.used_by || []).map((u) => `<span class="chip">${esc(MANAGE_NOUN[u.kind])} ${esc(u.name)}</span>`).join(" ")}${it.schedules ? ` <span class="chip warn">${it.schedules} scheduled run${it.schedules === 1 ? "" : "s"}</span>` : ""}</td></tr>`).join("")}</table>`;
      }).join("")}
      ${data.deleted.filter(show).length ? `<h3>Deleted <span class="muted small">their last version is kept</span></h3>
        <div class="row"><button data-restore ${st.delSel.size ? "" : "disabled"}>Restore selected${st.delSel.size ? ` (${st.delSel.size})` : ""}</button></div>
        <table class="manage-table">${data.deleted.filter(show).map((d) => `<tr>
          <td><input type="checkbox" data-dsel="${esc(key(d))}" ${st.delSel.has(key(d)) ? "checked" : ""}></td>
          <td>${esc(d.name)}</td><td class="muted small">${esc(MANAGE_NOUN[d.kind])}</td><td class="muted">${shortVer(d.version)}</td><td class="muted small">last saved ${esc(ago(d.saved_at))}</td></tr>`).join("")}</table>` : ""}`;
    const f = main.querySelector(".manage-filter");
    f.oninput = () => { st.q = f.value; draw(); const g = main.querySelector(".manage-filter"); g.focus(); g.setSelectionRange(g.value.length, g.value.length); };
    main.querySelectorAll("[data-sel]").forEach((c) => (c.onchange = () => { c.checked ? st.sel.add(c.dataset.sel) : st.sel.delete(c.dataset.sel); draw(); }));
    main.querySelectorAll("[data-dsel]").forEach((c) => (c.onchange = () => { c.checked ? st.delSel.add(c.dataset.dsel) : st.delSel.delete(c.dataset.dsel); draw(); }));
    main.querySelector("[data-del]").onclick = () => confirmDelete();
    main.querySelector("[data-restore]") && (main.querySelector("[data-restore]").onclick = async () => {
      const items = [...st.delSel].map((k) => ({ kind: k.split(":")[0], name: k.slice(k.indexOf(":") + 1) }));
      const res = await api("POST", "/api/manage/restore", { items });
      const bad = res.results.filter((r) => !r.ok);
      toast(bad.length ? bad.map((r) => `${r.name}: ${r.error}`).join("; ") : `Restored ${res.results.length}`);
      await loadRunbooks();
      viewManage();
    });
  };
  const confirmDelete = () => {
    const items = data.items.filter((it) => st.sel.has(key(it)));
    const going = new Set(items.map(key));
    const dlg = document.createElement("dialog");
    dlg.className = "edit-step abort-dialog";
    dlg.innerHTML = `<h3>Delete ${items.length === 1 ? `${MANAGE_NOUN[items[0].kind]} ${esc(items[0].name)}` : `${items.length} items`}?</h3>
      <p class="muted small">Their versions are kept: they can be restored from Manage.</p>
      <ul class="small">${items.map((it) => {
        const users = (it.used_by || []).filter((u) => !going.has(key(u)));
        const notes = [];
        if (it.kind === "runbook" && users.some((u) => u.kind === "collection")) notes.push(`it's taken out of ${users.filter((u) => u.kind === "collection").map((u) => esc(u.name)).join(", ")}`);
        if (it.schedules) notes.push(`its ${it.schedules} scheduled run${it.schedules === 1 ? " is" : "s are"} removed`);
        const breaks = users.filter((u) => u.kind !== "collection");
        if (breaks.length) notes.push(`<span class="warn-text">⚠ still used by ${breaks.map((u) => `${esc(MANAGE_NOUN[u.kind])} ${esc(u.name)}`).join(", ")}, which won't work without it</span>`);
        return `<li><b>${esc(MANAGE_NOUN[it.kind])} ${esc(it.name)}</b>${notes.length ? `: ${notes.join("; ")}` : ""}</li>`;
      }).join("")}</ul>
      <div class="error-text"></div>
      <div class="row"><button class="danger" data-ok>Delete</button><button data-close>Cancel</button></div>`;
    document.body.append(dlg);
    dlg.querySelector("[data-close]").onclick = () => dlg.close();
    dlg.onclose = () => dlg.remove();
    dlg.querySelector("[data-ok]").onclick = async () => {
      try {
        const res = await api("POST", "/api/manage/delete", { items: items.map((it) => ({ kind: it.kind, name: it.name })) });
        const bad = res.results.filter((r) => !r.ok);
        dlg.close();
        toast(bad.length ? `Not deleted: ${bad.map((r) => `${r.name} (${r.error})`).join("; ")}` : `Deleted ${res.results.length}`);
        await loadRunbooks();
        viewManage();
      } catch (e) { dlg.querySelector(".error-text").textContent = e.message; }
    };
    dlg.showModal();
  };
  draw();
}
