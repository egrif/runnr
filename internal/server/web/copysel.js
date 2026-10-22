"use strict";

// Copy on select: selecting text copies it, as in many terminals. It
// covers the browser terminals (term.js calls copySelected), code fields
// and inputs, and text on the page (run output, commands, logs). The
// sidebar's "Copy on select" turns it off (remembered), since selecting
// text in order to paste over it would copy it first.

const copySel = { last: "", on: true };
try { copySel.on = localStorage.getItem("runnr.copyOnSelect") !== "off"; } catch {}

function setCopyOnSelect(on) {
  copySel.on = on;
  try { localStorage.setItem("runnr.copyOnSelect", on ? "on" : "off"); } catch {}
}

// copySelected puts text on the clipboard (once per selection).
async function copySelected(text) {
  if (!copySel.on || !text || !text.trim() || text === copySel.last) return;
  try {
    await navigator.clipboard.writeText(text);
    copySel.last = text;
    toast(`Copied ${text.length > 40 ? `${text.length} characters` : `“${text.trim()}”`}`);
  } catch {}
}

// selectedText is what's selected now: in a text field, or on the page.
function selectedText() {
  const el = document.activeElement;
  if (el && (el.tagName === "TEXTAREA" || (el.tagName === "INPUT" && /^(text|search|url|email)$/.test(el.type)))) {
    return el.selectionStart !== el.selectionEnd ? el.value.slice(el.selectionStart, el.selectionEnd) : "";
  }
  if (el?.closest?.(".xterm")) return ""; // terminals copy their own selection
  return window.getSelection()?.toString() || "";
}

// A selection ends with the mouse button up, or a shift-arrow key up.
document.addEventListener("mouseup", (e) => {
  if (e.button !== 0 || e.target.closest?.(".xterm, input[type=password], button, a, select")) return;
  setTimeout(() => copySelected(selectedText()), 0);
});
document.addEventListener("keyup", (e) => {
  if (e.key === "Shift" || (e.shiftKey && /^(Arrow|Home|End|Page)/.test(e.key))) copySelected(selectedText());
});
// A new selection can copy the same text again.
document.addEventListener("selectionchange", () => { if (!selectedText()) copySel.last = ""; });
