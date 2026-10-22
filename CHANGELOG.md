# Changelog

## v0.3.3 — 2026-10-22

### Changed

- The `bifrost` AI provider is a generic Bifrost gateway: it needs
  `RUNNR_AI_BASE_URL` (no built-in gateway addresses) and takes its key from
  `RUNNR_AI_API_KEY` or `RUNNR_AI_API_KEY_CMD` (no built-in key command).
  `RUNNR_AI_BIFROST_ENV` is gone. If you used it, set `RUNNR_AI_BASE_URL`
  and `RUNNR_AI_API_KEY_CMD` yourself.

## v0.3.2 — 2026-10-22

Documentation brought up to date, with new screenshots.

### Changed

- README, the CLI and GUI tours, the API reference, the testing guide, the
  examples and the in-app help cover everything up to now: the run console,
  shells and background commands, go to / stop a step, changing inputs,
  import and export, Manage, path inputs, copy on select, and releases.
- New screenshots of a run in tmux (the layout, a `p` pane shell, a `b`
  job), the run console, import, export and Manage; every other screenshot
  retaken.
- The in-app help's examples are the made-up orders demo throughout.
- The CLI's key for changing a step for this run is `m` (modify): `c`
  already means continue at some prompts.

### Fixed

- `runnr fakes list` says "fake libraries"; `runnr export`'s help mentions
  YAML.

## v0.3.1 — 2026-10-22

More ways to work beside a run in the CLI.

### Added

- **`b` at a step prompt**: run a command in the background with the run's
  environment: a window of its own under `--tmux`, else watched with `w`.
  Recorded with the step; it ends with the run unless it ends first.
- **`p` at a step prompt (under `--tmux`)**: a shell with the run's
  variables in a pane beside the run, without holding up the prompt. What
  it publishes reaches the run at the next step; the session is recorded
  with the step.

### Fixed

- `runnr shell <run-id>` has the values the run's steps published (as last
  saved), not only its inputs and setup steps.
- The shell pane of a `--tmux` run is opened by the run itself, so it has
  every value, secrets included; what it publishes reaches the run.
