"use strict";

// A folder and file browser for paths on the machine runnr runs on (the
// browser can't hand them over): inputs of type dir or file get a Browse
// button (withPathPick), and imports and exports choose where with it.

// pickPath opens the browser. kind: "dir" (a folder), "file" (an existing
// file) or "save" (a file to write: a folder and a name). It resolves to
// the path chosen, or null.
function pickPath({ kind = "dir", start = "", title = "", name = "" } = {}) {
  return new Promise((resolve) => {
    const dlg = document.createElement("dialog");
    dlg.className = "edit-step path-picker";
    dlg.innerHTML = `<h3>${esc(title || { dir: "Choose a folder", file: "Choose a file", save: "Save as" }[kind])}</h3>
      <div class="row"><button type="button" data-up title="The folder above">↑</button><button type="button" data-home title="Your home folder">~</button>
        <input class="pp-path" spellcheck="false"><button type="button" data-go>Go</button></div>
      <ul class="pp-list"></ul>
      ${kind === "save" ? `<label class="row">File name <input class="pp-name" spellcheck="false" value="${esc(name)}"></label>` : ""}
      <div class="row"><span class="muted small pp-sel"></span><span class="spacer"></span>
        <button class="primary" data-ok>${{ dir: "Choose this folder", file: "Choose", save: "Save here" }[kind]}</button><button type="button" data-close>Cancel</button></div>`;
    document.body.append(dlg);
    const $p = (s) => dlg.querySelector(s);
    let cur = "", picked = "";
    const done = (v) => { resolve(v); dlg.close(); };
    const load = async (p) => {
      try {
        const r = await api("GET", `/api/fs?path=${enc(p)}`);
        cur = r.path;
        $p(".pp-path").value = cur;
        // Starting from a file: it's the one picked (or the name to save as).
        if (r.exists && !r.is_dir && r.asked !== cur) {
          picked = r.asked;
          if (kind === "save") $p(".pp-name").value = r.asked.split("/").pop();
        }
        $p(".pp-list").innerHTML = r.entries.map((e) => `<li class="${e.dir ? "dir" : "file"} ${!e.dir && kind === "dir" ? "dim" : ""}" data-name="${esc(e.name)}" data-dir="${e.dir ? 1 : ""}">${e.dir ? "📁" : "📄"} ${esc(e.name)}${e.dir ? "" : ` <span class="muted small">${fmtSize(e.size)}</span>`}</li>`).join("") || `<li class="muted">Empty folder</li>`;
        draw();
      } catch (e) { toast(e.message); }
    };
    const draw = () => {
      dlg.querySelectorAll(".pp-list li[data-name]").forEach((li) => li.classList.toggle("on", picked === `${cur}/${li.dataset.name}`));
      $p(".pp-sel").textContent = kind === "file" ? (picked ? picked : "Pick a file") : kind === "dir" ? cur : "";
      $p("[data-ok]").disabled = kind === "file" && !picked;
    };
    $p(".pp-list").onclick = (e) => {
      const li = e.target.closest("li[data-name]");
      if (!li) return;
      const p = `${cur === "/" ? "" : cur}/${li.dataset.name}`;
      if (li.dataset.dir) return load(p);
      if (kind === "file") { picked = p; draw(); }
      if (kind === "save") $p(".pp-name").value = li.dataset.name;
    };
    $p(".pp-list").ondblclick = (e) => { const li = e.target.closest("li[data-name]"); if (li && !li.dataset.dir && kind === "file") done(`${cur}/${li.dataset.name}`); };
    $p("[data-up]").onclick = () => load(cur.replace(/\/[^/]*$/, "") || "/");
    $p("[data-home]").onclick = () => load("~");
    $p("[data-go]").onclick = () => load($p(".pp-path").value);
    $p(".pp-path").onkeydown = (e) => { if (e.key === "Enter") { e.preventDefault(); load($p(".pp-path").value); } };
    $p("[data-close]").onclick = () => done(null);
    dlg.onclose = () => { dlg.remove(); resolve(null); };
    $p("[data-ok]").onclick = () => {
      if (kind === "dir") return done(cur);
      if (kind === "file") return done(picked);
      const n = $p(".pp-name").value.trim();
      if (!n) return $p(".pp-name").focus();
      done(n.startsWith("/") || n.startsWith("~") ? n : `${cur === "/" ? "" : cur}/${n}`);
    };
    dlg.showModal();
    load(start || "~");
  });
}

function fmtSize(n) {
  return n < 1024 ? `${n} B` : n < 1 << 20 ? `${(n / 1024).toFixed(1)} KB` : `${(n / (1 << 20)).toFixed(1)} MB`;
}

// withPathPick puts a Browse button after an input's field, if it's a path
// (type dir or file). The button fills in the field it follows.
function withPathPick(def, field) {
  if (!def || (def.type !== "dir" && def.type !== "file")) return field;
  return `<span class="path-field">${field}<button type="button" class="path-pick" data-pick="${def.type}" title="${def.type === "dir" ? "Browse for a folder" : "Browse for a file"} on this machine">${def.type === "dir" ? "📁" : "📄"} Browse…</button></span>`;
}

document.addEventListener("click", async (e) => {
  const b = e.target.closest(".path-pick");
  if (!b) return;
  e.preventDefault();
  const input = b.closest(".path-field")?.querySelector("input, textarea");
  if (!input) return;
  const v = await pickPath({ kind: b.dataset.pick, start: input.value || input.placeholder || "" });
  if (v == null) return;
  input.value = v;
  input.dispatchEvent(new Event("input", { bubbles: true }));
  input.dispatchEvent(new Event("change", { bubbles: true }));
});
