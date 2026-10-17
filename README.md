# runnr

A runbook manager and automater. Write runbooks as YAML, then run them from the
**CLI** or a local **web GUI** — step by step with a human in the loop, or fully
unattended for cron/CI.

- **Automated steps** run shell commands with live output, timeouts, retries and captured output.
- **Manual steps** show instructions and wait for an operator to mark them done, skipped or failed.
- **Confirmation gates** pause before risky commands.
- **Inputs** with defaults, choices and secrets (secrets are masked in output and history).
- **Shared variables**: a step can publish values (and secrets) for every later step.
- **Terminal steps** hand the terminal to tools like `terraform apply` or `argocd login --sso`.
- **Resume** long-running or multi-day procedures from where they stopped.
- **Run history** for every run, and **version history** for every runbook save.
- **SQLite or Postgres** storage: zero-setup locally, shared for a team.
- **HTTP API** for CI, cron and chatops ([docs/API.md](docs/API.md)).

## Install

```sh
make install        # or: go install github.com/egrif/runnr/cmd/runnr@latest
```

Requires Go 1.24+. Produces a single binary with the GUI embedded.

## Quick start

```sh
runnr import examples/*.yaml
runnr list
runnr run hello                              # interactive
runnr run deploy-service -u -y -v env=staging  # unattended
runnr serve --open                           # GUI + API at http://127.0.0.1:7777
```

## CLI

