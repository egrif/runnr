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
| GET | `/api/runbooks` | | `[{name, description, tags, steps, error?}]` |
| GET | `/api/runbooks/{name}` | | `{raw, runbook?, error?}` |
| POST | `/api/runbooks` | YAML | `201` runbook (`400` if invalid or exists) |
| PUT | `/api/runbooks/{name}` | YAML | runbook (renames if `name` changed) |
| DELETE | `/api/runbooks/{name}` | | `204` |
| POST | `/api/validate` | YAML | `{ok, error?}` |

## Runs

| Method | Path | Body | Returns |
|---|---|---|---|
| POST | `/api/runs` | start request | `202 {id}`, or the run record with `wait` |
| GET | `/api/runs?runbook=` | | recent runs (no step detail), newest first |
| GET | `/api/runs/{id}` | | `{run, live}` |
| GET | `/api/runs/{id}/events` | | Server-Sent Events (live runs only) |
| POST | `/api/runs/{id}/answer` | `{question_id, value, note?}` | `204` |
| POST | `/api/runs/{id}/cancel` | | `204` |
| POST | `/api/runs/{id}/resume` | start request (no `runbook`/`from`) | like `POST /api/runs` |

Start request:

```json
{
  "runbook": "deploy-service",
  "vars": {"env": "staging"},
  "unattended": true,     // no human prompts; manual/tty steps follow their unattended policy
  "yes": true,            // approve confirm-gated steps when unattended
  "dry_run": false,
  "from": "deploy",       // optional: start at a step id, 1-based number or name
  "wait": true            // block until done and return the run (requires unattended)
}
```

A run record has `status` (`running`, `succeeded`, `failed`, `aborted`) and a
`steps` array with each step's `status`, `command`, `output` (last 64 KiB),
`exit_code`, `error`, `note` and timings. Secrets are masked.

### Interactive runs over the API

Start without `unattended`, then read `/events`. Each event is
`{seq, kind, step, ...}`; kinds are `run_started`, `step_started`, `output`,
`step_finished`, `run_finished`, plus `question` and `answered`. Answer a
`question` with `POST /answer`:

| `question.type` | values |
|---|---|
| `input` | the input's value |
| `confirm` | `run`, `skip`, `abort` |
| `manual` | `done`, `skip`, `fail`, `abort` (optional `note`) |
| `failure` | `retry`, `skip`, `abort` |

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
