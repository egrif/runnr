"use strict";

// Launch screen, shared by "Run", "Run again" and "Resume": inputs, how to
// run, and for every step: include it or not, pause before/after it, what
// it will do (rendered with these inputs) and what it did last time.

const DONE = ["succeeded", "skipped", "forced"];
const enc = encodeURIComponent;

async function viewLaunch(el, name, mode, runId, scheduleId) {
  const data = await api("GET", `/api/runbooks/${enc(name)}`);
  const rb = data.runbook;
  const plan = scheduleId ? await api("GET", `/api/schedules/${enc(scheduleId)}`).catch(() => null) : null;
  let prev = null;
  if (runId) {
    prev = (await api("GET", `/api/runs/${enc(runId)}`)).run;
  } else {
    const latest = await api("GET", `/api/runs?runbook=${enc(name)}&limit=1`);
    if (latest.length) prev = (await api("GET", `/api/runs/${enc(latest[0].id)}`)).run;
  }
  const history = await api("GET", `/api/runs?runbook=${enc(name)}&limit=30`).catch(() => []);
  const st = {
    rb, prev, history, mode, runId, preview: null, open: new Set(),
    include: rb.steps.map(() => true), before: new Set(), after: new Set(),
  };
  if (mode === "resume" && prev) {
    // Leave out what already finished; setup (always) steps run again.
    st.include = rb.steps.map((s, i) => !!s.always || !DONE.includes(prev.steps[i]?.status));
  }

  const inputs = rb.inputs || [];
  const prefill = (i) => {
    if (plan?.inputs?.[i.name]) return plan.inputs[i.name];
    const v = prev && mode !== "run" ? prev.inputs?.[i.name] : undefined;
    return v && v !== "****" ? v : "";
  };
  const field = (i) => {
    const id = `in-${i.name}`;
    const v = prefill(i);
    let ctl;
    if (i.choices?.length) {
      ctl = `<select id="${id}" name="${esc(i.name)}">${i.required && !i.default ? `<option value=""></option>` : ""}${i.choices.map((c) => `<option ${c === (v || i.default) ? "selected" : ""}>${esc(c)}</option>`).join("")}</select>`;
    } else {
      ctl = `<input id="${id}" name="${esc(i.name)}" type="${i.secret ? "password" : "text"}" value="${esc(v)}" placeholder="${esc(i.default || "")}" ${i.required && !i.default && !i.secret ? "required" : ""}>`;
    }
    const hint = [i.description, i.secret && mode !== "run" && "secret: not stored, enter it again (or leave blank to be asked)"].filter(Boolean).join(" · ");
    return `<label for="${id}">${esc(i.name)}${i.required ? " *" : ""}</label>${ctl}${hint ? `<div class="hint">${esc(hint)}</div>` : ""}`;
  };
  const what = { run: "Run", rerun: "Run again", resume: "Resume" }[mode];
  const lastLink = prev ? `<a href="#/runs/${enc(prev.id)}">${esc(prev.id)}</a> (${esc(prev.status)})` : "";
  el.innerHTML = `
    ${plan ? `<div class="banner">Starting the run scheduled for ${new Date(plan.at).toLocaleString()}${plan.title ? `: <b>${esc(plan.title)}</b>` : ""}, with its inputs.</div>` : ""}
    ${mode !== "run" ? `<div class="banner">${what} of run ${lastLink}${mode === "resume" ? " — steps that already finished are left out; tick any you want to run again." : " with the same inputs."}</div>` : ""}
    <form id="run-form">
      <div class="card">
        ${inputs.length ? `<div class="form-grid">${inputs.map(field).join("")}</div>` : `<div class="muted">This runbook takes no inputs.</div>`}
        <div class="options">
          <label><input type="radio" name="mode" value="interactive" checked> Interactive</label>
          <label><input type="radio" name="mode" value="unattended"> Unattended</label>
          <label title="Stop before every step to choose: run, skip, open a shell, or carry on"><input type="checkbox" name="stepmode"> Step through every step</label>
          <label title="Approve confirm-gated steps automatically in unattended mode"><input type="checkbox" name="yes"> Auto-approve confirmations</label>
          <label><input type="checkbox" name="dry"> Dry run</label>
        </div>
      </div>
      <div class="row" style="margin:20px 0 4px">
        <h2 style="margin:0">Steps</h2>
        <span class="muted" id="count"></span>
        <span class="spacer"></span>
        <button type="button" data-bulk="all">All</button>
        <button type="button" data-bulk="none">None</button>
        ${prev ? `<button type="button" data-bulk="unfinished" title="Steps that didn't finish last time, plus always steps">Not done last time</button>` : ""}
        <button type="button" data-bulk="nopause">Clear pauses</button>
      </div>
      <p class="muted small">Click a step to see what it will do, what it did in earlier runs (History) and how it's set up (Details). Untick to skip it; ⏸ before / after stops there so you can check.</p>
      <ol class="steps" id="launch-steps"></ol>
      <div class="row start-bar"><button class="primary" type="submit" id="start"></button>
        ${mode === "run" ? `<button type="button" id="schedule-btn" title="Plan this run for later, with these inputs">Schedule…</button>` : ""}</div>
    </form>`;

  const form = $("#run-form");
  const vars = () => {
    const out = {};
    inputs.forEach((i) => {
      const v = form.elements[i.name].value;
      // Blank secrets are left out so an interactive run asks for them.
      if (!(i.secret && v === "")) out[i.name] = v;
    });
    return out;
  };
  const sync = () => {
    form.yes.disabled = form.mode.value !== "unattended";
    form.stepmode.disabled = form.mode.value === "unattended";
    const n = st.include.filter(Boolean).length;
    $("#count").textContent = `${n} of ${rb.steps.length} selected`;
    $("#start").textContent = `▶ ${what} (${n} step${n === 1 ? "" : "s"})`;
  };
  const draw = () => { renderLaunchSteps(st); sync(); };

  let t;
  const refreshPreview = async () => {
    try {
      st.preview = (await api("POST", "/api/preview", { runbook: rb.name, vars: vars(), from_run: mode === "resume" ? runId : "" })).steps;
      draw();
    } catch (e) { toast(e.message); }
  };
  form.addEventListener("input", (e) => { if (e.target.closest(".form-grid")) { clearTimeout(t); t = setTimeout(refreshPreview, 400); } });
  form.addEventListener("change", sync);
  form.querySelectorAll("[data-bulk]").forEach((b) => (b.onclick = () => {
    const k = b.dataset.bulk;
    if (k === "all") st.include = rb.steps.map(() => true);
    if (k === "none") st.include = rb.steps.map(() => false);
    if (k === "unfinished") st.include = rb.steps.map((s, i) => !!s.always || !DONE.includes(prev.steps[i]?.status));
    if (k === "nopause") { st.before.clear(); st.after.clear(); }
    draw();
  }));
  $("#launch-steps").addEventListener("click", (e) => {
    const b = e.target.closest("[data-inc],[data-before],[data-after],[data-open]");
    if (!b) return;
    if (b.dataset.inc !== undefined) { st.include[+b.dataset.inc] = b.checked; e.stopPropagation(); return draw(); }
    if (b.dataset.before !== undefined) { toggle(st.before, +b.dataset.before); e.preventDefault(); return draw(); }
    if (b.dataset.after !== undefined) { toggle(st.after, +b.dataset.after); e.preventDefault(); return draw(); }
    toggle(st.open, +b.dataset.open);
    draw();
  });
  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const nums = (pred) => rb.steps.map((_, i) => i).filter(pred).map((i) => i + 1);
    const body = {
      vars: vars(), unattended: form.mode.value === "unattended", yes: form.yes.checked, dry_run: form.dry.checked,
      step_mode: form.stepmode.checked, skip: nums((i) => !st.include[i]),
      pause_before: nums((i) => st.before.has(i)), pause_after: nums((i) => st.after.has(i)),
    };
    if (body.unattended) {
      const ch = rb.steps.map((s, i) => i).filter((i) => st.include[i] && rb.steps[i].choose && (rb.steps[i].ways || []).length > 1);
      if (ch.length) {
        if (!confirm(`Steps ${ch.map((i) => `${i + 1} (${rb.steps[i].name})`).join(", ")} normally ask which way to do them. Unattended, they'll use their preferred way. Start anyway?`)) return;
        body.use_preferred = true;
      }
    }
    try {
      const { id } = mode === "resume"
        ? await api("POST", `/api/runs/${enc(runId)}/resume`, body)
        : await api("POST", "/api/runs", { ...body, runbook: rb.name, previous: prev?.id || "" });
      location.hash = `#/runs/${id}`;
    } catch (e) { toast(e.message); }
  });
  $("#schedule-btn") && ($("#schedule-btn").onclick = () => scheduleDialog({ runbook: rb.name, inputs: vars() }));
  bindSide($("#launch-steps"), draw);
  draw();
  refreshPreview();
}

