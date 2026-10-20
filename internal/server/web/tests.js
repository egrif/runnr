"use strict";

// A runbook's tests (runnr test) in the GUI: the Tests tab lists its
// scenarios and runs them; the editor runs them against the unsaved draft.
// Results show what didn't go as expected, every call (and what answered
// it), the simulated operator's answers and the run's log.

async function viewTests(el, name) {
  el.innerHTML = `<p class="muted">Loading…</p>`;
  let data;
  try { data = await api("GET", `/api/runbooks/${enc(name)}/tests`); } catch (e) { el.innerHTML = `<p class="error-text">${esc(e.message)}</p>`; return; }
  if (!data.suite) {
    el.innerHTML = `<div class="card tests-empty">
      <h3>No tests yet</h3>
      <p>A test runs this runbook for real, with the commands that act on the world faked and a simulated operator
        answering its prompts, then checks how it went: statuses, the calls it made or didn't, their order, output.
        Nothing it isolates can run for real.</p>
      <p>Write a suite (scenarios, fakes, expectations) and add it with the CLI:</p>
      <pre class="cmd">runnr tests import ${esc(name)}.test.yaml
runnr fakes import aws-rds.yaml     # shared fake libraries, if it uses any</pre>
      <p class="muted small">The format is in docs/TESTING.md.</p></div>`;
    return;
  }
  const su = data.suite;
  const state = { last: data.last || null, running: "" };
  const draw = () => {
    const byName = new Map((state.last?.results || []).map((r) => [r.scenario, r]));
    const passed = state.last ? state.last.results.filter((r) => r.passed).length : 0;
    el.innerHTML = `
      <div class="row tests-head">
        <div><b>${esc(su.name)}</b> <span class="muted small">suite v${su.version}${su.uses?.length ? ` · fakes: ${su.uses.map(esc).join(", ")}` : ""}${su.isolate?.length ? ` · isolates ${su.isolate.map(esc).join(", ")}` : ""}</span></div>
        <span class="spacer"></span>
        ${state.last ? `<span class="badge ${passed === state.last.results.length ? "s-succeeded" : "s-failed"}">${passed}/${state.last.results.length} passing</span>
          <span class="muted small">${new Date(state.last.at).toLocaleTimeString()}</span>` : ""}
        <button class="primary" data-run-all ${state.running ? "disabled" : ""}>${state.running === "*" ? "Running…" : "▶ Run all tests"}</button>
      </div>
      <ul class="scenarios">${su.scenarios.map((sc) => {
        const r = byName.get(sc.name);
        return `<li class="scenario card ${r ? (r.passed ? "pass" : "fail") : ""}">
          <div class="row">
            <span class="icon">${r ? (r.passed ? "✔" : "✘") : "○"}</span>
            <b>${esc(sc.name)}</b>
            ${sc.description ? `<span class="muted small">${esc(sc.description)}</span>` : ""}
            <span class="spacer"></span>
            ${r ? `<span class="muted small">run ${esc(r.status)} · ${r.calls.length} calls · ${(r.took_ms / 1000).toFixed(1)}s</span>` : ""}
            <button data-run="${esc(sc.name)}" ${state.running ? "disabled" : ""}>${state.running === sc.name ? "Running…" : "Run"}</button>
          </div>
          ${r ? testResultHTML(r) : ""}
        </li>`;
      }).join("")}</ul>`;
    el.querySelector("[data-run-all]").onclick = () => run("");
    el.querySelectorAll("[data-run]").forEach((b) => (b.onclick = () => run(b.dataset.run)));
  };
  const run = async (scenario) => {
    state.running = scenario || "*";
    draw();
    try {
      const tr = await api("POST", `/api/runbooks/${enc(name)}/tests/run`, scenario ? { scenario } : {});
      if (!scenario) state.last = tr;
      else {
        const others = (state.last?.results || []).filter((r) => r.scenario !== scenario);
        state.last = { ...(state.last || {}), at: tr.at, results: [...others, ...tr.results] };
      }
    } catch (e) { toast(e.message); }
    state.running = "";
    draw();
  };
  draw();
}

// testResultHTML is one scenario's result: problems first, then what happened.
function testResultHTML(r) {
  const icon = (s) => ICONS[s] || "";
  return `
    ${r.problems?.length ? `<ul class="problems">${r.problems.map((p) => `<li>${esc(p)}</li>`).join("")}</ul>` : ""}
    <div class="test-steps" title="Each step's status in the simulated run">${r.steps.map((s, i) =>
      `<span class="ts s-${esc(s.status)}" title="${i + 1}. ${esc(s.name)}: ${esc(s.status)}${s.note ? ` (${esc(s.note)})` : ""}">${icon(s.status)}</span>`).join("")}</div>
    <details class="test-detail"><summary>Calls (${r.calls.length})${r.unused?.length ? ` · ${r.unused.length} unused fake${r.unused.length > 1 ? "s" : ""}` : ""} · operator · log</summary>
      <table class="calls">${r.calls.map((c) => `<tr class="${c.unfaked ? "refused" : ""}">
        <td class="muted">${c.step ? `step ${c.step}` : ""}</td>
        <td><code>${esc(c.argv.join(" "))}</code></td>
        <td class="muted">${c.unfaked ? "refused: no fake" : c.real ? "ran for real" : esc(c.from || "fake")}</td>
        <td class="muted">exit ${c.exit}</td></tr>`).join("") || `<tr><td class="muted">No calls to faked or isolated commands.</td></tr>`}</table>
      ${r.unused?.length ? `<p class="muted small">Unused fakes (no call matched): ${r.unused.map((u) => `<code>${esc(u)}</code>`).join(", ")}</p>` : ""}
      ${r.asked?.length ? `<p class="muted small">Operator: ${r.asked.map(esc).join("; ")}</p>` : ""}
      <pre class="log">${esc(r.log)}</pre>
    </details>`;
}

// runDraftTests runs a runbook's tests against the editor's unsaved YAML
// and shows the results in el.
async function runDraftTests(el, name, raw) {
  el.hidden = false;
  el.innerHTML = `<p class="muted">Running the tests against your unsaved changes…</p>`;
  try {
    const tr = await api("POST", `/api/runbooks/${enc(name)}/tests/run`, { raw });
    const passed = tr.results.filter((r) => r.passed).length;
    el.innerHTML = `<div class="row"><b>Tests against this draft</b>
        <span class="badge ${passed === tr.results.length ? "s-succeeded" : "s-failed"}">${passed}/${tr.results.length} passing</span>
        <span class="spacer"></span><button class="link" data-close>Hide</button></div>
      <ul class="scenarios">${tr.results.map((r) => `<li class="scenario card ${r.passed ? "pass" : "fail"}">
        <div class="row"><span class="icon">${r.passed ? "✔" : "✘"}</span><b>${esc(r.scenario)}</b><span class="spacer"></span>
          <span class="muted small">run ${esc(r.status)} · ${r.calls.length} calls</span></div>
        ${testResultHTML(r)}</li>`).join("")}</ul>`;
  } catch (e) {
    el.innerHTML = `<div class="row"><b>Tests</b><span class="spacer"></span><button class="link" data-close>Hide</button></div><p class="error-text">${esc(e.message)}</p>`;
  }
  el.querySelector("[data-close]").onclick = () => (el.hidden = true);
}
