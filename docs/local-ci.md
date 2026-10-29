# Repository-defined local CI

Registered CI nodes can run checks for any repository, language, or test
runner. A repository opts in with `.dyna/local-ci.yaml`. Dyna does not supply a
fallback recipe.

A repository without this file keeps its hosted CI unchanged. Dyna does not
start a local run or cancel a hosted run. The PR status says that local CI is
not configured.

## Trust and revisions

Dyna reads the file from Git, not from the working tree. Two revisions have
separate jobs:

- The **head** definition supplies commands, images, caches, and check
  expansion. A PR can therefore repair its own checks.
- The **base** definition supplies trial suites, file selection, and the checks
  required for completeness. A PR cannot remove or rename a check that its own
  merge gate requires.

A changed required set takes effect for later changes after that change merges.
Merge trials use the same rule. The base of a merge trial is the default-branch
commit frozen into that trial.

In `local` and `both` modes a pull request head needs no local result of its
own. The merge queue runs local CI on the merge trial instead: the head merged
into the frozen default-branch commit. A batch of changes runs once, on its tip.
The first conclusive local or hosted result on the trial releases the merge, and
a failure from either provider vetoes it. A local pass must cover every required
suite. When no CI node is enabled, hosted trial CI decides, as in `github`
mode. A pull request can therefore merge while `dyna git ci show <head>` has no
result. To see what gated a landed change, run `dyna git ci show <merge commit>`.

A saved failure or infrastructure result remains conclusive if its old commit
is no longer in the checkout. A passing result must still satisfy the required
base definition.

## Minimal example

This example runs pytest in a Python container. It reuses a pip cache and reads
a JUnit report.

```yaml
version: 1

trial: [test]

caches:
  - name: pip
    target: /cache/pip
    env: PIP_CACHE_DIR

checks:
  - id: pytest
    image: python:3.13-slim
    command: python -m pip install -e . && python -m pytest --junitxml=/reports/pytest.xml
    caches: [pip]
    reports: junit

suites:
  - id: test
    files: ['^src/', '^tests/', '^pyproject\.toml$']
    checks: [pytest]
```

Use a strict YAML subset: exactly one document and `version: 1`. Dyna rejects
unknown keys, duplicate identities, invalid references, and files over 1 MiB.

## Top-level fields

