"use strict";

// Scheduled runs: a calendar and an agenda of planned runs, with their
// maintenance windows and the planned starts of their sections and steps.
// Nothing starts on its own yet; "Start this run…" opens the launch page
// with the schedule's inputs filled in.

const sched = { view: "calendar", detail: 1, next: false, past: false, month: null, runs: [] };
const DETAIL = [["Windows only", 0], ["Sections", 1], ["Sections & steps", 2]];

function loadSchedPrefs() {
  try { Object.assign(sched, JSON.parse(localStorage.getItem("runnr.schedule") || "{}")); } catch {}
}
function saveSchedPrefs() {
  try { localStorage.setItem("runnr.schedule", JSON.stringify({ view: sched.view, detail: sched.detail, next: sched.next, past: sched.past })); } catch {}
}

const runEnd = (r) => Math.max(...r.entries.map((e) => +new Date(e.end || e.at)), +new Date(r.at));
const runStart = (r) => Math.min(...r.entries.map((e) => +new Date(e.at)), +new Date(r.at));
const dayKey = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
const hhmm = (d) => d.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
const runName = (r) => r.title ? `${r.runbook} — ${r.title}` : r.runbook;

// shownRuns: the runs to show; with "next actions only", every run that
// isn't over (past runs have no next action).
function shownRuns(includePast) {
  const now = Date.now();
  return sched.runs.filter((r) => (includePast && !sched.next) || runEnd(r) >= now);
}

// entriesOf lists a run's dated points at the chosen level of detail; with
// "next actions only", just the next one still to come (or its window, if
// that's open now and nothing else is due before it closes).
function entriesOf(r) {
  const es = r.entries.filter((e) => e.kind === "window" || e.kind === "notify" || e.kind === "start" || (e.kind === "section" && sched.detail >= 1) || (e.kind === "step" && sched.detail >= 2));
  if (!sched.next) return es;
  const now = Date.now();
  const next = es.find((e) => +new Date(e.at) >= now);
  if (next) return [next];
  const open = es.find((e) => e.kind === "window" && e.end && +new Date(e.end) >= now);
  return open ? [{ ...open, now: true }] : [];
}

// movedNote says when a date was moved back off a weekend.
const movedNote = (e) => e.planned ? ` <span class="muted small" title="Worked out as ${esc(new Date(e.planned).toLocaleString())}; moved back to the business day before">(moved from ${new Date(e.planned).toLocaleDateString([], { weekday: "short" })})</span>` : "";

function entryLabel(e) {
  return entryText(e) + movedNote(e);
}

