"use strict";

// In-browser terminals for steps that need one (terminal steps, shells,
// background steps). They live in a drawer outside #main, so re-rendering
// the run page doesn't disturb them; each session is a tab. Output arrives
// as run events; keystrokes and size changes go back with
// POST /api/runs/{id}/term.

const term = { runId: null, sessions: new Map(), active: null, queue: "", timer: null };

function termDrawer() {
  let d = $("#term-drawer");
  if (d) return d;
  d = document.createElement("div");
  d.id = "term-drawer";
  d.hidden = true;
  d.innerHTML = `
    <div class="term-head">
      <div class="term-tabs"></div>
      <span class="spacer"></span>
      <span class="muted small term-hint"></span>
      <button class="link term-copy-out" title="Copy the selection, or the whole session if nothing is selected (or select and press ⌘C / Ctrl+Shift+C)">Copy</button>
      <button class="link term-grow">Expand</button>
      <button class="link term-end" title="Hang up, like closing a terminal window">End session</button>
      <button class="link term-close">Hide</button>
    </div>
    <div class="term-main">
      <div class="term-side" hidden>
        <div class="row"><b>Step commands</b><span class="spacer"></span><button class="link term-copy">Copy</button></div>
        <pre class="term-cmd"></pre>
        <div class="term-expect"></div>
      </div>
      <div class="term-body"></div>
    </div>`;
  document.body.append(d);
  d.querySelector(".term-close").onclick = () => closeTermDrawer();
  d.querySelector(".term-copy-out").onclick = () => copyTerm();
  d.querySelector(".term-grow").onclick = (e) => {
    const big = d.classList.toggle("big");
    document.body.classList.toggle("term-big", big);
    e.target.textContent = big ? "Shrink" : "Expand";
    activeSession()?.fit.fit();
  };
  d.querySelector(".term-end").onclick = () => {
    const s = activeSession();
    if (s && !s.ended && confirm(`End “${s.title}”? Anything still running in it is hung up.`)) sendTerm(s, { end: true });
  };
  d.querySelector(".term-copy").onclick = async () => {
    try { await navigator.clipboard.writeText(d.querySelector(".term-cmd").textContent); toast("Copied"); }
    catch { toast("Select the text and copy it"); }
  };
  d.querySelector(".term-tabs").onclick = (e) => {
    const b = e.target.closest("[data-term]");
    if (b) showTerm(b.dataset.term);
  };
  window.addEventListener("resize", () => { if (!d.hidden) activeSession()?.fit.fit(); });
  return d;
}

const activeSession = () => term.sessions.get(term.active);

// runningBackground lists background sessions still going (for the run page).
function runningBackground() {
  return [...term.sessions.values()].filter((s) => s.background && !s.ended);
}

function renderTabs() {
  const d = termDrawer();
  d.querySelector(".term-tabs").innerHTML = [...term.sessions.values()].map((s) =>
    `<button class="term-tab ${s.id === term.active ? "on" : ""} ${s.ended ? "ended" : ""}" data-term="${esc(s.id)}" title="${esc(s.title)}">
      <span class="term-dot"></span>${s.background ? "◐ " : ""}${esc(s.title)}</button>`).join("");
  const s = activeSession();
  d.classList.toggle("ended", !!s?.ended);
  d.querySelector(".term-hint").textContent = !s ? "" : s.ended ? "ended · recorded with the step"
    : s.shell ? "the run's variables are loaded · type exit (or End session) to go back to the run"
    : s.background ? "keeps running while the run goes on" : "type here when it asks · recorded with the step";
  const side = d.querySelector(".term-side");
  side.hidden = !(s?.shell && s.command);
  d.querySelector(".term-cmd").textContent = s?.command || "";
  d.querySelector(".term-expect").innerHTML = s?.expect ? `<div class="expect"><b>Expect:</b> ${esc(s.expect)}</div>` : "";
}

// showTerm makes a session the visible tab and opens the drawer.
function showTerm(id) {
  const s = term.sessions.get(id ?? term.active);
  if (!s) return;
  const d = termDrawer();
  term.active = s.id;
  d.hidden = false;
  document.body.classList.add("term-open");
  for (const o of term.sessions.values()) o.el.hidden = o !== s;
  if (!s.opened) { s.xt.open(s.el); s.opened = true; }
  renderTabs();
  s.fit.fit();
  s.xt.focus();
}