| Command | |
|---|---|
| `runnr list` | List runbooks (`--json`) |
| `runnr new <name>` | Create from a starter template and open `$EDITOR` |
| `runnr edit <name>` | Edit in `$EDITOR`; only saved once it validates |
| `runnr show <name>` | Print YAML (`--version N` for an older save) |
| `runnr inspect <name> [step]` | Explore what will run: all steps, or one step's code, expected outcome and outputs (`-v` fills in inputs, `--all`) |
| `runnr versions <name>` | List saved versions |
| `runnr lib list/show/new/edit/import/delete/versions` | Manage function libraries |
| `runnr collection list/show/new/edit/import/delete` | Group runbooks that belong together, with what they're for |
| `runnr collection add <col> <runbook>...` / `remove <col> <runbook>...` | Put runbooks in a collection (`--create` to make it) or take them out (they stay in runnr) |
| `runnr schedule add/list/rm` | Plan runs ahead; `list` prints the agenda |
| `runnr collection move <runbook>... --to <col>` | Move runbooks to another collection (created if needed); `--from` when one is in several |
| `runnr lib export <name>...` | Print a library as shell code: `eval "$(runnr lib export aurora-helpers)"` |
| `runnr import <file>...` | Validate and add files to the library (`-f` to overwrite, `-` for stdin, `-c <collection>` to add them to a collection) |
| `runnr delete <name>` | Delete (`-f` to skip the prompt) |
| `runnr validate [name\|file]...` | Validate; all runbooks if none given |
| `runnr run <name\|file>` | Run a runbook (see below); `--from <step>` starts later |
| `runnr rerun <run-id>` | Run again with an earlier run's inputs, comparing each step with what it did then |
| `runnr resume <run-id>` | Continue a failed/aborted run at the step where it stopped |
| `runnr shell <runbook\|run-id>` | Run the setup (`always`) steps, then open a shell (the runbook's `shell`, else `$SHELL`) with that environment |
| `runnr note <run-id> [-s step] <text>` | Add a note to a run or step, during or after the run |
| `runnr history [run-id]` | List runs (`-r <runbook>`, `-n`), or show one in detail |
| `runnr history prune --older-than 90d` | Delete old finished runs |
| `runnr db info` / `runnr db import <dir>` | Show storage details / import a file-based runnr home |
| `runnr serve` (alias `ui`) | Serve the GUI and API (`--addr`, `--open`, `--token`, `--no-ui`) |

## Storage

By default everything is in a SQLite database at `~/.runnr/runnr.db`
(`--home`/`RUNNR_HOME` moves it). For a team, point every runnr (CLI and
server) at one Postgres database:

```sh
export RUNNR_DATABASE_URL=postgres://runnr:...@db.internal:5432/runnr   # or --db
runnr db import ~/.runnr-old    # optional: bring in file-based runbooks and runs
```

runnr creates its tables on first connect. It stores:

- **runbooks**: current YAML per name, with a version number.
- **runbook_versions**: every saved version, kept even after delete or rename.
  Edits from the CLI and GUI are checked against the version you started from,
  so a concurrent save is refused (`409` in the API) rather than overwritten.
- **runs**: one record per run (inputs, step statuses, last 64 KiB of output per
  step, the runbook version that ran), updated after every step. Secrets are
  masked; output of `tty` steps isn't captured. Prune with `runnr history prune`.

Older versions of runnr kept YAML and JSON files under `~/.runnr`; the default
SQLite database imports them once, on first use, and leaves the files alone.

### Running

```
runnr run <name|file> [-v key=value]... [--step] [--skip 3,5-7] [--pause-before 4] [--pause-after 4,9]
                      [--no-review] [-u] [-y] [--dry-run] [--json] [-q] [--no-history]
```

| Mode | Behaviour |
|---|---|
| **Interactive** (default on a TTY) | Asks for missing inputs, shows the plan for review, then runs. It stops on its own for confirm-gated steps, manual steps and failures (retry / skip / force / abort). |
| **Step by step** (`--step`) | Interactive, but pauses before *every* step: run / skip / continue without pausing / abort. |
| **Unattended** (`-u`, or stdin isn't a TTY) | Never prompts. Inputs from `-v` or defaults. Confirm-gated steps fail unless `-y`. Manual steps follow their `unattended` policy. Any failure aborts. |

`--json` implies unattended and prints the run record. Exit codes: `0` success, `1` failure, `130` aborted.

**Review.** Before an interactive run, rerun or resume starts, you get the
plan: every step with its kind, whether it confirms on its own, and how it
went last time (the run being rerun/resumed, otherwise this runbook's latest
run). From there: type step numbers to skip/include them, `b 4` / `a 4` to
pause before/after a step, `v 4` to see what it will do, `l 4` to see what it
did last time, `step` to step through everything, Enter to start.

**Pausing.** *Before* a step: run / skip / continue without
pausing / abort. *After* a step: look at the result (and its `expect`), then
continue / run it again / mark it failed (then the usual failure choices) /
abort. Steps with `confirm: true` always ask before running.

**Changing a step mid-run.** While a run is live you can change the
current step or any later one: **✎ edit** on the run page, or **[c]hange
step** at a CLI prompt (opens `$EDITOR` on the step's YAML). If the step is
waiting on a question (a failure, a pause, a confirmation), it starts again
with the new version. The change is only in that run until you save it: the
run page shows a **Save to runbook** reminder, the CLI asks at the end, and
`runnr save-edits <run-id>` does it later. Saving makes a new runbook
version; if someone changed that step in the runbook since, it's refused.

**Dangerous steps.** A step with `dangerous: true` runs like any other when
it's in sequence in a recorded run of the saved runbook: every earlier step
ran, was forced, or was left out by its own `when` (a resume counts what the
earlier run finished). Otherwise (a later start, skipped steps, Run this, a
runbook file) it lists what didn't run and waits for you to type the step
number. Unattended runs refuse it then, even with `--yes`.

At any step prompt you can also:

- **`v`**: view the step as it will run: commands with current values filled
  in, expected outcome, directory, what it publishes.
- **`w`**: watch a background step (its terminal; Ctrl-] to come back).
- **`l`**: what this step did last time (status, command, output, notes).
- **`e`**: show the run's state: every variable so far, credentials masked.
- **`n`**: add a note to the step (stored with the run; see `runnr history <id>`).
- **`!`**: open a shell (the runbook's `shell`, so pasted commands behave as
  they do in the steps; else your `$SHELL`) with the run's environment (inputs, values
  published so far, `env:`), in the step's `dir`, with the step's commands
  shown beside it (above it in the CLI) to run or adapt. Investigate, fix
  things, `exit` to come back to the same prompt; the session is recorded
  with the step. `echo name=value >> "$RUNNR_OUTPUT"`
  in that shell publishes a value to later steps. Ctrl-C in the shell doesn't
  touch the run.
- **`f`** (failed steps): force the step to count as success. A reason is
  required; the step is recorded as `forced` with it, and whatever it
  published is kept.

A failed or aborted run can be continued with `runnr resume <run-id>`: same
inputs (secret inputs are asked again) and the same state (secrets excepted),
starting at the step that didn't finish. Steps marked `always` (typically
environment setup) run again and re-derive the secrets. For
multi-day procedures, abort at a "wait" step and resume later.

