"use strict";

// What every runbook field is for. The editor's ⓘ tooltips and the help
// page both come from here, so they can't disagree.
const FIELD_HELP = {
  runbook: {
    name: "The runbook's identifier: lowercase letters, digits, '.', '_' or '-'. Used in URLs and `runnr run <name>`.",
    description: "What the runbook is for and what it does. The first line is shown in lists.",
    expect: "How things should stand once the runbook has run.",
    sections: "Descriptions for the groups of steps: for each section name used by steps, what it does and how things should stand at its end.",
    tags: "Labels for filtering the runbook list.",
    shell: "Shell that runs every step (sh by default). Use zsh or bash if your commands need their syntax.",
    profile: "Files sourced before every step, e.g. ~/.zshrc, so your aliases and functions work in steps. Adds the time it takes to load them to each step.",
    env: "Environment variables for every step, KEY=value. Values may use templates like {{.input}}.",
    sensitive: "Credential patterns to hide in output, state and history: name=regex, with {{SENSITIVE}} where the credential is. URL passwords, Bearer tokens and AWS keys are hidden already.",
    inputs: "Values the operator gives when starting a run. Available to steps as {{.name}} and $name.",
    libraries: "Function libraries this runbook uses: their functions can be called in every step and in shells opened from runnr.",
    functions: "This runbook's own shell functions (name, description, body). They win over a library function of the same name.",
    steps: "What the runbook does, in order.",
    maintenance_window: "The time production is down. Mark the step where it goes down (window: open) and the one where it's back (window: close); target is how long it should take, e.g. 45m. Runs show a live clock against the target and record the real downtime.",
    window_target: "How long production is expected (or allowed) to be down, e.g. 45m, 6h, 2d.",
    window_description: "What the window is, e.g. the announced outage.",
    notification_required: "The window has to be announced ahead: the notification is due notify_before (default 2w) before it opens, and shows on the Schedule calendar and agenda.",
  },
  input: {
    name: "Variable name: letters, digits and _. Steps use it as {{.name}} or $name.",
    description: "Shown next to the field when starting a run.",
    default: "Used when nothing is given.",
    choices: "If set, the value must be one of these (comma-separated). Shown as a dropdown.",
    required: "The run can't start without a value.",
    secret: "Hidden as you type, masked in output and history, and never stored: asked again on resume.",
  },
  step: {
    kind: "Scripted: a shell script. Manual: instructions for a person. Checks: several commands that must all pass. Add another way to offer several (e.g. Scripted and Manual).",
    ways: "Different ways to do the step, each with a name: Scripted, Manual, Checks, or your own name. The preferred one is used unless the step asks which; if a way fails you can try another.",
    choose: "Ask which way each time it runs (the preferred one is the default). Unattended runs use the preferred way, after you confirm when starting.",
    preferred: "The way used by default, and always in unattended runs.",
    window: "Opens: production goes down when this step starts. Closes: production is back when it finishes.",
    window_offset: "When this step is planned to start, relative to the window opening: -2w, -30m (before), 0, +12m, +1d4h (w weeks, d days, h hours, m minutes). A section's first step uses the section's if it has none.",
    out_of_order: "Planned times have to keep step order: one earlier than a planned time before it (or after T+0 before the opening step, or before T+0 after it) is treated as unscheduled, and the step just follows on.",
    offset_input: "An input that overrides this planned time (or the window's target) when a run or schedule gives it a value, e.g. a longer cutoff in production.",
    run: "Shell script to run. Templates like {{.input}} are filled in first; every input and published value is also an environment variable ($name).",
    manual: "Instructions for the operator, who marks the step done, skipped or failed. Templates are filled in.",
    checks: "Commands that each pass if they exit 0. Every check runs on each attempt (it doesn't stop at the first failure); the step passes when all pass.",
    name: "Shown in the plan, prompts and history.",
    description: "What we're doing in this step, and why. Shown when reviewing, stepping through and in Details.",
    id: "Optional short identifier, for --from, stop_after and references. Letters, digits, _.",
    section: "Heading the step is grouped under. Applies to the following steps until another section.",
    owner: "Who does this step (shown, not enforced).",
    expect: "By the end of this step, how things should stand (what success looks like). Shown when reviewing, at prompts, when checking a result, and beside a shell.",
    confirm: "Ask before running, even when not stepping through. Unattended runs need --yes (or Auto-approve).",
    dangerous: "Needs a double check (typing the step number) unless it runs in sequence in a recorded run of the saved runbook: every earlier step ran, was accepted, or was left out by its own condition. Starting later, skipping steps, Run this and runbook files all count as out of sequence; unattended runs refuse it then.",
    always: "Run even when starting later or resuming: for setup that later steps depend on.",
    tty: "The command needs a keyboard (prompts, live displays): it runs on a terminal you type into. Output is still recorded.",
    background: "Start the command and move on; it keeps running on its own terminal (a tab in the GUI, w in the CLI) until the “stop after” step finishes, or the run ends.",
    stop_after: "The step after which this background command is stopped (hung up).",
    continue_on_error: "If the step fails, record it and carry on instead of stopping.",
    optional: "The step isn't required. It's labelled optional everywhere; an interactive run asks before it (run or skip), and an unattended run carries on if it fails. It can't also be always.",
    when: "Only run the step if this template is true, e.g. {{eq .env \"production\"}}. Otherwise it's skipped.",
    timeout: "Stop the command if it runs longer than this, e.g. 30s, 10m.",
    retries: "Run a failing command again this many times before asking what to do.",
    retry_delay: "Wait between retries, e.g. 10s.",
    dir: "Directory the step runs in. Templates allowed, e.g. {{.REPO_DIR}}.",
    step_shell: "Shell for this step only (default: the runbook's).",
    step_env: "Environment variables for this step only, KEY=value.",
    outputs: "Shell variables this step sets that later steps should get (as $name and {{.name}}). Saved in the run's state.",
    secret_outputs: "Like Publishes, but masked everywhere and never stored (re-derived on resume).",
    capture: "Save the command's standard output (trimmed) as this variable.",
    unattended: "What a manual or terminal step does when nobody is there: fail (default), skip, or pass.",
    retry_checks: "What a retry runs (by repeat, or when you choose retry): all (default) reruns every check; failed reruns only the ones that haven't passed (passed ones stay passed); each runs the checks in order and retries the first failing one until it passes before going on to the next.",
    repeat_every: "With repeat: how long to wait between attempts, e.g. 20s.",
    repeat_timeout: "With repeat: give up (and fail) after this long, e.g. 10m.",
  },
  section: {
    name: "A section name used by steps (their section field).",
    description: "What this group of steps does.",
    expect: "How things should stand at the end of the section.",
    window_offset: "When the section is planned to start, relative to the maintenance window opening: -2w, -30m, 0, +20m, +3d.",
  },
  collection: {
    name: "The collection's identifier.",
    description: "What this group of runbooks is for.",
    expect: "How things stand once they've all run.",
    runbooks: "The runbooks in it, in the order they're usually run.",
  },
  library: {
    name: "The library's identifier, used under a runbook's libraries:.",
    description: "What the functions are for.",
    functions: "name → description and body. Each becomes a shell function: name() { body }. Arguments are $1, $2…; the run's variables are available as usual.",
  },
  check: {
    name: "What the check confirms, shown in the ✅/❌ table.",
    run: "Command that exits 0 when the check passes. Its output is kept for failing checks.",
  },
  way: {
    name: "Shown when choosing: Scripted, Manual, or your own name.",
    preferred: "The default way, and the one unattended runs use (the first if none is marked).",
    run: "A shell script (a Scripted way).",
    manual: "Instructions for a person (a Manual way).",
    checks: "Commands that must all pass (with the step's repeat).",
    tty: "The script needs a keyboard.",
  },
};

