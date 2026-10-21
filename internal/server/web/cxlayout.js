"use strict";

// The console's panes, as a layout the operator arranges: a tree of splits
// (side by side, or stacked) with a pane at each leaf. Drag a pane by its
// bar onto another: onto its left, right, top or bottom fifth to split it
// there (left and right win in the corners), anywhere else to swap the two.
// A pane's bar can split it (a new empty pane beside or below) or close it;
// an empty pane picks what it's for. The layout is kept in the browser.
//
// Each kind of pane is one element, built once and moved between leaves
// (so a terminal or an edit in progress survives a rearrangement); kinds
// not on screen wait in a hidden holder, where the console still draws
// into them.

// Kinds of pane. multi: there can be several (background jobs are spread
// across the background targets; each shell pane holds its own shell).
const CX_EDIT_HOST = `<div class="cx-edit-host" hidden><div class="cx-edit-head cx-pane-head"></div><div class="cx-edit-body"></div></div>`;
const CX_KINDS = {
  steps: { label: "Step list", html: `<nav class="cx-list"></nav>` },
  details: { label: "Step definition", html: `<section class="cx-pane cx-details"><div class="cx-pane-head"></div><div class="cx-pane-body"></div></section>` },
  edit: { label: "Step edit", html: `<section class="cx-pane cx-editpane"><div class="cx-pane-body"><p class="muted cx-edit-empty">Choose ✎ Edit this step in the step definition: it opens here.</p>${CX_EDIT_HOST}</div></section>` },
  action: { label: "Step activity", html: `<section class="cx-pane cx-action"><div class="cx-pane-head"></div><div class="cx-pane-body"><div class="cx-action-body"></div><div class="cx-fg" hidden><div class="cx-tabs"></div><div class="cx-term"></div></div>${CX_EDIT_HOST}</div></section>` },
  terminal: { label: "Terminal target", html: `<section class="cx-pane cx-termpane"><div class="cx-pane-head"><b>Terminal</b><span class="muted small cx-termpane-title"></span><span class="spacer"></span><button class="link" data-tp-end hidden>End session</button></div><div class="cx-pane-body cx-term"><p class="muted cx-termpane-empty">A terminal step's terminal, or a shell opened from a step's question, shows here.</p></div></section>` },
  background: { label: "Background target", multi: true, html: `<section class="cx-pane cx-bg"><div class="cx-pane-head"><b>Background</b><div class="cx-tabs"></div><span class="cx-bg-status small"></span></div><div class="cx-pane-body cx-term"><p class="muted cx-bg-empty">Background steps' terminals show here while they run.</p></div></section>` },
  shell: { label: "Shell", multi: true, html: `<section class="cx-pane cx-shellpane"><div class="cx-pane-head"><b>Shell</b><span class="muted small cx-shell-title"></span><span class="spacer"></span><button class="primary" data-sh-open>⌨ Open shell</button><button class="link" data-sh-end hidden>End session</button></div><div class="cx-pane-body cx-term"><p class="muted cx-shell-empty"></p></div></section>` },
};

const CX_DEFAULT_LAYOUT = { dir: "row", kids: [{ k: "steps" }, { dir: "col", kids: [{ k: "details" }, { k: "action" }, { k: "background" }], sizes: [1, 1.4, 0.8] }], sizes: [1, 3.2] };

// ---------- the tree ----------

const cxIsLeaf = (n) => n && !n.kids;
let cxEmptySeq = 0;
const cxNewEmpty = () => ({ k: "empty", id: `e${Date.now().toString(36)}${++cxEmptySeq}` });
const cxLeafId = (n) => n.id || n.k;
// cxNewLeaf is a pane of kind k: a kind there's one of is named after it;
// another background target or shell gets an id of its own.
const cxNewLeaf = (state, k) => (CX_KINDS[k]?.multi && (k !== "background" || cxFind(state.layout, "background"))
  ? { k, id: `${k}~${Date.now().toString(36)}${++cxEmptySeq}` } : { k });

function cxLoadLayout() {
  try {
    const t = JSON.parse(localStorage.getItem("runnr.consoleLayout") || "null");
    if (t && cxValidLayout(t)) return t;
  } catch {}
  return structuredClone(CX_DEFAULT_LAYOUT);
}

