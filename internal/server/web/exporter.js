"use strict";

// Exporting to files on the machine runnr runs on: a runbook with the
// libraries it uses (each beside it, so the folder imports again), and a
// run's logs with the runs linked to it by resuming.

const EXP_DIR = "runnr.exportDir";
const expDir = () => { try { return localStorage.getItem(EXP_DIR) || "~"; } catch { return "~"; } };
const expRemember = (path) => { try { localStorage.setItem(EXP_DIR, path.replace(/\/[^/]*$/, "") || "/"); } catch {} };

// exportRunbook asks where to write runbook name, and which of its
// libraries to write beside it.
function exportRunbook(name) {
  const st = { path: `${expDir()}/${name}.yaml`, files: [], libs: new Set(), overwrite: false };
  const dlg = document.createElement("dialog");
  dlg.className = "edit-step import-dialog";
  dlg.innerHTML = `<h3>Export ${esc(name)}</h3>
    <p class="muted small">The runbook as written (comments and all), with the libraries it uses beside it as <code>&lt;name&gt;.yaml</code>, so the folder imports again. On the machine runnr runs on.</p>
    <div class="row path-field"><input class="exp-path" spellcheck="false" value="${esc(st.path)}"><button type="button" data-browse>📄 Browse…</button></div>
    <div class="exp-files"></div>
    <div class="error-text"></div>
    <div class="row"><span class="spacer"></span><button class="primary" data-ok>Export</button><button data-close>Cancel</button></div>`;
  document.body.append(dlg);
  const $e = (s) => dlg.querySelector(s);
  const err = (m) => ($e(".error-text").textContent = m || "");
  let t;
  const plan = async () => {
    st.path = $e(".exp-path").value.trim();
    try {
      const res = await api("POST", `/api/runbooks/${enc(name)}/files/plan`, { path: st.path });
      const first = !st.files.length;
      st.files = res.files;
      if (first) st.libs = new Set(st.files.filter((f) => f.kind === "library" && !f.builtin && !f.missing).map((f) => f.name));
      draw(); err("");
    } catch (e) { st.files = []; draw(); err(e.message); }
  };
  const draw = () => {
    const take = (f) => f.kind === "runbook" || st.libs.has(f.name);
    const there = st.files.filter((f) => take(f) && f.exists);
    $e(".exp-files").innerHTML = st.files.length ? `<table class="imp-docs">${st.files.map((f) => `<tr>
        <td>${f.kind === "runbook" ? `<input type="checkbox" checked disabled>` : `<input type="checkbox" data-lib="${esc(f.name)}" ${st.libs.has(f.name) ? "checked" : ""} ${f.missing ? "disabled" : ""}>`}</td>
        <td class="small">${f.kind}</td>
        <td><b>${esc(f.name)}</b>${f.version ? ` <span class="muted small">v${f.version}</span>` : ""}${f.builtin ? ` <span class="badge-k">built into runnr</span>` : ""}<div class="muted small"><code>${esc(f.path)}</code></div></td>
        <td class="small">${f.missing ? `<span class="error-text">not stored</span>` : f.exists && take(f) ? `<span class="warn-text">⚠ a file is there</span>` : ""}</td></tr>`).join("")}</table>
      ${there.length ? `<label class="check small"><input type="checkbox" data-overwrite ${st.overwrite ? "checked" : ""}> Write over the ${there.length} file${there.length === 1 ? "" : "s"} already there</label>` : ""}` : "";
    dlg.querySelectorAll("[data-lib]").forEach((c) => (c.onchange = () => { c.checked ? st.libs.add(c.dataset.lib) : st.libs.delete(c.dataset.lib); draw(); }));
    $e("[data-overwrite]") && ($e("[data-overwrite]").onchange = (e) => { st.overwrite = e.target.checked; draw(); });
    $e("[data-ok]").disabled = !st.files.length || (there.length > 0 && !st.overwrite);
    const n = st.files.filter(take).length;
    $e("[data-ok]").textContent = n ? `Export ${n} file${n === 1 ? "" : "s"}` : "Export";
  };
  $e(".exp-path").oninput = () => { clearTimeout(t); t = setTimeout(plan, 300); };
  $e("[data-browse]").onclick = async () => {
    const p = await pickPath({ kind: "save", start: st.path, name: `${name}.yaml`, title: `Export ${name} to` });
    if (p) { $e(".exp-path").value = p; plan(); }
  };
  $e("[data-close]").onclick = () => dlg.close();
  dlg.onclose = () => dlg.remove();
  $e("[data-ok]").onclick = async () => {
    try {
      const res = await api("POST", `/api/runbooks/${enc(name)}/files`, { path: st.path, libraries: [...st.libs], overwrite: st.overwrite });
      expRemember(res.written[0].path);
      dlg.close();
      toast(`Exported ${res.written.map((f) => f.path.split("/").pop()).join(", ")} to ${res.written[0].path.replace(/\/[^/]*$/, "")}`);
    } catch (e) { err(e.message); }
  };
  dlg.showModal();
  plan();
}

