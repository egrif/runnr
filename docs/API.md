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
  The GUI asks for the token once and keeps it in an HttpOnly cookie.

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
| DELETE | `/api/runbooks/{name}` | | `204` (version history is kept) |
| GET | `/api/runbooks/{name}/versions` | | `[{version, created_at, bytes}]`, newest first |
| GET | `/api/runbooks/{name}/versions/{n}` | | `{version, raw}` |
| POST | `/api/validate` | YAML | `{ok, error?}` |
| POST | `/api/parse` | YAML | `{ok, runbook?, error?}`: the structured form |
| POST | `/api/format` | runbook JSON | `{ok, raw?, error?}`: YAML (validated; comments aren't kept) |
| POST | `/api/preview` | `{runbook \| raw, vars, from_run?}` | `{steps: [StepView]}`: what each step would do with these inputs (and that run's state) |

Send `If-Match: <version>` with `PUT` to get `409 Conflict` instead of
overwriting a newer save.

## Runs

| Method | Path | Body | Returns |
|---|---|---|---|
| POST | `/api/runs` | start request | `202 {id}`, or the run record with `wait` |
| GET | `/api/runs?runbook=&limit=` | | recent runs (no step detail), newest first; limit ≤ 1000, default 100 |
| GET | `/api/runs/{id}` | | `{run, live}` |
| GET | `/api/runs/{id}/events` | | Server-Sent Events (live runs only) |
| POST | `/api/runs/{id}/answer` | `{question_id, value, note?}` | `204` |
| POST | `/api/runs/{id}/cancel` | | `204` |
| POST | `/api/runs/{id}/resume` | start request (no `runbook`/`from`) | like `POST /api/runs` |
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
(system note, e.g. the force reason), `notes` (operator notes) and timings, plus `runbook_version`. Secrets are masked.

### Interactive runs over the API

Start without `unattended`, then read `/events`. Each event is
`{seq, kind, step, ...}`; kinds are `run_started`, `step_started`, `output`,
`step_finished`, `run_finished`, plus `question`, `answered` and `note`. Answer a
`question` with `POST /answer`:

| `question.type` | values |
|---|---|
| `input` | the input's value |
| `confirm` | `run`, `skip`, `abort` |
| `manual` | `done`, `skip`, `fail`, `abort` (optional `note`) |
| `failure` | `retry`, `manual` (run it myself), `skip`, `abort`, `force` (`note` = required reason; asked again without one) |
| `before` | `run`, `manual`, `skip`, `continue` (stop stepping through), `abort` |
| `after` | `continue`, `retry` (run again), `fail` (`note` = what's wrong; then a `failure` question), `abort` |

Questions carry `text` (the rendered command or instructions) and `expect`.
When the server is local, `confirm`/`manual`/`failure`/`before` questions also
offer `shell`, which opens a Terminal window with the run's environment and
then asks the same question again; `manual` (run it myself) opens one too.

`step_finished` events carry `state`: the run's variables, secrets masked.
`controls` events carry the live run's new `controls` after a PUT.

A StepView is `{index, name, section, owner, kind, command, expect, dir, when,
confirm, always, timeout, retries, outputs, secret_outputs}` with templates
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
