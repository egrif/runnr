"use strict";

// In-browser terminals for steps that need one (terminal steps, shells,
// background steps). Each sits in the page where its step is: a page marks
// the spot with <div data-term-slot="RUN:STEP">, and termMountAll() (after
// the page draws) puts each session in its spot, header and all. Moving the
// element keeps the terminal as it is, so re-drawing the page doesn't
// disturb it. (The run console hosts them in its panes instead:
// term.host = { started(s), ended(s), show(s) }.) Output arrives as run
// events; keystrokes and size changes go back with POST /api/runs/{id}/term.

const term = { sessions: new Map(), queue: "", timer: null, host: null, active: null };

// pasteable wraps commands that turn on shell options (set -e, -u,
// pipefail...) in a subshell, so pasting them into an interactive shell
// doesn't leave the options on: set -e there closes the shell at the first
// failing command, and -u breaks prompt and cd hooks.
function pasteable(cmd) {
  const sets = /^\s*(set\s+(-[a-zA-Z]*[eu]|-o\s+(errexit|nounset|pipefail))|setopt\b)/m.test(cmd);
  return sets ? { text: `(\n${cmd.replace(/\n+$/, "")}\n)\n`, wrapped: true } : { text: cmd, wrapped: false };
}

const activeSession = () => term.sessions.get(term.active);

// runningBackground lists background sessions still going.
function runningBackground() {
  return [...term.sessions.values()].filter((s) => s.background && !s.ended);
}

// termResult is how a session's command ended, for people: [text, kind].
function termResult(s) {
  if (!s.ended) return ["", ""];
  if (s.exit == null) return ["ended", ""];
  if (s.exit === 0) return ["✔ completed (exit 0)", "ok"];
  if (s.exit < 0) return ["■ stopped", "stopped"];
  return [`✘ exited with status ${s.exit}`, "fail"];
}

// termSlotKey is where a session goes: its run and step.
const termSlotKey = (runId, step) => `${runId}:${step}`;

// hasTerm reports whether a run's step has a session to show.
function hasTerm(runId, step) {
  return [...term.sessions.values()].some((s) => s.slot === termSlotKey(runId, step) && !s.gone);
}

// showTerm brings a session into view and gives it the keyboard.
function showTerm(id) {
  const s = term.sessions.get(id ?? term.active);
  if (!s) return;
  term.active = s.id;
  if (term.host) return term.host.show(s);
  termMountAll();
  s.box?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  s.xt.focus();
}

// termMountAll puts every session in its slot on the page (if the page has
// one for it), with a header: what it is, a hint, Copy and End session.
function termMountAll() {
  if (term.host) return;
  for (const s of term.sessions.values()) {
    if (s.gone) continue;
    // Its step's slot, or one that takes any of its run's terminals ("RUN:*").
    const slot = document.querySelector(`[data-term-slot="${CSS.escape(s.slot)}"]`) || document.querySelector(`[data-term-slot="${CSS.escape(`${s.runId}:*`)}"]`);
    if (!slot) continue;
    if (!s.box) {
      s.box = document.createElement("div");
      s.box.className = "term-box";
      s.box.innerHTML = `<div class="term-head"><span class="term-dot"></span><b class="term-title"></b><span class="muted small term-hint"></span>
          <span class="spacer"></span><button class="link term-copy-out" title="Copy the selection, or the whole session if nothing is selected (or select and press ⌘C / Ctrl+Shift+C)">Copy</button>
          <button class="link term-end" title="Hang up, like closing a terminal window">End session</button>
          <button class="term-x term-close" title="Close (its output is recorded with the step)">✕</button></div>
        <details class="term-side" hidden><summary>The step's commands, and functions you can call</summary>
          <div class="row"><span class="spacer"></span><button class="link term-copy">Copy commands</button></div>
          <pre class="term-cmd"></pre><div class="term-expect"></div><div class="term-fns"></div></details>
        <div class="term-body"></div>`;
      s.box.querySelector(".term-copy-out").onclick = () => copyTerm(s);
      s.box.querySelector(".term-close").onclick = () => termRemove(s);
      s.box.querySelector(".term-end").onclick = () => {
        if (!s.ended && confirm(`End “${s.title}”? Anything still running in it is hung up.`)) sendTerm(s, { end: true });
      };
      s.box.querySelector(".term-copy").onclick = async () => {
        const { text, wrapped } = pasteable(s.command || "");
        try { await navigator.clipboard.writeText(text); toast(wrapped ? "Copied, inside ( … ) so its set -e/-u don't stay on in this shell" : "Copied"); }
        catch { toast("Select the text and copy it"); }
      };
      s.box.querySelector(".term-body").append(s.el);
      s.el.hidden = false;
      new ResizeObserver(() => { if (s.el.offsetParent) try { s.fit.fit(); } catch {} }).observe(s.el);
    }
    termHead(s);
    if (s.box.parentElement !== slot) {
      slot.append(s.box);
      // Re-drawing the page dropped the keyboard; the terminal a step waits on takes it back.
      const idle = !document.activeElement || document.activeElement === document.body;
      if (s.opened && idle && !s.ended && !s.background && s.id === term.active) s.xt.focus();
    }
    if (!s.opened) {
      s.xt.open(s.el);
      s.opened = true;
      requestAnimationFrame(() => { try { s.fit.fit(); } catch {} });
      if (!s.background) s.xt.focus(); // a step waiting on its terminal gets the keyboard
    }
  }
}