function toggle(set, i) { set.has(i) ? set.delete(i) : set.add(i); }

function stepKindName(s) {
  if (s.background || s.kind === "background") return "background";
  if (s.checks?.length || s.kind === "checks") return "checks";
  if ((s.ways || []).length > 1 || s.kind === "ways") return `${(s.ways || []).length || "several"} ways`;
  if (s.manual || s.kind === "manual") return "manual";
  if (s.tty || s.kind === "tty") return "terminal";
  return "command";
}

function stepBadges(s) {
  return [
    `<span class="badge-k">${stepKindName(s)}</span>`,
    s.stop_after && `<span class="badge-k" title="Runs in the background until this step finishes">until ${esc(s.stop_after)}</span>`,
    s.repeat && `<span class="badge-k" title="Re-runs the checks until they all pass">repeats</span>`,
    s.confirm && `<span class="badge-k warn" title="Asks before running, even without pauses">confirms on its own</span>`,
    s.dangerous && `<span class="badge-k fail" title="Out of sequence, it needs the step number typed to run">⚠ dangerous</span>`,
    s.choose && `<span class="badge-k" title="Asks which way each time (unattended: the preferred one)">asks which way</span>`,
    s.window && `<span class="badge-k fail" title="${s.window === "open" ? "Production goes down when this step starts" : "Production is back when this step finishes"}">window ${s.window}s</span>`,
    s.always && `<span class="badge-k" title="Runs even when starting later or resuming">always</span>`,
    s.when && `<span class="badge-k" title="${esc(s.when)}">conditional</span>`,
    s.continue_on_error && `<span class="badge-k">continue on error</span>`,
  ].filter(Boolean).join(" ");
}