// helpIcon is the ⓘ shown next to a field; the text appears on hover/focus.
function helpIcon(text) {
  return text ? `<span class="info" tabindex="0" data-tip="${esc(text)}" aria-label="${esc(text)}">ⓘ</span>` : "";
}

// The tip floats on the page (position: fixed), kept inside the viewport.
(() => {
  let tip;
  const show = (el) => {
    tip ||= Object.assign(document.createElement("div"), { id: "tip" });
    tip.textContent = el.dataset.tip;
    tip.classList.toggle("code", !!el.dataset.tipCode);
    tip.style.left = tip.style.top = "0px";
    document.body.append(tip);
    const r = el.getBoundingClientRect(), t = tip.getBoundingClientRect();
    const left = Math.max(8, Math.min(r.left + r.width / 2 - 40, innerWidth - t.width - 8));
    const below = r.bottom + 6 + t.height <= innerHeight - 8;
    const above = r.top - 6 - t.height >= 8;
    tip.style.left = left + "px";
    // Too tall for either side: from the top of the window (click ƒ for all of it).
    tip.style.top = (below ? r.bottom + 6 : above ? r.top - 6 - t.height : 8) + "px";
  };
  const hide = () => tip?.remove();
  const at = (e) => e.target.closest?.(".info[data-tip], .cmt[data-tip], .fn-icon[data-tip]");
  document.addEventListener("mouseover", (e) => { const el = at(e); if (el) show(el); });
  document.addEventListener("mouseout", (e) => { if (at(e)) hide(); });
  document.addEventListener("focusin", (e) => { const el = at(e); if (el) show(el); });
  document.addEventListener("focusout", (e) => { if (at(e)) hide(); });
  document.addEventListener("scroll", hide, true);
})();

