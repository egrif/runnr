<p align="center"><img src="docs/brand/logo.png" alt="runnr" width="680"></p>

# runnr

A runbook manager and automater. Write runbooks as YAML, then run them from the
**CLI** or a local **web GUI** — step by step with a human in the loop, or fully
unattended for cron/CI.

- **Automated steps** run shell commands with live output, timeouts, retries and captured output.
- **Manual steps** show instructions and wait for an operator to mark them done, skipped or failed.
- **Confirmation gates** pause before risky commands.
- **Inputs** with defaults, choices and secrets (secrets are masked in output and history); mark the ones that say what a run is for (a target) as **identifiers** to see and search them in the history.
- **Shared variables**: a step can publish values (and secrets) for every later step.
- **Terminal steps** hand the terminal to tools like `terraform apply` or `argocd login --sso`.
- **Resume** long-running or multi-day procedures from where they stopped; go back to a step, jump ahead, or stop a running step without stopping the run.
- **Shells and background commands** beside a run, with its variables (secrets too) kept up to date.
- **Run history** for every run (filter by runbook; search by identifier, e.g. which silo), and **version history** for every runbook save; delete and restore from **Manage**.
- **Import and export**: a project folder at once (placeholders kept, references checked), a runbook with its libraries, a run's logs with the runs it resumed.
- **Tests** that run a runbook with its commands faked and a simulated operator, without touching anything ([docs/TESTING.md](docs/TESTING.md)).
- **SQLite or Postgres** storage: zero-setup locally, shared for a team.
- **A run console** in the GUI: the run on one screen, in panes you arrange.
- **HTTP API** for CI, cron and chatops ([docs/API.md](docs/API.md)).

See it in action: tours of **[the GUI](docs/TOUR.md)** and **[the CLI](docs/CLI.md)**, with screenshots.

## Install

```sh
make install        # or: go install github.com/egrif/runnr/cmd/runnr@latest
```

