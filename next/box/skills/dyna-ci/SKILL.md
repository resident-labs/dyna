---
name: dyna-ci
description: Use when you write, debug or speed up a GitHub Actions workflow, whether its jobs run on Dyna's self-hosted runners or on GitHub-hosted ones
---

# GitHub Actions CI

## Route a job

Use one scale-set label as `runs-on`: `dyna-small`, `dyna-medium`, `dyna-large` or `dyna-xlarge`, not an array with `self-hosted`. Each repository has its own scale sets. The owner enables the repository's CI; naming a label alone does not enable runners.

Each runner is ephemeral: one job in one container. The entrypoint ends after 6 hours even if no job arrives, with a 30-second kill grace. Set a workflow's job timeout below that bound if it needs time to report a failure.

The image's job-started hook refuses a different repository and fork pull requests before any step runs, including `always()` and action pre steps. It accepts `push`, `workflow_dispatch` and same-repository `pull_request` events, not `pull_request_target`. Keep outside-contributor fork PRs on GitHub-hosted runners.

## Files and network

The runner is user 1001 with a read-only root and dropped capabilities. `$HOME` (`/home/runner`) and `/tmp` are size-capped tmpfs; files there disappear with the container. A normal Actions checkout is under the runner's home, not a persistent checkout.

Each host slot has a writable persistent build folder at `/build` (`DYNA_CI_BUILD`). Its `cargo` folder is mounted at `CARGO_HOME=/box/cache/cargo`, and its `target` folder at `CARGO_TARGET_DIR=/box/work/<owner>/<repo>/target`. These survive jobs on that slot, not moves to another host or slot. The host's cache is also mounted read-only at `DYNA_CI_CACHE`; it is not a writable checkout. Do not rely on an earlier job's workspace being present.

The container has no direct network. Its HTTP proxy reaches public addresses only, not private host or LAN services. Tools need to honor the proxy environment. Installing system packages in a workflow cannot change the read-only root; use the room image below.

## Rust compiler cache

Cargo compiles through `RUSTC_WRAPPER=sccache`. It reads and writes the host's shared compiler cache, which that host's boxes use too. Cache failure falls back to compiling. Set `RUSTC_WRAPPER` to an empty value to opt out.

sccache skips incremental compiles. Set `CARGO_INCREMENTAL=0` in a workflow for its own crates to hit the cache. Dependencies benefit without that change. Cache keys include compiler paths, the working directory and `CARGO_` variables: keep the fixed `CARGO_HOME` and `CARGO_TARGET_DIR` above to share entries with boxes. Changing them or adding `CARGO_` variables can cause misses. Identical source alone does not guarantee a hit across different checkout paths or toolchains.

Use a separate Cargo CI profile on either runner, keeping local `dev` builds unchanged. `debug = 0` reduces build artifacts; `debug = "line-tables-only"` keeps filename/line backtraces instead. `incremental = false` makes the sccache advice structural. Run `cargo test --profile ci`:
```toml
[profile.ci]
inherits = "dev"
debug = 0
incremental = false
```

## Jobs on GitHub-hosted runners

For public repositories whose jobs cannot use Dyna runners, including fork PRs, use `ubuntu-latest`. GitHub-hosted jobs have fresh machines, not Dyna's persistent `/build` or provided sccache configuration. Cache Cargo dependencies and build output with an action keyed by `Cargo.lock` and the toolchain, such as `Swatinem/rust-cache` (which includes those keys). Select the Rust toolchain before restoring its cache.

On Dyna, do not add `actions/cache` for `target`: persistent `/build` mounts and host sccache already cover it; uploading and restoring artifacts through the proxy can cost more time than it saves. Keep runner-specific setup in separate conditional steps. For a repository with Dyna CI enabled, this job routes fork PRs to GitHub and other events to Dyna; add a GitHub-hosted fallback for disabled or unavailable Dyna CI as needed:
```yaml
jobs:
  rust:
    runs-on: ${{ github.event.pull_request.head.repo.fork && 'ubuntu-latest' || 'dyna-medium' }}
    steps:
      - uses: actions/checkout@v6
      - uses: dtolnay/rust-toolchain@stable
      - if: runner.environment == 'github-hosted'
        uses: Swatinem/rust-cache@v2
      - if: runner.environment == 'self-hosted'
        run: sccache --show-stats
      - run: cargo test --profile ci
```