// Kept for the run page's "Show terminal" link.
function showTermDrawer() { showTerm(); }

function closeTermDrawer() {
  const d = $("#term-drawer");
  if (d) d.hidden = true;
  document.body.classList.remove("term-open");
}

// resetTerm is called when leaving a run page.
function resetTerm() {
  closeTermDrawer();
  for (const s of term.sessions.values()) s.xt.dispose();
  term.sessions.clear();
  term.runId = term.active = null;
  $("#term-drawer .term-body")?.replaceChildren();
}

function termEvent(runId, e) {
  const t = e.term;
  if (e.kind === "term_start") return startTerm(runId, t, e.step);
  const s = term.sessions.get(t.id);
  if (!s) return;
  if (e.kind === "term") {
    s.xt.write(Uint8Array.from(atob(t.data), (c) => c.charCodeAt(0)));
  } else if (e.kind === "term_end") {
    s.ended = true;
    s.xt.write("\r\n\x1b[2m── session ended ──\x1b[0m\r\n");
    renderTabs();
    // It's recorded with the step, so get out of the way, unless another
    // session is still going.
    setTimeout(() => {
      if (term.active !== s.id) return;
      const next = [...term.sessions.values()].find((o) => !o.ended && !o.background);
      if (next) showTerm(next.id); else closeTermDrawer();
    }, 1500);
  }
}

function startTerm(runId, t, step) {
  term.runId = runId;
  const d = termDrawer();
  const el = document.createElement("div");
  el.className = "term-pane";
  el.hidden = true;
  d.querySelector(".term-body").append(el);
  const dark = matchMedia("(prefers-color-scheme: dark)").matches;
  const xt = new Terminal({
    cursorBlink: true, scrollback: 5000, fontSize: 13,
    fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
    theme: dark ? undefined : { background: "#15171c", foreground: "#e4e6eb" },
  });
  const fit = new FitAddon.FitAddon();
  xt.loadAddon(fit);
  const s = { id: t.id, title: t.title, step, shell: !!t.shell, background: !!t.background, command: t.command, expect: t.expect, xt, fit, el, ended: false, opened: false };
  // ⌘C / Ctrl+Shift+C copy the selection; plain Ctrl+C still interrupts.
  xt.attachCustomKeyEventHandler((ev) => {
    const copyKey = ev.key.toLowerCase() === "c" && (ev.metaKey || (ev.ctrlKey && ev.shiftKey));
    if (ev.type === "keydown" && copyKey && xt.hasSelection()) { copyTerm(); return false; }
    return true;
  });
  xt.onData((data) => {
    if (s.ended) return;
    term.queue += data;
    clearTimeout(term.timer);
    term.timer = setTimeout(() => { const q = term.queue; term.queue = ""; if (q) sendTerm(s, { data: q }); }, 12);
  });
  xt.onResize(({ rows, cols }) => sendTerm(s, { rows, cols }));
  term.sessions.set(s.id, s);
  if (s.background) {
    // Runs alongside the rest; it gets a tab but doesn't take over.
    renderTabs();
    if (d.hidden) toast(`Started in the background: ${t.title}`);
  } else {
    showTerm(s.id);
  }
}

async function copyTerm() {
  const s = activeSession();
  if (!s) return;
  let text = s.xt.getSelection();
  if (!text) {
    const buf = s.xt.buffer.active, lines = [];
    for (let i = 0; i < buf.length; i++) lines.push(buf.getLine(i)?.translateToString(true) ?? "");
    text = lines.join("\n").replace(/\n+$/, "\n");
  }
  try {
    await navigator.clipboard.writeText(text);
    toast(s.xt.hasSelection() ? "Copied selection" : "Copied the whole session");
  } catch { toast("Couldn't reach the clipboard; select the text and press ⌘C"); }
}

function sendTerm(s, extra) {
  if (!term.runId || s.ended) return;
  api("POST", `/api/runs/${enc(term.runId)}/term`, { id: s.id, ...extra }).catch(() => {});
}
