"use strict";

// About: runnr's version and build, where its data is, and how long the
// server has been up (GET /api/about), with a way to copy it all.

const REPO = "https://github.com/egrif/runnr";

async function openAbout() {
  let a;
  try { a = await api("GET", "/api/about"); } catch (e) { return toast(e.message); }
  const b = a.build || {};
  const release = /^v\d+\.\d+\.\d+$/.test(b.version) ? b.version : "";
  const commit = b.commit ? b.commit.slice(0, 7) + (b.modified ? " (with uncommitted changes)" : "") : "";
  const up = duration(a.started, new Date().toISOString());
  const rows = [
    ["Commit", commit, b.commit && !b.modified ? `${REPO}/commit/${b.commit}` : ""],
    ["Committed", b.committed && new Date(b.committed).toLocaleString()],
    ["Built with", `${a.go} · ${a.platform}`],
    ["Data", `${a.database?.kind}: ${a.database?.location}`],
    ["Server up", `since ${new Date(a.started).toLocaleString()}${up ? ` (${up})` : ""}`],
    ["Claude", a.claude ? "on (Explain and Review)" : "off (set ANTHROPIC_API_KEY to turn it on)"],
  ].filter(([, v]) => v);
  const text = `runnr ${b.version}\n` + rows.map(([k, v]) => `${k}: ${v}`).join("\n") + `\nBrowser: ${navigator.userAgent}`;

  const dlg = document.createElement("dialog");
  dlg.className = "about-dialog";
  dlg.innerHTML = `
    <div class="about-head"><img src="brand/mark-wide.png" alt="" height="40"><div><h2>runnr</h2><div class="muted">${esc(b.version)}</div></div></div>
    <p class="muted small">Runbooks you can run: from the command line, this web interface, or its API.</p>
    <table class="kv about-kv">${rows.map(([k, v, href]) => `<tr><td>${esc(k)}</td><td>${href ? `<a href="${esc(href)}" target="_blank" rel="noopener">${esc(v)}</a>` : esc(v)}</td></tr>`).join("")}</table>
    <p class="about-links small">
      <a href="${REPO}" target="_blank" rel="noopener">Repository</a> ·
      <a href="${release ? `${REPO}/releases/tag/${release}` : `${REPO}/blob/main/CHANGELOG.md`}" target="_blank" rel="noopener">${release ? `What's new in ${esc(release)}` : "Changelog"}</a> ·
      <a href="#/help" data-close>Runbook format help</a>
    </p>
    <div class="row"><span class="spacer"></span><button data-copy title="For a bug report: the version, build, data location and browser">Copy details</button><button class="primary" data-close>Close</button></div>`;
  document.body.append(dlg);
  dlg.querySelectorAll("[data-close]").forEach((x) => x.addEventListener("click", () => dlg.close()));
  dlg.querySelector("[data-copy]").onclick = async (e) => {
    try { await navigator.clipboard.writeText(text); e.target.textContent = "Copied"; } catch { toast("Couldn't copy"); }
    setTimeout(() => (e.target.textContent = "Copy details"), 1400);
  };
  dlg.addEventListener("click", (e) => { if (e.target === dlg) dlg.close(); });
  dlg.onclose = () => dlg.remove();
  dlg.showModal();
}
