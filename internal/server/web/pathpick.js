"use strict";

// A folder and file browser for paths on the machine runnr runs on (the
// browser can't hand them over): inputs of type dir or file get a Browse
// button (withPathPick), and imports and exports choose where with it.

// pickPath opens the browser. kind: "dir" (a folder), "file" (an existing
// file) or "save" (a file to write: a folder and a name). It resolves to
// the path chosen, or null; with multi (files), to a list of them. accept
// (extensions) dims the files it can't use. The
// list sorts by name or date (the last choice is remembered).
function pickPath({ kind = "dir", start = "", title = "", name = "", multi = false, accept = null } = {}) {
  multi = multi && kind === "file";
  return new Promise((resolve) => {
    const dlg = document.createElement("dialog");
    dlg.className = "edit-step path-picker";
    dlg.innerHTML = `<h3>${esc(title || { dir: "Choose a folder", file: "Choose a file", save: "Save as" }[kind])}</h3>
      <div class="row"><button type="button" data-up title="The folder above">↑</button><button type="button" data-home title="Your home folder">~</button>
        <input class="pp-path" spellcheck="false"><button type="button" data-go>Go</button></div>
      <div class="row pp-sort small"><span class="muted">Sort by</span><button type="button" class="link" data-sort="name"></button><button type="button" class="link" data-sort="date"></button></div>
      <ul class="pp-list"></ul>
      ${kind === "save" ? `<label class="row">File name <input class="pp-name" spellcheck="false" value="${esc(name)}"></label>` : ""}
      <div class="row"><span class="muted small pp-sel"></span><span class="spacer"></span>
        <button class="primary" data-ok>${{ dir: "Choose this folder", file: "Choose", save: "Save here" }[kind]}</button><button type="button" data-close>Cancel</button></div>`;
    document.body.append(dlg);
    const $p = (s) => dlg.querySelector(s);
    let cur = "", picked = "", entries = [];
    const chosen = new Set(); // multi: the files picked, in any folder
    let sort = { by: "name", desc: false };
    try { sort = { ...sort, ...JSON.parse(localStorage.getItem("runnr.pickSort") || "{}") }; } catch {}
    const done = (v) => { resolve(v); dlg.close(); };
    let seq = 0;
    const load = async (p) => {
      const mine = ++seq; // a slower listing of a folder left since is dropped
      try {
        const r = await api("GET", `/api/fs?path=${enc(p)}`);
        if (mine !== seq) return;
        cur = r.path;
        $p(".pp-path").value = cur;
        // Starting from a file: it's the one picked (or the name to save as).
        if (r.exists && !r.is_dir && r.asked !== cur) {
          picked = r.asked;
          if (kind === "save") $p(".pp-name").value = r.asked.split("/").pop();
        }
        entries = r.entries;
        list();
      } catch (e) { if (mine === seq) toast(e.message); }
    };
    // list draws the folder's entries, sorted: folders first, then by name
    // or by date (newest first unless reversed).
    const list = () => {
      const by = sort.by === "date" ? (a, b) => new Date(b.mod || 0) - new Date(a.mod || 0) : (a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: "base", numeric: true });
      const byName = (a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: "base", numeric: true });
      const es = [...entries].sort((a, b) => (a.dir !== b.dir ? (a.dir ? -1 : 1) : (sort.desc ? -1 : 1) * (by(a, b) || byName(a, b))));
      const usable = (e) => e.dir ? true : kind !== "dir" && (!accept || accept.some((x) => e.name.toLowerCase().endsWith(x)));
      $p(".pp-list").innerHTML = es.map((e) => `<li class="${e.dir ? "dir" : "file"} ${usable(e) ? "" : "dim"}" data-name="${esc(e.name)}" data-dir="${e.dir ? 1 : ""}"><span class="pp-name-col">${e.dir ? "📁" : "📄"} ${esc(e.name)}</span><span class="muted small">${e.dir ? "" : fmtSize(e.size)}</span><span class="muted small pp-date">${e.mod ? fmtWhen(e.mod) : ""}</span></li>`).join("") || `<li class="muted">Empty folder</li>`;
      dlg.querySelectorAll("[data-sort]").forEach((b) => {
        const on = b.dataset.sort === sort.by;
        b.classList.toggle("on", on);
        b.textContent = (b.dataset.sort === "name" ? "Name" : "Date") + (on ? ((sort.by === "date") !== sort.desc ? " ↓" : " ↑") : "");
        b.title = on ? "Click to reverse" : "";
      });
      draw();
    };
    dlg.querySelectorAll("[data-sort]").forEach((b) => (b.onclick = () => {
      sort = b.dataset.sort === sort.by ? { ...sort, desc: !sort.desc } : { by: b.dataset.sort, desc: false };
      try { localStorage.setItem("runnr.pickSort", JSON.stringify(sort)); } catch {}
      list();
    }));
    const draw = () => {
      dlg.querySelectorAll(".pp-list li[data-name]").forEach((li) => {
        const p = `${cur === "/" ? "" : cur}/${li.dataset.name}`;
        li.classList.toggle("on", multi ? chosen.has(p) : picked === p);
      });
      $p(".pp-sel").textContent = multi ? (chosen.size ? `${chosen.size} file${chosen.size === 1 ? "" : "s"} picked` : "Pick files (click each one)")
        : kind === "file" ? (picked ? picked : "Pick a file") : kind === "dir" ? cur : "";
      $p("[data-ok]").disabled = kind === "file" && !(multi ? chosen.size : picked);
    };
    $p(".pp-list").onclick = (e) => {
      const li = e.target.closest("li[data-name]");
      if (!li || li.classList.contains("dim")) return;
      const p = `${cur === "/" ? "" : cur}/${li.dataset.name}`;
      if (li.dataset.dir) return load(p);
      if (multi) { chosen.has(p) ? chosen.delete(p) : chosen.add(p); draw(); }
      else if (kind === "file") { picked = p; draw(); }
      if (kind === "save") $p(".pp-name").value = li.dataset.name;
    };
    $p(".pp-list").ondblclick = (e) => { const li = e.target.closest("li[data-name]"); if (li && !li.dataset.dir && kind === "file" && !multi) done(`${cur}/${li.dataset.name}`); };
    $p("[data-up]").onclick = () => load(cur.replace(/\/[^/]*$/, "") || "/");
    $p("[data-home]").onclick = () => load("~");
    $p("[data-go]").onclick = () => load($p(".pp-path").value);
    $p(".pp-path").onkeydown = (e) => { if (e.key === "Enter") { e.preventDefault(); load($p(".pp-path").value); } };
    $p("[data-close]").onclick = () => done(null);
    dlg.onclose = () => { dlg.remove(); resolve(null); };
    $p("[data-ok]").onclick = () => {
      if (kind === "dir") return done(cur);
      if (multi) return done([...chosen]);
      if (kind === "file") return done(picked);
      const n = $p(".pp-name").value.trim();
      if (!n) return $p(".pp-name").focus();
      done(n.startsWith("/") || n.startsWith("~") ? n : `${cur === "/" ? "" : cur}/${n}`);
    };
    dlg.showModal();
    load(start || "~");
  });
}

// fmtWhen is a file's date: the time today, else the day (and the year
// if it isn't this one).
function fmtWhen(t) {
  const d = new Date(t), now = new Date();
  if (d.toDateString() === now.toDateString()) return d.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
  return d.toLocaleDateString([], { month: "short", day: "numeric", ...(d.getFullYear() !== now.getFullYear() && { year: "numeric" }) }) +
    (d.getFullYear() === now.getFullYear() ? " " + d.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" }) : "");
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