- `trial` lists suites run for every merge trial. It also defines the suites
  eligible for pull-request file selection and their order. A node can limit
  itself to some of these suites; see
  [Registered CI nodes](pr-tracking.md#registered-ci-nodes).
- `caches` declares managed caches that checks can reference.
- `checks` declares executable commands.
- `suites` groups checks for selection, manual runs, and aggregates.

A check can appear in more than one suite. It runs once per run. This is useful
when a small manual suite and a larger aggregate share a command.

## Checks

Each item in `checks` supports these fields:

- `id` is required. It is the stable result identity, with lowercase letters,
  numbers, and hyphens.
- `name` is the provider and display name. It defaults to `id`.
- `command` is required unless `job` is set. Dyna runs it with Bash from
  `/workspace` in a container, or from the check worktree for a host check.
- `labels` is an optional list of node labels. A run that selects this check
  needs one node with every listed label, in addition to the `ci` label.
  Names use lowercase letters, digits, `-`, `_`, and `.`. A label can also
  be `key=value`, such as `arch=x86_64`.
- `image` is the container image. It is required unless `host: true` or `job`
  is set.
- `job` names a GitHub Actions job instead of a command, so one definition runs
  the same work as the provider. Dyna runs the job with a pinned `act` inside
  the usual queue, deadline and report handling. See
  [Workflow job checks](#workflow-job-checks).
- `host: true` runs outside a container. The node owner must allow this;
  see [Host checks](#host-checks).
- `hostUser: true` starts a container as root, creates the host UID and GID,
  and runs the command through `setpriv`. The image needs `getent`, `groupadd`,
  `useradd`, and `setpriv`.
- `setup` is an optional root command for a `hostUser` container. Use it for
  image preparation, such as installing a package. It requires `hostUser`.
- `env` is a map of literal environment values. It can set tool flags and shard
  indices.
- `caches` lists cache names declared at the top level.
- `reports` is `none`, `junit`, or `go`. The default is `none`. `junit` requires
  completed tests in XML. `go` also reads Go JSON test events for failure
  details.
- `providerGroup` groups provider jobs, such as shards of one test job. It
  defaults to the check's display name.
- `hostedAggregate: true` declares that the provider's job of this check's name
  only requires other jobs and runs no test. When Dyna cancels a queued hosted
  run for a local run, it reads a failure of such a job as the cancellation
  echoed back, not as a test result. A suite `group` is read the same way. A
  provider name alone never counts, because a job can share a name and run tests.
- `priority` is a nonnegative integer. Higher values start first. Equal
  priorities keep definition order.
- `cpus` is the CPU count the check asks for, and `memory` its memory, such as
  `memory: 16g`. Both keep a default when they are absent; see
  [Resource limits](#resource-limits).
- `io: heavy` marks a check whose work saturates the node's disk, such as a
  container build or a linker with many object files. A node runs only a few
  heavy checks at once, beside any light ones; `io: light`, the default, needs
  no such room. See [Resource limits](#resource-limits).

For example, this check needs a probed Linux GPU node:

```yaml
  - id: gpu-test
    command: go test ./internal/gpu
    image: golang:1.26
    labels: [os=linux, gpu]
```

The scheduler combines the labels of every check in a run because one node
runs that run's checks. Automatic CI and `dyna git ci run` use the same filter.
Owner labels and probed labels both match. A node probe supplies
`os=<name>`, `arch=<name>`, `gpu` when it finds a GPU, and `docker` or
`containerd` when it finds that runtime. `dyna node show N` and the node
page show owner and probed labels separately.

Dyna supplies isolated home and temporary directories, report paths, a safe Git
directory setting, and `JUNIT_REPORT_PATH`. The runner always writes
`execution.json` from the check's admitted execution time. A `junit` or `go`
command writes its XML under the same report directory.

## Git LFS

Dyna pushes the commit to the CI node with `GIT_LFS_SKIP_PUSH=1` and
without the repository's pre-push hooks. The checkout on the node sets
`GIT_LFS_SKIP_SMUDGE=1`. A check therefore sees Git LFS pointer files, not the
file contents.

A check that needs the real file contents must fetch them itself. Run
`git lfs pull` in the check, with credentials the check provides. Dyna does
not send LFS credentials to the CI node.

A failed push or checkout is a job in the result. That job keeps the git error
text, so a trial names the error instead of an unknown job.

## Workflow job checks

A check with `job` runs a job from a workflow file, in containers `act` starts,
instead of running `command` itself:

```yaml
checks:
  - id: go-static
    job:
      workflow: .github/workflows/ci.yml
      name: go-static
  - id: go-test-7
    job:
      workflow: .github/workflows/ci.yml
      name: go-shard
      matrix: ['shard:7']
```

- `workflow` is one file under `.github/workflows` of the same revision.
- `name` is the job id in that file, not its display name.
- `matrix` selects one configuration of a matrix job, as `key:value`. Each
  configuration is its own check.

The job runs as a `pull_request` event for the revision, with the pull
request's base commit in the event payload. Dyna's own environment does not
reach the job containers: `act` runs with a fixed `PATH`, its own home and an
empty environment file, and jobs cannot use the node's container socket.
Each job container runs as the node user's numeric UID and GID, so files it
writes on mounted folders belong to that user on the node.

A workflow job needs the node's host-check grant, because the workflow
chooses the containers and their options. See [Host checks](#host-checks).

Set `reports: junit` to read the JUnit the workflow uploaded as an artifact.
An upload is stored as an archive, so Dyna unpacks it into the check's report
directory before reading it.

The node's container runtime socket is a node setting, for a host that
runs rootless Podman instead of Docker:

```sh
dyna node edit build-node --container-socket unix:///run/user/1000/podman/podman.sock
```

## Warming the act cache

A job check restores the node's act cache, so a run is fast only when something
filled that cache. Name the provider job that fills it:

```yaml
warm:
  workflow: .github/workflows/ci.yml
  name: cache-warm
```

After the default branch moves, Dyna runs that job through act on each CI
node that serves the repository, into the same cache store the job checks
read. The run takes queue room like every other check, so it never starves a
pull request's checks, and each node remembers the commit it last warmed, so
a commit warms a node once.

## Resource limits

A check runs with 2 CPUs and 4 GiB of memory in a container, and with 2 CPUs and
5 GiB as a host check. A CPU-bound or memory-hungry step asks for more:

```yaml
  - id: cargo-build
    image: rust:1
    command: cargo nextest run --workspace --no-run
    cpus: 8
    memory: 16g
```

`cpus` is a whole number. `memory` needs a unit, `m` or `g`.

Dyna bounds a request by the node, so a repository cannot take more than the
node has: the node's last probe bounds both, and a request above the bound
is clamped. The check's result and its log say so:

```
Dyna clamped cpus from 32 to 16 and memory from 64 GiB to 32 GiB
```

A default is never clamped, so a small node keeps the limits its checks
already had. A container check keeps 1 GiB above its own memory limit in the
systemd scope, so the docker client fits in the scope. A host check's scope is
its only memory bound, so it gets exactly the granted memory.

Every check, and every `dyna job` job, joins one queue per node. The queue
admits a unit of work when its CPUs and memory fit the node's free capacity,
when the node keeps enough free disk (at least 20 GiB and 5% of the disk, but
never more than 10% of it, or under a set `--max-disk-percent` limit), and, for an IO-heavy unit,
when fewer heavy units run than the node's limit. That free-disk rule is the
only one: a node whose disk starts to block work warns each room's root once,
with the queue's own words, and its new work waits in its queue until space is
freed. Recovery frees that same boundary: a run that starts with the disk
blocking removes Dyna's own caches and idle scratch builds first, so the node
that frees its own space admits work again. Work that waits is oldest
first and rooms take turns, and the check's log names the reason it waits, such
as `local CI: 4 of 4 IO-heavy jobs running, position 3 of 7`. The owner or the
room's root sets a node's limits with `dyna node edit <name> --max-io-heavy
N --max-disk-percent P`.

The scope also bounds the tasks a check may start: 768, or 256 for each CPU it
was given when that is more. A compiler and its linker threads need many tasks
per CPU, so a large build that asks for more CPUs gets a larger bound.

Each check starts a build daemon of its own. Dyna gives every check a separate
`SCCACHE_SERVER_PORT`, taken from its place in the node queue, and keeps the
cache directory the repository set. Ending one check therefore stops only its
own sccache server, not a server another check still uses. A check that sets
`SCCACHE_SERVER_PORT` itself keeps that value.

A check holds in the node queue what it asks for. A wide check therefore shares
the node with fewer others, so keep the node's capacity and the requests
in mind together.

## Caches

Each item in the top-level `caches` list has:

- `name`, a stable lowercase cache identity.
- `target`, a clean absolute mount path in the container.
- `env`, an optional environment variable that receives the cache path.

A container check needs `target` on each referenced cache. A host check needs
`env`: the runner sets it to the managed host path. Dyna creates only the caches
a check declares and mounts no others. The names `runs`, `slots`, `diagnostics`,
and `caches` are reserved for runner state.

Cache directories remain owned and locked by Dyna under its separate `caches/`
directory. The runner refuses unsafe symbolic links before mounting them. Every
cache has age cleanup and a size cap. If a root-owned tree defeats the host
cleanup, Dyna retries that tree through a throwaway container on the same
runtime. A failed cleanup posts one node notice with its path and reason.

A cache folder is shared by every run on the node, including runs of other
commits at the same time, so it must hold only what can be shared: a check that
keeps something it mutates there, such as a copied source tree or a work
directory, has to lock it itself.

## Concurrency contract

Many runs, of different commits, can be active on one node at the same time.
Dyna and the repository share the work as follows.

| Thing | Scope | Who keeps it safe |
| --- | --- | --- |
| Source checkout and working directory | One per run | Dyna |
| Build output (`target`, `build`, `node_modules`, `.venv`) | One per run | The check: keep it inside the run's checkout |
| Home, temporary directory and report paths | One per run | Dyna |
| Declared caches | Shared by every run on the node | Dyna mounts them; the tool must be safe for parallel writers |
| Toolchain (compiler, runtime, language version manager) | Shared by every run on the node | Install it once, then treat it as read-only |
| Slot lock and its file descriptors | Dyna only | Dyna closes them before it starts your command |

Rules for a check:

- Never reuse one build directory across commits. Old file times can make a
  build tool trust stale output from another commit. Speed comes from a shared
  compiler cache, not from a shared build directory.
- Declare in `caches` only what is safe for parallel writers: a compiler cache
  such as sccache or ccache, or a package download cache such as the Cargo
  registry, npm, or pip. A content-addressed store also qualifies. Do not put a
  source tree, a build directory, or a language version manager there.
- Install the toolchain once per node, under a lock you take yourself, in a
  separate setup step. Runs then only read it. Two runs that install or update
  components in the same toolchain home at once will conflict.
- Write test reports to the report path Dyna supplies. Never write them to a
  fixed shared path, or two runs will mix their results.
- Pick your own descriptor numbers for the locks your command takes, and stop
  daemons you start. A background server cannot keep queue room after the check
  ends: the check's place in the node queue ends with it.

### Example: a Rust check

This host check shares only the Cargo download cache and the sccache compiler
cache. Each run builds in its own `target` directory inside its checkout.

```yaml
caches:
  - name: cargo-registry
    env: CARGO_HOME
  - name: sccache
    env: SCCACHE_DIR

checks:
  - id: rust
    host: true
    command: |
      set -u
      # Host checks get a temporary HOME, so name a persistent toolchain home.
      # The node owner creates /srv/ci/rustup and makes it writable.
      export RUSTUP_HOME=/srv/ci/rustup
      toolchain=1.85.0
      # Install the pinned toolchain once per node. Later runs skip the install.
      (
        flock 200
        rustup toolchain list | grep -q "^$toolchain" ||
          rustup toolchain install "$toolchain" --profile minimal -c clippy
      ) 200>/srv/ci/rustup.lock || exit 1
      export RUSTC_WRAPPER=sccache
      export CARGO_TARGET_DIR="$PWD/target"
      cargo +"$toolchain" clippy --all-targets -- -D warnings || exit 1
      status=0
      cargo +"$toolchain" nextest run --profile ci || status=$?
      # Copy the report even when tests fail, so Dyna sees each failed test.
      cp target/nextest/ci/junit.xml "$JUNIT_REPORT_PATH"
      exit "$status"
    reports: junit
    caches: [cargo-registry, sccache]
```

The toolchain home and the lock file must live in folders that all runs on the
node share. The per-run home and temporary directories would give each run its
own toolchain and its own lock, so the example uses fixed paths. Host checks
also need the node owner's permission; see [Host checks](#host-checks). Enable JUnit output for the `ci` profile in
`.config/nextest.toml`; the last step copies the file to `JUNIT_REPORT_PATH`. The
same shape works for other languages: replace the toolchain step, the cache
declarations, and the test command (for example `npm ci` with the npm cache, or
`pip install` with the pip cache).

## Suites

Each item in `suites` has:

- `id`, the stable suite name used by `dyna git ci run --suite`.
- `checks`, a nonempty list of check IDs.
- `files`, optional RE2 regular expressions. Dyna matches them against
  repository-relative paths with slashes. A pull request selects a `trial`
  suite when any expression matches any changed file.
- `group`, an optional aggregate check name. Every check in the suite feeds
  this aggregate, and a complete result must include it.

Suites outside `trial` are available for focused manual runs. A suite with no
`files` patterns is not selected by a pull-request diff.

Examples:

```sh
dyna git ci run HEAD --suite test
dyna git ci run HEAD --suite lint --node build-node
dyna git ci show HEAD --json
```

A manual run uses the only enabled CI node in the registry and prints its
name. When several nodes are enabled, the run asks for `--node <name>`.
The `localci.remote` setting no longer selects the node. The selected suites
determine the run's check labels, and the node must have all of them.

## Host checks

Host execution is off by default on every node. A one-time upgrade
migration preserves permission only when saved `computer-smoke` evidence proves
that the old command started. A passed result or a test failure provides that
proof. A setup failure does not. Every other existing node and every node
added later starts off. The repository cannot turn it on, and neither can a
session or a workflow worker. The owner allows it for every room, and the root
of a room allows it for its own room:

```sh
dyna node edit build-node --allow-host-checks
```

A room root's permission covers that room's CI work only, so it never lets
another room's repositories run outside a container. The node record names
who allows host checks, the owner or the roots of rooms, and the Node page
shows those names.

Use this only for repositories you trust with the node account. Host commands
run outside container isolation, although Dyna still applies the check's
systemd scope, timeout, temporary home, and cache guards.

The node page and `dyna node show <name>` show the setting. Disable it
with:

```sh
dyna node edit build-node --disallow-host-checks
```

A host check on a node without permission does not run. Its local result is
an infrastructure error, so hosted CI can resume when available.

## Aggregates and hosted cancellation

Use `group` when several checks must produce one required result. Give sharded
checks the same `providerGroup` so Dyna can recognize the provider's jobs.
Dyna uses this graph to distinguish a real hosted failure from an aggregate
that only repeats cancelled dependencies.