function termHead(s) {
  const b = s.box;
  const [result, kind] = termResult(s);
  b.classList.toggle("ended", !!s.ended);
  b.dataset.result = kind;
  b.querySelector(".term-title").textContent = `${s.background ? "◐ " : "⌨ "}${s.title}`;
  b.querySelector(".term-close").hidden = !(s.ended && s.background);
  b.querySelector(".term-hint").textContent = s.ended ? `${result} · recorded with the step`
    : s.shell ? "the run's variables are loaded · type exit (or End session) to go back to the run"
    : s.background ? "keeps running while the run goes on" : "type here when it asks · recorded with the step";
  b.querySelector(".term-end").hidden = !!s.ended;
  const side = b.querySelector(".term-side");
  side.hidden = !(s.shell && (s.command || s.functions?.length));
  b.querySelector(".term-cmd").textContent = s.command || "";
  b.querySelector(".term-fns").innerHTML = s.functions?.length ? `<b>Functions you can call</b><table class="kv">${s.functions.map((f) =>
    `<tr><td><code>${esc(f.name)}</code></td><td>${esc(f.description || "")} <span class="muted">${esc(f.from === "runbook" ? "" : f.from)}</span></td></tr>`).join("")}</table>` : "";
  b.querySelector(".term-expect").innerHTML = s.expect ? `<div class="expect"><b>Expect:</b> ${esc(s.expect)}</div>` : "";
}

// resetTerm is called when leaving a page with terminals.
function resetTerm() {
  for (const s of term.sessions.values()) s.xt.dispose();
  term.sessions.clear();
  term.active = null;
  term.host = null;
}

// base is where its terminals take keystrokes (default: the run's); a
// session from elsewhere (a console's shell) is keyed by runId too, since
// terminal ids are only unique within their stream.
function termEvent(runId, e, base) {
  const t = e.term;
  if (e.kind === "term_start") return startTerm(runId, t, e.step, base);
  const s = term.sessions.get(base ? `${runId}/${t.id}` : t.id);
  if (!s) return;
  if (e.kind === "term") {
    s.xt.write(Uint8Array.from(atob(t.data), (c) => c.charCodeAt(0)));
  } else if (e.kind === "term_end") {
    s.ended = true;
    s.exit = t.exit;
    const [result, kind] = termResult(s);
    const color = { ok: "32", fail: "31", stopped: "33" }[kind] || "2";
    s.xt.write(`\r\n\x1b[1;${color}m── ${result} ──\x1b[0m\r\n`);
    if (term.host) return term.host.ended(s);
    if (s.box) termHead(s);
    // A background job stays, saying how it ended, until closed (✕), unless
    // its step says to close it; a step's own terminal is put away shortly
    // (the step's log has it).
    if (s.background) { if (s.closeOnExit) termRemove(s); }
    else setTimeout(() => termRemove(s), 2500);
  }
}

// termRemove takes a session off the page.
function termRemove(s) {
  if (s.gone) return;
  s.gone = true;
  s.box?.remove();
  s.xt.dispose();
  term.sessions.delete(s.id);
}

function startTerm(runId, t, step, base) {
  const el = document.createElement("div");
  el.className = "term-pane";
  el.hidden = true;
  const dark = matchMedia("(prefers-color-scheme: dark)").matches;
  const xt = new Terminal({
    cursorBlink: true, scrollback: 5000, fontSize: 13,
    fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
    theme: dark ? undefined : { background: "#15171c", foreground: "#e4e6eb" },
  });
  const fit = new FitAddon.FitAddon();
  xt.loadAddon(fit);
  const s = { id: base ? `${runId}/${t.id}` : t.id, termId: t.id, base: base || `/api/runs/${enc(runId)}`, runId, slot: termSlotKey(runId, step), title: t.title, step, shell: !!t.shell, background: !!t.background, closeOnExit: !!t.close_on_exit, command: t.command, expect: t.expect, functions: t.functions, xt, fit, el, ended: false, opened: false };
  // ⌘C / Ctrl+Shift+C copy the selection; plain Ctrl+C still interrupts.
  xt.attachCustomKeyEventHandler((ev) => {
    const copyKey = ev.key.toLowerCase() === "c" && (ev.metaKey || (ev.ctrlKey && ev.shiftKey));
    if (ev.type === "keydown" && copyKey && xt.hasSelection()) { copyTerm(s); return false; }
    return true;
  });
  xt.onData((data) => {
    if (s.ended) return;
    term.queue += data;
    clearTimeout(term.timer);
    term.timer = setTimeout(() => { const q = term.queue; term.queue = ""; if (q) sendTerm(s, { data: q }); }, 12);
  });
  xt.onResize(({ rows, cols }) => sendTerm(s, { rows, cols }));
  // Copy on select: once the mouse lets go of a selection.
  el.addEventListener("mouseup", () => setTimeout(() => { if (xt.hasSelection()) copySelected(xt.getSelection()); }, 0));
  term.sessions.set(s.id, s);
  if (!s.background) term.active = s.id;
  if (term.host) return term.host.started(s);
  termMountAll();
}

async function copyTerm(s = activeSession()) {
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
  if (!s.runId || s.ended) return;
  api("POST", `${s.base}/term`, { id: s.termId, ...extra }).catch(() => {});
}
