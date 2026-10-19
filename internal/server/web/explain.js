"use strict";

// Ask Claude about a step (or the whole runbook). The whole runbook goes
// along as context, cached on Anthropic's side, so asking about other steps
// soon after is quick and cheap. Shown only when the server has a key.

let claudeOn = false;
// After every script has loaded (api() is in app.js).
addEventListener("DOMContentLoaded", () => {
  api("GET", "/api/claude").then((c) => { claudeOn = !!c.enabled; document.body.classList.toggle("claude-on", claudeOn); }).catch(() => {});
});

// explainBtn is the ✨ button for a step (1-based; 0: the whole runbook).
function explainBtn(step, label = "✨ Explain") {
  return `<button type="button" class="link explain-btn" data-explain="${step}" title="Ask Claude what this does, what it relies on from earlier steps, its risks, and how to improve it">${label}</button>`;
}

// openExplain shows the panel and asks. source is {runbook} for a saved
// runbook or {raw} for the one being edited (it needn't be saved).
async function openExplain(source, step, title) {
  let p = $("#explain-panel");
  if (!p) {
    p = document.createElement("aside");
    p.id = "explain-panel";
    p.innerHTML = `<div class="row ex-head"><b class="ex-title"></b><span class="spacer"></span><button class="link" data-ex-close>Close</button></div>
      <div class="ex-body"></div>
      <form class="row ex-ask"><input name="q" placeholder="Ask a follow-up about this step…" autocomplete="off"><button class="primary">Ask</button></form>
      <div class="muted small ex-usage"></div>`;
    document.body.append(p);
    p.querySelector("[data-ex-close]").onclick = () => { p.hidden = true; p._abort?.abort(); };
  }
  p.hidden = false;
  p.querySelector(".ex-title").textContent = `✨ ${title}`;
  // The conversation so far, so follow-ups know what "it" is.
  const history = [];
  const ask = async (question) => {
    p._abort?.abort();
    const ctl = (p._abort = new AbortController());
    const body = p.querySelector(".ex-body");
    const entry = document.createElement("div");
    entry.className = "ex-entry";
    entry.innerHTML = `${question ? `<div class="ex-q">${esc(question)}</div>` : ""}<div class="ex-a"><span class="muted">Thinking…</span></div>`;
    body.append(entry);
    entry.scrollIntoView({ block: "end" });
    const out = entry.querySelector(".ex-a");
    let text = "";
    try {
      const res = await fetch("/api/explain", {
        method: "POST", signal: ctl.signal,
        headers: { "X-Runnr": "1", "Content-Type": "application/json" },
        body: JSON.stringify({ ...source, step, question, history }),
      });
      if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error || res.statusText);
      const reader = res.body.getReader();
      const dec = new TextDecoder();
      let buf = "";
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        buf += dec.decode(value, { stream: true });
        let nl;
        while ((nl = buf.indexOf("\n\n")) >= 0) {
          const line = buf.slice(0, nl).replace(/^data: /, "");
          buf = buf.slice(nl + 2);
          let ev;
          try { ev = JSON.parse(line); } catch { continue; }
          if (ev.text) { text += ev.text; out.innerHTML = md(text); }
          if (ev.error) throw new Error(ev.error);
          if (ev.usage) {
            const u = ev.usage;
            const read = u.cache_read_input_tokens || 0, wrote = u.cache_creation_input_tokens || 0;
            p.querySelector(".ex-usage").textContent = `${ev.model} · ${u.input_tokens + read + wrote} tokens in (${read ? `${read} from the cache` : wrote ? `${wrote} cached for the next question` : "not cached: the runbook is short"}), ${u.output_tokens} out`;
          }
        }
        body.scrollTop = body.scrollHeight;
      }
      if (text) {
        const asked = history.length ? question : (question ? `${step ? `About step ${step}` : "About this runbook"}: ${question}` : (step ? `Explain step ${step}.` : "Review this runbook."));
        history.push({ role: "user", content: asked }, { role: "assistant", content: text });
      }
    } catch (e) {
      if (e.name !== "AbortError") out.innerHTML = `${md(text)}<div class="error-text">${esc(e.message)}</div>`;
    }
  };
  p.querySelector(".ex-body").innerHTML = "";
  p.querySelector(".ex-usage").textContent = "";
  const form = p.querySelector(".ex-ask");
  form.onsubmit = (e) => {
    e.preventDefault();
    const q = form.q.value.trim();
    if (!q) return;
    form.q.value = "";
    ask(q);
  };
  form.q.placeholder = step ? "Ask a follow-up about this step…" : "Ask about this runbook…";
  ask("");
}

// md renders the Markdown Claude writes: headings, bullets, code, bold.
function md(src) {
  const out = [];
  let list = false;
  const inline = (s) => esc(s).replace(/`([^`]+)`/g, "<code>$1</code>").replace(/\*\*([^*]+)\*\*/g, "<b>$1</b>");
  const parts = src.split(/^```[^\n]*\n?/m);
  parts.forEach((part, k) => {
    if (k % 2 === 1) { if (list) { out.push("</ul>"); list = false; } out.push(`<pre class="cmd">${esc(part.replace(/\n$/, ""))}</pre>`); return; }
    for (const line of part.split("\n")) {
      const h = line.match(/^#{1,6}\s+(.*)/), li = line.match(/^\s*[-*]\s+(.*)/);
      if (li) { if (!list) { out.push("<ul>"); list = true; } out.push(`<li>${inline(li[1])}</li>`); continue; }
      if (list) { out.push("</ul>"); list = false; }
      if (h) out.push(`<h4>${inline(h[1])}</h4>`);
      else if (line.trim()) out.push(`<p>${inline(line)}</p>`);
    }
  });
  if (list) out.push("</ul>");
  return out.join("");
}