## Capacity

Check `dyna host list` before requesting a change. Ready hosts with `ci` on take runners through the same admission as boxes; placement prefers hosts with the image ready and then available CPU and memory headroom. A session box waiting for CPU or memory takes priority over new runners. A repository's runner maximum, host slot limit and room host rules also bound placement.

The owner's host `ci` switch controls new runners, not ones already running. `dyna host set NAME --ci on|off` changes it. New hosts normally have CI on except the engine host; existing hosts retain their switch. These changes are owner-only. The host's `ci_scale` (`dyna host set NAME --ci-scale 1-4`, owner-only) multiplies CI CPU and memory reservation budgets, default 1: twice host CPUs and once host memory at 1. It does not multiply physical resources. The real-memory guard and 16-runner slot cap still apply.

A room's `dyna room hosts ROOM --only POOL[,HOST...]` rule restricts both boxes and its repository's runners; `--clear` removes it. A room without a rule may use shared pool nodes. A queued job that no host takes can grow the pool named by the room's rule, and its node goes after the pool's idle interval once unused. See dyna-nodes for adding hosts and pools; asking for a machine or a host-rule change follows the room's approval rules.

## CI runner image

To add packages or drivers, save a room image named `ci-runner`. Unlike a box image, it starts from `${DYNA_CI_BASE}`:

```Dockerfile
ARG DYNA_CI_BASE
FROM ${DYNA_CI_BASE}
USER root
RUN apt-get update && apt-get install -y --no-install-recommends mesa-vulkan-drivers && rm -rf /var/lib/apt/lists/*
USER 1001:1001
```

Save it with `dyna image save ci-runner ./dir` (or the `image` tool). Ask your parent first because it affects the room's jobs. The repository's PRs cannot change this room image.

- Layers add to the shared runner image. Dyna enforces the entrypoint, user, read-only root, no direct network and dropped capabilities; image settings do not override them. Files replaced in a layer affect only that room's runners.
- The first build on each CI host takes minutes in the background. A job may wait or use the shared image; saving again rebuilds.
- A failed build falls back to the shared image. `dyna host list` shows the build error; fix the image and save again.
- On a host, `podman ps --filter label=dyna.ci` shows `dyna-ci-room-<room>:<id>-<hash>` for a room image or `dyna-ci-runner:<hash>` for the shared one. `dyna image show ci-runner` gives the id.

## Inspect and control a run

The room's GitHub `actions` write group allows dispatch, rerun and cancel on branches the caller may push. The Actions tools describe that scope and their refusals. Load them as needed; the setting's allowed repositories still apply.

GitHub does not provide live step log downloads while a job is running. Use job step status to see the current step, and fetch logs after the job completes. A missing running-job log is not evidence that the job is stuck.

**When runs look stuck.** A room configures its own CI: each job's label, GitHub-hosted or Dyna runners, the `ci-runner` image and its workflows; turning CI on for a repository, its runner limits, the host settings above and the pool's code stay with the owner and room dyna. Check in this order before reporting the pool as broken. First, list a queued run's jobs: a queued run with no jobs has not been expanded by GitHub, so the pool has nothing to place. Second, read each waiting job's label and runner name: a job on `ubuntu-latest`, or running on a "GitHub Actions" runner, never touches Dyna's pool. Third, check https://www.githubstatus.com for an open Actions or Webhooks incident; during one, do not rerun or push to retrigger, and look at the queue again once GitHub reports recovery. Only when jobs with a `dyna-*` label stay queued for several minutes with no runner taking them is Dyna's placement in question (capacity, the repository's CI switch or the pool). A session cannot read `dyna host list` itself, so report to room dyna's root, which can, with the run ID, the job's label and how long it has waited.
