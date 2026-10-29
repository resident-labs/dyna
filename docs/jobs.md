# Jobs

A **job** is one command that runs on a machine, detached. It survives the end
of the command that submitted it, an SSH drop, and an engine restart. Jobs use
the detached node executor that local CI uses (`internal/jobs`), join the
machine's node queue, and store their log and exit code.

## Run a job

`dyna job run [--node N] [--commit REF] [--cpus N] [--memory 16g] [--io heavy|light] [--timeout 2h] [--image IMG | --host] [--artifact PATH]... -- CMD`

- With no `--node`, the job runs on the local node (the engine host), so a
  one-machine owner needs no setup.
- `--commit REF` pushes that exact commit of the current repository to the
  node the way local CI does, checks it out in a fresh per-job directory, and
  removes the directory at the end. Without it the command runs with no source,
  in an empty work folder of its own. Either way, the work folder is the
  command's working directory, and `/workspace` in a container.
- `--host` runs on the machine itself; `--image IMG` runs in a container. The
  command's first word is the container's entrypoint and the rest are its
  arguments, so any image works, with or without a shell.
  Host execution on a remote machine needs that machine's host-check grant
  (`dyna machine edit N --allow-host-checks`); the local node needs none.
- The job joins the machine's node queue, so jobs and local CI checks share one
  admission bound per machine. `--io heavy` marks a job that saturates the
  disk; a node runs only a few heavy units at once. A job that waits shows why
  in `dyna job show`.

- `--artifact PATH` keeps a file or folder of the work folder after the job
  ends, such as `--artifact target/release/app`. Repeat it for more paths. The
  engine copies them back before it posts the job's end and removes the folder,
  so the woken session can fetch them at once. A path the job never made is
  named in the job's note; the job's result does not change. One job keeps at
  most 4 GiB.

The command returns at once with the job's ID. The job is detached: it keeps
running when the submitting session's turn ends or the engine restarts, which
reattaches through the stored job folder and unit.

## Read and control

- `dyna job show ID` — the job's state, exit code and command.
- `dyna job logs ID` — the last of the job's output.
- `dyna job list [--all]` — open jobs, or every recent one.
- `dyna job cancel ID` — stop a queued or running job.
- `dyna job fetch ID [--to DIR]` — copy a finished job's kept artifacts into
  DIR (default: the current folder), with their paths and file modes.

States: `queued`, `running`, then one of `passed`, `failed`, `cancelled`, `lost`, `timed_out`.

## Events

Each job posts typed events to the session that submitted it: `job.queued`
(quiet, with the wait reason), `job.started` (quiet) and `job.finished` (with
state and exit code), which wake that session. Agents never poll for a job's
end; the finished event is the wake.

The jobs table lives in `dyna.db`; the logs live under `<data>/jobs/<id>/log` and kept artifacts under `<data>/jobs/<id>/artifacts`.
