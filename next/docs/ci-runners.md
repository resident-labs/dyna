# CI runners

Dyna serves a repository's GitHub Actions jobs from one runner scale set, named
`dyna`. Workflows say `runs-on: dyna`. The engine holds one long poll per
repository. For each job GitHub assigns, admission picks a CI host and the engine
starts one ephemeral runner container there. The container ends with its job.

This replaces v1's local CI. What stays is that CI work runs in a capped container,
off the owner's machine, and that a fork's change cannot decide its own checks. What
goes is Dyna's scheduler, act, trial suites and report parsing: GitHub runs the
workflow and reports the result. v1's node queue becomes admission, and its
free-disk rule becomes the disk floor boxes use.

## Turn it on

The pool does nothing until both of these are true: the repository is in box mode
on the engine, and the owner sets the repository variable. The order matters, so
that no job waits for a pool that does not exist yet.

1. The engine's `GH_TOKEN` needs admin on the repository, and Actions variables
   write for the lease. The pool was tested with the owner's `gh` token, which is
   broader than that. A fine-grained token is untested.
2. Add the host, if it is new: `dyna host add NAME --ssh user@host --owner-only-admin`.
   A laptop or any host that logs out needs `sudo loginctl enable-linger USER`
   first, because rootless containers stop with the user's session. A host is for
   sessions too: with no `--room`, every room's boxes can land on it, and admission
   prefers the host with the most free memory. Read the warning under
   [What a runner can touch](#what-a-runner-can-touch) first.
3. Turn the repository on. COUNT is the most runners at once on that host:

   ```sh
   dyna room repo ci resident-labs/dyna --mode box --host pve=4
   ```

   The first job builds the runner image on the host, which takes some minutes.
   Check that the lease variable `DYNA_CI_POOL_UNTIL` appears on the repository.
4. Set the repository variable `DYNA_CI_SELF_HOSTED` to `true`.

Adding a host later is step 2, then the same command with one more `--host`.
Nothing else changes. Use the same size for every host: GitHub gives a job to any
idle runner of a scale set, so one set cannot promise a job a size. A second size
needs a second scale set, which is not built.

## Turn it off

- **Stop new jobs now:** delete `DYNA_CI_SELF_HOSTED`, or set it to anything but
  `true`. New runs use GitHub-hosted runners. Jobs already on the pool finish. The
  switch only reaches `ci-runner.sh`, so a branch that sets `runs-on: dyna` itself
  still gets a runner. To stop every job, use `--mode github`.
- **Remove the pool:** `dyna room repo ci resident-labs/dyna --mode github`. The
  next tick kills that repository's runners, even mid-job, and deletes its scale
  set and registrations. The host's proxy goes with its last runner.
- **Revoke:** do both, then rotate the engine's `GH_TOKEN`. The repository's
  Settings > Actions > Runners page shows whatever is left, and each entry there
  can be removed by hand. A JIT config is good for one job on one runner, so there
  is no long-lived runner credential to revoke.

## What runs here

`ci.yml` sends `next`, `next-extras` and `next-web` to the pool. `changes` and
`pool-watch` stay on GitHub-hosted runners, because they decide things while the
pool may be down. `relay` needs a container runtime. `next-pi` stays hosted because
one Pi test rewrites a file with a same-size value and expects the change to show:
on tmpfs both writes can land in one clock tick, so size and time both match.
`next-web-width` is hosted too. It has not been tried on the pool.

## What a runner can touch

Each runner is a rootless Podman container, built from
`next/box/images/ci-runner` and set in `next/box/podman/runner.go`:

- **Host:** no host mounts, no Podman socket, no capabilities, `no-new-privileges`,
  a read-only image. Its process runs as a subordinate UID on the host, not as the host
  user.
- **Network:** `--network=none`. It has only loopback and no route. The only way
  out is an HTTPS proxy, reached over one Unix socket in the `dyna-ci-egress`
  volume. The proxy (`next/box/egress`) runs on the host and refuses everything
  but port 443 on public addresses. It checks the address it dials, so a public
  name that resolves to a private address is refused. Tailnet, LAN, loopback and
  link-local addresses are all refused. Every job on a host shares this proxy, and
  it cannot tell them apart. It caps tunnels and request size, so one job cannot
  crash it, but a job that holds every tunnel starves the others until it ends.
- **Disk:** everything writable is a tmpfs capped at three quarters of the memory
  limit, and tmpfs counts against that limit. So a job cannot grow the host's disk
  at all, and all it wrote dies with it.
- **Secrets:** the only one is its JIT config. It reaches `podman run` on standard
  input, never argv. Podman keeps it in the container's config, and conmon has it
  in its environment, both readable only by the host user, until the container
  goes. A job can read it from its own runner, and by then it is spent.
- **Time:** a runner ends after 6 hours, job or no job. A job can stop that clock,
  since it shares the entrypoint's user, so Podman ends the container 10 minutes
  later from outside it.
- **Kernel:** the container shares the host's kernel, and on an LXC guest that is
  the hypervisor's kernel. A job has no capabilities, but the default seccomp profile
  lets it create a user namespace, and inside that it can reach kernel code such as
  nftables. A kernel bug is therefore a way out that ends above the subordinate UID.
  Keep CI hosts on a patched kernel.

A host's `containers.conf` can make host networking the default, as pve's does,
so the runner passes `--network=none` every time. A session box passes no network
flag, so on such a host it runs in the host's network: it can reach the LAN and the
host's loopback services, see host abstract sockets, listen on the host's addresses,
and reach every other box on that host. `dyna host add` does not check for this.
Do not add a host like pve while sessions can be placed on it.

pve is an LXC guest with no `/dev/net/tun`. pasta and slirp4netns cannot start
there, which is why the way out is a proxy and not a filtered network.

## Forks

Three layers. The first is a repository setting, and a pull request can edit the
second, so the third is the one that holds:

1. GitHub's fork approval setting holds a fork's run until the owner approves it.
   This repository uses `all_external_contributors`, which holds every run. With
   the weaker `first_time_contributors`, a returning contributor is not held.
2. `next/scripts/ci-runner.sh` routes a fork's pull request to `ubuntu-latest`.
3. `job-started.sh` is in the runner image, so no pull request can edit it. It runs
   before any step, reads the head repository from the event GitHub sent, and
   refuses any job that is not a push, a manual run, or a pull request from this
   repository. If an approved fork rewrites `runs-on: dyna`, this is what stops it.

A refusal has to kill the runner's worker. The runner treats a failed hook as a
failed step and carries on, so a step with `if: always()` and any action's pre
step would still run on this host. The job shows as failed. It may name a worker
crash, and the reason is in its "Set up runner" log. A fork can still spend a
runner's start each time its run is approved.

Do not use `pull_request_target` or `workflow_run` to build pull request code. The
runner refuses both, and they would run a fork's code with this repository's token.
Job-level `env:` does not reach the hook, and a `container:` job cannot move it: the
runner gives the hook an empty environment plus its own `GITHUB_*` values, and runs
it outside any job container.

## When things die

- **No pool at all:** the engine renews `DYNA_CI_POOL_UNTIL` every 5 minutes for
  10 minutes, only while a poll is alive and some host has capacity. Once it
  expires, new runs use GitHub-hosted runners.
- **Pool dies after a run was routed to it:** GitHub would let the job wait 24
  hours. The `pool-watch` job, on a hosted runner, cancels the run after 20 minutes
  with work queued and no progress. A rerun goes hosted once the lease is gone.
- **Engine or listener dies:** jobs in progress finish, since runners do not need
  the engine. Queued jobs wait. The next listener reads the assigned count and
  starts their runners. A session left by a crash holds the set for a few seconds,
  and the engine retries until it clears. Two engines cannot share a set.
- **Host drops off the network mid-job:** the host leaves the capacity GitHub sees,
  and jobs go to other hosts. GitHub shows the runner offline within about a minute
  and fails its job after about 10. A rerun picks another runner. The hourly reap
  deletes the registrations of idle runners on that host, and a runner whose
  registration is gone exits by itself within a minute of the host's return.
- **Disk below the floor, or memory above 80%:** admission refuses the host, and
  capacity falls to what is already running.

## Requests

Long polls and JIT configs go to the Actions service, not the REST API, so they
spend none of the budget in spec 7.7. REST use is one lease write every 5 minutes,
one registration list an hour, and the token exchange when the client's admin
token expires. Below 20% of quota the REST client pauses at least 10 minutes.

`github.com/actions/scaleset` is a public preview, MIT licensed. Only
`next/engine/ci/scaleset.go` imports it, behind the `Scaleset` interface. It is
pinned to v0.4.0 because v0.5.0 needs Go 1.27.3 and `next/` is on 1.26.
