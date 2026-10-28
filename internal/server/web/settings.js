"use strict";

// Settings: the folders placeholders stand for on this machine (for
// everything, each collection and each runbook), and this browser's own
// choices.

const settingsUI = { q: "" };

async function viewSettings() {
  await loadCollections();
  const data = await api("GET", "/api/folders");
  const scopes = data.scopes || {}, rbs = data.runbooks || {};
  const val = (scope, n) => scopes[scope]?.[n];

  // What a scope's runs would have for n without the scope's own value.
  const globalOf = (n) => (val("", n) ? { raw: val("", n).raw, value: val("", n).value, from: "everything" } : null);
  const colOf = (cols, n) => {
    const set = cols.map((c) => [c, val(c, n)]).filter(([, v]) => v);
    if (!set.length) return null;
    if (set.length === 1 || set.every(([, v]) => v.raw === set[0][1].raw)) return { ...set[0][1], from: `collection ${set.map(([c]) => c).join(", ")}` };
    return { raw: "", value: "", from: `collections ${set.map(([c]) => c).join(", ")}, which differ`, differ: true };
  };
  const inherited = { "": () => null };

  const usedByCol = (c) => [...new Set((c.collection?.runbooks || []).flatMap((r) => rbs[r]?.used || []))].sort();
  const rbNames = [...new Set([...Object.keys(rbs).filter((r) => rbs[r].used?.length),
    ...Object.keys(scopes).filter((s) => s.startsWith("runbook:")).map((s) => s.slice(8))])].sort();

  const q = settingsUI.q.toLowerCase();
  const shows = (label, names) => !q || label.toLowerCase().includes(q) || names.some((n) => n.toLowerCase().includes(q.replace(/@/g, "")));

  // rows: a scope's placeholders: those it should have (names) and those set.
  const rows = (scope, names, inherit) => {
    const own = scopes[scope] || {};
    const all = [...new Set([...names, ...Object.keys(own).sort()])];
    return `<table class="set-rows">${all.map((n) => {
      const v = own[n], up = inherit(n);
      const [note, tip, cls] = v
        ? v.why ? [`✘ ${v.why}`, `${v.value} ${v.why}`, "error-text"] : v.value !== v.raw ? [`→ ${v.value}`, v.value, ""] : ["", "", ""]
        : up?.differ ? [`${up.from}: set it here`, "", "warn-text"]
        : up ? [`from ${up.from}`, up.value || up.raw, ""]
        : scope.startsWith("runbook:") ? ["not set: a run can't start", "", "error-text"] : ["", "", ""];
      return `<tr><td><code>@${esc(n)}@</code></td>
        <td>${withPathPick({ type: "dir" }, `<input data-set="${esc(n)}" data-scope="${esc(scope)}" value="${esc(v?.raw || "")}" placeholder="${esc(up?.raw || "not set")}" spellcheck="false" autocomplete="off">`)}</td>
        <td class="small muted set-note"><span class="${cls}" title="${esc(tip)}">${esc(note)}</span>${v ? ` <button class="link" data-unset="${esc(n)}" data-scope="${esc(scope)}" title="Remove it here${up ? `: it comes ${up.from} again` : ""}">✕</button>` : ""}</td></tr>`;
    }).join("")}
    <tr><td colspan="3"><button class="link small" data-add-row title="Give another placeholder a folder${scope ? " here" : " for everything"}">+ add a placeholder</button><input class="set-new" data-scope="${esc(scope)}" placeholder="NAME=FOLDER" spellcheck="false" autocomplete="off" hidden></td></tr></table>`;
  };

  const colBlocks = collections.filter((c) => shows(c.name, usedByCol(c).concat(Object.keys(scopes[c.name] || {})))).map((c) =>
    `<div class="set-block"><div class="set-head"><b>${esc(c.name)}</b><span class="muted small">${(c.collection?.runbooks || []).length} runbook${(c.collection?.runbooks || []).length === 1 ? "" : "s"}</span></div>
      ${rows(c.name, usedByCol(c), globalOf)}</div>`).join("");

  const rbBlocks = rbNames.filter((r) => shows(r, (rbs[r]?.used || []).concat(Object.keys(scopes[`runbook:${r}`] || {})))).map((r) => {
    const f = rbs[r];
    const cols = f?.collections || [];
    const inherit = (n) => colOf(cols, n) || globalOf(n);
    const state = !f ? `<span class="muted small">no longer here</span>`
      : f.problems?.length ? `<span class="badge-k fail" title="${esc(f.problems.map((p) => `@${p.name}@ ${p.why}`).join("\n"))}">✘ ${f.problems.length} not ready</span>`
      : `<span class="badge-k ok" title="Every placeholder it names is a folder here">✔ ready</span>`;
    return `<div class="set-block"><div class="set-head"><a href="#/runbooks/${enc(r)}"><b>${esc(r)}</b></a>${state}
      <span class="muted small">${cols.length ? `in ${cols.map(esc).join(", ")}` : "in no collection"}</span></div>
      ${rows(`runbook:${r}`, f?.used || [], inherit)}</div>`;
  }).join("");

  let name = "";
  try { name = localStorage.getItem("runnr.author") || ""; } catch {}
  main.innerHTML = `
    <div class="row page-head"><h1>Settings</h1></div>
    <p class="muted small page-sub">Folders are kept on this machine, in runnr; the rest is this browser's.</p>
    <section class="card">
      <div class="row set-title"><h2>Folders${helpIcon("Runbooks name folders by placeholder (@REPO_ROOT@/tools) and are kept as written, so they're the same on every machine. A run fills each in from, first to last: what's given for the run, its runbook's, its collection's, and those for everything. A value can name another (@REPOS_ROOT@/ops-runbooks) or start with ~. A run doesn't start while one it names isn't a folder here.")}</h2>
        <span class="spacer"></span><input type="search" id="set-filter" placeholder="Filter by name or @PLACEHOLDER@…" value="${esc(settingsUI.q)}"></div>
      <p class="muted small">A run takes each from: the run → its runbook → its collection → everything.</p>
      ${!q || shows("everything", ["REPOS_ROOT", ...Object.keys(scopes[""] || {})]) ? `<h3>For everything</h3><div class="set-block">${rows("", ["REPOS_ROOT"], inherited[""])}</div>` : ""}
      ${colBlocks ? `<h3>By collection</h3>${colBlocks}` : ""}
      ${rbBlocks ? `<h3>By runbook</h3>${rbBlocks}` : q ? "" : `<h3>By runbook</h3><p class="muted small">No runbook names a placeholder.</p>`}
    </section>
    <section class="card">
      <h2>This browser</h2>
      <div class="form-grid">
        <label for="set-name">Your name</label><input id="set-name" value="${esc(name)}" placeholder="for notes, and who's watching a run">
        <label for="set-view">Open runs in</label><select id="set-view"><option value="classic" ${runView() === "classic" ? "selected" : ""}>the run page</option><option value="console" ${runView() === "console" ? "selected" : ""}>the console</option></select>
        <label for="set-copy">Copy on select</label><label class="check"><input type="checkbox" id="set-copy" ${copySel.on ? "checked" : ""}> selecting text (in terminals, code and output) copies it</label>
      </div>
    </section>`;

  const reload = (msg) => { if (msg) toast(msg); viewSettings(); };
  const put = (scope, n, value) => {
    const body = scope.startsWith("runbook:") ? { runbook: scope.slice(8) } : { collection: scope };
    return api("PUT", "/api/folders", { ...body, name: n, value })
      .then(() => reload(value ? `@${n}@ set${scope ? ` for ${scope.replace(/^runbook:/, "")}` : " for everything"}` : `@${n}@ removed`))
      .catch((e) => toast(e.message));
  };
  main.querySelectorAll("[data-set]").forEach((el) => (el.onchange = () => put(el.dataset.scope, el.dataset.set, el.value.trim())));
  main.querySelectorAll("[data-unset]").forEach((b) => (b.onclick = () => put(b.dataset.scope, b.dataset.unset, "")));
  main.querySelectorAll("[data-add-row]").forEach((b) => (b.onclick = () => { const i = b.nextElementSibling; b.hidden = true; i.hidden = false; i.focus(); }));
  main.querySelectorAll(".set-new").forEach((el) => (el.onchange = () => {
    const [n, ...v] = el.value.split("=");
    if (!n.trim() || !v.join("=").trim()) return toast("Give it as NAME=FOLDER");
    put(el.dataset.scope, n.trim().replace(/^@|@$/g, ""), v.join("=").trim());
  }));
  const filter = $("#set-filter");
  let t;
  filter.oninput = () => { clearTimeout(t); t = setTimeout(() => { settingsUI.q = filter.value; viewSettings().then(() => { const f = $("#set-filter"); f.focus(); f.setSelectionRange(f.value.length, f.value.length); }); }, 250); };
  $("#set-name").onchange = (e) => { try { localStorage.setItem("runnr.author", e.target.value.trim()); toast("Saved"); } catch {} };
  $("#set-view").onchange = (e) => { runView(e.target.value); toast("Saved"); };
  $("#set-copy").onchange = (e) => setCopyOnSelect(e.target.checked);
}
