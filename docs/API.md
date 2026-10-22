# runnr HTTP API

`runnr serve` exposes a JSON API (the GUI uses the same one).

```sh
runnr serve                                  # GUI + API on 127.0.0.1:7777, no auth
RUNNR_TOKEN=$(openssl rand -hex 32) runnr serve --addr 0.0.0.0:7777 --no-ui
```

## Auth

- **No token** (default): loopback only. Mutating requests must carry `X-Runnr: 1`
  and be same-origin, so other web pages can't drive your local runnr.
- **With `--token`/`RUNNR_TOKEN`**: every `/api` request needs
  `Authorization: Bearer <token>`. Required to listen on anything but loopback.
  The GUI asks for the token once (`POST /api/login {token}` → `204`) and
  keeps it in an HttpOnly cookie.

runnr runs shell commands. Anyone holding the token can run any runbook and
create new ones, so treat the token like shell access. Use TLS (e.g. a reverse
proxy) off-host.

## Runbooks

| Method | Path | Body | Returns |
|---|---|---|---|
| GET | `/api/runbooks` | | `[{name, version, updated_at, description, tags, steps, error?}]` |
| GET | `/api/runbooks/{name}` | | `{raw, version, updated_at, runbook?, error?}` (+ `ETag: <version>`) |
| POST | `/api/runbooks` | YAML | `201 {name, version, runbook}` (`400` if invalid or exists) |
| PUT | `/api/runbooks/{name}` | YAML | `{name, version, runbook}`; renames if `name` changed |
| DELETE | `/api/runbooks/{name}` | | `204`: version history is kept; it's taken out of its collections and its scheduled runs are removed |
| GET | `/api/runbooks/{name}/versions` | | `[{version, created_at, bytes}]`, newest first |
| GET | `/api/runbooks/{name}/versions/{n}` | | `{version, raw}` |
| POST | `/api/validate` | YAML | `{ok, error?}` |
| POST | `/api/parse` | YAML | `{ok, runbook?, comments?, error?}`: the structured form, and the YAML's comments by field path (`"steps.3.run"`, `"steps.3"` for the item, `""` for the file) |
| POST | `/api/format` | runbook JSON, or `{runbook, comments}` | `{ok, raw?, error?}`: YAML (validated), with the comments put back on their fields |
| POST | `/api/preview` | `{runbook \| raw, vars, from_run?}` | `{steps: [StepView]}`: what each step would do with these inputs (and that run's state) |
| GET | `/api/runbooks/{name}/steps/{n}` | | `{yaml, version}`: step `n` (1-based) of the saved runbook |
| PUT | `/api/runbooks/{name}/steps/{n}` | step YAML (+ `If-Match`) | `{version, runbook}`: replaces that step, the rest of the YAML as it was, as a new version |
| POST | `/api/parse-step` | step YAML | `{ok, step?, error?}` |
| POST | `/api/format-step` | step JSON | `{ok, raw?, error?}` |
| GET | `/api/template?name=` | | `{raw}`: a starter runbook |

Send `If-Match: <version>` with `PUT` to get `409 Conflict` instead of
overwriting a newer save.

## Import, export, manage

Paths are on the machine runnr runs on.

| Method | Path | Body | Returns |
|---|---|---|---|
| POST | `/api/import/check?set=NAME=VALUE` | a runbook or library's YAML | `{kind, name, exists, version?, updated_at?, same?, builtin?, placeholders?, unset?, error?}` |
| POST | `/api/import?as=&replace=1&collection=&set=NAME=VALUE` | YAML | `201 {kind, name, version, replaced?, collection?}`; `409 {error, check}` if the name is taken (unless `as` or `replace`); `400 {error, unset}` for placeholders without a value |
| POST | `/api/import/scan` | `{dir, values?, renames?, include?, skip?}` | the plan for a project folder: `{dir, repo_root, docs, values, unset, ignored}` |
| POST | `/api/import/project` | as scan, `+ {replace?, replace_all?, collection?}` | `{plan, results: [{rel, kind, name, status, version?, replaced?, error?}]}` |
| GET | `/api/fs?path=&hidden=1` | | `{path, parent, home, entries: [{name, dir, size?}], asked, exists, is_dir}`: the folder at `path` (or holding it, or the nearest that exists) |
| POST | `/api/runbooks/{name}/files/plan` | `{path}` | `{files: [{kind, name, path, version?, exists?, builtin?, missing?}]}`: the runbook and the libraries it uses (beside it as `<name>.yaml`) |
| POST | `/api/runbooks/{name}/files` | `{path, libraries, overwrite?}` | `{written: [...]}`; files already there are refused without `overwrite` |
| GET | `/api/manage` | | `{items: [{kind, name, version, updated_at, used_by, schedules?, builtin?, error?}], deleted: [{kind, name, version, saved_at}]}` |
| POST | `/api/manage/delete` | `{items: [{kind, name}]}` | `{results: [{kind, name, ok, error?, also?}]}` (`also`: collections and scheduled runs a runbook was taken out of) |
| POST | `/api/manage/restore` | `{items: [{kind, name}]}` | `{results: [{kind, name, ok, version?, error?}]}`: saves the last version again |

A project folder's `docs` are `{path, rel, kind, name, original, error?,
placeholders?, refs?, included?, skipped?, exists, version?, updated_at?,
same?, builtin?}`, `kind` being `runbook`, `library`, `fakes`, `suite` or
`run` (a run record, JSON). `values` fill in placeholders (`@NAME@`;
`PROJECT_DIR` and `REPO_ROOT` default to the folder and its git root),
`renames` are `{"kind:name": "new"}` (references to it follow), `include`
adds files or folders from elsewhere, `skip` leaves files out (by path), and
`replace` names files to save as the next version of the one with their
name. A result's `status` is `imported`, `replaced`, `unchanged`, `skipped`,
`exists` (taken, not replaced) or `failed`. Kinds for manage are `runbook`,
`library`, `fakes`, `suite` and `collection`.

## Collections

`GET /api/collections`, `POST /api/collections` (YAML), `GET / PUT / DELETE /api/collections/{name}`
(PUT takes `If-Match`), `POST /api/collections/validate`. A collection is
`{name, description, expect, runbooks: [...]}`.

Membership (these change only the `runbooks` list, keeping the rest of the YAML):

| Method | Path | Body | Does |
|---|---|---|---|
| POST | `/api/runbooks?collection=NAME` | runbook YAML | create a runbook and add it to that collection (created if needed) |
| POST | `/api/collections/{name}/runbooks` | `{runbooks: [...], create?}` | append runbooks (which must exist); `create` makes the collection |
| DELETE | `/api/collections/{name}/runbooks/{runbook}` | | take a runbook out (it stays in runnr) |
| POST | `/api/runbooks/{name}/move` | `{to, from?}` | move into `to` (created if needed), out of `from`, or out of the one collection it's in |
| PUT | `/api/collections/{name}/runbooks` | `{runbooks: [...]}` | replace the list (e.g. to reorder); runbooks new to it must exist |
| PATCH | `/api/collections/{name}` | `{name?, description?, expect?}` | change those fields in place (empty removes description/expect) |

## Claude

| Method | Path | Body | Returns |
|---|---|---|---|
| GET | `/api/claude` | | `{enabled, provider?, model?}`: whether a model is set up |
| POST | `/api/explain` | `{runbook \| raw, step, question?, history?, fresh?}` | server-sent events: `{"text"}` pieces of the answer, then `{"usage", "provider", "model"}` or `{"error"}` |

`step` is 1-based (0: the whole runbook). `raw` is runbook YAML being edited
(it needn't be saved). `history` is the conversation so far
(`[{role, content}]`) for follow-ups. The runbook (masked) and its library
functions are sent as a cached system prompt; `usage` says how many input
tokens came from the cache (`cache_read_input_tokens`) or were written to it;
`usage.local` means the answer was stored by runnr from an identical earlier
question (send `fresh: true` to ask anew).

## Schedules

Runs planned ahead of time. Nothing starts them yet.

| Method | Path | Body | Returns |
|---|---|---|---|
| GET | `/api/runbooks/{name}/export?v=name=value&download=1&format=html` | | the runbook as Markdown to follow by hand, or with `format=html` as a page to read (`v` fills in inputs; `download` saves it as a file) |
| GET | `/api/schedules?from=RFC3339&tz=Area/City` | | planned runs, soonest first, each `{id, runbook, at, title, notes, inputs, description, window, entries, error}`; `from` leaves out runs entirely before it |
| POST | `/api/schedules` | `{runbook, at, title?, notes?, inputs?, created_by?}` | `201` the schedule |
| GET / PUT / DELETE | `/api/schedules/{id}` | (PUT: as POST) | the schedule / `204` |

`at` is when the maintenance window opens (T+0) for a runbook with one,
else when the run starts. `entries` are `{kind: notify|window|section|step|start,
name, step, at, end?, offset, planned?}`: the notification (if the window
needs one), the window (with `end` if it has a target),
and each section and step with a planned start, from the runbook as saved
now with the schedule's `inputs` (which can override offsets and the
target). Dates that land on a weekend in `tz` (default: the server's time
zone) move back to the Friday before (or, after the run's start, forward
to the Monday after), with `planned` holding the date worked out; the window, and entries during it, don't move. Secret inputs
aren't stored.

## Trying steps out ("Run this")

| Method | Path | Body | Returns |
|---|---|---|---|
| POST | `/api/test-run` | `{raw, step, vars, extra, setup, unattended?}` | `202 {id}`: a live run (events, answers, terminals as usual) that isn't recorded |
| GET / PUT | `/api/runbooks/{name}/test-values` | `{vars, extra, setup}` | the runbook's saved test values (secret inputs are never stored) |

`raw` is the runbook YAML as edited (it needn't be saved); `step` is 1-based;
`extra` seeds state variables an earlier step would publish; `setup` runs the
`always` steps first.

## Libraries

| Method | Path | Body | Returns |
|---|---|---|---|
| GET | `/api/libraries` | | `[{name, description, functions, version, updated_at, error?}]` |
| GET | `/api/libraries/{name}` | | `{raw, version, updated_at, library?, script?, error?}` (`script`: the functions as shell code) |
| POST | `/api/libraries` | YAML | `201 {name, version, library}` |
| PUT | `/api/libraries/{name}` | YAML | `{name, version, library}` (`If-Match: <version>` → `409` on conflict) |
| DELETE | `/api/libraries/{name}` | | `204` (versions kept) |
| GET | `/api/libraries/{name}/versions` | | `[{version, created_at, bytes}]` |
| POST | `/api/libraries/validate` | YAML | `{ok, error?}` |

Starting a run fails with `400` if a library the runbook uses doesn't exist.

## Runs

| Method | Path | Body | Returns |
|---|---|---|---|
| POST | `/api/runs` | start request | `202 {id}`, or the run record with `wait` |
| GET | `/api/runs?runbook=&limit=` | | recent runs (no step detail), newest first; limit ≤ 1000, default 100 |
| GET | `/api/runs/{id}` | | `{run, live}` |
| DELETE | `/api/runs/{id}` | | `204`; `409` while it's running |
| GET | `/api/runs/{id}/plan` | | `{steps: [StepView], version, latest, changed?, shape?, live}`: the steps as this run uses them, and the runbook's newest version (`changed`: steps not run yet that it has differently; `shape`: why it can't be adopted) |
| POST | `/api/runs/{id}/adopt-version` | `{version}` | `{changed, restarted, version}`: a live run takes that version for the steps it hasn't run (`409` if it can't) |
| GET | `/api/runs/{id}/chain` | | `[{id, status, started, ended?, resumes?, runbook_version?, steps}]`: the run and the runs linked to it by resuming, oldest first |
| GET | `/api/runs/{id}/logs?ids=&format=raw\|json\|both&single=1` | | those runs of the chain (all by default) as raw logs, records (JSON) or both, in one file per format or one per run: a file, or a zip of several |
| POST | `/api/runs/{id}/logs` | `{ids?, format, single?, path, overwrite?}` | `{written: [paths]}`: the same, into folder `path` |
| GET | `/api/runbooks/{name}/tests` | | the runbook's test suite (`suite`: name, version, fake libraries, isolated commands, scenarios; `null` without one) and its latest results (`last`) |
| POST | `/api/runbooks/{name}/tests/run` | `{scenario?, raw?}` | runs the suite (or one scenario), against the saved runbook or the draft YAML in `raw`: `{at, draft, results: [{scenario, passed, problems, status, steps, calls, asked, unused, log}]}` |
| GET | `/api/runs/{id}/log?download=1` | | the run as a plain-text log (`download` saves it as a file) |
| GET | `/api/runs/{id}/events` | | Server-Sent Events (live runs only) |
| POST | `/api/runs/{id}/answer` | `{question_id, value, note?}` | `204` |
| POST | `/api/runs/{id}/goto` | `{step, force?}` | `204`: while the run waits on a question (those carry `goto: true`), goes to step `step` (1-based): back (it and the steps after it run again), the same step again, or ahead (the steps between are skipped). It waits before running. `409` if the run isn't waiting, or (`needs_force`) going back past a point of no return without `force` |
| POST | `/api/runs/{id}/steps/{n}/reopen` | | `204`: goes back to step `n`, the last that succeeded (questions carry `reopenable`); `goto` does this and more |
| POST | `/api/runs/{id}/stop` | `{step?, by?}` | `204`: interrupts the running step's command (`step` 1-based; `0`: whichever). The step fails ("stopped by …") and the run asks what to do; `409` if it isn't running |
| PUT | `/api/runs/{id}/inputs` | `{values: {name: value}, by?}` | `{changed, stale, replaces, restarted}`: changes a live run's inputs, checked as at the start. They take effect before its next command; a step waiting to run its command starts again (`restarted`). `stale`: per input, the steps (1-based) that already ran with it; `replaces`: inputs a step had published a value for. `400` if a value isn't allowed |
| POST | `/api/runs/{id}/cancel` | `{author?, reason?, force?}` | `{aborted, message}`: aborts it. A run this server runs stops now; one running elsewhere is asked to stop (waits up to 8s); one whose process is gone, or with `force`, is marked aborted |
| POST | `/api/runs/{id}/resume` | start request (no `runbook`/`from`); `"new_run": true` for a new run | like `POST /api/runs`: the same run ID (it carries on), unless `new_run` |
| POST | `/api/runs/{id}/notes` | `{step, text, author?}` (`step` 1-based; `0` = the run) | `201 {time, author, text}` |
| GET | `/api/runs/{id}/preview` | | `{steps: [StepView]}` with the run's inputs and current state |
| GET / PUT | `/api/runs/{id}/controls` | `{step_mode, skip, pause_before, pause_after}` | the live run's controls; changes apply to steps not reached yet |

Start request:

```json
{
  "runbook": "deploy-service",
  "vars": {"env": "staging"},
  "unattended": true,     // no human prompts; manual/tty steps follow their unattended policy
  "yes": true,            // approve confirm-gated steps when unattended
  "dry_run": false,
  "from": "deploy",       // optional: start at a step id, 1-based number or name
  "skip": [3, 5],         // optional: 1-based steps to leave out (on resume, replaces "start where it stopped")
  "pause_before": [4],    // interactive: stop before these steps
  "pause_after": [4, 9],  // interactive: stop after these to check the result
  "step_mode": false,     // interactive: pause before every step
  "previous": "<run id>", // compare against this run (default: the resumed run, else the latest)
  "wait": true            // block until done and return the run (requires unattended)
}
```

A run record has `status` (`running`, `succeeded`, `failed`, `aborted`), `notes`,
and a `steps` array with each step's `status` (also `pending`, `skipped`, or
`forced`), `command`, `output` (last 64 KiB), `exit_code`, `error`, `note`
(system note, e.g. the force reason), `notes` (operator notes), `shells`
(shell sessions opened at it), `jobs` (commands the operator ran in the
background at it: `{command, started, ended, exit_code, output, note}`) and
timings, plus `runbook_version`, `resumes` (a `new_run` resume), `resumptions` (each time it was resumed in place, with `runbook_version` and `was_version` when the runbook had changed), `removed` (steps it ran that a changed runbook no longer has), `edits` and `input_changes`
(`{at, name, old, new, by, step}`). Secrets are masked.

### Interactive runs over the API

Start without `unattended`, then read `/events`. Each event is
`{seq, kind, step, ...}`; kinds are `run_started`, `step_started`, `output`,
`step_finished`, `run_finished`, plus `question`, `answered`, `note`,
`step_progress`, `window`, `controls` and `inputs` (the run's inputs
changed: `run` carries `inputs`, `input_changes` and `state`). Answer a
`question` with `POST /answer`:

| `question.type` | values |
|---|---|
| `input` | the input's value |
| `confirm` | `run`, `skip`, `abort` |
| `danger` (dangerous step out of sequence) | `run` with `note` = the question's `confirm` (the step number; asked again otherwise), `skip`, `abort` |
| `typed` (a point of no return, `confirm_typing`) | `run` with `note` = the question's `confirm`, `skip`, `abort` |
| `ask` (a step's `runnr_confirm`) | `yes`, `no` |
| `ask_text` (a step's `runnr_ask`) | the answer |
| `manual` | `done`, `skip`, `fail`, `abort` (optional `note`) |
| `failure` | `retry`, `skip`, `abort`, `force` (`note` = required reason; asked again without one), and `way` (try another way) for steps with several ways |
| `before` | `run`, `skip`, `continue` (stop stepping through), `abort` |
| `choose` (which way, for steps set to choose or after `way`) | a way's name (the first option is the preferred one), or `abort` |
| `after` | `continue`, `retry` (run again), `fail` (`note` = what's wrong; then a `failure` question), `abort` |

**Changing steps during a run.** `GET /api/runs/{id}/steps/{n}` returns
`{yaml, current, editable}` for a live run's step `n` (1-based). `PUT` the
same path with the step as YAML to change it for the rest of the run (the
current step or a later one); if it was waiting on a question, it starts
again (`{"restarted": true}`). The run records `edits` and, once
`POST /api/runs/{id}/save-edits` makes them a new runbook version,
`edits_saved` (that version). Saving fails if the step has changed in the
runbook since the run started.

Questions carry `text` (the rendered command or instructions), `expect`,
and on a step `goto: true` (see `POST /goto`).
`confirm`/`danger`/`manual`/`failure`/`before`/`after` questions also offer `shell`,
which opens a terminal session with the run's environment and then asks the
same question again; `tty` steps open one too.

**Terminal sessions** stream as events: `term_start` (`term: {id, title,
shell, command, expect, background}`: for shells, the step's commands to show
beside it; background sessions keep running while the run moves on),
`term` (`term.data`: base64 output, secrets masked), and `term_end`. Send
keystrokes and the viewer's size with:

| Method | Path | Body |
|---|---|---|
| POST | `/api/runs/{id}/term` | `{id, data?, rows?, cols?, end?}` (`data` is what was typed, as UTF-8; `end: true` hangs up) |

What a `tty` step printed is recorded as the step's `output`, and shell
sessions as its `shells`, cleaned of terminal control codes.

**Shells of their own** (the console's Shell panes) don't depend on a run
being live: they can open before a run, alongside it, or after it.

| Method | Path | Body | Returns |
|---|---|---|---|
| POST | `/api/shells` | `{run, step?}` or `{runbook, vars?, step?}` | `201 {id, title, step, phase}`: `phase` is `before` (the runbook's environment, the inputs given), `alongside` (a live run at a step: what it publishes reaches the run at its next step, recorded with the step) or `after` (the run's inputs and published values) |
| GET | `/api/shells/{id}/events` | | Server-Sent Events: `term_start`, `term`, `term_end`, and `output` for errors |
| POST | `/api/shells/{id}/term` | as `/api/runs/{id}/term` | keystrokes, size, `end` |

`step_finished` events carry `state`: the run's variables, secrets masked.
`step_progress` events carry a step's `result` while it's still going (each
attempt of a group of checks). A background
step finishes with status `background` and later gets another
`step_finished` when it stops. Step results may include `checks` (latest
attempt: `{name, passed, exit_code, output, error}`) and `way` (the way it
was done, for steps with several). Runs of a runbook with a maintenance
window carry `window: {target, open_step, close_step, opened, closed}`;
`window` events (with `run`) say when it opens and closes.

Starting an unattended run whose steps include ones set to `choose` needs
`"use_preferred": true` (they'll use their preferred way); otherwise it's
refused with the step numbers.
`controls` events carry the live run's new `controls` after a PUT.

A StepView is `{index, name, section, owner, kind, command, expect, dir, when,
confirm, always, timeout, retries, outputs, secret_outputs, ways, choose,
window, offset, offset_sec}` with templates
filled in (unknown values as `<name>`).
The run record's `state` holds the same as of the last finished step.

On reconnect the stream replays from `seq` 0.

## Examples

```sh
H="Authorization: Bearer $RUNNR_TOKEN"

# Run and wait (CI, cron, chatops)
curl -sf -H "$H" -X POST localhost:7777/api/runs \
  -d '{"runbook":"hello","vars":{"who":"ci"},"unattended":true,"wait":true}' | jq .status

# Fire and forget, then poll
id=$(curl -s -H "$H" -X POST localhost:7777/api/runs -d '{"runbook":"hello","unattended":true}' | jq -r .id)
curl -s -H "$H" localhost:7777/api/runs/$id | jq .run.status

# Follow live output
curl -sN -H "$H" localhost:7777/api/runs/$id/events

# Resume a failed run, overriding an input
curl -s -H "$H" -X POST localhost:7777/api/runs/$id/resume -d '{"vars":{"env":"staging"},"unattended":true}'
```