// exportLogs exports a run's logs, with the runs linked to it by resuming.
async function exportLogs(runId) {
  let chain;
  try { chain = await api("GET", `/api/runs/${enc(runId)}/chain`); } catch (e) { return toast(e.message); }
  const st = { ids: new Set(chain.map((r) => r.id)), format: "raw", single: chain.length > 1, path: expDir(), overwrite: false };
  const dlg = document.createElement("dialog");
  dlg.className = "edit-step import-dialog";
  const when = (t) => new Date(t).toLocaleString();
  dlg.innerHTML = `<h3>Export logs</h3>
    <p class="muted small">${chain.length > 1 ? `This run and the ${chain.length - 1} run${chain.length === 2 ? "" : "s"} linked to it by resuming, oldest first.` : "This run (no runs resumed it, and it resumes none)."}</p>
    <table class="imp-docs">${chain.map((r) => `<tr><td><input type="checkbox" data-id="${esc(r.id)}" checked></td>
      <td><code>${esc(r.id)}</code>${r.id === runId ? ` <span class="badge-k">this run</span>` : ""}<div class="muted small">${esc(when(r.started))}${r.resumes ? ` · resumes ${esc(r.resumes)}` : ""}</div></td>
      <td class="small"><span class="badge s-${esc(r.status)}">${esc(r.status)}</span></td></tr>`).join("")}</table>
    <div class="row small exp-opts">
      <b>As</b><label class="check"><input type="radio" name="fmt" value="raw" checked> raw logs (to read)</label>
      <label class="check"><input type="radio" name="fmt" value="json"> records (JSON, to import)</label>
      <label class="check"><input type="radio" name="fmt" value="both"> both</label>
      <label class="check" title="A page per run laying it out in time: its steps, tries, shells, resumes and notes"><input type="checkbox" data-timeline> + timeline (HTML)</label></div>
    <div class="row small exp-opts"><b>In</b><label class="check"><input type="radio" name="single" value="1" ${st.single ? "checked" : ""}> one file</label>
      <label class="check"><input type="radio" name="single" value="" ${st.single ? "" : "checked"}> a file per run</label></div>
    <div class="row path-field"><input class="exp-path" spellcheck="false" value="${esc(st.path)}" title="The folder to write them in"><button type="button" data-browse>📁 Browse…</button></div>
    <label class="check small"><input type="checkbox" data-overwrite> Write over files already there</label>
    <div class="error-text"></div>
    <div class="row"><button data-download title="Download through the browser instead (a zip for several files)">⇩ Download</button><span class="spacer"></span><button class="primary" data-ok>Save to folder</button><button data-close>Cancel</button></div>`;
  document.body.append(dlg);
  const $e = (s) => dlg.querySelector(s);
  const read = () => {
    st.ids = new Set([...dlg.querySelectorAll("[data-id]")].filter((c) => c.checked).map((c) => c.dataset.id));
    st.format = dlg.querySelector("[name=fmt]:checked").value + ($e("[data-timeline]").checked ? ",timeline" : "");
    st.single = !!dlg.querySelector("[name=single]:checked").value;
    st.path = $e(".exp-path").value.trim();
    st.overwrite = $e("[data-overwrite]").checked;
    $e("[data-ok]").disabled = $e("[data-download]").disabled = !st.ids.size;
  };
  dlg.addEventListener("change", read);
  $e("[data-browse]").onclick = async () => { const p = await pickPath({ kind: "dir", start: st.path, title: "Export logs to" }); if (p) { $e(".exp-path").value = p; read(); } };
  $e("[data-close]").onclick = () => dlg.close();
  dlg.onclose = () => dlg.remove();
  const ordered = () => chain.map((r) => r.id).filter((id) => st.ids.has(id));
  $e("[data-download]").onclick = () => {
    read();
    const q = new URLSearchParams({ ids: ordered().join(","), format: st.format, single: st.single ? "1" : "" });
    const a = document.createElement("a");
    a.href = `/api/runs/${enc(runId)}/logs?${q}`;
    a.download = "";
    document.body.append(a); a.click(); a.remove();
  };
  $e("[data-ok]").onclick = async () => {
    read();
    try {
      const res = await api("POST", `/api/runs/${enc(runId)}/logs`, { ids: ordered(), format: st.format, single: st.single, path: st.path, overwrite: st.overwrite });
      try { localStorage.setItem(EXP_DIR, st.path); } catch {}
      dlg.close();
      toast(`Wrote ${res.written.length} file${res.written.length === 1 ? "" : "s"} to ${st.path}`);
    } catch (e) { $e(".error-text").textContent = e.message; }
  };
  read();
  dlg.showModal();
}
