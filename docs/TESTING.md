# Testing runbooks

`runnr test` runs a runbook for real, unchanged, with the commands that act
on the world faked. A *simulated operator* answers its prompts, and each
*scenario* says how the run should go. Nothing a test isolates can run for
real.

```sh
runnr fakes import aws-rds.yaml          # fake libraries: canned responses, reusable
runnr tests import replica-demo.test.yaml # a runbook's test suite
runnr test replica-demo                  # every scenario
runnr test replica-demo -s two-replicas -v --log
runnr fakes check aws-rds                # fakes vs the AWS CLI's real response shapes
```

## How it works

For each scenario, runnr makes a scratch directory with a small *shim* for
every faked or isolated command (`aws`, `kubectl`, `terraform`, …) and puts
it first on `PATH` for the run. A shim hands the call back to runnr, which
picks the most specific fake that matches, logs the call and answers it.
The runbook's own steps and library functions call `aws` by name, so they
reach the fakes without any change.

- **Isolation.** Commands under `isolate:` never run for real: a call no
  fake answers is refused (`no fake answered a call to isolated aws (step
  4): aws rds delete-db-instance …`) and fails the scenario, whatever it
  expects. Other commands (`grep`, `jq`, `date`) run as usual; a faked
  command that isn't isolated runs for real when no fake matches.
- **Scripts called by path** (`./tools/report.sh`) can't be caught on
  `PATH`: list them under `files:`, which puts a shim at that path inside
  the scenario's directory, and point an input at `{{.test_dir}}/…`.
- **Time.** Retries and repeats don't really wait (a check repeating every
  5 minutes is tried again at once), but a repeat still gives up after its
  `timeout`, counted in simulated time. A fake's `delay:` and `sleep` in a
  script are real.
- **The run** is an ordinary interactive run of the saved runbook (in
  sequence, so `dangerous` steps only ask when out of sequence). It isn't
  recorded in the history.

## Fake libraries

```yaml
name: aws-rds
description: RDS and Aurora, as the AWS CLI answers.
fakes:
  - command: aws rds describe-db-instances        # the program, then words the call contains, in order
    json:                                         # the full response: the call's --query and --output apply
      DBInstances:
        - DBInstanceIdentifier: "{{.source_dbid}}" # templates use the run's values at the time of the call
          DBInstanceStatus: available
          Endpoint: {Address: "{{.source_dbid}}.abc.us-east-1.rds.amazonaws.com", Port: 5432}
  - command: aws rds describe-db-clusters
    args: {--db-cluster-identifier: "*-aurora-*"} # only calls with this flag value (a glob)
    sequence:                                     # successive calls; the last one repeats
      - json: {DBClusters: [{Status: creating}]}
      - json: {DBClusters: [{Status: available}]}
  - command: aws rds delete-db-instance           # it changes something: just answer
    stdout: '{"DBInstance": {"DBInstanceStatus": "deleting"}}'
  - command: psql
    stdin: SELECT count(*)                        # only when standard input contains this
    stdout: "0\n"
  - command: terraform apply
    match: -auto-approve                          # a regular expression on the whole call
    stdout: "Apply complete! Resources: 2 added, 0 changed, 0 destroyed.\n"
    exit: 0
    delay: 2s
```

A fake answers with `stdout`, `stderr`, `exit` (default 0) and `json`:

- **`aws`**: the JSON is the full API response. The call's own `--query`
  (JMESPath) and `--output` (`json`, the default; `text`; `yaml`) are
  applied to it, so the runbook's queries really run: a wrong field name
  fails the test, as it would for real.
- **`kubectl`**: `-o json`, `-o yaml` and `-o jsonpath=…` (paths, `[*]`,
  `[n]`, `['key']`, `{range …}{end}`, `"literals"`).
- **Anything else**: the JSON, printed.

When several fakes match, the most specific wins: the most words in
`command`, then `args`, `match` and `stdin`. Ties go to the one listed
first, in this order: the scenario's fakes, the suite's, then the
libraries' in `uses:` order. That's how a scenario overrides one answer.