Requires Go 1.24+. Produces a single binary with the GUI embedded. Or
download a built binary (macOS and Linux, arm64 and amd64) from the
[releases](https://github.com/egrif/runnr/releases); every merge to main is
one. `runnr version` says which you have (and the commit it was built from).

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
| `runnr show <name>` | Print YAML (`--version V` for an older save: its hash, a prefix of it, or its short form) |
| `runnr inspect <name> [step]` | Explore what will run: all steps, or one step's code, expected outcome and outputs (`-v` fills in inputs, `--all`) |
| `runnr versions <name>` | List saved versions |
| `runnr lib list/show/new/edit/import/delete/versions` | Manage function libraries |
| `runnr collection list/show/new/edit/import/delete` | Group runbooks that belong together, with what they're for |
| `runnr collection add <col> <runbook>...` / `remove <col> <runbook>...` | Put runbooks in a collection (`--create` to make it) or take them out (they stay in runnr) |
| `runnr schedule add/list/rm` | Plan runs ahead; `list` prints the agenda |
| `runnr explain <runbook> [step] [-q question] [--fresh]` | Ask a model about a step, or review the whole runbook (see "Ask Claude" for setup) |
| `runnr export <runbook> [-v k=v] [-o file] [-f html]` | Write it as Markdown to follow by hand: a collapsed overview, a setup block, then each section and step with its code (also **Export as Markdown** on the run page); `-f html` (or `-o file.html`) writes a page to read instead (**View as HTML**); `-f yaml` (or `-o file.yaml`) writes the runbook as written with the libraries it uses beside it (`--lib`, `--no-libs`), so the folder imports again (**⇩ Export…** on the runbook's page) |
| `runnr collection move <runbook>... --to <col>` | Move runbooks to another collection (created if needed); `--from` when one is in several |
| `runnr lib export <name>...` | Print a library as shell code: `eval "$(runnr lib export db-helpers)"` |
| `runnr import <file\|folder>...` | Add runbooks, libraries, fakes, test suites and run records: files, or a project folder (its subfolders too); `-` reads a runbook from stdin, `-c` adds the runbooks to a collection. Placeholders `@NAME@` are kept as written (a run fills them in: `runnr folders`); references are checked, and a missing library defined elsewhere in the repository comes along (`--no-found` leaves it out). A taken name is refused unless `-f` (made its current version) or `--rename NAME=NEW` (references follow; `--as NEW` for one file). `-n` says what it would do; `--diff` also shows what saving over each taken name would change. In the GUI: **⇪ Import…** in the sidebar does the same for a folder, with **Show what it changes** for a taken name |
| `runnr delete <name>` | Delete a runbook (`-f` to skip the prompt): it leaves its collections, its scheduled runs are removed, and its versions are kept |
| `runnr restore [kind name]` | List what was deleted, or restore it: deleting a runbook, library, fake library, test suite or collection keeps its versions. In the GUI: **Manage** in the sidebar lists everything stored with what uses it, deletes several at once (saying first what depends on them), and restores |
| `runnr folders [runbook]` / `folders set [-c <col>] NAME=FOLDER...` / `unset` | The folders placeholders stand for on this machine: `@REPOS_ROOT@` for everything, a collection's `@REPO_ROOT@` and `@PROJECT_DIR@` (see [Placeholders](#placeholders)); given a runbook, what a run of it would have. A run takes `--collection` and `--set NAME=FOLDER` for itself |
| `runnr history --delete <run-id>` | Delete a finished run's record (the GUI: **Delete run** on its page) |
| `runnr history <run-id> --export <folder>` | Export the run's logs with the runs linked to it by resuming (`--only` for just it): `--as raw` (logs to read), `json` (records to import: `runnr import` takes them, or **Also import** in the GUI's import) or `both`; `--one-file` puts them in one file. The GUI: **⇩ Export logs…** on a finished run (to a folder, or a download) |
| `runnr test <suite\|runbook\|file>` | Test a runbook: run it with its commands faked and a simulated operator, against each scenario's expectations (`-s`, `-v`, `--log`); see [Testing runbooks](docs/TESTING.md) |
| `runnr tests import/list/show/delete` · `runnr fakes import/list/show/delete/check` | Manage test suites and fake libraries; `fakes check` compares AWS CLI fakes with the CLI's real response shapes |
| `runnr validate [name\|file]...` | Validate; all runbooks if none given |
| `runnr run <name\|file>` | Run a runbook (see below); `--from <step>` starts later, `--tmux` runs it in tmux |
| `runnr rerun <run-id>` | Run again with an earlier run's inputs, comparing each step with what it did then |
| `runnr resume <run-id>` | Continue a failed/aborted run at the step where it stopped, in the same run (`--new-run` for a new one) |
| `runnr attach [run-id\|runbook]` | Go back to a run started with `--tmux` |
| `runnr watch <run-id>` | Follow a run's steps as it goes (the `--tmux` side pane) |
| `runnr save-edits <run-id>` | Save the step changes made during a run as a new runbook version |
| `runnr doctor <runbook>` | Check that the programs it needs (its `requires`, and its libraries') are here |
| `runnr suspend <run-id> [-m reason] [--now]` | Suspend a run, to resume later: it stops once the running step finishes (at once if it's waiting for someone; `--now` stops the step too) and ends `suspended` |
| `runnr abort <run-id> [-m reason] [--force]` | Abort a run running elsewhere (another terminal, the GUI), or one left "running" by a process that's gone |
| `runnr shell <runbook\|run-id>` | Run the setup (`always`) steps, then open a shell (the runbook's `shell`, else `$SHELL`) with that environment; given a run, with its inputs and what its steps published, as last saved (secrets are asked for or re-derived) |
| `runnr note <run-id> [-s step] <text>` | Add a note to a run or step, during or after the run |
| `runnr history [run-id]` | List runs (`-r <runbook>`, `-n`, `-f <text>`: those whose identifiers, like a target, or ID contain it; a FOR column shows the identifiers), or show one in detail (`--raw`: as a plain log, each step's command, output and result in order; **Raw log** on a past run's page in the GUI; `--timeline`: a chart of the run in time, then its key moments; `--timeline=html` for a page) |
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

- **runbooks**: current YAML per name, and its version.
- **runbook_versions**: every saved version, kept even after delete or rename.
  Edits from the CLI and GUI are checked against the version you started from,
  so a concurrent save is refused (`409` in the API) rather than overwritten.
- **runs**: one record per run (inputs, step statuses, last 64 KiB of output per
  step, the versions of the runbook and libraries that ran), updated after
  every step. Secrets are masked; output of `tty` steps isn't captured. Prune
  with `runnr history prune`.

Libraries, fake libraries, test suites and collections are kept the same way.

**Versions** are the SHA-256 of the text, shown as its first and last four
characters (`3f2a…9c1d`); anywhere a version is asked for, that short form,
the whole hash or a prefix of it will do. The same text is the same version
in any runnr, so versions compare across installations. Saving text a
document had before makes that version current again rather than adding
one. An exported run carries the runbook and library text it ran; importing
it into another runnr keeps them as versions there.

A database from before versions were hashes is converted the first time
this runnr opens it (a SQLite file is copied aside first, as
`runnr.db.before-hashes-<time>`): runs then name the hash of what they ran.

Older versions of runnr kept YAML and JSON files under `~/.runnr`; the default
SQLite database imports them once, on first use, and leaves the files alone.

### Placeholders

Runbooks, libraries, fake libraries and test suites name folders by
placeholder, so they're the same on every machine:

```yaml
inputs:
  - {name: tools_dir, default: "@PROJECT_DIR@/tools"}
```

They're kept as written (imported, stored and exported with the `@…@` in),
and each machine says what they stand for, with `runnr folders` or the
GUI's **Collections** page: `@REPOS_ROOT@` (where your repositories are)
for everything, and `@REPO_ROOT@` and `@PROJECT_DIR@` for a collection's
runbooks. A value can name another, so only `@REPOS_ROOT@` need differ
between machines:

```sh
runnr folders set REPOS_ROOT=~/dev
runnr folders set -c payments REPO_ROOT=@REPOS_ROOT@/ops-runbooks \
    PROJECT_DIR=@REPO_ROOT@/projects/db-upgrade
```

A run fills them in when it starts, from its runbook's collection (in
several, where they agree), or `--collection`'s; `--set NAME=FOLDER` (or
the Run page's **Folders**) gives one for that run only, and a resume
keeps them. Any other `@NAME@` works the same way. A run doesn't start
while a placeholder it names has no value, or its value isn't a folder
here; tests, shells and the editor's **Run this** too. The values are this
machine's own: collections and exports leave them out, and a run records
the folders it had. An exported run (logs, records, timeline) has those
folders written as their placeholders again, so your paths stay here.

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

`--json` implies unattended and prints the run record. Exit codes: `0` success, `1` failure, `130` aborted, `75` suspended (to resume later).

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
current step or any later one: **✎ edit** on the run page, or **[m]odify
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

**Optional steps.** A step with `optional: true` isn't required. It's
labelled optional in the plan, the step list, the GUI and the exports. An
interactive run stops before it to ask: run it, or skip it and carry on. An
unattended run runs it, and if it fails, records the failure and carries on
without it. (A step can't be both `always` and `optional`.)

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
- **`b`**: run a command in the background with the run's environment (a
  window of its own under `--tmux`; else `w` watches it). It's recorded
  with the step, and ends with the run unless it ends first.
- **`p`** (under `--tmux`): a shell like `!`, in a pane beside the run, so
  the prompt stays where it is. What it publishes
  (`echo name=value >> "$RUNNR_OUTPUT"`) reaches the run when the next step
  starts, and the session is recorded with the step; `exit` closes the pane.
- **`g`**: go to a step: back (Enter: the step before; it and the steps
  after it run again), this step again, or ahead (the steps between are
  skipped, "jumped over"). The step gone to waits for you before it runs.
  Going back past a point of no return asks you to type yes; a background
  job still running in the way has to be ended first. In the GUI: **↺ Back
  to step N** and **Go to step** under every question.
- **Ctrl-C** while a step's command runs stops that step, not the run (as
  abort does: Ctrl-C, then terminated if it won't stop). It fails as
  "stopped by you", and you choose what's next: retry, skip, or go to a
  step. Ctrl-C again within 3 seconds aborts the run. In the GUI: **■ Stop
  step** on the running step (the run page and the console).
- **`i`**: change the run's inputs. The new values are checked as at the
  start and take effect before the run's next command: a step waiting to
  run its command starts again with them; a command already running keeps
  what it has. It says which steps already ran with the old value, and
  replaces a value a step published under the same name. Each change is
  recorded with the run (who, when, old → new), and a resume uses the new
  values. In the GUI: **✎ Change inputs** on a live run (the run page, or
  the console's Inputs), or `PUT /api/runs/{id}/inputs`.
- **`f`** (failed steps): force the step to count as success. A reason is
  required; the step is recorded as `forced` with it, and whatever it
  published is kept.
- **`m`**: modify the step for this run (see above).
- **`a`**: abort the run.

Shells opened during a run (`!`, `p`, the `--tmux` shell pane, the
console's) keep up with it: zsh reads the run's values again before each
prompt and command, bash before each prompt, so what later steps publish
(secrets too) is there. They're in a file only you can read
(`$RUNNR_VARS`), removed when the run ends. Plain `sh` keeps the values it
opened with.

A failed or aborted run can be continued with `runnr resume <run-id>`: same
inputs (secret inputs are asked again) and the same state (secrets excepted),
starting at the step that didn't finish. Steps marked `always` (typically
environment setup) run again and re-derive the secrets. For
multi-day procedures, suspend the run and resume it later.

A resume carries the same run on: same run ID, one record and one log
(`runnr history <id>`, `--raw`). The steps that finished stay as they went,
a step done again keeps its earlier tries beside it ("before the run was
resumed"), and the record lists each time it was resumed. `--new-run` (or
`"new_run": true` in the API) starts a new run that points back at it
instead.

If the runbook has changed since, the resume carries the run on with the
new version: steps are matched by name, a new step is still to do, and the
record notes which version it was resumed with. Steps it ran that the new
version no longer has stay in the record (and their notes become the
run's). `--new-run` won't start from a changed runbook.

**Watching together.** Everyone who opens a live run in the GUI sees it
live, and the run's header shows who's watching (the name each person
gives, as for notes). Who may drive it is set there (🔒) or when it starts
(**Only I drive it**): everyone, only some people, or everyone except some.
The others see it read-only: if they try to change it (answer, pause,
stop, suspend, type in its terminal…), they're asked first and can
override; everyone is told, and the run notes it. Names aren't checked,
so this is a safety catch against driving someone else's run by mistake,
not a lock. A terminal takes the window size of whoever typed in it last.

**Suspending.** To stop a run on purpose and pick it up later (tomorrow,
after a change window, once someone else has done their part), suspend it:
`z` at a prompt in the terminal running it, **‖ Suspend…** on its page or
in its console, or `runnr suspend <run-id> -m "why"` from anywhere. It
stops once the step running now has finished (a command isn't cut short;
**Carry on instead** takes the request back until then), or at once if it's
waiting for someone; `--now` (**Stop the step running now too**) stops
that step as well, to do again. The run ends `suspended`, with who and
why, and `runnr resume` (or **Resume…**) carries it on in the same run.
Background steps stop with it; optional background watchers start again on
resume.

**Aborting.** In the terminal running it, Ctrl-C or `a` at a prompt aborts
a run. From anywhere else, `runnr abort <run-id> -m "why"` (or **Abort
run** on its page in the GUI) asks the runnr running it to stop, whether
that's a terminal or `runnr serve`, here or on another machine sharing the
database: the step running now is stopped and the run ends aborted, with
who and why, ready to resume. A run whose process is gone (it exited on
this machine) is marked aborted straight away; one left "running" by a
process on another machine is marked with `--force` (in the GUI: **Mark
it aborted now**).

## GUI

`runnr serve` serves a local web app:

- **Run, Run again, Resume** all open the same launch screen: inputs (carried
  over for rerun/resume), interactive / unattended / step through everything,
  and per step: include or skip, ⏸ pause before, ⏸ pause after, a badge if it
  confirms on its own, and last time's result. Click a step to see **what it
  will do** (rendered with your inputs, and the earlier run's state on
  resume) next to **what it did last time**.
- **While a run is live**, upcoming steps can still be skipped or paused, and
  step-through switched on or off. Open any step: what it does is on the
  left, and on the right **This run** (its output as it arrives, checks,
  errors) is tabbed with **History** (what it did in earlier runs) and
  **Details** (how it's set up). Prompts (confirm, manual, failure, pause
  before and after) are answered in the browser; notes and the run's state
  are there too. While the run waits for you, every question has **↺ Back
  to step N** and **Go to step**: back to a step that ran (it and the ones
  after it run again), this step again, or ahead (the ones between are
  skipped); the step gone to waits for you before it runs. **■ Stop step**
  interrupts the running step's command (it fails, and you choose what's
  next) without stopping the run, and **✎ Change inputs** changes the
  inputs for the rest of the run.
- **Finished runs**: **Raw log**, **⇩ Export logs…** (the run and the runs
  linked to it by resuming, as raw logs, records to import, or both, plus a
  timeline page per run if you like; to a folder or a download), **Run
  again…**, **Resume…** and **Delete run**.
- **◷ Timeline** (any run, running or done): the run laid out in time.
  Each step's tries as bars (earlier tries, before a resume, faded), the
  shells and jobs alongside them, background steps, the maintenance window
  (with a closer look at it), the time it was stopped before a resume,
  notes, input changes and edits, and a list of key moments. Long idle
  stretches, like a night between a failure and its resume, are cut short
  so the rest still reads. It's a page of its own: open it in a tab or
  download it.
- **Editing**: YAML with live validation, or a **structured view** (switch
  any time). Each step opens as **details | command | run**: the common
  details (what we're doing, by the end, section, owner, confirm) with an
  **Advanced** section for the rest (any that are set show above it),
  then the command or procedure. **Run this** runs just that step with your
  unsaved edits in a third pane (the earlier `always` steps first if you
  like; nothing after it runs), using the runbook's **test values** (inputs and
  extra variables; secret inputs stay in the browser). Test runs aren't
  recorded. The panes are resizable. The editor opens in the structured view
  (YAML is a click away). Steps sit under their **section headings**, each a
  compact line (name, planned time, ▸ About for its description); drag ⠿ to
  reorder steps and sections or move a step into another section. A step's
  **ways** are tabs. **Comments**: 💬 beside a field shows its YAML comment
  on hover and edits it on click; they're saved as YAML comments on that
  field and move with it.
- **Collections** is a board: a card per collection, editable in place.
  Drag a runbook to another card to move it (Alt/⌥ to add it there too),
  within a card to reorder, or to "Not in a collection" to take it out.
- **Collections** group runbooks in the sidebar, with what they're for.
- **⇪ Import…** in the sidebar imports a project folder on the machine
  runnr runs on (as `runnr import <folder>`: references,
  libraries found elsewhere in the repository, a choice for each name
  that's taken), plus run records or other files added from elsewhere, or
  uploaded files. **⇩ Export…** on a runbook's page writes it with the
  libraries it uses beside it (each can be left out).
- **Manage** lists everything stored (runbooks, libraries, fake libraries,
  test suites, collections) with what uses each, deletes several at once
  (saying first what depends on them) and restores what was deleted.
- Inputs of `type: dir` or `file` get **Browse…**: a browser of the
  folders and files on the machine runnr runs on.
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
overview: |                     # the strategy, from a high level (Markdown): run page, HTML view, export
  Build and push, then roll out region by region. Stop at the first region
  whose error rate rises: the rollback is redeploying the old tag.
sections:                       # what each group of steps does, and how things stand after it
  - name: Deploy
    description: Roll the new version out.
    expect: All pods run the new version.
tags: [deploy]
shell: bash                     # default: sh
profile: [~/.zshrc]             # sourced before every step: your aliases/functions work (slower)
libraries: [db-helpers]         # shared shell functions every step (and shell) can call
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
    identifier: true            # what a run is for: shown with it in the history, which can be searched by it
  - name: token
    secret: true                # masked as **** in output and history
    required: true
  - name: infra_dir
    type: dir                   # a folder (or file) on this machine: the GUI's field gets Browse…
    default: ~/code/infrastructure
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
    section: Deploy             # starts the Deploy section
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
  - name: Post the release notes
    run: ./post-notes.sh
    optional: true              # not required: labelled optional; interactive runs ask first
                                # (run or skip); unattended runs carry on if it fails
  - id: watcher
    name: Status dashboard
    run: ./status.sh --watch
    background: true            # starts and moves on; runs on its own terminal
    stop_after: verify          # stopped (hung up) after that step finishes
  - name: Promote
    run: ./promote.sh {{.cluster}}
    confirm_typing: '{{.cluster}}'   # a point of no return: type the cluster's name to run it
    skip_if: ./promoted.sh {{.cluster}}   # already done? skip it
    ok_if: 'status: promoted'      # exit 0 isn't enough: the output must match (fail_if: the opposite)
    verify: ./promoted.sh {{.cluster}}    # on resume, proves this still stands (else it's done again)
  - name: After the apply
    wait_for: [watcher]          # wait for a background step to finish by itself (fails if it failed)
    run: ./after.sh
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
      - { name: Manual, manual: "Update the secrets in the console, then restart." }
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

**Asking from a step.** A script can ask the operator a question partway
through: `runnr_confirm "Ready to cut over?"` succeeds on yes, and
`answer=$(runnr_ask "Which cluster?")` gets text. Both are defined in every
step. The question goes to whoever is running it: the CLI asks at its
prompt, the GUI (and console) in the step's action pane, a test's operator
answers it (`ask:`), and in an unattended run it fails rather than going on
unanswered. In a `tty` step they read the terminal. (A plain `read` in a
step that isn't `tty` gets no input: it has no keyboard.)

**Stopping a step.** Aborting a run, a step's `timeout`, or stopping the
step (**■ Stop step**, Ctrl-C in the CLI) stops what's running the way you
would: Ctrl-C to the step's processes, SIGTERM 10 seconds later if they're
still there, SIGKILL at 20. Stopping the step leaves the run going: the
step fails and you choose what's next. A tool like Terraform
gets to stop cleanly and release its lock. Detached background steps
(`--tmux`) are left running.

**Background steps** (`background: true`) start and let the run move on.
They run on their own terminal until the `stop_after` step finishes (or the
run ends; `wait_for` on a later step lets one finish instead). When one
ends, its terminal stays open saying how (✔ completed, ✘ exited with a
status, ■ stopped) until you close it, or closes at once with
`close_on_exit: true`. They're visible as a tab in the GUI or with `w` in the CLI, and their
output is recorded with the step. They can't publish outputs.

**Copy on select.** In the GUI, selecting text copies it: in the browser
terminals, code fields and inputs, and run output. **Copy on select** at
the bottom of the sidebar turns it off (selecting text to paste over it
copies it first).

**The run console** (▣ Console on a run, or a runbook's Console tab) puts the
run on one screen, in panes you arrange: drag a pane by its bar onto
another pane's left, right, top or bottom edge to split it there, or onto
its middle to swap them; its bar can also split it (a new empty pane beside
or below) or close it. An empty pane is for whatever isn't on screen:
Step list, Step definition, Step activity, Step edit (else the editor is a
tab in Step activity), Terminal target (where a terminal step or a step's
shell opens; else a tab in Step activity), Background target (several take
background jobs in turn, each to the one with the fewest running) or Shell
(there can be several), or Run log. A shell opens any time: before the run, with the
runbook's environment and the inputs as filled in; while the steps run, at
the step the run is on, alongside it (values it publishes reach the run
when the next step starts, and it's recorded with the step, each line with
its time, so far as it's gone at each step and when the run ends, even if
it's still open); or after the run, with what it published. As the run
goes on to each step, the shell is ended and a new one opened at that
step, unless its 📌 pin is on: a pinned shell stays open, and ↻ starts it
again at the step the run is on now. It stays open into the run's console
and after the run ends, until you exit it, close its pane or leave the
console. When a background job starts and no Background target is on
screen, one is added. A Run
log pane shows every step's output, in order. While a step runs, ■ Stop
step in the toolbar stops it. The layout is kept in your browser; Reset
layout puts it back.

**Running in tmux.** `runnr run --tmux` (or `resume --tmux`) runs in a
tmux session, so the run outlives the terminal: close it, lose the ssh
connection, then `runnr attach` (or detach on purpose with the prefix key and
`d`). Beside the run are `runnr watch` (the steps, live) and a shell with the
run's variables, secrets included, opened by the run itself: what it
publishes reaches the run (`p` at a prompt opens another). Each background step gets a window of its own and runs
outside runnr: if the run fails or is aborted while one is running (a
Terraform apply, say), it's left running, and `runnr resume --tmux` follows it
again. Without `--tmux`, background steps end with the run, and resuming
starts them again. Outside tmux, runnr uses its own tmux server (`tmux -L
runnr`); inside tmux, the current one.

**Requirements.** `requires:` lists the programs a runbook (or a library)
needs: `- terraform`, or `{cmd: terraform, version: "1.5", install: "brew
install terraform", optional: true}` (`check:` is the command that prints its
version, default `CMD --version`). Runs check them first and refuse to start
without the required ones; `runnr doctor RUNBOOK` lists them.

**The std library** ships with runnr: `libraries: [std]` gives
`is_aws_sso`/`require_aws_sso PROFILE [ROLE]`, `has_aws_profile`,
`has_kube_context`/`require_kube_access CONTEXT [NS]`, `git_fresh_branch DIR
BRANCH` (refuses uncommitted or unpushed work, fetches, fast-forwards), and
`tf_init DIR [ARGS]`, `tf_plan DIR PLANFILE`/`tf_apply DIR PLANFILE` (apply exactly that plan),
`tf_summary` and `tf_nochange DIR` (for `verify`). `runnr lib show std`
prints them; a library saved as `std` replaces it.

**Checks** run several commands as one step: each passes if it exits 0,
every one runs on each attempt, and the step passes when all pass. Results
show as a ✅/❌ table. With `repeat`, the group re-runs on its own until it
passes or times out; otherwise a failure offers retry, skip, force or abort.
`retry_checks` says what a retry runs: `all` (default), only the `failed`
ones (passed ones stay passed), or `each` in turn, retrying the first
failing check until it passes before going on to the next.
`parallel_checks: true` runs an attempt's checks all at once (not with
`each`), which helps when checks wait on something; checks that must run in
order belong in one check.

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

**Notification.** `notification_required: true` on the maintenance window
(due `notify_before`, default `2w`, before it opens) puts a "notification
due" date on the schedule.

**Business days.** On the schedule, dates worked out from offsets
(sections, steps, the notification) that land on a weekend move to a
weekday at the same time: back to the Friday before when they come before
the run's start, forward to the Monday after when they follow it. Only the
window, and what happens during it, falls on a weekend.

**Ask Claude (or another model).** With a model set up when runnr starts,
every step gets **✨ Explain** (run, launch and editor pages; the editor asks
about the step as edited, unsaved) and each runbook a **✨ Review**: what the
step does, what it relies on from earlier steps (the setup steps
especially), what to check, its risks, and suggested changes; ask
follow-ups in the panel. The CLI is `runnr explain <runbook> [step] [-q
question] [--fresh]`.

The whole runbook (credentials masked by its sensitive patterns) and its
library functions go along as context, marked for prompt caching: the first
question writes it to the provider's cache, and questions about any step of
that runbook soon after read it back at about a tenth of the price. Answers
are also kept in runnr's database: the same question about an unchanged
runbook, of the same model, is answered from there, free (**Ask again** /
`--fresh` asks anew). Step code and descriptions are sent to the provider.

| Setting | |
|---|---|
| `ANTHROPIC_API_KEY` | Claude through Anthropic (default model `claude-sonnet-5-5`) |
| `RUNNR_AI_PROVIDER=bedrock` | Claude through Amazon Bedrock, signed with `RUNNR_AI_AWS_PROFILE` (or `AWS_PROFILE`; SSO works, via the AWS CLI) in `AWS_REGION`; `RUNNR_AI_MODEL` is the model or inference profile id. Answers arrive whole |
| `RUNNR_AI_PROVIDER=openai` | any OpenAI-compatible API: OpenAI, OpenRouter, a LiteLLM gateway, or local models (`RUNNR_AI_BASE_URL=http://localhost:11434/v1` for Ollama); key from `RUNNR_AI_API_KEY` or `OPENAI_API_KEY` |
| `RUNNR_AI_PROVIDER=bifrost` | a [Bifrost](https://github.com/maximhq/bifrost) AI gateway (OpenAI-compatible): `RUNNR_AI_BASE_URL` (its `/v1` URL; required), the key from `RUNNR_AI_API_KEY` or a command that prints it (`RUNNR_AI_API_KEY_CMD`, e.g. for a short-lived SSO token; it's fetched again when it expires), `RUNNR_AI_VIRTUAL_KEY` (sent as `x-bf-vk`). Models are `<provider>/<model>` (default `openai/gpt-5.4`; a bare `gpt-…` or `claude-…` name gets its provider added) |
| `RUNNR_AI_API_KEY_CMD`, `RUNNR_AI_HEADERS`, `RUNNR_AI_CACHE_HINTS=1` | for other gateways: a command that prints the key, extra headers (`Name: value; Name: value`), and Anthropic cache marks on the runbook context |
| `RUNNR_AI_MODEL` | the model (`RUNNR_CLAUDE_MODEL` also works) |
| `RUNNR_AI_CACHE_TTL=1h` | Anthropic: keep the runbook in the prompt cache an hour instead of five minutes (writes cost about 2× instead of 1.25×) |

**Export.** `runnr export` (or **Export as Markdown**) writes a runbook as
a document to follow by hand, like a wiki runbook: a collapsed overview,
a setup block (inputs, environment, helper functions), then per section
each step with a bullet of what it's doing and its code in collapsible
blocks; other ways are collapsed under it. Paste the blocks in order into
one shell: placeholders become shell variables, steps that exit, `cd` or
set shell options run in a subshell, values steps publish still land in
your shell, and multi-line blocks are grouped so an interactive command
can't swallow the lines after it. The setup block turns on zsh's
`interactive_comments` and turns off `!` history expansion, which
otherwise break pasted code. Templates with logic (`{{if eq .env …}}`)
are worked out with the inputs given (`-v`), so export again for other
values.

**HTML view.** `runnr export <runbook> -o page.html` (or **View as HTML**
on the run page) writes one self-contained page to read or share:
the overview and the sections at a glance, collapsible inputs, sections
and steps that expand and collapse, and ways as tabs (the preferred one
first). Each step's code is in one box with a Copy button, either **as
written** (with its templates; checks one after another) or **as runnr
runs it**: a script to paste into one shell, in which the step runs in a
shell of its own as in a run (templates filled in, its directory and
environment, the trap that publishes its outputs), and what it publishes
comes back as environment variables for the steps after it. The setup it
needs (inputs, runnr's variables, the runbook's environment, profile
files, functions and the setup steps) goes either **once, at the top**,
or **in each step**, so any step can be pasted on its own. Values only a
run can know show as `<name>`, secret inputs as `****` (the setup asks for
them). To follow it by hand, **✓ Mark done** at the end of a step greys it
out, collapses it, notes the time and opens the next one (optional steps
can be skipped); a done step can still be opened, or marked not done. The
page keeps this only while it's open.

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
# runnr lib import db-helpers.yaml
name: db-helpers
description: Helpers for database migrations
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
Built in: URL passwords (only the password in `scheme://u:****@`), `Bearer` tokens, AWS
secret keys and session tokens, credential settings (`password=…`,
`PGPASSWORD=…`, `"api_key": "…"`, `client_secret: …`: a name with
password, passphrase, secret, token, api key, access key, private key or
credential in it, then `=` or `:`), credential flags (`--password …`), and
the IDs in Terraform's status lines for `random_password` resources
(`Importing... [id=****]`: an imported password's ID is the password).
A value published (`outputs`, `$RUNNR_OUTPUT` or `capture`) under such a
name (`db_password`, `api_token`, `dbpass`) is kept secret as if it were
published with `secret_outputs`. Everything that leaves runnr (exports,
raw logs, timelines, `history --json`) and every imported run is scanned
again with these patterns and the runbook's own, and the values of inputs
and variables with such names are hidden wherever they appear, so a run
recorded before runnr knew to hide something doesn't pass it on. To mask
what's already stored, `runnr history --scrub [run-id]` (or **Scrub
credentials…** on the history page; `--check` to look first) rewrites the
runs and compacts the database. Add your own with `sensitive`: name →
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
internal/project/   importing a project folder; exporting runbooks and run logs
internal/runtest/   runnr test: fakes, scenarios, the simulated operator
examples/           sample runbooks
.github/            tests on every pull request; a release on every merge to main
```

The engine never talks to a human directly; the CLI, GUI and unattended modes
are each an `engine.Interactor`.

## Releasing

Every merge to `main` is a release, made from CHANGELOG.md:

- A pull request starts CHANGELOG.md with its version and date, e.g.
  `## v1.2.3 — 2026-10-22`, and what's in it. Patch, minor or major is the
  version you write; it must be above the last release.
- Before it can merge, GitHub Actions checks the changelog (`changelog`)
  and runs the tests (`test`): `main` accepts only pull requests that pass
  both.
- On merge, the workflow tags the version, builds runnr for macOS and
  Linux (amd64, arm64) and publishes the release with the section as its
  notes.