function cxValidLayout(t) {
  const seen = new Set();
  const ok = (n) => {
    if (cxIsLeaf(n)) {
      if (n.k !== "empty" && !CX_KINDS[n.k]) return false;
      const id = cxLeafId(n);
      if (seen.has(id)) return false;
      seen.add(id);
      return true;
    }
    return (n.dir === "row" || n.dir === "col") && Array.isArray(n.kids) && n.kids.length >= 2 && n.kids.every(ok);
  };
  return ok(t);
}

function cxSaveLayout(state) { try { localStorage.setItem("runnr.consoleLayout", JSON.stringify(state.layout)); } catch {} }

// cxFind locates a leaf by id: [parent split or null, index, node].
function cxFind(node, id, parent = null, i = 0) {
  if (cxIsLeaf(node)) return cxLeafId(node) === id ? [parent, i, node] : null;
  for (let k = 0; k < node.kids.length; k++) {
    const r = cxFind(node.kids[k], id, node, k);
    if (r) return r;
  }
  return null;
}

// cxLeafIds are the ids of the panes of kind on screen, in layout order.
function cxLeafIds(state, kind) {
  const out = [];
  const walk = (n) => (cxIsLeaf(n) ? n.k === kind && out.push(cxLeafId(n)) : n.kids.forEach(walk));
  walk(state.layout);
  return out;
}
const cxPlaced = (state, kind) => cxLeafIds(state, kind).length > 0;

function cxSizesOf(n) {
  if (!n.sizes || n.sizes.length !== n.kids.length) n.sizes = n.kids.map(() => 1);
  return n.sizes;
}

// cxReplace puts node in old's place (the root, or a child of its parent).
function cxReplace(state, parent, i, node) {
  if (!parent) state.layout = node;
  else parent.kids[i] = node;
}

// cxSplit puts leaf beside the leaf targetId, on side (left|right|top|bottom).
function cxSplit(state, targetId, side, leaf) {
  const f = cxFind(state.layout, targetId);
  if (!f) return;
  const [parent, i, target] = f;
  const dir = side === "left" || side === "right" ? "row" : "col";
  const before = side === "left" || side === "top";
  if (parent && parent.dir === dir) {
    const sizes = cxSizesOf(parent);
    const half = sizes[i] / 2;
    sizes[i] = half;
    parent.kids.splice(before ? i : i + 1, 0, leaf);
    sizes.splice(before ? i : i + 1, 0, half);
    return;
  }
  cxReplace(state, parent, i, { dir, kids: before ? [leaf, target] : [target, leaf], sizes: [1, 1] });
}

// cxRemove takes leaf id out; a split left with one pane becomes that pane.
function cxRemove(state, id) {
  const f = cxFind(state.layout, id);
  if (!f) return;
  const [parent, i] = f;
  if (!parent) { state.layout = cxNewEmpty(); return; }
  parent.kids.splice(i, 1);
  cxSizesOf(parent).splice(i, 1);
  if (parent.kids.length === 1) {
    const only = parent.kids[0];
    const g = cxFindParent(state.layout, parent);
    if (g) g[0].kids[g[1]] = only; else state.layout = only;
  }
}

function cxFindParent(node, child) {
  if (cxIsLeaf(node)) return null;
  for (let k = 0; k < node.kids.length; k++) {
    if (node.kids[k] === child) return [node, k];
    const r = cxFindParent(node.kids[k], child);
    if (r) return r;
  }
  return null;
}

function cxSwap(state, a, b) {
  const fa = cxFind(state.layout, a), fb = cxFind(state.layout, b);
  if (!fa || !fb) return;
  const [pa, ia, na] = fa, [pb, ib, nb] = fb;
  cxReplace(state, pa, ia, nb);
  cxReplace(state, pb, ib, na);
}

// ---------- drawing ----------

// cxMountConsole draws the console's frame: the top, the layout, and the
// panes (in their places, or the holder).
function cxMountConsole(state) {
  document.body.classList.add("console-mode");
  main.innerHTML = `<div class="cx"><header class="cx-top"></header><div class="cx-body"></div><div class="cx-holder" hidden></div><div class="cx-drop" hidden><span></span></div></div>`;
  state.frames = {};
  const holder = main.querySelector(".cx-holder");
  for (const [k, d] of Object.entries(CX_KINDS)) if (!d.multi || k === "background") holder.append(cxFrame(state, { k }));
  state.layout = cxLoadLayout();
  cxDrawLayout(state);
}

