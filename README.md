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
| `runnr import <file>...` | Validate and add files to the library (`-f` to overwrite, `-` for stdin) |
| `runnr delete <name>` | Delete (`-f` to skip the prompt) |
| `runnr validate [name\|file]...` | Validate; all runbooks if none given |
| `runnr run <name\|file>` | Run a runbook (see below); `--from <step>` starts later |
| `runnr rerun <run-id>` | Run again with an earlier run's inputs, comparing each step with what it did then |
| `runnr resume <run-id>` | Continue a failed/aborted run at the step where it stopped |
| `runnr shell <runbook\|run-id>` | Run the setup (`always`) steps, then open `$SHELL` with that environment |
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
| **Interactive** (default on a TTY) | Asks for missing inputs, shows the plan for review, then runs. It stops on its own for confirm-gated steps, manual steps and failures (retry / run it myself / skip / force / abort). |
| **Step by step** (`--step`) | Interactive, but pauses before *every* step: run / run it myself / skip / continue without pausing / abort. |
| **Unattended** (`-u`, or stdin isn't a TTY) | Never prompts. Inputs from `-v` or defaults. Confirm-gated steps fail unless `-y`. Manual steps follow their `unattended` policy. Any failure aborts. |

`--json` implies unattended and prints the run record. Exit codes: `0` success, `1` failure, `130` aborted.

**Review.** Before an interactive run, rerun or resume starts, you get the
plan: every step with its kind, whether it confirms on its own, and how it
went last time (the run being rerun/resumed, otherwise this runbook's latest
run). From there: type step numbers to skip/include them, `b 4` / `a 4` to
pause before/after a step, `v 4` to see what it will do, `l 4` to see what it
did last time, `step` to step through everything, Enter to start.

**Pausing.** *Before* a step: run / run it myself / skip / continue without
pausing / abort. *After* a step: look at the result (and its `expect`), then
continue / run it again / mark it failed (then the usual failure choices) /
abort. Steps with `confirm: true` always ask before running.

At any step prompt you can also:

- **`v`**: view the step as it will run: commands with current values filled
  in, expected outcome, directory, what it publishes.
- **`l`**: what this step did last time (status, command, output, notes).
- **`e`**: show the run's state: every variable so far, secrets as `****`.
- **`n`**: add a note to the step (stored with the run; see `runnr history <id>`).
- **`!`**: open your `$SHELL` with the run's environment (inputs, values
  published so far, `env:`), in the step's `dir`. Investigate, fix things,
  `exit` to come back to the same prompt. `echo name=value >> "$RUNNR_OUTPUT"`
  in that shell publishes a value to later steps. Ctrl-C in the shell doesn't
  touch the run.
- **`m`** (before a step, or after it fails): run it yourself. runnr prints the
  commands and opens a shell with the run's environment; paste or adapt them,
  `exit`, then say how it went. Values you publish in that shell are kept.
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
  any time) to edit inputs and steps as forms, reorder, duplicate and delete
  steps. The structured view saves by regenerating the YAML, so YAML comments
  are dropped (it asks first).
- History, versions, notes.

Steps that need a terminal (`tty`), "run it myself" and "open shell" get a
**terminal in the page**: you type into it as in any terminal, everyone
watching the run sees it, and what it printed is recorded with the step.
This works whether the server is local or remote.

It binds to `127.0.0.1` by default and refuses other addresses unless you set
an API token (`--token` or `RUNNR_TOKEN`), since it can run commands. See
[docs/API.md](docs/API.md).

## Runbook format

```yaml
name: deploy-service            # lowercase identifier; also the filename
description: Deploy a service
tags: [deploy]
shell: bash                     # default: sh
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
    timeout: 10m
    retries: 2
    retry_delay: 30s
    dir: ./infra
    env: { TOKEN: "{{.token}}" }
  - name: Smoke test
    run: curl -fsS https://example.com/healthz
    continue_on_error: true
```

Each step has exactly one of `run` or `manual`. Every input and published
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
run after each step (secrets as `****`), shown in history, the GUI and at
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
Shells (`!`) and "run it myself" work the same way, so what you did by hand
is recorded too. Unattended runs have no one to type, so `tty` steps follow
their `unattended` policy. Shell aliases aren't loaded in steps, so call real
binaries (`terraform`, not `tf`).

Secrets published via `$RUNNR_SECRET` are masked from the moment they're
published; don't echo them in the step that creates them.

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