function helpTable(group, keys) {
  return `<table class="help-fields"><tbody>${keys.map(([k, label]) =>
    `<tr><td><code>${esc(label || k)}</code></td><td>${esc(FIELD_HELP[group][k])}</td></tr>`).join("")}</tbody></table>`;
}

function viewHelp() {
  main.innerHTML = `
  <div class="help">
    <h1>Runbook format</h1>
    <p class="muted">A runbook is a YAML file (or the structured editor's form). This page describes every field. In the editor, hover the ⓘ next to a field for the same text.</p>
    <nav class="help-toc">
      <a href="#help-top">Runbook</a> · <a href="#help-inputs">Inputs</a> · <a href="#help-steps">Steps</a> ·
      <a href="#help-kinds">Kinds of step</a> · <a href="#help-window">Maintenance window</a> · <a href="#help-templates">Templates &amp; state</a> ·
      <a href="#help-functions">Functions &amp; libraries</a> · <a href="#help-masking">Masking</a> · <a href="#help-running">Running</a> · <a href="#help-example">Example</a>
    </nav>

    <h2 id="help-top">Runbook</h2>
    ${helpTable("runbook", [["name"], ["description"], ["expect"], ["sections"], ["tags"], ["shell"], ["profile"], ["env"], ["sensitive"], ["libraries"], ["functions"], ["inputs"], ["steps"]])}

    <h3>Describing what's happening</h3>
    <p>Every level can say what it's doing and how things should stand afterwards: a <b>collection</b> of runbooks, the <b>runbook</b>, each <b>section</b>, and each <b>step</b> (<code>description</code> and <code>expect</code>).</p>
    <pre class="cmd">description: Move a silo from RDS to Aurora with ~20 minutes of downtime.
expect: The silo runs on Aurora; RDS is detached and can be deleted.
sections:
  - name: 3. Cutoff
    description: Stop all traffic to RDS and confirm Aurora has every write.
    expect: The app is down, RDS takes no writes, Aurora is caught up.
steps:
  - name: Maintenance mode ON
    section: 3. Cutoff
    description: Put the app in maintenance mode so customers see a notice, not errors.
    expect: Greenhouse shows the maintenance page.
    run: lotus maintenance-mode on …</pre>
    <p><b>Collections</b> group runbooks (managed under Collections, or <code>runnr collection</code>):</p>
    <pre class="cmd">name: aurora-migration
description: Moving GHR silos from RDS to Aurora.
expect: All silos on Aurora; RDS instances retired.
runbooks: [ghr-aurora-migration, ghr-aurora-fallback]</pre>
    ${helpTable("collection", [["name"], ["description"], ["expect"], ["runbooks"]])}

    <h2 id="help-inputs">Inputs</h2>
    <pre class="cmd">inputs:
  - name: silo_env
    description: prod-sN or staging-sN
    required: true
  - name: env
    default: staging
    choices: [staging, production]
  - name: token
    secret: true</pre>
    ${helpTable("input", [["name"], ["description"], ["default"], ["choices"], ["required"], ["secret"]])}

    <h2 id="help-steps">Steps</h2>
    <p>Every step has a <code>name</code> and exactly one of <code>run</code>, <code>manual</code>, <code>checks</code> or <code>ways</code>. The rest is optional.</p>
    ${helpTable("step", [["name"], ["description"], ["expect"], ["id"], ["section"], ["owner"], ["confirm"], ["dangerous"], ["choose"], ["window"], ["window_offset"], ["always"], ["optional"], ["when"], ["continue_on_error"],
      ["tty"], ["background"], ["stop_after"], ["timeout"], ["retries"], ["retry_delay"], ["dir"], ["step_shell", "shell"], ["step_env", "env"],
      ["outputs"], ["secret_outputs", "secret_outputs"], ["capture"], ["unattended"]])}

    <h2 id="help-kinds">Kinds of step</h2>
    <h3>Command</h3>
    <pre class="cmd">- name: Resolve version
  run: git describe --tags
  capture: version</pre>
    <p>${esc(FIELD_HELP.step.run)}</p>

    <h3>Manual</h3>
    <pre class="cmd">- name: Announce the cutover
  owner: Datastores
  manual: Post in #eng_manual_changes and ping the on-call.</pre>
    <p>${esc(FIELD_HELP.step.manual)}</p>

    <h3>Terminal command (<code>tty</code>)</h3>
    <pre class="cmd">- name: Terraform apply
  tty: true
  run: terraform apply</pre>
    <p>${esc(FIELD_HELP.step.tty)} Without a terminal (unattended), it follows <code>unattended</code>.</p>

    <h3>Background command</h3>
    <pre class="cmd">- id: watcher
  name: Cutover watcher
  background: true
  stop_after: app_back
  run: ./cutover-window.rb status --watch</pre>
    <p>${esc(FIELD_HELP.step.background)} Its output is recorded with the step. A background command can't publish outputs.</p>

    <h3>Checks</h3>
    <pre class="cmd">- name: Ready for promotion
  checks:
    - name: Aurora reachable
      run: psql "$aurora" -c 'select 1'
    - name: LSN unchanged for 20s
      run: ./lsn-stable.sh 20
  repeat:
    every: 20s
    timeout: 10m
  retry_checks: failed   # all (default) | failed | each</pre>
    <p>${esc(FIELD_HELP.step.checks)} <code>retry_checks</code>: ${esc(FIELD_HELP.step.retry_checks)} Without <code>repeat</code>, a failing group asks what to do: retry re-runs every check. With it, the group re-runs on its own every <code>every</code> until all pass, giving up after <code>timeout</code>.</p>
    ${helpTable("check", [["name"], ["run"]])}

    <h3>Several ways</h3>
    <pre class="cmd">- name: Secrets → HAProxy
  choose: true            # ask which, each time (otherwise: the preferred one)
  ways:
    - name: Scripted
      run: ./secret-swap.rb --to haproxy --apply
      preferred: true
    - name: Manual
      manual: lotus secrets edit, check the URLs, lotus session commit
    - name: lotus by hand
      run: lotus secrets edit
      tty: true</pre>
    <p>${esc(FIELD_HELP.step.ways)} ${esc(FIELD_HELP.step.choose)} The step's other settings (dir, env, outputs, repeat…) apply whichever way runs.</p>
    ${helpTable("way", [["name"], ["preferred"], ["run"], ["manual"], ["checks"], ["tty"]])}

    <h2 id="help-window">Maintenance window</h2>
    <pre class="cmd">inputs:
  - {name: env, choices: [staging, production]}
  - {name: t_promote, description: when to promote, e.g. +25m in production}
maintenance_window:
  target: 45m
  notification_required: true   # due 2w before, or set notify_before
sections:
  - {name: Prep, window_offset: -30m}
  - {name: Cutover, window_offset: 0}
steps:
  - {name: Maintenance mode ON, section: Cutover, window: open, run: …}
  - {name: Promote, window_offset: +10m, offset_input: t_promote, manual: …}
  - {name: Maintenance mode OFF, window: close, run: …}</pre>
    <p>${esc(FIELD_HELP.runbook.maintenance_window)}</p>
    <p>${esc(FIELD_HELP.step.window_offset)} ${esc(FIELD_HELP.step.offset_input)} (<code>offset_input</code> on sections and steps, <code>target_input</code> on the window.) ${esc(FIELD_HELP.step.out_of_order)} Planned times show as T-30m, T+0, T+10m in the plan; while the window is open each step shows how far ahead or behind it is. A run that stops with the window open says so plainly: production may still be down.</p>
    <p><b>Notification.</b> <code>notification_required: true</code> (with <code>notify_before</code>, default <code>2w</code>) puts a “notification due” date on the calendar ahead of each scheduled window.</p>
    <p><b>Business days.</b> On the calendar and agenda, dates worked out from offsets (sections, steps, the notification) that land on a Saturday or Sunday move to a weekday at the same time: back to the Friday before when they come before the run's start, forward to the Monday after when they follow it. Only the maintenance window, and what happens during it, falls on a weekend.</p>
    <p><b>Scheduling.</b> Plan runs ahead under <a href="#/schedule">Schedule</a> (or <code>runnr schedule add</code>): pick when the window opens and the run's inputs; the calendar and agenda show each window and the planned starts of its sections and steps. Nothing starts on its own; <i>Start this run…</i> opens the launch page with those inputs.</p>

    <h2 id="help-templates">Templates &amp; state</h2>
    <p><code>run</code>, <code>manual</code>, <code>expect</code>, <code>when</code>, <code>dir</code> and <code>env</code> are Go templates: <code>{{.name}}</code> is a value; helpers are <code>default</code>, <code>upper</code>, <code>lower</code>, <code>trim</code>, <code>contains</code> and <code>quote</code> (shell-quote). An unknown name is an error.</p>
    <p>The run's <b>state</b> is every input and published value. Every later step gets all of it, both as <code>{{.name}}</code> and as the environment variable <code>$name</code>. Steps publish with <code>outputs</code> / <code>secret_outputs</code> (shell variables), <code>capture</code> (standard output), or by writing <code>name=value</code> lines to <code>$RUNNR_OUTPUT</code> / <code>$RUNNR_SECRET</code>. Each step runs in its own shell, so plain shell variables and <code>cd</code> don't carry over. State is saved after each step (credentials masked) and restored on resume; secrets are re-derived by <code>always</code> steps.</p>
    <p>Steps also get <code>RUNNR_RUN_ID</code> and <code>RUNNR_RUNBOOK</code>.</p>

    <h2 id="help-functions">Functions &amp; libraries</h2>
    <p>Put commands you use in many steps or runbooks into shell functions. A <b>library</b> is a named set of them, managed under <a href="#/libraries">Libraries</a> (or <code>runnr lib</code>); a runbook uses it by listing it, and can also define its own:</p>
    <pre class="cmd">libraries: [aurora-helpers]
functions:
  wait_healthy:
    description: Poll a URL until it returns 200. Usage: wait_healthy URL
    body: |
      until curl -fsS "$1" >/dev/null; do sleep 5; done</pre>
    ${helpTable("library", [["name"], ["description"], ["functions"]])}
    <p>Every step can call them (commands, checks, every way, background steps), and shells opened from runnr have them too, loaded after your own shell config. Elsewhere: <code>eval "$(runnr lib export aurora-helpers)"</code>. A run fails to start if a library it uses is missing. The run's functions are also in <code>$RUNNR_FUNCTIONS</code> to source by hand.</p>

    <h2 id="help-masking">Masking</h2>
    <p>Secret inputs and <code>secret_outputs</code> are hidden wherever they appear: output, commands, errors, state, history and terminals in the browser. When a pattern recognises a secret, only the credential part is hidden (<code>postgres://app:****@db/app</code>); otherwise all of it (<code>****</code>). Built-in patterns cover URL passwords, Bearer tokens and AWS secret keys; add your own with <code>sensitive</code>:</p>
    <pre class="cmd">sensitive:
  api_key: 'X-Api-Key: {{SENSITIVE}}'
  ticket: 'token={{SENSITIVE}}&amp;'</pre>
    <p>The text around <code>{{SENSITIVE}}</code> is a regular expression; <code>{{SENSITIVE}}</code> matches characters up to the next space or quote.</p>

    <h2 id="help-running">Running</h2>
    <ul>
      <li><b>Interactive</b>: review the plan (skip steps, pause before or after any of them), then run. It stops for confirmations, manual steps, steps set to ask which way, and failures (retry / another way / skip / force with a reason / abort).</li>
      <li><b>Step through</b>: stop before every step: run, skip, carry on without stopping, or abort.</li>
      <li><b>Unattended</b>: no questions. Confirmations need auto-approve, manual and terminal steps follow <code>unattended</code>, steps with several ways use the preferred one (starting asks you to confirm that for steps set to ask), and any failure stops the run.</li>
      <li>At any question: open a <b>shell</b> with the run's variables and the step's commands beside it, add a <b>note</b>, see the <b>state</b>, or view a background step.</li>
      <li><b>Change a step mid-run</b>: <b>✎ edit</b> on the current step or a later one (CLI: <b>[c]hange step</b>). A step waiting on a question starts again with the change. It's only in that run until you <b>Save to runbook</b>.</li>
      <li><b>Dangerous steps</b> (<code>dangerous: true</code>) run like any other in sequence. Out of sequence (earlier steps skipped, a later start, Run this, a runbook file) they list what didn't run and wait for you to type the step number; a resume counts the steps the earlier run finished.</li>
      <li><b>Resume</b> continues a stopped run at the step that didn't finish, with the same inputs and state; <b>run again</b> repeats it, comparing each step with last time.</li>
    </ul>

    <h2 id="help-example">Example</h2>
    <pre class="cmd">${esc(REFERENCE)}</pre>
  </div>`;
  main.querySelectorAll(".help-toc a").forEach((a) => (a.onclick = (e) => {
    e.preventDefault();
    document.getElementById(a.getAttribute("href").slice(1))?.scrollIntoView({ behavior: "smooth" });
  }));
}

