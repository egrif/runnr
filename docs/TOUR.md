# A tour of runnr

Screenshots of the web GUI (`runnr serve`), using a demo runbook that moves an
"orders" database to a new cluster. Everything shown also works from the
[CLI](CLI.md).

- [Runbooks](#runbooks)
- [Running](#running)
- [Editing](#editing)
- [Schedule](#schedule)
- [Function libraries](#function-libraries)
- [Collections](#collections)

## Runbooks

Pick inputs, choose how to run (interactive, step-through or unattended), and
see every step before it runs: grouped by section, with each section's purpose
and end state, planned times relative to the maintenance window (T-2d, T+0…),
and what each step did last time. Untick steps to skip them, or set ⏸ pauses.

![The run page for a runbook](screenshots/runbook.png)

## Running

A live run. While the maintenance window is open, a banner counts the downtime
against its target.

![A run with the maintenance window open](screenshots/running-window.png)

Steps stop for confirmations, manual instructions and failures. Each step shows
what it's doing, its command and its output; ⚠ dangerous steps ask for a typed
double check when they're run out of order. You can open a shell with the
run's variables, add notes, or ✎ edit a step mid-run and run it again.

![A run waiting for confirmation](screenshots/running.png)

Background steps (here, a status board) keep running in their own terminal tab
while the run goes on; terminal steps run in the browser too.

![A background step's terminal](screenshots/running-terminal.png)

## Editing

The structured editor (YAML is a click away). Sections are headings in the
step list; drag ⠿ to reorder steps and sections. A step with several ways to
do it (Scripted, Manual…) shows them as tabs. ▶ Run this tries a step with test
values; 💬 marks YAML comments on fields; ⓘ explains every field.

![The structured editor](screenshots/editor.png)

## Schedule

Plan runs ahead of time. For a runbook with a maintenance window, you schedule
when the window opens and runnr lays out the notification, sections and steps
around it, moving prep dates off weekends.

![The schedule as a calendar](screenshots/calendar.png)

The same as an agenda, with each run's inputs; "Next actions only" shows just
what's coming up for each run.

![The schedule as an agenda](screenshots/agenda.png)

## Function libraries

Shell functions shared by runbooks: every step can call them, and shells opened
from runnr have them loaded. Steps that use one show ƒ, with the definition on
hover.

![A function library](screenshots/libraries.png)

## Collections

Group runbooks that belong together. Drag a runbook between collections to move
it (Alt/⌥ to add it to another as well); names and descriptions edit in place.

![Collections](screenshots/collections.png)
