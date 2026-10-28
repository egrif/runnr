# Changelog

## v0.14.0 — 2026-10-28

Runs for the same thing: starting one asks about an unfinished one, and a
new run isn't compared with earlier ones unless you ask.

### Added

- **Ask before starting over.** If the latest run for the same identifier
  inputs (the same target) from the past week is still running or stopped
  partway, starting another asks first: **Resume it…** (or open it, if it's
  running), **Start a new run**, or cancel. At the CLI: `[r]esume it /
  [n]ew run anyway / [q]uit` once the inputs are in; unattended, a note.
  API: `POST /api/runbooks/{name}/unfinished`.
- `--compare <run-id|last>` on `runnr run` (and `rerun`, `resume`): show
  what each step did in that run, in the review and at each step (`l`).

### Changed

- **No "last time" unless asked.** A new run is no longer compared with the
  runbook's latest run: no ✓/✗ marks beside its steps, no "last time" line
  at each step, and a step opens on its Details rather than its History.
  Rerun and resume still compare with that run; a step's History tab shows
  any earlier run. The API's `previous` defaults to none for a new run.

## v0.13.0 — 2026-10-28

A calmer web interface: the navigation is across the top, each page
shows its main action with the rest in a menu, and a Settings page sets
placeholders' folders for everything, a collection or a runbook.

### Changed

- **Navigation across the top.** Runbooks, History, Schedule, Collections
  and Libraries are in a top bar, with **+ New runbook** and **⇪ Import…**;
  **Settings**, **Manage runnr** and **Runbook format help** are under
  **⚙**. The left column is just the runbooks.
- **One main action per page; the rest under ⋯.** The run page shows
  **Resume…** (or **Run again…**) and **▣ Console**; Run again, Timeline,
  Raw log, Export logs and Delete are under ⋯. On the runbook page,
  Schedule, View as HTML and Export as Markdown are under ⋯ beside **Run**,
  and Export to files under ⋯ by the title. The console's header is one
  row, with Reset layout under ⋯.
- **Green means the main action or where you are.** Links are the text's
  colour, and selected tabs and toggles are grey, not green.
- **Status once.** A run's status is its badge; the reason it stopped is a
  line under the title.
- **Less on screen until it's wanted.** Step through, Auto-approve, Only I
  drive it and Dry run are under **More options**; a step's ⏸ before /
  after show on hover (or when set); page explanations are in an ⓘ; the
  console's pane controls show on hover, and its Background pane is hidden
  for a runbook without background steps; collection descriptions read as
  text until clicked.
- Long durations read as days and hours (`2d 6h`, not `3268m53s`).

### Added

- **Settings page** (under ⚙). Sets the folders placeholders stand for on
  this machine: `@REPOS_ROOT@` and others for everything, and any
  placeholder for a collection or a single runbook. Pick a collection to
  edit it and its runbooks' own folders; each runbook shows where its values
  come from and whether it can start.
  It also holds this browser's choices: your name, whether runs open in the
  run page or the console, and Copy on select. The Collections page no
  longer sets folders. `GET /api/folders` also gives each runbook's
  resolved folders (`runbooks`).
- **About runnr** (under ⚙): the version, commit and Go it was built with,
  where the data lives, how long the server has been up and whether Claude
  is on, with links to the repository and this version's release notes, and
  **Copy details** for a bug report. `GET /api/about` gives the same.

## v0.12.0 — 2026-10-27

Folders given for a run are kept for the runbook.

### Changed

- **A run's folders stay with its runbook.** `--set NAME=FOLDER` (or the
  Run page's Folders) now also sets that folder for the runbook, so its
  next runs have it too, ahead of its collection's; where its collections
  disagree, it settles which. `runnr folders set|unset -r <runbook>`, and
  **✕ forget** on the Run page, change them; a renamed runbook keeps them.
  Tests, shells and the editor's Run this don't keep theirs.

### Fixed

- `runnr folders` crashed when nothing was set for everything.

## v0.11.0 — 2026-10-27

Placeholders are kept as written, and each machine says what folders they
stand for.

