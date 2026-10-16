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
    kind: "Command: a shell script. Manual: instructions for a person. Checks: several commands that must all pass. Alternatives: different ways to do the step, picked when it runs.",
    run: "Shell script to run. Templates like {{.input}} are filled in first; every input and published value is also an environment variable ($name).",
    manual: "Instructions for the operator, who marks the step done, skipped or failed. Templates are filled in.",
    checks: "Commands that each pass if they exit 0. Every check runs on each attempt (it doesn't stop at the first failure); the step passes when all pass.",
    alternatives: "Ways to do this step. The operator picks one when the step runs; unattended runs use the first whose “when” holds. Each is a command or a short list of steps.",
    name: "Shown in the plan, prompts and history.",
    description: "What we're doing in this step, and why. Shown when reviewing, stepping through and in Details.",
    id: "Optional short identifier, for --from, stop_after and references. Letters, digits, _.",
    section: "Heading the step is grouped under. Applies to the following steps until another section.",
    owner: "Who does this step (shown, not enforced).",
    expect: "By the end of this step, how things should stand (what success looks like). Shown when reviewing, at prompts, when checking a result, and beside a shell.",
    confirm: "Ask before running, even when not stepping through. Unattended runs need --yes (or Auto-approve).",
    always: "Run even when starting later or resuming: for setup that later steps depend on.",
    tty: "The command needs a keyboard (prompts, live displays): it runs on a terminal you type into. Output is still recorded.",
    background: "Start the command and move on; it keeps running on its own terminal (a tab in the GUI, w in the CLI) until the “stop after” step finishes, or the run ends.",
    stop_after: "The step after which this background command is stopped (hung up).",
    continue_on_error: "If the step fails, record it and carry on instead of stopping.",
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
    repeat_every: "With repeat: how long to wait between attempts, e.g. 20s.",
    repeat_timeout: "With repeat: give up (and fail) after this long, e.g. 10m.",
  },
  section: {
    name: "A section name used by steps (their section field).",
    description: "What this group of steps does.",
    expect: "How things should stand at the end of the section.",
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
  alternative: {
    name: "Shown when choosing.",
    when: "Template; unattended runs pick the first alternative where this holds (or that has none).",
    run: "A command, for a one-command alternative.",
    tty: "The command needs a keyboard.",
    steps: "A short list of steps instead of one command. Retrying resumes at the step that failed. They inherit the step's directory, shell and environment.",
  },
};

// helpIcon is the ⓘ shown next to a field; the text appears on hover/focus.
function helpIcon(text) {
  return text ? `<span class="info" tabindex="0" data-tip="${esc(text)}" aria-label="${esc(text)}">ⓘ</span>` : "";
}

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
      <a href="#help-kinds">Kinds of step</a> · <a href="#help-templates">Templates &amp; state</a> ·
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
    <p>Every step has a <code>name</code> and exactly one of <code>run</code>, <code>manual</code>, <code>checks</code> or <code>alternatives</code>. The rest is optional.</p>
    ${helpTable("step", [["name"], ["description"], ["expect"], ["id"], ["section"], ["owner"], ["expect"], ["confirm"], ["always"], ["when"], ["continue_on_error"],
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
    timeout: 10m</pre>
    <p>${esc(FIELD_HELP.step.checks)} Without <code>repeat</code>, a failing group asks what to do: retry re-runs every check. With it, the group re-runs on its own every <code>every</code> until all pass, giving up after <code>timeout</code>.</p>
    ${helpTable("check", [["name"], ["run"]])}

    <h3>Alternatives</h3>
    <pre class="cmd">- name: Secrets → HAProxy
  alternatives:
    - name: secret-swap script
      run: ./secret-swap.rb --to haproxy --apply
    - name: by hand with lotus
      steps:
        - name: Edit the secrets
          run: lotus secrets edit
          tty: true
        - name: Review
          manual: Check the URLs.
        - name: Commit
          run: lotus session commit
          confirm: true</pre>
    <p>${esc(FIELD_HELP.step.alternatives)} One level only: steps inside an alternative can't have alternatives or run in the background.</p>
    ${helpTable("alternative", [["name"], ["when"], ["run"], ["tty"], ["steps"]])}

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
    <p>Every step can call them (commands, checks, alternatives, background steps), and shells opened from runnr have them too, loaded after your own shell config. Elsewhere: <code>eval "$(runnr lib export aurora-helpers)"</code>. A run fails to start if a library it uses is missing. The run's functions are also in <code>$RUNNR_FUNCTIONS</code> to source by hand.</p>

    <h2 id="help-masking">Masking</h2>
    <p>Secret inputs and <code>secret_outputs</code> are hidden wherever they appear: output, commands, errors, state, history and terminals in the browser. When a pattern recognises a secret, only the credential part is hidden (<code>postgres://app:****@db/app</code>); otherwise all of it (<code>****</code>). Built-in patterns cover URL passwords, Bearer tokens and AWS secret keys; add your own with <code>sensitive</code>:</p>
    <pre class="cmd">sensitive:
  api_key: 'X-Api-Key: {{SENSITIVE}}'
  ticket: 'token={{SENSITIVE}}&amp;'</pre>
    <p>The text around <code>{{SENSITIVE}}</code> is a regular expression; <code>{{SENSITIVE}}</code> matches characters up to the next space or quote.</p>

    <h2 id="help-running">Running</h2>
    <ul>
      <li><b>Interactive</b>: review the plan (skip steps, pause before or after any of them), then run. It stops for confirmations, manual steps, choices between alternatives and failures (retry / skip / force with a reason / abort).</li>
      <li><b>Step through</b>: stop before every step: run, skip, carry on without stopping, or abort.</li>
      <li><b>Unattended</b>: no questions. Confirmations need auto-approve, manual and terminal steps follow <code>unattended</code>, alternatives use <code>when</code>, and any failure stops the run.</li>
      <li>At any question: open a <b>shell</b> with the run's variables and the step's commands beside it, add a <b>note</b>, see the <b>state</b>, or view a background step.</li>
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