function entryText(e) {
  if (e.kind === "notify") return `📣 Maintenance window notification due <span class="muted">T${esc(e.offset)}</span>`;
  if (e.kind === "window") {
    if (e.now) return `🔴 Maintenance window open now, until ${new Date(e.end).toLocaleString([], { weekday: "short", hour: "numeric", minute: "2-digit" })}`;
    if (!e.end) return "🔴 Maintenance window";
    const end = new Date(e.end), sameDay = dayKey(end) === dayKey(new Date(e.at));
    const until = sameDay ? hhmm(end) : end.toLocaleString([], { weekday: "short", month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
    return `🔴 Maintenance window until ${until} <span class="muted">(${fmtOffset((end - new Date(e.at)) / 1000, false)})</span>`;
  }
  if (e.kind === "section") return `▸ ${esc(e.name)} <span class="muted">T${esc(e.offset)}</span>`;
  if (e.kind === "step") return `· ${e.step + 1}. ${esc(e.name)} <span class="muted">T${esc(e.offset)}</span>`;
  return "▶ starts";
}

async function viewSchedule() {
  loadSchedPrefs();
  // The server moves derived dates off weekends in our time zone.
  const tz = (() => { try { return Intl.DateTimeFormat().resolvedOptions().timeZone; } catch { return ""; } })();
  sched.runs = await api("GET", `/api/schedules${tz ? `?tz=${enc(tz)}` : ""}`);
  sched.month ||= (() => { const d = new Date(); return new Date(d.getFullYear(), d.getMonth(), 1); })();
  drawSchedule();
}

function drawSchedule() {
  main.innerHTML = `
    <div class="row"><h1>Schedule</h1><span class="spacer"></span><button class="primary" id="new-sched">+ Schedule a run</button></div>
    <p class="muted">Runs planned ahead of time. For a runbook with a maintenance window, the time is when the window opens (T+0); its sections and steps with planned times are laid out around it. Nothing starts on its own.</p>
    <div class="row sched-bar">
      <div class="seg"><button data-view="calendar" class="${sched.view === "calendar" ? "on" : ""}">Calendar</button><button data-view="agenda" class="${sched.view === "agenda" ? "on" : ""}">Agenda</button></div>
      <label>Show <select id="sched-detail">${DETAIL.map(([l, v]) => `<option value="${v}" ${v === sched.detail ? "selected" : ""}>${l}</option>`).join("")}</select></label>
      <label class="check"><input type="checkbox" id="sched-next" ${sched.next ? "checked" : ""}> Next actions only</label>
      ${sched.view === "agenda" && !sched.next ? `<label class="check"><input type="checkbox" id="sched-past" ${sched.past ? "checked" : ""}> Show past</label>` : ""}
    </div>
    <div id="sched-body">${sched.view === "calendar" ? calendarHTML() : agendaHTML()}</div>`;
  $("#new-sched").onclick = () => scheduleDialog({});
  main.querySelectorAll("[data-view]").forEach((b) => (b.onclick = () => { sched.view = b.dataset.view; saveSchedPrefs(); drawSchedule(); }));
  $("#sched-detail").onchange = (e) => { sched.detail = +e.target.value; saveSchedPrefs(); drawSchedule(); };
  $("#sched-next").onchange = (e) => { sched.next = e.target.checked; saveSchedPrefs(); drawSchedule(); };
  $("#sched-past") && ($("#sched-past").onchange = (e) => { sched.past = e.target.checked; saveSchedPrefs(); drawSchedule(); });
  main.querySelectorAll("[data-month]").forEach((b) => (b.onclick = () => {
    const m = b.dataset.month;
    sched.month = m === "today" ? new Date(new Date().getFullYear(), new Date().getMonth(), 1) : new Date(sched.month.getFullYear(), sched.month.getMonth() + +m, 1);
    drawSchedule();
  }));
  main.querySelectorAll("[data-sched]").forEach((el) => (el.onclick = (ev) => {
    ev.stopPropagation();
    scheduleDialog(sched.runs.find((r) => r.id === el.dataset.sched));
  }));
  main.querySelectorAll("[data-day]").forEach((el) => (el.ondblclick = () => scheduleDialog({ at: `${el.dataset.day}T22:00` })));
}

function calendarHTML() {
  const m = sched.month;
  const first = new Date(m.getFullYear(), m.getMonth(), 1);
  const start = new Date(first);
  start.setDate(1 - first.getDay()); // the Sunday on or before the 1st
  const byDay = new Map();
  for (const r of shownRuns(true)) {
    for (const e of entriesOf(r)) {
      const k = dayKey(new Date(e.at));
      if (!byDay.has(k)) byDay.set(k, []);
      byDay.get(k).push({ r, e });
    }
  }
  const today = dayKey(new Date());
  const cells = [];
  for (let i = 0; i < 42; i++) {
    const d = new Date(start);
    d.setDate(start.getDate() + i);
    const k = dayKey(d);
    const items = (byDay.get(k) || []).sort((a, b) => new Date(a.e.at) - new Date(b.e.at));
    // A run's first item on a day shows its name; the rest are indented under it.
    const seen = new Set();
    cells.push(`<div class="cal-day ${d.getMonth() === m.getMonth() ? "" : "other"} ${k === today ? "today" : ""}" data-day="${k}" title="Double-click to schedule a run on this day">
      <div class="cal-date">${d.getDate()}</div>
      ${items.map(({ r, e }) => {
        const head = !seen.has(r.id) ? (seen.add(r.id), `<div class="cal-run" data-sched="${esc(r.id)}" title="${esc(runName(r))}">${esc(runName(r))}</div>`) : "";
        const plain = { notify: "Maintenance window notification due", window: "Maintenance window", start: "Run starts" }[e.kind] || e.name;
        const tip = `${hhmm(new Date(e.at))} ${plain}${e.offset ? ` (T${e.offset})` : ""}${e.planned ? `, moved from ${new Date(e.planned).toLocaleDateString([], { weekday: "long" })}` : ""}`;
        return `${head}<div class="cal-item ${e.kind}" data-sched="${esc(r.id)}" title="${esc(tip)}">${hhmm(new Date(e.at))} ${entryLabel(e)}</div>`;
      }).join("")}
    </div>`);
  }
  return `<div class="row cal-head"><button data-month="-1">◀</button><b>${m.toLocaleDateString([], { month: "long", year: "numeric" })}</b><button data-month="1">▶</button><button data-month="today">Today</button></div>
    <div class="cal-grid">${["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"].map((d) => `<div class="cal-dow">${d}</div>`).join("")}${cells.join("")}</div>`;
}

function agendaHTML() {
  const rs = shownRuns(sched.past);
  if (!rs.length) return `<div class="empty">Nothing scheduled${sched.past ? "" : " from now on"}.</div>`;
  const now = Date.now();
  const days = new Map();
  for (const r of rs) {
    for (const e of entriesOf(r)) {
      const k = dayKey(new Date(e.at));
      if (!days.has(k)) days.set(k, new Map());
      if (!days.get(k).has(r.id)) days.get(k).set(r.id, []);
      days.get(k).get(r.id).push(e);
    }
  }
  return [...days.keys()].sort().map((k) => `<div class="agenda-day"><h3>${new Date(k + "T12:00").toLocaleDateString([], { weekday: "long", day: "numeric", month: "long", year: "numeric" })}</h3>
    ${[...days.get(k).entries()].map(([id, es]) => {
      const r = rs.find((x) => x.id === id);
      const inputs = Object.entries(r.inputs || {}).map(([a, b]) => `<code>${esc(a)}=${esc(b)}</code>`).join(" ");
      return `<div class="card agenda-run ${runEnd(r) < now ? "dim" : ""}">
        <div class="row"><b class="link-like" data-sched="${esc(r.id)}">${esc(runName(r))}</b>
          <span class="muted small">${esc(r.description || "")}</span><span class="spacer"></span>
          <a href="#/runbooks/${enc(r.runbook)}?schedule=${enc(r.id)}">Start this run…</a></div>
        ${r.error ? `<div class="error-text">${esc(r.error)}</div>` : ""}
        ${inputs ? `<div class="muted small">${inputs}</div>` : ""}
        ${r.notes ? `<div class="muted small">${esc(r.notes)}</div>` : ""}
        <table class="agenda-items">${es.sort((a, b) => new Date(a.at) - new Date(b.at)).map((e) =>
          `<tr class="${e.kind}"><td>${hhmm(new Date(e.at))}</td><td>${entryLabel(e)}</td></tr>`).join("")}</table>
      </div>`;
    }).join("")}</div>`).join("");
}

// knownAuthor is the operator's name if they've given it (no prompt).
function knownAuthor() {
  try { return localStorage.getItem("runnr.author") || ""; } catch { return ""; }
}

// localInput turns a time into a datetime-local value.
function localInput(t) {
  const d = new Date(t);
  return `${dayKey(d)}T${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}

// scheduleDialog creates or edits a scheduled run. sc may carry runbook,
// at, inputs (e.g. from the launch page).
async function scheduleDialog(sc) {
  const editing = !!sc.id;
  const dlg = document.createElement("dialog");
  dlg.className = "edit-step sched-dialog";
  document.body.append(dlg);
  dlg.onclose = () => dlg.remove();
  const state = { runbook: sc.runbook || runbooks[0]?.name || "", inputs: { ...(sc.inputs || {}) } };
  const draw = async () => {
    let rb = null;
    try { rb = state.runbook ? (await api("GET", `/api/runbooks/${enc(state.runbook)}`)).runbook : null; } catch {}
    const overrides = (name) => {
      const what = [];
      if (rb?.maintenance_window?.target_input === name) what.push("the window's target length");
      for (const s of rb?.sections || []) if (s.offset_input === name) what.push(`section “${s.name}”`);
      for (const s of rb?.steps || []) if (s.offset_input === name) what.push(`step “${s.name}”`);
      return what.length ? `Overrides the planned time of ${what.join(", ")}` : "";
    };
    const field = (i) => {
      const v = state.inputs[i.name] ?? "";
      const hint = [i.description, overrides(i.name)].filter(Boolean).join(" · ");
      const ctl = i.choices?.length
        ? `<select data-in="${esc(i.name)}"><option value=""></option>${i.choices.map((c) => `<option ${c === v ? "selected" : ""}>${esc(c)}</option>`).join("")}</select>`
        : withPathPick(i, `<input data-in="${esc(i.name)}" value="${esc(v)}" placeholder="${esc(i.default || "")}">`);
      return row(i.name, hint, ctl);
    };
    dlg.innerHTML = `<h3>${editing ? "Scheduled run" : "Schedule a run"}</h3>
      <div class="fgroup-rows">
        ${row("Runbook", "", `<select id="sd-rb">${runbooks.map((r) => `<option ${r.name === state.runbook ? "selected" : ""}>${esc(r.name)}</option>`).join("")}</select>`)}
        ${row(rb?.maintenance_window ? "Window opens at" : "Starts at", rb?.maintenance_window ? "When the maintenance window is to open (T+0). Planned times of sections and steps are laid out from here." : "When the run is to start.",
          `<input type="datetime-local" id="sd-at" value="${esc(sc.at ? (sc.at.length > 16 ? localInput(sc.at) : sc.at) : "")}">`)}
        ${row("Title", "What it is, e.g. prod-s101 cutover", `<input id="sd-title" value="${esc(sc.title || "")}">`)}
        ${row("Notes", "", `<textarea id="sd-notes" rows="2">${esc(sc.notes || "")}</textarea>`)}
        ${(rb?.inputs || []).filter((i) => !i.secret).map(field).join("")}
      </div>
      ${(rb?.inputs || []).some((i) => i.secret) ? `<p class="muted small">Secret inputs aren't stored; they're asked for when the run starts.</p>` : ""}
      <div class="error-text"></div>
      <div class="row">
        <button class="primary" data-save>Save</button><button data-close>Cancel</button>
        <span class="spacer"></span>
        ${editing ? `<a href="#/runbooks/${enc(sc.runbook)}?schedule=${enc(sc.id)}" data-start>Start this run…</a><button class="danger" data-del>Delete</button>` : ""}
      </div>`;
    dlg.querySelector("#sd-rb").onchange = (e) => {
      sc = { ...sc, at: dlg.querySelector("#sd-at").value, title: dlg.querySelector("#sd-title").value, notes: dlg.querySelector("#sd-notes").value };
      state.runbook = e.target.value; state.inputs = {}; draw();
    };
    dlg.querySelector("[data-close]").onclick = () => dlg.close();
    dlg.querySelector("[data-start]")?.addEventListener("click", () => dlg.close());
    dlg.querySelector("[data-del]")?.addEventListener("click", async () => {
      if (!confirm("Delete this scheduled run?")) return;
      try { await api("DELETE", `/api/schedules/${enc(sc.id)}`); dlg.close(); toast("Deleted"); if (location.hash === "#/schedule") viewSchedule(); }
      catch (e) { dlg.querySelector(".error-text").textContent = e.message; }
    });
    dlg.querySelector("[data-save]").onclick = async () => {
      keep();
      const at = dlg.querySelector("#sd-at").value;
      if (!at) return (dlg.querySelector(".error-text").textContent = "Pick a date and time");
      const body = { runbook: state.runbook, at: new Date(at).toISOString(), title: dlg.querySelector("#sd-title").value.trim(),
        notes: dlg.querySelector("#sd-notes").value.trim(), inputs: Object.fromEntries(Object.entries(state.inputs).filter(([, v]) => v !== "")), created_by: knownAuthor() };
      try {
        await api(editing ? "PUT" : "POST", editing ? `/api/schedules/${enc(sc.id)}` : "/api/schedules", body);
        dlg.close();
        toast(editing ? "Saved" : "Scheduled");
        if (location.hash === "#/schedule") viewSchedule();
      } catch (e) { dlg.querySelector(".error-text").textContent = e.message; }
    };
    if (!dlg.open) dlg.showModal();
  };
  // keep remembers what's typed into the inputs across redraws.
  const keep = () => dlg.querySelectorAll("[data-in]").forEach((el) => (state.inputs[el.dataset.in] = el.value));
  await draw();
}