// ---------- maintenance window ----------

// parseOffset reads a window offset like -2w, -30m, +1d4h, 0 (seconds, or null).
function parseOffset(v) {
  if (v == null || v === "") return null;
  const t = String(v).trim().replace(/^\+/, "");
  if (t === "0") return 0;
  const m = t.match(/^(-)?((?:\d+(?:\.\d+)?(?:w|d|h|m|s))+)$/);
  if (!m) return null;
  let sec = 0;
  for (const [, n, u] of m[2].matchAll(/(\d+(?:\.\d+)?)(w|d|h|m|s)/g)) sec += +n * { w: 604800, d: 86400, h: 3600, m: 60, s: 1 }[u];
  return m[1] ? -sec : sec;
}

// fmtOffset shows seconds as T-style offsets: -2w, -30m, +0, +1d4h.
function fmtOffset(sec, sign = true) {
  let s = Math.round(Math.abs(sec));
  const neg = sec < 0 && s > 0;
  let body = "";
  for (const [u, n] of [["w", 604800], ["d", 86400], ["h", 3600], ["m", 60], ["s", 1]]) {
    if (s >= n) { body += `${Math.floor(s / n)}${u}`; s %= n; }
  }
  return (sign ? (neg ? "-" : "+") : neg ? "-" : "") + (body || "0");
}

// planPoints lists each section's and step's planned start (seconds from
// the window opening), in step order, marking ones out of order (earlier
// than a planned time before them, or on the wrong side of the opening) as
// ignored: those steps are unscheduled and just follow on. Same rules as
// the server's.
function planPoints(steps, sections) {
  const pts = [];
  steps.forEach((s, i) => {
    if (s.section && (i === 0 || steps[i - 1].section !== s.section)) {
      const v = parseOffset((sections || []).find((x) => x.name === s.section)?.window_offset);
      if (v != null) pts.push({ step: i, section: s.section, offset: v });
    }
    const v = parseOffset(s.window_offset);
    if (v != null) pts.push({ step: i, offset: v });
  });
  const open = steps.findIndex((s) => s.window === "open");
  let lo = null;
  for (const p of pts) {
    if (open >= 0 && p.step >= open && (lo == null || lo < 0)) lo = 0;
    if ((lo != null && p.offset < lo) || (open >= 0 && p.step < open && p.offset > 0)) p.ignored = true;
    else lo = p.offset;
  }
  return pts;
}

// plannedOffset is when step i is planned to start (seconds, or null): its
// own planned time, or its section's for a section's first step.
function plannedOffset(steps, sections, i) {
  const pts = planPoints(steps, sections).filter((p) => p.step === i && !p.ignored);
  const own = pts.find((p) => !p.section);
  return own ? own.offset : pts.length ? pts[0].offset : null;
}

