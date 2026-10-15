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
| `runnr versions <name>` | List saved versions |
| `runnr import <file>...` | Validate and add files to the library (`-f` to overwrite, `-` for stdin) |
| `runnr delete <name>` | Delete (`-f` to skip the prompt) |
| `runnr validate [name\|file]...` | Validate; all runbooks if none given |
| `runnr run <name\|file>` | Run a runbook (see below); `--from <step>` starts later |
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
runnr run <name|file> [-v key=value]... [-u] [-y] [--dry-run] [--json] [-q] [--no-history]
```

| Mode | Behaviour |
|---|---|
| **Interactive** (default on a TTY) | Prompts for missing inputs, manual steps and confirmations. On failure: retry / skip / force / abort. |
| **Unattended** (`-u`, or stdin isn't a TTY) | Never prompts. Inputs from `-v` or defaults. Confirm-gated steps fail unless `-y`. Manual steps follow their `unattended` policy. Any failure aborts. |

`--json` implies unattended and prints the run record. Exit codes: `0` success, `1` failure, `130` aborted.

At any step prompt you can also:

- **`n`**: add a note to the step (stored with the run; see `runnr history <id>`).
- **`!`**: open your `$SHELL` with the run's environment (inputs, values
  published so far, `env:`), in the step's `dir`. Investigate, fix things,
  `exit` to come back to the same prompt. `echo name=value >> "$RUNNR_OUTPUT"`
  in that shell publishes a value to later steps. Ctrl-C in the shell doesn't
  touch the run.
- **`f`** (failed steps): force the step to count as success. A reason is
  required; the step is recorded as `forced` with it, and whatever it
  published is kept.

A failed or aborted run can be continued with `runnr resume <run-id>`: same
inputs (secret inputs are asked again), starting at the step that didn't
finish. Steps marked `always` (typically environment setup) run again. For
multi-day procedures, abort at a "wait" step and resume later.

## GUI

`runnr serve` serves a local web app: browse/filter runbooks, edit YAML with live
validation, start runs (interactive or unattended, dry run, from any step),
watch output stream in, answer manual/confirm/failure prompts in the browser,
resume failed runs, and browse history.

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
    section: Setup              # groups steps under a heading in CLI and GUI
    always: true                # runs even with --from / resume
    run: |
      echo "region=us-east-1" >> "$RUNNR_OUTPUT"   # → {{.region}} and $region later
      echo "db_pass=$(fetch-pass)" >> "$RUNNR_SECRET"  # same, but masked everywhere
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

`tty` steps run attached to your terminal under `runnr run` and aren't
recorded. In the GUI or unattended they become manual steps ("run this in a
terminal"), following their `unattended` policy. Shell aliases aren't loaded in
steps, so call real binaries (`terraform`, not `tf`).

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
