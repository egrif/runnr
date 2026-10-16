"use strict";

// In-browser terminal for steps that need one (terminal steps, shells, run
// it myself). It lives in a drawer outside #main, so re-rendering the run
// page doesn't disturb it. Output arrives as run events; keystrokes and
// size changes go back with POST /api/runs/{id}/term.

const term = { runId: null, id: null, xt: null, fit: null, queue: "", timer: null, ended: false };

function termDrawer() {
  let d = $("#term-drawer");
  if (d) return d;
  d = document.createElement("div");
  d.id = "term-drawer";
  d.hidden = true;
  d.innerHTML = `
    <div class="term-head">
      <span class="term-dot"></span><span class="term-title"></span>
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
    term.fit?.fit();
  };
  d.querySelector(".term-end").onclick = () => {
    if (!term.ended && confirm("End this terminal session? Anything still running in it is hung up.")) sendTerm({ end: true });
  };
  d.querySelector(".term-copy").onclick = async () => {
    try { await navigator.clipboard.writeText(d.querySelector(".term-cmd").textContent); toast("Copied"); }
    catch { toast("Select the text and copy it"); }
  };
  window.addEventListener("resize", () => { if (term.fit && !d.hidden) term.fit.fit(); });
  return d;
}

// copyTerm copies the selection, or everything the session showed.
async function copyTerm() {
  if (!term.xt) return;
  let text = term.xt.getSelection();
  if (!text) {
    const buf = term.xt.buffer.active, lines = [];
    for (let i = 0; i < buf.length; i++) lines.push(buf.getLine(i)?.translateToString(true) ?? "");
    text = lines.join("\n").replace(/\n+$/, "\n");
  }
  try {
    await navigator.clipboard.writeText(text);
    toast(term.xt.hasSelection() ? "Copied selection" : "Copied the whole session");
  } catch { toast("Couldn't reach the clipboard; select the text and press ⌘C"); }
}

// showTermDrawer brings a hidden session back.
function showTermDrawer() {
  const d = $("#term-drawer");
  if (!d || !term.xt) return;
  d.hidden = false;
  document.body.classList.add("term-open");
  term.fit.fit();
  term.xt.focus();
}

function closeTermDrawer() {
  const d = $("#term-drawer");
  if (d) d.hidden = true;
  document.body.classList.remove("term-open");
}

// resetTerm is called when leaving a run page.
function resetTerm() {
  closeTermDrawer();
  term.xt?.dispose();
  Object.assign(term, { runId: null, id: null, xt: null, fit: null, queue: "", ended: false });
}

function termEvent(runId, e) {
  const t = e.term;
  if (e.kind === "term_start") return openTerm(runId, t, e.step);
  if (t.id !== term.id || !term.xt) return;
  if (e.kind === "term") {
    term.xt.write(Uint8Array.from(atob(t.data), (c) => c.charCodeAt(0)));
  } else if (e.kind === "term_end") {
    term.ended = true;
    term.xt.write("\r\n\x1b[2m── session ended ──\x1b[0m\r\n");
    $("#term-drawer").classList.add("ended");
    $("#term-drawer .term-hint").textContent = "session ended · recorded with the step";
    // It's recorded with the step, so get out of the way (unless another session started).
    const ended = t.id;
    setTimeout(() => { if (term.id === ended) closeTermDrawer(); }, 1500);
  }
}

function openTerm(runId, t, step) {
  term.xt?.dispose();
  Object.assign(term, { runId, id: t.id, ended: false, queue: "" });
  const d = termDrawer();
  d.hidden = false;
  d.classList.remove("ended");
  document.body.classList.add("term-open");
  d.querySelector(".term-title").textContent = t.title;
  d.querySelector(".term-hint").textContent = t.shell
    ? "the run's variables are loaded · type exit (or End session) to go back to the run"
    : "type here when it asks · recorded with the step";
  // Shells show the step's commands alongside, to run or adapt.
  const side = d.querySelector(".term-side");
  side.hidden = !(t.shell && t.command);
  d.querySelector(".term-cmd").textContent = t.command || "";
  d.querySelector(".term-expect").innerHTML = t.expect ? `<div class="expect"><b>Expect:</b> ${esc(t.expect)}</div>` : "";
  const body = d.querySelector(".term-body");
  body.innerHTML = "";
  const dark = matchMedia("(prefers-color-scheme: dark)").matches;
  term.xt = new Terminal({
    cursorBlink: true, convertEol: false, scrollback: 5000, fontSize: 13,
    fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
    theme: dark ? undefined : { background: "#15171c", foreground: "#e4e6eb" },
  });
  term.fit = new FitAddon.FitAddon();
  term.xt.loadAddon(term.fit);
  // ⌘C / Ctrl+Shift+C copy the selection; plain Ctrl+C still interrupts.
  term.xt.attachCustomKeyEventHandler((ev) => {
    const copyKey = ev.key.toLowerCase() === "c" && (ev.metaKey || (ev.ctrlKey && ev.shiftKey));
    if (ev.type === "keydown" && copyKey && term.xt.hasSelection()) {
      copyTerm();
      return false;
    }
    return true;
  });
  term.xt.open(body);
  term.fit.fit();
  term.xt.focus();
  term.xt.onData((data) => {
    if (term.ended) return;
    term.queue += data;
    clearTimeout(term.timer);
    term.timer = setTimeout(flushTerm, 12);
  });
  term.xt.onResize(({ rows, cols }) => sendTerm({ rows, cols }));
  sendTerm({ rows: term.xt.rows, cols: term.xt.cols });
}

function flushTerm() {
  if (!term.queue) return;
  const data = term.queue;
  term.queue = "";
  sendTerm({ data });
}

function sendTerm(extra) {
  if (!term.runId || term.ended) return;
  api("POST", `/api/runs/${enc(term.runId)}/term`, { id: term.id, ...extra }).catch(() => {});
}