## GUI

`runnr serve` serves a local web app:

- **Run, Run again, Resume** all open the same launch screen: inputs (carried
  over for rerun/resume), interactive / unattended / step through everything,
  and per step: include or skip, ⏸ pause before, ⏸ pause after, a badge if it
  confirms on its own, and last time's result. Click a step to see **what it
  will do** (rendered with your inputs, and the earlier run's state on
  resume) next to **what it did last time**.
- **While a run is live**, upcoming steps can still be skipped or paused, and
  step-through switched on or off. Open any step for its output, what it will
  do, or last time's result. Prompts (confirm, manual, failure, pause before
  and after) are answered in the browser; notes and the run's state are there
  too.
- **Editing**: YAML with live validation, or a **structured view** (switch
  any time). Each step opens as **details | command | run**: the common
  details (what we're doing, by the end, section, owner, confirm) with an
  **Advanced** section for the rest (any that are set show above it),
  then the command or procedure. **Run this** runs just that step with your
  unsaved edits in a third pane (the earlier `always` steps first if you
  like; nothing after it runs), using the runbook's **test values** (inputs and
  extra variables; secret inputs stay in the browser). Test runs aren't
  recorded. The panes are resizable. The structured view saves by
  regenerating the YAML, so YAML comments are dropped (it asks first).
- **Collections** group runbooks in the sidebar, with what they're for.
- History, versions, notes.

Steps that need a terminal (`tty`) and "open shell" get a
**terminal in the page**: you type into it as in any terminal, everyone
watching the run sees it, and what it printed is recorded with the step.
This works whether the server is local or remote.

It binds to `127.0.0.1` by default and refuses other addresses unless you set
an API token (`--token` or `RUNNR_TOKEN`), since it can run commands. See
[docs/API.md](docs/API.md).

## Runbook format

```yaml
name: deploy-service            # lowercase identifier
description: |                  # what it's for and what it does (first line shows in lists)
  Deploy a service with a short confirmation gate.
expect: The new version serves traffic and dashboards are green.   # how things stand at the end
sections:                       # what each group of steps does, and how things stand after it
  - name: Deploy
    description: Roll the new version out.
    expect: All pods run the new version.
tags: [deploy]
shell: bash                     # default: sh
profile: [~/.zshrc]             # sourced before every step: your aliases/functions work (slower)
libraries: [aurora-helpers]     # shared shell functions every step (and shell) can call
functions:                      # this runbook's own functions
  wait_healthy:
    description: Poll a URL until it returns 200
    body: until curl -fsS "$1" >/dev/null; do sleep 5; done
sensitive:                      # credentials to hide wherever they appear; {{SENSITIVE}} = the hidden part
  api_key: 'X-Api-Key: {{SENSITIVE}}'
  ticket_token: 'token={{SENSITIVE}}&' 
env:                            # exported to every step; values are templates
  DEPLOY_ENV: "{{.env}}"
inputs:
  - name: env
    description: Target environment
    default: staging
    choices: [staging, production]
  - name: token
    secret: true                # masked as **** in output and history
    required: true
steps:
  - name: Derive environment
    section: Setup              # heading for this and the following steps
    always: true                # runs even with --from / resume
    run: |
      region=us-east-1
      db_pass=$(fetch-pass)
    outputs: [region]           # these shell variables join the run's state → $region, {{.region}}
    secret_outputs: [db_pass]   # same, but masked everywhere and never stored
    expect: Prints nothing; fails if the password can't be fetched.
  - name: Resolve version
    description: Work out which version we're deploying.   # what we're doing (and why)
    expect: The version tag is known.                       # by the end of this step
    run: git describe --tags
    capture: version            # trimmed stdout → {{.version}} for later steps
  - name: Terraform apply
    tty: true                   # attach to the terminal (prompts, SSO, live displays)
    run: terraform apply
  - name: Announce
    owner: Datastores           # who does this step (shown, not enforced)
    manual: |
      Post "Deploying {{.version}} to {{.env}}" in #deploys.
    when: '{{eq .env "production"}}'
    unattended: skip            # fail (default) | skip | pass
  - name: Deploy
    run: ./deploy.sh {{quote .version}}
    confirm: true               # ask first; unattended needs --yes
    dangerous: true             # out of sequence, type the step number to run it
    timeout: 10m
    retries: 2
    retry_delay: 30s
    dir: ./infra
    env: { TOKEN: "{{.token}}" }
  - name: Smoke test
    run: curl -fsS https://example.com/healthz
    continue_on_error: true
  - id: watcher
    name: Status dashboard
    run: ./status.sh --watch
    background: true            # starts and moves on; runs on its own terminal
    stop_after: verify          # stopped (hung up) after that step finishes
  - id: verify
    name: Ready?
    checks:                     # all must pass; each runs every attempt
      - { name: API up, run: curl -fsS https://example.com/healthz }
      - { name: Queue drained, run: ./queue-depth.sh | grep -qx 0 }
    repeat: { every: 20s, timeout: 10m }   # retry until all pass
  - name: Rotate secrets
    choose: true                # ask which way each time (else: the preferred)
    ways:
      - { name: Scripted, run: ./rotate.sh, preferred: true }
      - { name: Manual, manual: Update the secrets in the console, then restart. }
```

