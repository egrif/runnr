# A tour of the runnr CLI

The terminal side of runnr, using the same demo runbook as the
[GUI tour](TOUR.md): moving an "orders" database to a new cluster. The CLI
and the GUI share one store, so a runbook saved, scheduled or run in one
shows up in the other. Every command is listed in the
[README](../README.md#cli); `runnr <command> --help` has the details.

- [Find your way around](#find-your-way-around)
- [Look before you run](#look-before-you-run)
- [Running](#running)
- [Out of sequence](#out-of-sequence)
- [Unattended](#unattended)
- [Schedule](#schedule)
- [History](#history)
- [More](#more)

## Find your way around

Runbooks, the collections that group them, and the function libraries
they share.

![runnr list, collection list and lib list](screenshots/cli-list.png)

## Look before you run

`runnr inspect` lays a runbook out the way it will run: the sections and
their purpose, the maintenance window, what each step is (run, manual,
checks, ways…) and when it's planned relative to the window. Give it a step
number to see that step's code, or its ways (`*` marks the preferred one).
Add `-v key=value` to see the code with inputs filled in, or use `--all` to
show every step.

![runnr inspect, for the whole runbook and for one step](screenshots/cli-inspect.png)

## Running

`runnr run` asks for any inputs you didn't pass with `-v`, then shows the
plan. Each step's result from last time is beside it. Before you start, you
can skip steps, set pauses before or after a step, view a step's code, or
step through every step.

![Reviewing the plan before a run](screenshots/cli-review.png)

Steps run in order. Setup (`always`) steps publish values that later steps
use. A `tty` step connects you to its terminal, with the output recorded.
Checks show each check's result and the attempt.

![The first steps: setup, a tty step, checks](screenshots/cli-run.png)

A manual step waits until you mark it done. A background step keeps running
alongside the others: press `w` at any prompt to watch it. The step that
opens the maintenance window says so and asks first (`confirm`). Planned
times show against the window (T+10m, and how far ahead or behind you are).
A step with ways runs its preferred way. At any prompt you can view the
step, see what it did last time, look at the environment, add a note, open
a shell, or change the step for this run.

![A manual step, a background step, the window opening, a step with ways](screenshots/cli-run-window.png)

The window closes with how long it took against its target. The background
step stops after the step it was tied to, and the run ends with a summary.

![The window closing and the end of the run](screenshots/cli-run-end.png)

## In tmux

`runnr run <runbook> --tmux` (and `resume --tmux`) starts the run in a tmux
session and attaches to it:

```
┌──────────────────────────┬──────────────────┐
│ the run: prompts, output │ runnr watch: the │
│                          │ steps, live      │
│                          ├──────────────────┤
│                          │ a shell with the │
│                          │ run's variables  │
└──────────────────────────┴──────────────────┘
```

The run outlives your terminal. Close it, lose the connection, or detach
(prefix, then `d`), and `runnr attach` gets it back; with several such
runs, `runnr attach <run-id|runbook>`. Someone else logged in as the same
user can attach and watch too.

Each background step runs in a window of its own (`bg N`), outside runnr. If
the run fails or is aborted while one is still going, it's left running:
a Terraform apply isn't killed halfway. `runnr resume <run> --tmux` follows
it again, and a later step with `wait_for` waits for it to finish.

## Out of sequence

`--from` starts later. A `dangerous` step that runs when earlier steps
didn't lists what was skipped. To run it, you type its step number, not
just `y`. A run that's aborted or fails can be continued with
`runnr resume <run-id>`.

![A dangerous step out of sequence](screenshots/cli-dangerous.png)

## Unattended

`-u` (`--unattended`) runs without prompts, for cron or CI. `-y`
(`--yes`) approves `confirm` steps. Manual and `tty` steps can't run
unattended unless the runbook says how (`unattended: pass` or `skip`).
Steps set to ask which way to go (`choose`) need `--use-preferred` to take
their preferred way.

![An unattended run](screenshots/cli-unattended.png)

## Schedule

`runnr schedule add <runbook> --at "2026-10-31 22:00"` plans a run. The
agenda lays out each planned run by day: when the window must be
announced, the window itself, and when each section (or step, with
`--detail steps`) is due. Dates that fall on a weekend move to a weekday
(prep back to Friday, follow-up forward to Monday). Only the window stays
on the weekend. `--next` shows just the next thing to do for each run.

![runnr schedule list, in full and --next](screenshots/cli-schedule.png)

## History

Every run is kept with its inputs, the state its steps published, and what
each step ran and printed. Secrets are masked. `runnr rerun <run-id>` runs
again with the same inputs and compares each step with what it did last
time.

![runnr history, the list and one run](screenshots/cli-history.png)

## More

- `runnr new` / `runnr edit` open a runbook in `$EDITOR`. It's saved only
  once it validates, and every save is a version (`runnr versions`,
  `runnr show --version N`).
- `runnr shell <runbook|run-id>` runs the setup steps, then opens a shell
  with their environment and the runbook's library functions.
- `runnr explain <runbook> [step]` asks a model what a step does, what it
  relies on, and its risks. See "Ask Claude" in the README for setup.
- `runnr export <runbook>` writes the runbook as Markdown to follow by hand,
  with copy/paste-safe shell blocks. `-o page.html` writes a page to read
  instead: steps that expand, ways as tabs, and code as written or exactly
  as runnr runs it ([screenshot](TOUR.md#html-view)).
- `runnr serve --open` starts the GUI.
- `runnr doctor <runbook>` checks the programs its `requires` lists (and
  its libraries'), with versions and how to install what's missing.
- `runnr watch <run-id>` follows a run's steps as it goes.
