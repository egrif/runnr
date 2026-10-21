"use strict";

// The function picker: insert a function's name (and how it's called) at
// the cursor in a code field. The ƒ button beside a code field opens it,
// and Ctrl+Shift+F opens it for the code field last typed in. It lists the
// runbook's own functions, those of the libraries it uses, and runnr's
// (runnr_ask, runnr_confirm).

const fnPick = { last: null, context: null };

// Built into every step.
const RUNNR_FNS = {
  runnr_ask: { description: "Ask the operator a question and print the answer. Usage: runnr_ask PROMPT", from: "runnr" },
  runnr_confirm: { description: "Ask the operator yes or no; succeeds on yes. Usage: runnr_confirm PROMPT", from: "runnr" },
};

// setFnContext tells the picker which runbook's functions to offer: a
// function returning the runbook (as the form has it) or its YAML.
function setFnContext(get) { fnPick.context = get; }

const isCodeField = (el) => el?.tagName === "TEXTAREA" && (el.classList.contains("code") || el.classList.contains("editor"));

document.addEventListener("focusin", (e) => { if (isCodeField(e.target)) fnPick.last = e.target; });
document.addEventListener("keydown", (e) => {
  if (e.ctrlKey && e.shiftKey && !e.altKey && !e.metaKey && e.key.toLowerCase() === "f") {
    const t = isCodeField(document.activeElement) ? document.activeElement : fnPick.last;
    if (!t || !t.isConnected) return; // not editing code: leave the key alone
    e.preventDefault();
    openFnPicker(t);
  }
});
document.addEventListener("click", (e) => {
  const b = e.target.closest("[data-fnpick]");
  if (!b) return;
  e.preventDefault();
  e.stopPropagation();
  const t = b.dataset.fnpick ? document.querySelector(b.dataset.fnpick) : b.closest(".code-wrap")?.querySelector("textarea");
  if (t) openFnPicker(t);
}, true);

// The ƒ button for a code field (it goes in the field's .code-wrap).
const fnPickBtn = (sel = "") => `<button type="button" class="fn-pick-btn" data-fnpick="${esc(sel)}" title="Insert a function (Ctrl+Shift+F)">ƒ</button>`;

async function pickerFunctions() {
  let rb = null;
  try {
    const c = fnPick.context?.();
    if (typeof c === "string") {
      const res = await api("POST", "/api/parse", c, true);
      rb = res.ok ? res.runbook : null;
    } else rb = c;
  } catch {}
  await loadLibs(rb?.libraries || []);
  return { ...fnsOf(rb), ...Object.fromEntries(Object.entries(RUNNR_FNS).map(([k, f]) => [k, { ...f }])) };
}

// usageOf is how a function is called, from "Usage: name ARGS" in its
// description, else just its name.
function usageOf(name, f) {
  const m = (f.description || "").match(new RegExp(`Usage:\\s*(${name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\b[^\\n]*)`));
  return m ? m[1].trim() : name;
}

async function openFnPicker(target) {
  const start = target.selectionStart, end = target.selectionEnd;
  const fns = await pickerFunctions();
  const names = Object.keys(fns).sort((a, b) => (fns[a].from === "runnr") - (fns[b].from === "runnr") || a.localeCompare(b));
  const dlg = document.createElement("dialog");
  dlg.className = "fn-picker";
  dlg.innerHTML = `<div class="row"><b>Insert a function</b><span class="spacer"></span><span class="muted small">↑↓ choose · Enter inserts · Shift+Enter inserts how it's called · Esc</span></div>
    <input type="search" placeholder="Filter functions…" autocomplete="off">
    <ul class="fn-list-pick"></ul>`;
  document.body.append(dlg);
  const input = dlg.querySelector("input"), list = dlg.querySelector("ul");
  let shown = [], sel = 0;
  const draw = () => {
    const q = input.value.toLowerCase();
    shown = names.filter((n) => !q || n.toLowerCase().includes(q) || (fns[n].description || "").toLowerCase().includes(q));
    sel = Math.min(sel, Math.max(0, shown.length - 1));
    list.innerHTML = shown.map((n, k) => `<li class="${k === sel ? "on" : ""}" data-k="${k}">
        <code>${esc(n)}</code> <span class="muted small">${esc(fns[n].from || "")}</span>
        <div class="small">${esc((fns[n].description || "").split("\n")[0])}</div></li>`).join("")
      || `<li class="muted">${names.length ? "No function matches" : "This runbook has no functions (add a library, or define some)"}</li>`;
    list.querySelector("li.on")?.scrollIntoView({ block: "nearest" });
  };
  const insert = (k, withUsage) => {
    const n = shown[k];
    if (!n) return;
    const text = withUsage ? usageOf(n, fns[n]) : `${n} `;
    dlg.close();
    target.focus();
    target.setRangeText(text, start, end, "end");
    target.dispatchEvent(new Event("input", { bubbles: true }));
    target.dispatchEvent(new Event("change", { bubbles: true }));
  };
  input.oninput = () => { sel = 0; draw(); };
  input.onkeydown = (e) => {
    if (e.key === "ArrowDown") { sel = Math.min(shown.length - 1, sel + 1); draw(); e.preventDefault(); }
    if (e.key === "ArrowUp") { sel = Math.max(0, sel - 1); draw(); e.preventDefault(); }
    if (e.key === "Enter") { e.preventDefault(); insert(sel, e.shiftKey); }
  };
  list.onclick = (e) => { const li = e.target.closest("[data-k]"); if (li) insert(+li.dataset.k, e.shiftKey); };
  dlg.onclose = () => { dlg.remove(); if (document.activeElement === document.body) target.focus(); };
  draw();
  dlg.showModal();
  input.focus();
}