Each step has exactly one of `run`, `manual`, `checks` or `ways`. Every input and published
value is available to later steps both as a template variable and as an
environment variable. `run`, `manual`, `when`, `dir` and `env` values are Go
templates, with helpers
`default`, `upper`, `lower`, `trim`, `contains`, `quote` (shell-quote). Unknown
variables are an error. `when` is truthy unless it renders to empty, `false`,
`0`, `no` or `off`. Steps also get `RUNNR_RUN_ID` and `RUNNR_RUNBOOK` in their
environment.

**State.** Inputs and everything published (`outputs`, `secret_outputs`,
`capture`, or `name=value` lines written to `$RUNNR_OUTPUT`/`$RUNNR_SECRET`)
form the run's state. Every later step gets all of it as environment variables
and template values, whichever step published it. The state is saved with the
run after each step (credentials masked), shown in history, the GUI and at
prompts (`e`), and restored on resume.

`expect` describes what success looks like; it's shown when reviewing,
inspecting, and running a step yourself.

**Terminal steps.** A normal step has no keyboard: its output is captured,
but anything that stops to ask (like `terraform apply`'s approval) fails.
`tty: true` runs the step on a pseudo-terminal connected to you: your
terminal under `runnr run`, or a terminal in the page in the GUI. The command
sees a real terminal (prompts, colours, progress bars), you type as usual
(Ctrl-C goes to the command, not runnr), and the output is shown live *and*
recorded with the step, cleaned of colour and cursor codes, secrets masked.
Shells (`!`) work the same way, so what you did by hand is recorded too.
**End session** (GUI) hangs up a terminal like closing its window. Unattended runs have no one to type, so `tty` steps follow
their `unattended` policy.

**Background steps** (`background: true`) start and let the run move on.
They run on their own terminal until the `stop_after` step finishes (or the
run ends), are visible as a tab in the GUI or with `w` in the CLI, and their
output is recorded with the step. They can't publish outputs.

**Checks** run several commands as one step: each passes if it exits 0,
every one runs on each attempt, and the step passes when all pass. Results
show as a ✅/❌ table. With `repeat`, the group re-runs on its own until it
passes or times out; otherwise a failure offers retry (re-runs the group),
skip, force or abort.

**Ways** are different ways to do one step: Scripted, Manual, Checks, or
any you name. The preferred way runs (always, in unattended runs) unless the
step has `choose: true`, which asks each time, preferred as the default.
Starting an unattended run with such steps asks you to confirm
(`--use-preferred`, or `use_preferred` in the API). When a way fails you can
**try another way**. The step's other settings (`dir`, `env`, `outputs`,
`repeat`…) apply whichever way runs.