### Changed

- **Files keep their placeholders.** `@PROJECT_DIR@`, `@REPO_ROOT@` and
  any other `@NAME@` are imported, stored and exported as written, so a
  runbook is the same (and the same version) on every machine. Importing
  no longer fills them in: `runnr import --set` and the importer's
  placeholder values are gone. A runbook stored by an older runnr keeps
  the paths it was filled in with.
- **A run fills them in with this machine's folders** when it starts:
  `@REPOS_ROOT@` set for everything, and `@REPO_ROOT@`, `@PROJECT_DIR@`
  (or any others) set for a collection, from the runbook's collection (in
  several, where they agree) or `--collection`'s; `--set NAME=FOLDER`
  gives one for that run only, and a resume keeps it. A value can name
  another (`@REPOS_ROOT@/ops-runbooks`). **A run doesn't start**
  while one it names has no value or isn't a folder here; nor do tests,
  shells or the editor's Run this.

### Added

- `runnr folders` lists the folders set here; `runnr folders <runbook>`
  says what a run of it would have; `runnr folders set [-c collection]
  NAME=FOLDER…` and `unset` change them. `runnr run`, `resume`, `shell`
  and `test` take `--collection` and `--set`.
- The GUI: **Collections** has the folders for everything and on each
  collection's card; the Run page has **Folders**, to see them, choose the
  collection and give one for the run; previews show the folders filled in.
- A run's record keeps the folders it had (`folders`, `collection`,
  `folder_set`); exported, its logs and records have those folders written
  as their placeholders again (`@REPO_ROOT@/tools`), so local paths aren't
  passed on. API: `GET`/`PUT /api/folders`, `GET
  /api/runbooks/{name}/folders`, and `collection`/`folders` in start,
  resume, preview and test requests.

## v0.10.0 — 2026-10-26

Versions are content hashes, the same in every runnr.

### Changed

- **A version is the SHA-256 of the text** (runbooks, libraries, fake
  libraries, test suites, collections), shown as its first and last four
  characters, `3f2a…9c1d`. Numbers counted per database meant nothing in
  another runnr; a hash names the same text everywhere, so versions
  compare across installations. `--version`, the API's `{version}`,
  `If-Match` and `adopt-version` take the hash, a prefix of it, or the
  short form.
- **Saving text a document had before makes that version current again**
  instead of adding a copy; restoring a deleted document does the same.
  `runnr versions` lists them by when each was first saved and marks the
  current one.
