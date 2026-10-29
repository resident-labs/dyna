# Jobs

A **job** is one command that runs on a node, detached. It survives the end
of the command that submitted it, an SSH drop, and an engine restart. Jobs use
the detached node executor that local CI uses (`internal/jobs`), join the
node's queue, and store their log and exit code.

## Run a job

`dyna job run [--node N | --label L]... [--commit REF] [--cpus N] [--memory 16g] [--io heavy|light] [--resource NAME]... [--workspace] [--timeout 2h] [--idle 20m] [--image IMG | --host] [--artifact PATH]... -- CMD`

- With no `--node` or `--label`, the job runs on the local node (the engine
  host), so a one-node owner needs no setup.
- Repeat `--label L` to place the job without naming a node. Dyna chooses an
  enabled node that the job's room may use, has every requested label, and
  has the most free CPU capacity in the node queue. The choice accounts for
  running and queued jobs and CI checks. `--node` and `--label` are mutually
  exclusive. If no node has every label, submission fails with the missing
  label instead of creating a job that can never start.
- Owner labels come from `dyna node edit N --label L`. Probed labels come
  from the last successful node probe: `os=linux` or `os=darwin`,
  `arch=<node architecture>`, `gpu` when a GPU is visible, and `docker`
  or `containerd` for a container runtime. Both kinds place jobs. For example:
  `dyna job run --label os=linux --label gpu -- nvidia-smi`.
- `--commit REF` pushes that exact commit of the current repository to the
  node the way local CI does, checks it out in a fresh per-job directory, and
  removes the directory at the end. Without it the command runs with no source,
  in an empty work folder of its own. Either way, the work folder is the
  command's working directory, and `/workspace` in a container.
- A local host job gets the caller's `DYNA_SCRATCH`; a remote job leaves it unset. Use `--workspace` or `--artifact` to send files.
- `--host` runs on the node itself; `--image IMG` runs in a container. The
  command's first word is the container's entrypoint and the rest are its
  arguments, so any image works, with or without a shell.
  Host execution on a remote node needs that node's host-check grant
  (`dyna node edit N --allow-host-checks`); the local node needs none.
- The job joins the node's queue, so jobs and local CI checks share one
  admission bound per node. `--io heavy` marks a job that saturates the
  disk; a node runs only a few heavy units at once. A job that waits shows why
  in `dyna job show`.

- `--idle D` stops a running job as `timed_out` after its log has no new
  output for D. D must be positive and no longer than `--timeout`. The default
  is no idle timeout.
- `--artifact PATH` keeps a file or folder of the work folder after the job
  ends, such as `--artifact target/release/app`. Repeat it for more paths. The
  engine copies them back before it posts the job's end and removes the folder,
  so the woken session can fetch them at once. A path the job never made is
  named in the job's note; the job's result does not change. One job keeps at
  most 4 GiB. Artifact paths are relative to the kept workspace too.

The command returns at once with the job's ID. The job is detached: it keeps
running when the submitting session's turn ends or the engine restarts, which
reattaches through the stored job folder and unit.

## Workspaces

`--workspace` needs `--commit`. The job uses its session's kept checkout for
that repository on the selected node. Jobs of other sessions and other
repositories use other folders. Dyna forces a detached checkout of the exact
commit and runs `git clean -fd`, so tracked changes and untracked files are
reset while ignored build output such as `target/` stays warm. The workspace
is the command's working directory and `/workspace` in a container. One job
runs at a time in a workspace; another job that uses it waits with a visible
reason. `dyna job show ID` names the workspace.

The per-job log folder is still removed when the job ends, but the workspace
stays. The engine removes it when its session closes, after 24 hours without
use, or when the node is below its admission disk floor. Low-disk cleanup
uses the least recently used workspace first. A queued or running job always
protects its workspace from cleanup.

## Resources

Repeat `--resource NAME` to hold one unit of each named resource for the
job's whole run. Names use lowercase letters, digits, `-`, `_` and `.`,
up to 64 characters. A name that the node does not declare is a one-unit lock,
so it needs no setup. Declare a larger count with
`dyna node edit N --resource NAME=COUNT`; repeat the flag for more
resources, and use `NAME=0` to remove a declaration. `dyna node show N`
lists the declarations.

For example, cozycraft can serialize access to its one shared test container:

```sh
dyna job run --node hermeslab --resource cozybodies-test --image cozycraft-test -- ./run-tests
```

A job that waits for a resource shows the holding job and its room in
`dyna job show`. Admission counts the resources of running jobs again after
an engine restart.

## Use a job from a workflow

Workflow scripts call `job(spec)`. The spec accepts `command`, `node`,
`labels`, `commit`, `cpus`, `memory`, `workspace`, `timeout`, `idle`,
`image`, `host` and `artifacts`, matching `dyna job run`. Durations are
strings such as `"20m"`. The function waits for the engine's finished event
and returns `{ id, node, state, exitCode, log }`, where `log` is the
`dyna job logs ID` command. Other final states resolve normally; API failures
reject. It does not poll. A resumed workflow waits for its original job ID
instead of submitting again. Stopping the run cancels an unfinished job.

```js
const check = await job({
  command: ['go', 'test', './...'],
  labels: ['os=linux', 'docker'],
  commit: 'HEAD',
  image: 'go:1.25',
  timeout: '20m',
})
```

Read `dyna guide workflows` for the result contract and error handling.

## Read and control

- `dyna job show ID` — the job's state, exit code, command and requested resources.
- `dyna job logs ID` — the last of the job's output.
- `dyna job list [--all]` — open jobs, or every recent one.
- `dyna job cancel ID` — stop a queued or running job.
- `dyna job fetch ID [--to DIR]` — copy a finished job's kept artifacts into
  DIR (default: the current folder), with their paths and file modes.

States: `queued`, `running`, then one of `passed`, `failed`, `cancelled`, `lost`, `timed_out`.

## Events

Each job posts typed events to the session that submitted it: `job.queued`
(quiet, with the wait reason), `job.started` (quiet) and `job.finished` (with
its outcome), which wake that session. Agents never poll for a job's
end; the finished event is the wake.

The jobs and workspaces tables live in `dyna.db`; the logs live under `<data>/jobs/<id>/log` and kept artifacts under `<data>/jobs/<id>/artifacts`.