**Maintenance window.** `maintenance_window: {target: 45m}` (or `6h`, `2d`) describes the time
production is down: mark the step where it goes down `window: open` and the
one where it's back `window: close`. `window_offset` on sections and steps
(`-2w`, `-30m`, `0`, `+12m`, `+1d4h`: w weeks, d days, h hours, m minutes)
is when they're planned to start relative to the
window opening; `offset_input` (and `target_input` on the window) names an
input that overrides it when given a value, e.g. a longer cutoff in
production. Planned times have to keep step order: one earlier than a
planned time before it (or after T+0 before the opening step, or before
T+0 after it) is treated as unscheduled, and the step just follows on.
Plans show T-30m, T+0, T+12m; runs show a live window clock
against the target and each step's planned and actual time; history keeps
the real downtime, and a run that stops with the window open says so.

**Schedule.** Plan runs ahead: `runnr schedule add <runbook> --at "2026-11-01
22:00" -v env=prod` (for a runbook with a window, the time is when it
opens), or **Schedule…** on the launch page. The GUI's **Schedule** page shows
a calendar and an agenda of planned runs with their windows and, as you
choose, the planned starts of sections and steps, or just the next action of each run;
`runnr schedule list [--detail windows|sections|steps] [--next]` (next actions) prints the
agenda. Nothing starts on its own yet: **Start this run…** opens the launch
page with the schedule's inputs.

The GUI's **Runbook format help** page describes every field, and the
structured editor shows the same text on each field's ⓘ.

**Functions and libraries.** Shell functions used across steps or
runbooks go in a library: a named, versioned set of functions managed with
`runnr lib` or under Libraries in the GUI. A runbook that lists it under
`libraries:` (and its own `functions:`) can call them in every step, and
shells opened from runnr load them after your own zsh/bash config. In any
other terminal: `eval "$(runnr lib export <name>)"`. A run won't start if a
library it uses is missing.

```yaml
# runnr lib import aurora-helpers.yaml
name: aurora-helpers
description: Helpers for Aurora migrations
functions:
  kpsql:
    description: "Run SQL from a pod in the cluster. Usage: kpsql URL [SQL]"
    body: |
      kubectl run "psql-$RANDOM" --rm -i --restart=Never --image=postgres:16-alpine \
        --env="URL=$1" -- psql "$URL" -XtA -c "${2:-select 1}"
```

**What steps can see.** Steps run as you, on the machine runnr runs on: your
files, home directory and permissions, and the environment runnr was started
with (so your PATH, asdf shims and exported variables like `AWS_PROFILE`, if
you start runnr from your terminal). Logins that tools keep in files carry
across steps and to and from your own terminal: `aws sso login` (in
`~/.aws/sso/cache`), kubectl contexts (`~/.kube/config`), `argocd login`
(`~/.config/argocd/config`). What a step's shell only holds in memory ends
with the step: shell variables (publish them with `outputs`), credentials
exported as environment variables (publish with `secret_outputs`), and `cd`
(use `dir`). Aliases and functions from your shell config aren't loaded
unless the runbook sets `profile` (e.g. `[~/.zshrc]`), which sources those
files before every step; that can add a few seconds per step. Shells opened
with `!` are interactive and load your config as usual.

**Masking.** runnr hides credentials, not whole values. Secret inputs and
`secret_outputs` are hidden wherever they appear, but only their sensitive
part when a pattern recognises it: a secret database URL shows as
`postgres://app:****@db.internal:5432/app`, while a bare password shows as
`****`. Patterns also catch credentials that were never declared secret.
Built in: URL passwords (only the password in `scheme://u:****@`), `Bearer` tokens, and AWS
secret keys and session tokens. Add your own with `sensitive`: name →
regular expression, with `{{SENSITIVE}}` where the credential is (it matches
a run of non-space, non-quote characters). Masking applies to output,
commands, errors, state, history and terminal sessions in the GUI. Secrets
are never stored, so resume re-derives them (steps marked `always`). Values
published via `$RUNNR_SECRET` are masked from the moment they're published;
don't echo them in the step that creates them.

## Layout

```
cmd/runnr/          entrypoint
internal/runbook/   format, validation, templating
internal/engine/    executor + Interactor interface (unattended impl here)
internal/cli/       cobra commands + terminal Interactor
internal/server/    HTTP API, SSE, browser Interactor, embedded web/ UI
internal/store/     SQLite/Postgres storage: runbooks, versions, runs
examples/           sample runbooks
```

The engine never talks to a human directly; the CLI, GUI and unattended modes
are each an `engine.Interactor`.
