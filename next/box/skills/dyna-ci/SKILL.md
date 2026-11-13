---
name: dyna-ci
description: Use when you write, debug or speed up a GitHub Actions workflow, whether its jobs run on Dyna's self-hosted runners or on GitHub-hosted ones
---

# GitHub Actions on Dyna's runners

Dyna runs a repository's Actions jobs on its own hosts, one runner container per job, sized by `runs-on` and gone
when the job ends. The repositories it serves are on dyna_room's "Dyna CI runners" line. Turning a repository's
runners on or off, its runner maximum, hosts and host rules are root's (skill dyna-admin); others ask their parent.

## Route a job

`runs-on` names one label alone, not an array with `self-hosted`: `dyna-small` (2 CPUs, 4 GiB), `dyna-medium`
(4, 8 GiB), `dyna-large` (8, 16 GiB) or `dyna-xlarge` (16, 32 GiB). Before any step, `always()` and action pre
steps included, the runner refuses a job from another repository and any event but `push`, `workflow_dispatch` and
a `pull_request` from the same repository; its "Set up runner" log says why. So fork PRs go to GitHub, and PR code
is never built under `pull_request_target`. A runner ends after 6 hours: keep `timeout-minutes` well under 360.

While the pool serves the repository, the engine keeps its variable `DYNA_CI_POOL_UNTIL` at a Unix time 15 minutes
ahead, renewed every 5 minutes. This job, under `jobs:`, sends fork PRs, and every job while the pool is down, to
GitHub; a job then sets `needs: route` and `runs-on: ${{ needs.route.outputs.runner }}`:
```yaml
  route:
    runs-on: ubuntu-latest
    outputs:
      runner: ${{ steps.pick.outputs.runner }}
    steps:
      - id: pick
        env:
          LEASE: ${{ vars.DYNA_CI_POOL_UNTIL }}
          SAME_REPO: ${{ github.event_name != 'pull_request' || github.event.pull_request.head.repo.full_name == github.repository }}
        run: |
          [ "$SAME_REPO" = true ] && [ "${LEASE:-0}" -gt "$(date +%s)" ] && r=dyna-medium || r=ubuntu-latest
          echo "runner=$r" >> "$GITHUB_OUTPUT"
```

## Files, tools and network

The job runs as user 1001 on a read-only root, so `apt-get` and `sudo` fail. The image is Ubuntu 24.04 with
build-essential, git, curl, jq and python3, without Go, Node or Rust: setup actions install them. `$HOME` (with the
checkout) and `/tmp` are tmpfs, each capped at three quarters of the size's memory and counted against it.
`/build` (`DYNA_CI_BUILD`) is the host slot's persistent folder: its `cargo` is mounted at `CARGO_HOME` and its
`target` at `CARGO_TARGET_DIR`, the paths a box of the repository uses. The next job on a slot may be another
repository's, so never rely on what an earlier job left; a folder over its cap when a job starts (80 GiB, less on a
small disk, never under 40 GiB) loses `target`, then everything. `DYNA_CI_CACHE` is the host's box cache, read-only.
The only network is an HTTPS proxy (`https_proxy`) to port 443 of public addresses: plain HTTP, SSH remotes such as
`git@github.com:`, other ports and private or LAN addresses fail, as does a tool that ignores the proxy.

## Speed up Rust builds

On Dyna, rustc runs through `RUSTC_WRAPPER=sccache` into the host's compiler cache, shared with its boxes; a cache
failure falls back to compiling, and an empty `RUSTC_WRAPPER` opts out. Set `CARGO_INCREMENTAL=0` (or the profile
below) for the workspace's own crates to hit it. Keys hold paths and every `CARGO_` variable: setting `CARGO_HOME`,
`CARGO_TARGET_DIR` or another, such as `CARGO_TERM_COLOR`, misses what boxes built. A profile for CI keeps `dev` as
it is: `[profile.ci]` with `inherits = "dev"`, `debug = 0` and `incremental = false`; run `cargo test --profile ci`.
Jobs on GitHub-hosted runners start on a fresh machine: after the toolchain step, add a step with
`if: runner.environment == 'github-hosted'` and `uses: Swatinem/rust-cache@v2`. On Dyna, add no `actions/cache`
for `target`: `/build` and sccache already cover it, and moving it through the proxy makes it slower.

## Add packages to the runner image

A room image named `ci-runner` adds layers for every repository of the room, and a PR cannot change it. Ask your
parent first, since every job of the room gets it. Its folder holds a Containerfile:
```Dockerfile
ARG DYNA_CI_BASE
FROM ${DYNA_CI_BASE}
USER root
RUN apt-get update && apt-get install -y --no-install-recommends mesa-vulkan-drivers && rm -rf /var/lib/apt/lists/*
USER 1001:1001
```
Save it with the image tool or `dyna image save ci-runner ./dir`, read it with `dyna image show ci-runner`, undo
with `dyna image remove ci-runner`. Dyna's user, entrypoint, read-only root and network hold whatever it says. A
host builds it, in minutes, when a job there first needs it; a failed build runs jobs on the shared image. So check
with a job that uses the new package; if it is missing, root reads the build error in `dyna host list` (dyna-admin).

## Inspect and control a run

Read runs, jobs and job step status with the GitHub tools `actions_list` and `actions_get`, and logs with
`get_job_logs` after the job ends: GitHub gives no log of a running job, so a missing one does not mean it is stuck.
Dispatch, rerun and cancel use `actions_run_trigger`, present when dyna_room's "GitHub writes" line includes
`actions`; without it, ask your parent (root: dyna-admin).

## When runs look stuck

Check in order: a queued run with no jobs is not expanded by GitHub yet; a job on `ubuntu-latest` or a "GitHub
Actions" runner never touches Dyna; during an Actions or Webhooks incident on https://www.githubstatus.com, wait
for recovery without rerunning or pushing; a `dyna-*` job of a repository off the "Dyna CI runners" line never
runs. Only `dyna-*` jobs queued over 15 minutes with no runner point at Dyna: tell your parent the run ID, the
job's label and the wait (root: dyna-diagnose).
