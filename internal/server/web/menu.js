"use strict";

// Menus: a page's less-used actions, behind a "⋯" button (and the
// settings menu in the top bar). Each is a <details class="menu">; the
// buttons and links inside keep their ids, so pages wire them up as before.

// moreMenu wraps items (HTML: buttons and links) in a ⋯ menu; "" if none.
function moreMenu(items, title = "More") {
  const html = items.filter(Boolean).join("");
  if (!html) return "";
  return `<details class="menu"><summary class="icon-only" title="${esc(title)}">⋯</summary><div class="menu-list">${html}</div></details>`;
}

// A menu closes when something in it is chosen, on a click elsewhere, or on Escape.
document.addEventListener("click", (e) => {
  const inMenu = e.target.closest("details.menu");
  for (const m of document.querySelectorAll("details.menu[open]")) {
    if (m !== inMenu || e.target.closest(".menu-list button, .menu-list a")) m.open = false;
  }
});
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape") document.querySelectorAll("details.menu[open]").forEach((m) => (m.open = false));
});
