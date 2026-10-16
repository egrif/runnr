"use strict";

// Function libraries: named sets of shell functions that runbooks list under
// `libraries:`. Their functions can be called in every step and are loaded
// into shells opened from runnr.

async function viewLibraries() {
  const libs = await api("GET", "/api/libraries");
  main.innerHTML = `
    <div class="row"><h1>Libraries</h1><span class="spacer"></span><button class="primary" id="new-lib">+ New library</button></div>
    <p class="muted">Shell functions shared by runbooks. A runbook lists the libraries it uses under <code>libraries:</code>; their functions can then be called in every step and are loaded into shells opened from runnr. In any other terminal: <code>eval "$(runnr lib export &lt;name&gt;)"</code>.</p>
    ${libs.length ? `<table><thead><tr><th>Library</th><th>Functions</th><th>Description</th><th>Version</th></tr></thead><tbody>
      ${libs.map((l) => `<tr data-lib="${esc(l.name)}"><td><b>${esc(l.name)}</b></td>
        <td><code>${esc([...l.functions].sort().join(", "))}</code></td>
        <td class="${l.error ? "error-text" : "muted"}">${esc(l.error ? "invalid: " + l.error.split("\n")[0] : l.description || "")}</td><td>${l.version}</td></tr>`).join("")}
    </tbody></table>` : `<div class="empty">No libraries yet.</div>`}`;
  main.querySelectorAll("tr[data-lib]").forEach((tr) => (tr.onclick = () => (location.hash = `#/libraries/${enc(tr.dataset.lib)}`)));
  $("#new-lib").onclick = () => (location.hash = "#/libraries/new");
}

async function viewLibrary(name) {
  const isNew = name === "new";
  const data = isNew
    ? { raw: `name: my-functions\ndescription: Shell functions shared by runbooks.\nfunctions:\n  greet:\n    description: Say hello. Usage: greet NAME\n    body: |\n      echo "hello, \${1:-world}"\n` }
    : await api("GET", `/api/libraries/${enc(name)}`);
  const lib = data.library;
  const fns = lib ? Object.entries(lib.functions).sort(([a], [b]) => a.localeCompare(b)) : [];
  main.innerHTML = `
    <div class="row"><div><h1>${isNew ? "New library" : esc(name)}</h1><div class="muted">${esc(lib?.description || "")}</div></div></div>
    <div class="lib-layout">
      <div>
        <div class="row" style="margin-bottom:8px">
          <button class="primary" id="save">Save</button><button id="validate">Validate</button>
          ${data.version ? `<span class="muted">version ${data.version} · saved ${new Date(data.updated_at).toLocaleString()}</span>` : ""}
          <span class="spacer"></span>${isNew ? "" : `<button class="danger" id="delete">Delete</button>`}
        </div>
        <div id="validation" class="validation"></div>
        <textarea class="editor" id="yaml" spellcheck="false">${esc(data.raw)}</textarea>
        <details class="card" style="margin-top:12px"><summary>Format</summary><pre class="cmd">name: aurora-helpers          # lowercase identifier
description: What these are for
functions:
  kpsql:                        # called as: kpsql URL SQL
    description: Run psql from a throwaway pod in the cluster
    body: |
      kubectl run "psql-$RANDOM" --rm -i --restart=Never ...</pre></details>
      </div>
      <div>
        <h3>Functions</h3>
        ${fns.length ? `<table class="kv">${fns.map(([n, f]) => `<tr><td><code>${esc(n)}</code></td><td>${esc(f.description || "")}</td></tr>`).join("")}</table>` : `<p class="muted">Save to see the functions.</p>`}
        ${isNew ? "" : `<h3>Use it</h3>
          <p class="small">In a runbook:</p><pre class="cmd">libraries: [${esc(name)}]</pre>
          <p class="small">In any terminal:</p><pre class="cmd">eval "$(runnr lib export ${esc(name)})"</pre>`}
        ${data.script ? `<details><summary class="small">As shell code</summary><pre class="cmd">${esc(data.script)}</pre></details>` : ""}
      </div>
    </div>`;
  const ta = $("#yaml");
  const show = (res) => {
    const v = $("#validation");
    v.className = "validation " + (res.ok ? "ok" : "bad");
    v.textContent = res.ok ? "✔ valid" : res.error;
  };
  if (data.error) show({ ok: false, error: data.error });
  const validate = async () => show(await api("POST", "/api/libraries/validate", ta.value, true));
  let t;
  ta.addEventListener("input", () => { clearTimeout(t); t = setTimeout(validate, 400); });
  ta.addEventListener("keydown", (e) => { if (e.key === "Tab") { e.preventDefault(); ta.setRangeText("  ", ta.selectionStart, ta.selectionEnd, "end"); } });
  $("#validate").onclick = validate;
  $("#save").onclick = async () => {
    try {
      const res = isNew
        ? await api("POST", "/api/libraries", ta.value, true)
        : await api("PUT", `/api/libraries/${enc(name)}`, ta.value, true, { "If-Match": String(data.version) });
      toast("Saved");
      location.hash = `#/libraries/${enc(res.name)}`;
      if (res.name === name) route();
    } catch (e) { show({ ok: false, error: e.message }); }
  };
  if (!isNew) $("#delete").onclick = async () => {
    if (!confirm(`Delete library "${name}"? Runbooks that use it won't run until it's back (its versions are kept).`)) return;
    await api("DELETE", `/api/libraries/${enc(name)}`);
    toast("Deleted");
    location.hash = "#/libraries";
  };
}