## Test suites

```yaml
runbook: replica-demo
uses: [aws-rds]                       # fake libraries
isolate: [aws, kubectl, terraform, psql]
files: {"tools/report.sh": report.sh} # a fake command at a path in the test directory
inputs: {db: orders, tools: "{{.test_dir}}/tools"}
functions: {require_vpn: "return 0"}  # replace library or runbook functions
fakes:                                # for every scenario, before the libraries
  - {command: report.sh, stdout: reported}
operator:                             # how the simulated operator answers (defaults below)
  confirm: yes
  steps:
    8: {danger: type}                 # by step number, id or name
scenarios:
  - name: happy
    expect:
      status: succeeded
      steps: {setup: succeeded, 3: succeeded}
      output: {setup: "host=orders.abc"}
      order: [aws rds delete-db-instance, aws rds describe-db-clusters, report.sh]
  - name: two-replicas                # mostly as expected, one surprise
    description: Two cross-region replicas: the step must refuse, not delete either.
    fakes:
      - command: aws rds describe-db-instances
        args: {--query: "*ReadReplica*"}
        json: {DBInstances: [{ReadReplicaDBInstanceIdentifiers: [a, b]}]}
    expect:
      status: failed
      steps: {2: failed}
      not_called: [aws rds delete-db-instance]
  - name: declined
    operator: {steps: {2: {confirm: "no"}}}
    expect: {steps: {2: skipped}, not_called: [aws rds delete-db-instance]}
```

**The operator.** Each answer is a word, or a list of words used in turn
(the last one repeats). `operator.steps` overrides the defaults for one step,
and a scenario's `operator` overrides the suite's.

| Prompt | Answers | Default |
| --- | --- | --- |
| `confirm` | `yes`, `no` | `yes` |
| `danger` (typed check out of sequence) | `type`, `decline` | `type` |
| `manual` | `done`, `skip`, `fail` | `done` |
| `failure` | `abort`, `retry`, `skip`, `way`, `force` | `abort` |
| `before` (pauses, optional steps) | `run`, `skip`, `abort` | `run` |
| `after` (pauses) | `continue`, `retry`, `fail`, `abort` | `continue` |
| `way` (steps that ask which way) | a way's name | the preferred one |

`operator.inputs` answers inputs the run asks for. Retries are answered
again each time, so `failure: [retry, retry, abort]` retries twice.

**Expectations.**

- `status`: the run's status, one of `succeeded`, `failed` or `aborted`. An
  operator who answers a failure with `abort` leaves the run `failed`.
- `steps`: per-step status, by number, id or name.
- `called` and `not_called`: a program and words, matched like a fake's
  `command`.
- `order`: calls that must happen in this order.
- `output`: text a step's recorded output must contain.

## Keeping fakes true to the real tools

- **Run the real queries.** Because `aws` fakes hold the whole response and
  the call's `--query` is applied, a test exercises the runbook's actual
  JMESPath, not a copy of its answer.
- **`runnr fakes check`** compares every `aws` fake's JSON with the response
  shape the AWS CLI itself describes (`aws SERVICE OPERATION
  --generate-cli-skeleton output`, which needs no credentials). It lists
  fields the real response doesn't have, with the nearest real name:
  `DBInstances[0].Endpoint.Adress isn't in the response (did you mean
  Address?)`.
- **`runnr test -v`** lists every call and what answered it, and any fake
  the suite or a scenario defines that no call used: a sign the runbook
  changed under it.

## Where tests live

Suites and fake libraries are stored in runnr like runbooks and function
libraries, versioned (`runnr tests import|list|show|delete`, `runnr fakes
import|list|show|delete|check`). `runnr test` also takes a suite file, and
`--fakes file.yaml` adds a library from a file, so tests can live in a repo
beside the runbook. Recorded or hand-written fake data often names real
hosts and accounts: keep it with the runbook, not anywhere public.