// ignoredOffset is step i's own planned time if it's out of order (or null).
function ignoredOffset(steps, sections, i) {
  return planPoints(steps, sections).find((p) => p.step === i && !p.section && p.ignored)?.offset ?? null;
}

// windowSpan: indexes of the steps that open and close the window (-1 if none).
function windowSpan(steps) {
  return [steps.findIndex((s) => s.window === "open"), steps.findIndex((s) => s.window === "close")];
}

const inWindow = (steps, i) => { const [o, c] = windowSpan(steps); return o >= 0 && i >= o && (c < 0 || i <= c); };

function plannedBadge(steps, sections, i) {
  const ig = ignoredOffset(steps, sections, i);
  return offsetBadge(plannedOffset(steps, sections, i)) +
    (ig == null ? "" : ` <span class="badge-k planned ignored" title="Out of order (earlier than a planned time before it, or on the wrong side of the window opening), so it's unscheduled: the step just follows on">T${fmtOffset(ig)}</span>`);
}

// offsetBadge shows a planned start (seconds from the window opening).
function offsetBadge(sec) {
  return sec == null ? "" : `<span class="badge-k planned" title="Planned start, relative to the maintenance window opening">T${fmtOffset(sec)}</span>`;
}

function lastTime(r, run) {
  if (!r) return `<p class="muted">No earlier run to compare with.</p>`;
  if (r.status === "pending") return `<p class="muted">Not reached in run ${esc(run.id)}.</p>`;
  const lines = (r.output || "").replace(/\n$/, "").split("\n");
  const shown = lines.slice(-200);
  return `
    <p><span class="s-${esc(r.status)}">${ICONS[r.status] || ""} ${esc(r.status)}</span>
      <span class="muted">${[r.note, r.exit_code != null && `exit ${r.exit_code}`, duration(r.started, r.ended), `run ${run.id}`].filter(Boolean).map(esc).join(" · ")}</span></p>
    ${r.command ? `<pre class="cmd">${esc(r.command)}</pre>` : ""}
    ${r.output ? `<pre class="log">${lines.length > shown.length ? `<span class="info">… ${lines.length - shown.length} earlier lines</span>\n` : ""}${shown.map(esc).join("\n")}</pre>` : ""}
    ${r.error && r.status !== "succeeded" ? `<div class="error-text">${esc(r.error)}</div>` : ""}
    ${renderChecks(r.checks)}${renderSub(r)}
    ${(r.shells || []).map((t, n) => `<details class="shell-log"><summary>Shell session ${n + 1}</summary><pre class="log">${esc(t)}</pre></details>`).join("")}
    ${renderNotes(r.notes)}`;
}

function renderLaunchSteps(st) {
  const { rb, prev, preview } = st;
  $("#launch-steps").innerHTML = rb.steps.map((s, i) => {
    const r = prev?.steps[i];
    const view = preview?.[i] || s;
    return `${sectionHeading(rb.steps, i, rb.sections)}
      <li class="step ${st.include[i] ? "" : "dim"} ${inWindow(rb.steps, i) ? "in-window" : ""}">
        <div class="step-head" data-open="${i}">
          <input type="checkbox" data-inc="${i}" ${st.include[i] ? "checked" : ""} title="Include this step">
          <span class="n">${i + 1}</span>
          <span class="name">${esc(s.name)}${s.owner ? ` <span class="owner">${esc(s.owner)}</span>` : ""} ${stepBadges(s)} ${preview ? offsetBadge(preview[i]?.offset_sec) : plannedBadge(rb.steps, rb.sections, i)}</span>
          ${r && r.status !== "pending" ? `<span class="last s-${esc(r.status)}" title="Last time: ${esc(r.status)}">${ICONS[r.status]}</span>` : ""}
          <button type="button" class="pill ${st.before.has(i) ? "on" : ""}" data-before="${i}" title="Stop before this step (run / skip / open a shell)">⏸ before</button>
          <button type="button" class="pill ${st.after.has(i) ? "on" : ""}" data-after="${i}" title="Stop after this step to check the result">⏸ after</button>
        </div>
        ${st.open.has(i) ? `<div class="step-body two-col">
          <div><h4>Will do</h4>${stepDetails(view)}</div>
          <div>${sidePanel(`launch:${i}`, i, s, st.history, prev)}</div>
        </div>` : ""}
      </li>`;
  }).join("");
}