function cxFrame(state, leaf) {
  const id = cxLeafId(leaf);
  if (state.frames[id]) return state.frames[id];
  const el = document.createElement("div");
  el.className = `cx-frame cx-frame-${leaf.k}`;
  el.dataset.kind = leaf.k;
  el.dataset.leaf = id;
  const label = leaf.k === "empty" ? "Empty pane" : CX_KINDS[leaf.k].label;
  el.innerHTML = `<div class="cx-frame-bar" title="Drag to move: onto another pane's edge to split it, its middle to swap">
      <span class="cx-grip">⠿</span><span class="cx-frame-title">${esc(label)}</span><span class="spacer"></span>
      <button data-fsplit="right" title="Split: a new pane to the right">◫</button>
      <button data-fsplit="bottom" title="Split: a new pane below">⊟</button>
      <button data-fclose title="Close this pane${leaf.k === "empty" ? "" : " (bring it back from an empty pane)"}">✕</button>
    </div>
    <div class="cx-frame-body">${leaf.k === "empty" ? `<div class="cx-empty"><p class="muted">An empty pane.</p><label>Use it for <select data-fuse></select></label></div>` : CX_KINDS[leaf.k].html}</div>`;
  state.frames[id] = el;
  el.querySelector("[data-fclose]").onclick = () => {
    cxRemove(state, id);
    // An empty pane, or another background target or shell, goes; the
    // others wait in the holder to be put back.
    if (leaf.k === "empty" || leaf.id) { el.remove(); delete state.frames[id]; }
    cxLayoutChanged(state);
  };
  el.querySelectorAll("[data-fsplit]").forEach((b) => (b.onclick = () => {
    cxSplit(state, id, b.dataset.fsplit, cxNewEmpty());
    cxLayoutChanged(state);
  }));
  el.querySelector(".cx-frame-bar").addEventListener("pointerdown", (e) => cxDragStart(state, id, e));
  return el;
}

// cxLayoutChanged redraws after the tree changed, and keeps it.
function cxLayoutChanged(state) {
  cxSaveLayout(state);
  cxDrawLayout(state);
  (state.render || cxRender)(state);
}

function cxDrawLayout(state) {
  const body = main.querySelector(".cx-body"), holder = main.querySelector(".cx-holder");
  for (const el of Object.values(state.frames)) holder.append(el);
  body.replaceChildren(cxBuild(state, state.layout, []));
  // Empty panes offer what isn't on screen.
  for (const el of body.querySelectorAll(".cx-frame-empty")) {
    const sel = el.querySelector("[data-fuse]");
    const free = Object.entries(CX_KINDS).filter(([k, d]) => d.multi || !cxPlaced(state, k));
    sel.innerHTML = `<option value="">choose…</option>` + free.map(([k, d]) => `<option value="${k}">${esc(d.label)}</option>`).join("")
      + (free.length ? "" : `<option disabled>(everything is on screen)</option>`);
    sel.onchange = () => {
      if (!sel.value) return;
      const f = cxFind(state.layout, el.dataset.leaf);
      if (!f) return;
      cxReplace(state, f[0], f[1], cxNewLeaf(state, sel.value));
      el.remove();
      delete state.frames[el.dataset.leaf];
      cxLayoutChanged(state);
    };
  }
  cxApplySizes(state);
}

function cxBuild(state, node, path) {
  if (cxIsLeaf(node)) {
    const f = cxFrame(state, node);
    f.dataset.path = path.join(".");
    return f;
  }
  const el = document.createElement("div");
  el.className = `cx-node cx-${node.dir}`;
  cxSizesOf(node);
  node.kids.forEach((kid, k) => {
    if (k > 0) {
      const g = document.createElement("div");
      g.className = `cx-gut cx-gut-${node.dir}`;
      g.title = "Drag to resize";
      g.onpointerdown = (e) => cxGutterDrag(state, node, k - 1, el, e);
      el.append(g);
    }
    const c = cxBuild(state, kid, [...path, k]);
    c.dataset.slot = k;
    el.append(c);
  });
  el._node = node;
  return el;
}

// cxApplySizes sets each pane's share of its split (and, while a step is
// being edited, gives its definition more room).
function cxApplySizes(state) {
  for (const el of main.querySelectorAll(".cx-node")) {
    const n = el._node;
    const sizes = cxSizesOf(n);
    [...el.children].filter((c) => c.dataset.slot !== undefined).forEach((c) => {
      c.style.flex = `${sizes[+c.dataset.slot]} 1 0`;
    });
  }
}

