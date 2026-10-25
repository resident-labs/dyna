# Repository-defined local CI

Registered CI machines can run checks for any repository, language, or test
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
  eligible for pull-request file selection and their order.
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
- `command` is required. Dyna runs it with Bash from `/workspace` in a
  container, or from the check worktree for a host check.
- `image` is the container image. It is required unless `host: true` is set.
- `host: true` runs outside a container. The machine owner must allow this;
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
- `priority` is a nonnegative integer. Higher values start first. Equal
  priorities keep definition order.

Dyna supplies isolated home and temporary directories, report paths, a safe Git
directory setting, and `JUNIT_REPORT_PATH`. The runner always writes
`execution.json` from the check's admitted execution time. A `junit` or `go`
command writes its XML under the same report directory.

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
cache has age cleanup and a size cap.

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
dyna git ci run HEAD --suite lint --node build-machine
dyna git ci show HEAD --json
```

## Host checks

Host execution is off by default on every machine. A one-time upgrade
migration preserves permission only when saved `computer-smoke` evidence proves
that the old command started. A passed result or a test failure provides that
proof. A setup failure does not. Every other existing machine and every machine
added later starts off. The repository cannot turn it on. Only the machine owner
can explicitly allow it:

```sh
dyna machine edit build-machine --allow-host-checks
```

Use this only for repositories you trust with the machine account. Host commands
run outside container isolation, although Dyna still applies the check's
systemd scope, timeout, temporary home, and cache guards.

The machine page and `dyna machine show <name>` show the setting. Disable it
with:

```sh
dyna machine edit build-machine --disallow-host-checks
```

Removing a machine's CI role also clears the permission. A host check on a
machine without permission does not run. Its local result is an infrastructure
error, so hosted CI can resume when available.

## Aggregates and hosted cancellation

Use `group` when several checks must produce one required result. Give sharded
checks the same `providerGroup` so Dyna can recognize the provider's jobs.
Dyna uses this graph to distinguish a real hosted failure from an aggregate
that only repeats cancelled dependencies.