// ---------- functions used by a step ----------
// The ƒ marker beside a step's description (or script) names the library
// and runbook functions its code calls; hovering it shows their definitions.

const libFns = new Map(); // library name → its functions (once loaded)

// loadLibs fetches the libraries a runbook uses that aren't loaded yet;
// it reports whether it loaded any (so the caller can draw again).
async function loadLibs(names) {
  const missing = (names || []).filter((n) => !libFns.has(n));
  await Promise.all(missing.map(async (n) => {
    try { libFns.set(n, (await api("GET", `/api/libraries/${enc(n)}`)).library?.functions || {}); }
    catch { libFns.set(n, {}); }
  }));
  return missing.length > 0;
}

// fnsOf is every function a runbook's steps can call: its libraries' (as
// loaded so far), then its own, which win.
function fnsOf(rb) {
  const out = {};
  for (const l of rb?.libraries || []) for (const [k, f] of Object.entries(libFns.get(l) || {})) out[k] = { ...f, from: `library ${l}` };
  for (const [k, f] of Object.entries(rb?.functions || {})) out[k] = { ...f, from: "this runbook" };
  return out;
}

// fnIcon is the ƒ marker for the functions text calls, or "" if none.
function fnIcon(text, fns) {
  if (!text || !fns) return "";
  const used = Object.keys(fns).filter((n) => new RegExp(`(^|[^\\w-])${n.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}([^\\w-]|$)`, "m").test(text));
  if (!used.length) return "";
  const tip = used.map((n) => {
    const f = fns[n];
    const desc = (f.description || "").trim().split("\n").map((l) => `# ${l}`).join("\n");
    return `# ${n}: from ${f.from}\n${desc ? desc + "\n" : ""}${n}() {\n${(f.body || "").replace(/\s+$/, "")}\n}`;
  }).join("\n\n");
  return `<span class="fn-icon" tabindex="0" role="button" data-tip="${esc(tip)}" data-tip-code="1" title="">ƒ ${esc(used.join(", "))}</span>`;
}

// Clicking ƒ opens the definitions where they can be scrolled and copied.
document.addEventListener("click", (e) => {
  const el = e.target.closest?.(".fn-icon[data-tip]");
  if (!el) return;
  e.preventDefault();
  e.stopPropagation();
  document.getElementById("tip")?.remove();
  const dlg = document.createElement("dialog");
  dlg.className = "edit-step fn-dialog";
  dlg.innerHTML = `<div class="row"><h3>Functions used here</h3><span class="spacer"></span><button data-close>Close</button></div><pre class="cmd"></pre>`;
  dlg.querySelector("pre").textContent = el.dataset.tip;
  document.body.append(dlg);
  dlg.onclose = () => dlg.remove();
  dlg.querySelector("[data-close]").onclick = () => dlg.close();
  dlg.addEventListener("click", (ev) => { if (ev.target === dlg) dlg.close(); });
  dlg.showModal();
}, true);
