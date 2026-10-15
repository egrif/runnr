# runnr

A runbook manager and automater. Write runbooks as YAML, then run them from the
**CLI** or a local **web GUI** — step by step with a human in the loop, or fully
unattended for cron/CI.

- **Automated steps** run shell commands with live output, timeouts, retries and captured output.
- **Manual steps** show instructions and wait for an operator to mark them done, skipped or failed.
- **Confirmation gates** pause before risky commands.
- **Inputs** with defaults, choices and secrets (secrets are masked in output and history).
- **Run history** for every run, viewable in both interfaces.

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
runnr ui --open                              # GUI at http://127.0.0.1:7777
```

## CLI

| Command | |
|---|---|
| `runnr list` | List runbooks (`--json`) |
| `runnr new <name>` | Create from a starter template and open `$EDITOR` |
| `runnr edit <name>` | Edit in `$EDITOR`; only saved once it validates |
| `runnr show <name>` | Print YAML |
| `runnr import <file>...` | Validate and copy files into the library (`-f` to overwrite) |
| `runnr delete <name>` | Delete (`-f` to skip the prompt) |
| `runnr validate [name\|file]...` | Validate; all runbooks if none given |
| `runnr run <name\|file>` | Run a runbook (see below) |
| `runnr history [run-id]` | List runs (`-r <runbook>`, `-n`), or show one in detail |
| `runnr ui` | Start the web GUI (`--addr`, `--open`) |

Data lives in `~/.runnr` (`runbooks/*.yaml`, `runs/*.json`); override with `--home` or `RUNNR_HOME`.

### Running

```
runnr run <name|file> [-v key=value]... [-u] [-y] [--dry-run] [--json] [-q] [--no-history]
```

| Mode | Behaviour |
|---|---|
| **Interactive** (default on a TTY) | Prompts for missing inputs, manual steps and confirmations. On failure: retry / skip / abort. |
| **Unattended** (`-u`, or stdin isn't a TTY) | Never prompts. Inputs from `-v` or defaults. Confirm-gated steps fail unless `-y`. Manual steps follow their `unattended` policy. Any failure aborts. |

`--json` implies unattended and prints the run record. Exit codes: `0` success, `1` failure, `130` aborted.

## GUI

`runnr ui` serves a local web app: browse/filter runbooks, edit YAML with live
validation, start runs (interactive or unattended, dry run), watch output stream
in, answer manual/confirm/failure prompts in the browser, and browse history.

It binds to `127.0.0.1` by default. It can execute commands, so don't expose it
on a shared network; mutating API calls require a same-origin `X-Runnr` header.

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
  - name: Resolve version
    run: git describe --tags
    capture: version            # trimmed stdout → {{.version}} for later steps
  - name: Announce
    manual: |
      Post "Deploying {{.version}} to {{.env}}" in #deploys.
    when: '{{eq .env "production"}}'
    unattended: skip            # fail (default) | skip | pass
  - name: Deploy
    run: ./deploy.sh {{quote .version}}
    confirm: true               # ask first; unattended needs --yes
    timeout: 10m
    retries: 2
    dir: ./infra
    env: { TOKEN: "{{.token}}" }
  - name: Smoke test
    run: curl -fsS https://example.com/healthz
    continue_on_error: true
```

Each step has exactly one of `run` or `manual`. `run`, `manual`, `when`, `dir`
and `env` values are Go templates over inputs and captured values, with helpers
`default`, `upper`, `lower`, `trim`, `contains`, `quote` (shell-quote). Unknown
variables are an error. `when` is truthy unless it renders to empty, `false`,
`0`, `no` or `off`. Steps also get `RUNNR_RUN_ID` and `RUNNR_RUNBOOK` in their
environment.

## Layout

```
cmd/runnr/          entrypoint
internal/runbook/   format, validation, templating
internal/engine/    executor + Interactor interface (unattended impl here)
internal/cli/       cobra commands + terminal Interactor
internal/server/    HTTP API, SSE, browser Interactor, embedded web/ UI
internal/store/     filesystem storage for runbooks and run history
examples/           sample runbooks
```

The engine never talks to a human directly; the CLI, GUI and unattended modes
are each an `engine.Interactor`.