- Shells opened during a run (that pane, `p`, `!`, the console's) see what
  later steps publish, secrets too: zsh reads the run's values again before
  each prompt and command, bash before each prompt. They're kept in a file
  only you can read, removed when the run ends.

## v0.3.0 — 2026-10-22

Move about a run, and stop a step without stopping the run.

### Added

- **Go to a step** while the run waits for you (before a step, after it
  failed, or pausing after it): back to any step that ran (it and the steps
  after it run again), this step again, or ahead (the steps between are
  skipped). The step waits for you before running. ↺ Back to step N and Go
  to step under every question in the GUI; `g` at the CLI's prompts.
  Going back past a point of no return asks first; a background job still
  running in the way has to be ended first.
- **Stop a running step**: ■ Stop step in the GUI (the run page and the
  console), or Ctrl-C in the CLI (Ctrl-C again to abort the run). Its
  command is interrupted as abort would; the step fails, "stopped by …",
  and the run asks what to do: retry, skip, or go to a step.

## v0.2.3 — 2026-10-22

### Fixed

- The release workflow tags as github-actions (an annotated tag needs a
  name): v0.2.2's release failed at tagging, and was published by hand.

## v0.2.2 — 2026-10-22

Every merge to main is now a release.

### Added

- **Releases from the changelog.** A pull request starts CHANGELOG.md with
  its version and date; GitHub Actions checks that and runs the tests, and
  main accepts only pull requests that pass both. On merge the version is
  tagged and released with its notes, with runnr built for macOS and Linux
  (amd64, arm64).

### Fixed

- The shell test no longer stops at Ubuntu's compinit prompt.

## v0.2.1 — 2026-10-22

### Added

- **Export a runbook** to a file (⇩ Export… on its page, or
  `runnr export <runbook> -o file.yaml`), as written, with the libraries it
  uses beside it: each listed, and each can be left out.
- **Export logs** of a run and the runs linked to it by resuming (⇩ Export
  logs…, or `runnr history <id> --export <folder>`): raw logs to read,
  records (JSON) to import, or both; in one file or a file per run; to a
  folder, or a download.
- **Import runs**: run records (JSON) import with the rest (`runnr import`,
  or the import dialog, which can also add files and folders from
  elsewhere).
- **Inputs that are paths**: `type: dir` or `type: file`; their fields in
  the GUI get a Browse… button that browses this machine.

## v0.2.0 — 2026-10-22

Shells you can open any time, importing a whole project folder, changing a
run's inputs while it runs, and a page to delete and restore what's stored.

### Added

- **Shells, any time.** A console Shell pane opens a shell before the run
  (the runbook's environment, with the inputs as filled in), alongside it
  (at the step it's on; what it publishes reaches the run at the next
  step, and it's recorded with the step), or after it (the run's values).
  It stays open from the console before the run into the run's console and
  after the run ends, until you exit it. **↻ Reopen** starts it again at the
  step the run is on now.
- **Import a project folder.** **⇪ Import…** in the sidebar, and
  `runnr import <folder>`, take a folder's runbooks, libraries, fake
  libraries and test suites at once:
  - placeholders are filled in: `@PROJECT_DIR@` (the folder),
    `@REPO_ROOT@` (its git root), and any other `@NAME@` from values you
    give (`--set NAME=VALUE`); a file using one without a value isn't
    imported, and says so;
  - references are checked (a runbook's libraries, a suite's runbook and
    fakes); a missing library defined elsewhere in the repository comes
    along;
  - renaming a file on the way in (`--rename NAME=NEW`) changes the
    references to it in the other files, leaving the rest as written;
  - a name that's taken is never saved over without a choice: save it as
    the next version (`-f`; earlier versions are kept), rename it, or skip
    it. `-n` says what would happen.
- **Import files** from the sidebar too (Upload files instead…), with the
  same choices for a taken name and fields for placeholders.
- **Change a run's inputs while it runs.** **✎ Change inputs** on a live
  run (its page, or the console's Inputs), `[i]nputs` at the CLI's step
  prompts, or `PUT /api/runs/{id}/inputs`. Values are checked as at the
  start and take effect before the next command; a step waiting to run its
  command starts again with them. Each change is recorded (who, when, old
  → new), a resume uses the new values, and steps that already ran with
  the old value are pointed out.
- **Manage** in the sidebar: everything stored (runbooks, libraries, fake
  libraries, test suites, collections) with what uses each. Delete several
  at once, with what depends on them spelled out first, and restore what
  was deleted (its versions are kept). `runnr restore [kind name]` in the
  CLI.
- **Delete a run**: Delete run on a finished run's page, or
  `runnr history --delete <run-id>`.
- **Copy on select** in the GUI: selecting text in a terminal, a code field
  or run output copies it. **Copy on select** in the sidebar turns it off.

### Changed

- Deleting a runbook takes it out of its collections and removes its
  scheduled runs.
- `runnr import` takes folders and any runnr file (not only runbooks);
  with `-f` it says whether each file was added, unchanged or replaced
  (version N → M). `runnr import -` and `runnr lib import` refuse
  placeholders without a value.
- Shells alongside a run are no longer hung up when the run ends.

## v0.1.0 — 2026-10-21

The first release: runnr as a runbook manager with a CLI, a web GUI and an
HTTP API, storing runbooks and runs in SQLite or Postgres.

### Runbooks

- Steps that run commands, manual steps, terminal (tty) steps, background
  steps (they can outlive runnr under tmux), checks with repeat, ways to do
  a step (choose one, or try another when one fails), optional steps,
  dangerous steps (a typed double check out of sequence), points of no
  return, sections, and a maintenance window with planned times.
- Inputs (required, choices, secrets), shared outputs and secret outputs,
  `env`, a profile sourced before every step, `requires` (programs a
  runbook needs, checked by `runnr doctor`), and function libraries shared
  by steps and shells (with runnr's own `std`).
- Steps can ask the operator (`runnr_ask`, `runnr_confirm`).
- Collections group runbooks; versions are kept for every save.

### Running

- Interactive and unattended runs, review before starting, step mode,
  pause before or after steps, skip, retry, force a failed step with a
  reason, notes on steps and runs, and a shell at any step with the run's
  variables and the step's commands beside it.
- Resume a failed or aborted run where it stopped, rerun, and compare with
  the run before ("last time").
- Change steps during a run, rerun them, and save the changes to the
  runbook.
- Abort a run from anywhere (`runnr abort`, the GUI), including one whose
  process is gone.
- `runnr run --tmux`: the run outlives the terminal; `runnr attach` and
  `runnr watch`.
- Schedule runs ahead, with a calendar and an agenda; maintenance window
  notifications.

### GUI

- A run page (with History and Details for each step) and a run console:
  the steps, the selected step's definition, what it's doing, terminals and
  background jobs on one screen, in panes you arrange (drag to split or
  swap; Step list, Step definition, Step activity, Step edit, Terminal
  target, Background targets, Shells, Run log).
- Set up a run in the console before it starts; edit a step in place.
- A structured runbook editor (sections, ways, comments, a function
  picker, checks for zsh-only syntax in runbooks that don't run zsh), and
  a YAML view.
- Terminals in the browser for terminal steps, shells and background jobs.

### Testing, docs and help

- `runnr test`: run a runbook with its commands faked and a simulated
  operator, with fake libraries and test suites, optionally sandboxed;
  Tests in the GUI.
- Export a runbook as Markdown to follow by hand, or view it as an HTML
  page (copy each step in one click, mark steps done).
- Raw logs of past runs (`runnr history --raw`, Raw log in the GUI).
- Explain: ask an AI model about a runbook's steps, through Anthropic,
  other providers or an AI gateway; answers are kept locally.