- The raw log's first line names the whole hash (`run <id> · <runbook>
  @<hash> · <mode>`); logs with the old `v<N>` still import (their version
  isn't known).

### Added

- **Runs record the versions of the libraries they used** (`libraries`),
  as well as the runbook's.
- **An exported run carries the runbook and library text it ran**
  (`definitions`); importing it into another runnr keeps them as versions
  there (and adds the runbook if that runnr hasn't one by its name).

### Upgrading

- The first time this runnr opens an older database it converts it: each
  kind's tables are keyed by hash, and runs' version numbers become the
  hashes of what they ran. A run whose number has no saved version (one
  imported from another runnr) no longer names a version. A SQLite file is
  copied aside first, as `runnr.db.before-hashes-<time>`; take a backup of
  a shared Postgres database yourself. Older runnr builds can't read the
  converted database.

## v0.9.2 — 2026-10-26

Terraform's imported passwords are masked.

### Fixed

- **Terraform password IDs are masked**: a built-in sensitive pattern hides
  the ID in Terraform's status lines for `random_password` resources
  (`…random_password.x: Importing... [id=****]`). Importing one uses the
  password as its ID, so applies that import passwords printed them in
  output, records and logs. Runs stored before this: `runnr history
  --scrub` (exports and raw logs are masked again as they leave runnr).

## v0.9.1 — 2026-10-26

`runnr version`, and every build knows its version.

### Added

- **`runnr version`** prints the version, the commit it was built from
  (and whether that checkout had changes), and the Go and platform it was
  built for.

### Fixed

- A runnr built with `go install ./cmd/runnr` or `go build` said `dev`:
  only the release build and `make` stamped the version. It now uses the
  version Go records from the checkout's tags (`v0.9.0`, or a development
  version between releases), or the module's version for `go install
  github.com/egrif/runnr/cmd/runnr@v0.9.0`.

## v0.9.0 — 2026-10-26

A step's checks can run all at once.

### Added

- **`parallel_checks: true`** runs a step's checks all at once rather
  than one after another (results still show in order; `retry_checks:
  failed` and `all` work as before, `each` is refused). Questions from
  checks (`runnr_ask`) come one at a time.

## v0.8.0 — 2026-10-26

Importing runs from another runnr (records or raw logs); keeping credentials out of the history and everything exported from it.

### Added

- **Import runs**: runs exported from another runnr come in from the
  history page (**⇪ Import runs…**: files, a folder, the zip an export
  downloads, or files from the browser's computer) or `runnr history
  --import` (`--check` to look first): records (`.json`) or **raw logs**
  (`.log`). A run from a raw log is rebuilt from its text: close, not
  exact (times to the second; no sections, state, jobs or step edits).
  A run already here is left alone; one exported while still going comes
  in aborted. Imported runs show ⇪ in the history and on their page. API:
  `POST /api/runs/import`.
- **The file picker sorts** by name or date (click again to reverse; the
  choice is remembered) and shows each file's date.
- **Scrub the history**: `runnr history --scrub [run-id]` (or **Scrub
  credentials…** on the history page) masks credentials in the stored runs,
  all of them or one: their output, commands, notes and reasons, as exports
  are masked. `--check` shows what it would change. Runs still going are
  left alone, and the database is compacted afterwards so the old text
  isn't left in it. API: `POST /api/runs/scrub`.

### Changed

- **More credentials are masked** before anything is recorded: settings
  like `password=…`, `PGPASSWORD=…`, `"api_key": "…"` and `client_secret:
  …`, flags like `--password …`, and values published or captured under a
  credential name (`db_password`, `api_token`, `dbpass`), which are now
  kept secret. Before, these were saved in plain text in run records and
  logs unless declared secret.
- **Exports are scanned for credentials** again as they leave runnr: export
  files and zips (raw logs, records, timelines), Raw log, the timeline
  page, `runnr history --raw/--json/--timeline`, and imported runs before
  they're kept. They're masked with the built-in patterns and the
  runbook's own, and the values of inputs and variables named as
  credentials are hidden wherever they appear. So a run recorded before
  runnr knew to hide something doesn't pass it on.
- The SQLite database (it holds every run's commands and output) is now
  readable by its owner only (it was readable by everyone on the machine),
  and a new runnr home folder is too.

## v0.7.0 — 2026-10-23

Watching a run together; seeing what an import would change; showing a secret as you type it.

### Added

- **Presence**: a live run's page and console show who has it open (the
  name each tab gives, as for notes), updated as people come and go.
- **Read-only, with override**: who may drive a live run is set from its
  header (🔒) or when it starts (**Only I drive it**): everyone, only some
  people, or everyone except some. The others see it read-only: anything
  that changes the run asks first and can be overridden, which everyone
  watching sees and the run notes. A safety catch, not a lock (names
  aren't checked). API: `PUT /api/runs/{id}/access`, `X-Runnr-User`,
  `X-Runnr-Session`, `X-Runnr-Override`; `403 {code: "read_only"}`.

- **Import shows what it would change**: a runbook (or library) whose
  name is taken shows a diff of the stored version against the file
  (**Show what it changes +N −M** in the GUI's import; `runnr import
  --diff` prints them and imports nothing; `diff` in the API's scan and
  check results).
- Secret fields in the GUI (run forms, the console, the editor's test
  values, questions, changing inputs) have an 👁 to show what's typed and
  hide it again.

### Changed

- **Run this** (in the editor) asks for all the runbook's inputs in one
  dialog first, filled with the values used last (or the test values), and
  can save them as the runbook's test values, or not ask again (Test
  values turns asking back on). A required input with no
  default no longer fails with "missing required input".
- A run's terminals take the window size of whoever typed in them last;
  other viewers' sizes apply once they type.

## v0.6.0 — 2026-10-23

Suspend a run, to resume it later; identifier inputs in the history.

### Added

- **Suspend**: stop a run on purpose and carry it on later. ‖ Suspend… on
  a live run's page or console, `z` at a CLI prompt, `runnr suspend
  <run-id> [-m why] [--now]` from anywhere, or `POST
  /api/runs/{id}/suspend`. It stops once the step running now finishes
  (a command isn't cut short; **Carry on instead** takes the request
  back), or at once if it's waiting for someone; `now` stops the running
  step too, to do again. The run ends `suspended` (CLI exit status 75)
  with who and why, and Resume carries it on in the same run ("resumed …
  (it was suspended by …)"). Optional background watchers stopped by the
  suspension start again on resume. Aborted and failed runs resume as
  before.
- **Identifier inputs**: mark the inputs that say what a run is for (a
  target: a silo, a cluster) `identifier: true` (an Identifier column in
  the editor). Their values show with each run in the history (a For
  column; `runnr history`'s FOR) and at the top of the run and its
  console, and the history can be searched by them: a search box in the
  GUI, `runnr history -f <text>`, `GET /api/runs?q=`. Runs from before the
  inputs were marked show them too.
- The GUI's run history has a runbook picker.

## v0.5.0 — 2026-10-22

A run's timeline.

### Added

- **◷ Timeline**: a run laid out in time, on its page and in its console
  (a page of its own: open it in a tab or download it), and in a terminal
  with `runnr history <id> --timeline` (`--timeline=html` for the page).
  Each step's tries as bars, earlier tries (before a resume) faded; the
  shells (from their timed lines) and jobs alongside; background steps;
  the maintenance window, with a closer look at it; the time the run was
  stopped before a resume; notes, input changes, edits and resumes; then
  the key moments. Long idle stretches are cut short so the rest reads.
- Export logs (GUI, `--as`, the API's `format`) can add a timeline page per
  run: `timeline`, alone or with the others (`raw,timeline`).

### Fixed

- A run started unattended from the GUI or API can be driven from the
  console like an interactive one: shells open alongside it, its
  background steps get terminals (and the Background pane), and Stop step,
  go to step and changing inputs work.

## v0.4.0 — 2026-10-22

A resume carries the same run on, and console shells follow the run.

### Changed

- **Resuming continues the run**: the same run ID, one record in the
  history and one log (`runnr history <id> --raw`). Steps that finished
  stay as they went; a step done again keeps its earlier tries ("before the
  run was resumed"); the record lists each resume (when, from which step,
  how the run had ended). The run page shows them, and the console's Run
  log has the earlier tries.
- **A changed runbook doesn't start a new log**: resuming after the runbook
  was edited carries the run on with the new version. Steps are matched by
  name (a new one is still to do; notes follow their steps), the record
  notes the version it was resumed with, and steps the new version no
  longer has stay in the record (`removed`), their notes moved to the run.
  The resume screens say the runbook changed.
- `runnr resume --new-run` (API: `"new_run": true`) starts a new run that
  points back at the old one, as before (not from a changed runbook).
- Under `--tmux`, a resumed run's session is `runnr-<runbook>-r2-<id>` (the
  earlier session may still hold background steps the resume follows).

- **Console shells follow the run**: as the run goes on to each step, a
  Shell pane's shell is ended and opened again at the new step, unless
  it's pinned (📌, off by default); ↻ reopens it at the current step.
- **Shell transcripts are kept while open**: a shell alongside the run is
  recorded with its step so far at each step and when the run ends, not
  only when it's closed. A shell nothing was entered in isn't recorded.
- **Timed shell lines**: each line of an interactive shell's transcript
  carries the time it was entered or printed (`[15:04:05] …`).
- When a background job starts and the console has no Background target
  on screen, one is added.

### Fixed

- A note on a step carried over by a resume showed twice.

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