function cxGutterDrag(state, node, i, el, e) {
  e.preventDefault();
  const g = e.currentTarget;
  g.setPointerCapture(e.pointerId);
  const row = node.dir === "row";
  const total = row ? el.clientWidth : el.clientHeight;
  const sizes = cxSizesOf(node), sum = sizes.reduce((a, b) => a + b, 0);
  const start = row ? e.clientX : e.clientY, a0 = sizes[i], b0 = sizes[i + 1];
  document.body.classList.add("cx-dragging");
  g.onpointermove = (ev) => {
    const d = (((row ? ev.clientX : ev.clientY) - start) / total) * sum;
    const min = sum * 0.05;
    sizes[i] = Math.max(min, Math.min(a0 + b0 - min, a0 + d));
    sizes[i + 1] = a0 + b0 - sizes[i];
    cxApplySizes(state);
  };
  g.onpointerup = () => {
    g.onpointermove = g.onpointerup = null;
    document.body.classList.remove("cx-dragging");
    cxSaveLayout(state);
  };
}

// ---------- dragging a pane ----------

// cxDropZone: where over a pane's rect a drop would go.
function cxDropZone(rect, x, y) {
  const rx = (x - rect.left) / rect.width, ry = (y - rect.top) / rect.height;
  if (rx < 0.2) return "left";
  if (rx > 0.8) return "right";
  if (ry < 0.2) return "top";
  if (ry > 0.8) return "bottom";
  return "swap";
}

function cxDragStart(state, id, e) {
  if (e.button !== 0 || e.target.closest("button, select, input")) return;
  const bar = e.currentTarget, src = state.frames[id];
  const sx = e.clientX, sy = e.clientY;
  let dragging = false, target = null, zone = null;
  const drop = main.querySelector(".cx-drop"), label = drop.querySelector("span");
  bar.setPointerCapture(e.pointerId);
  const move = (ev) => {
    if (!dragging) {
      if (Math.hypot(ev.clientX - sx, ev.clientY - sy) < 6) return;
      dragging = true;
      src.classList.add("cx-moving");
      document.body.classList.add("cx-dragging-pane");
    }
    const under = document.elementFromPoint(ev.clientX, ev.clientY)?.closest(".cx-frame");
    target = under && under !== src && under.closest(".cx-body") ? under : null;
    if (!target) { drop.hidden = true; zone = null; return; }
    const r = target.getBoundingClientRect();
    zone = cxDropZone(r, ev.clientX, ev.clientY);
    const box = { swap: [r.left, r.top, r.width, r.height], left: [r.left, r.top, r.width / 2, r.height], right: [r.left + r.width / 2, r.top, r.width / 2, r.height],
      top: [r.left, r.top, r.width, r.height / 2], bottom: [r.left, r.top + r.height / 2, r.width, r.height / 2] }[zone];
    Object.assign(drop.style, { left: `${box[0]}px`, top: `${box[1]}px`, width: `${box[2]}px`, height: `${box[3]}px` });
    const what = src.querySelector(".cx-frame-title").textContent, there = target.querySelector(".cx-frame-title").textContent;
    label.textContent = zone === "swap" ? `Swap ${what} and ${there}` : `${what} goes ${{ left: "on the left of", right: "on the right of", top: "above", bottom: "below" }[zone]} ${there}`;
    drop.dataset.zone = zone;
    drop.hidden = false;
  };
  const up = () => {
    bar.removeEventListener("pointermove", move);
    bar.removeEventListener("pointerup", up);
    bar.removeEventListener("pointercancel", up);
    drop.hidden = true;
    src.classList.remove("cx-moving");
    document.body.classList.remove("cx-dragging-pane");
    if (!dragging || !target || !zone) return;
    const tid = target.dataset.leaf;
    if (zone === "swap") cxSwap(state, id, tid);
    else {
      const f = cxFind(state.layout, id);
      if (!f) return;
      const leaf = f[2];
      cxRemove(state, id);
      cxSplit(state, tid, zone, leaf);
    }
    cxLayoutChanged(state);
  };
  bar.addEventListener("pointermove", move);
  bar.addEventListener("pointerup", up);
  bar.addEventListener("pointercancel", up);
}

// cxResetLayout goes back to the standard arrangement.
function cxResetLayout(state) {
  for (const [id, el] of Object.entries(state.frames)) if (/^e|~/.test(id)) { el.remove(); delete state.frames[id]; }
  state.layout = structuredClone(CX_DEFAULT_LAYOUT);
  cxLayoutChanged(state);
}
