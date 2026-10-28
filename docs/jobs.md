# Jobs

A **job** is one command that runs on a machine, detached. It survives the end
of the command that submitted it, an SSH drop, and an engine restart. Jobs use
the detached node executor that local CI uses (`internal/jobs`), take one of the
machine's existing CI slots, and store their log and exit code.

## Run a job

`dyna job run [--node N] [--commit REF] [--cpus N] [--memory 16g] [--timeout 2h] [--image IMG | --host] -- CMD`

- With no `--node`, the job runs on the local node (the engine host), so a
  one-machine owner needs no setup.
- `--commit REF` pushes that exact commit of the current repository to the
  node the way local CI does, checks it out in a fresh per-job directory, and
  removes the directory at the end. Without it the command runs with no source.
- `--host` runs on the machine itself; `--image IMG` runs in a container.
  Host execution on a remote machine needs that machine's host-check grant
  (`dyna machine edit N --allow-host-checks`); the local node needs none.
- The job takes one of the machine's CI slots, so jobs and local CI checks
  share one admission bound per machine.

The command returns at once with the job's ID. The job is detached: it keeps
running when the submitting session's turn ends or the engine restarts, which
reattaches through the stored job folder and unit.

## Read and control

- `dyna job show ID` — the job's state, exit code and command.
- `dyna job logs ID` — the last of the job's output.
- `dyna job list [--all]` — open jobs, or every recent one.
- `dyna job cancel ID` — stop a queued or running job.

States: `queued`, `running`, then one of `passed`, `failed`, `cancelled`, `lost`, `timed_out`.

## Events

Each job posts typed events to the session that submitted it: `job.started`
(quiet) and `job.finished` (with state and exit code), which wake that session.
Agents never poll for a job's end; the finished event is the wake.

The jobs table lives in `dyna.db`; the logs live under `<data>/jobs/<id>/log`.
