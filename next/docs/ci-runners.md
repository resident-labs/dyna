# CI runners

Dyna serves a repository's GitHub Actions jobs from one runner scale set per size,
named `dyna-small`, `dyna-medium`, `dyna-large` and `dyna-xlarge`, the box sizes. A
workflow picks the size with `runs-on: dyna-large`, and plain `dyna` names no set.
The engine holds one long poll per set. For each job GitHub assigns, admission picks
a host as it picks one for a box, and the engine starts one ephemeral runner
container of that size there. The container ends with its job.

This replaces v1's local CI. What stays is that CI work runs in a capped container,
off the owner's machine, and that a fork's change cannot decide its own checks. What
goes is Dyna's scheduler, act, trial suites and report parsing: GitHub runs the
workflow and reports the result. v1's node queue becomes admission, and its
free-disk rule becomes the disk floor boxes use.

## Turn it on

The pool does nothing until both of these are true: the repository is on at the
engine, and the owner sets the repository variable. The order matters, so
that no job waits for a pool that does not exist yet.

1. The engine's `GH_TOKEN` needs admin on the repository, and Actions variables
   write for the lease. The pool was tested with the owner's `gh` token, which is
   broader than that. A fine-grained token is untested.
2. Let the engine's user reach the host. The host needs rootless Podman 5 on
   systemd with `cpu`, `memory` and `pids` delegated, `git`, `curl`, and `fuse3`
   with `user_allow_other` in `/etc/fuse.conf`, and that is all: `dyna host add`
   copies the Dyna binary across, and `jq`, `socat` and the rest live in the
   runner image. The engine's key must be a plain one in the
   host user's `authorized_keys`. A key with a forced command cannot run
   anything. SSH once by hand as the engine's user, because the
   engine cannot answer a host key prompt. A laptop or any host that logs out needs
   `sudo loginctl enable-linger USER` first, because rootless containers stop with
   the user's session.
3. Keep sessions off a CI host with `--agents off`. A host has two switches:
   `agents`, on by default, lets new session boxes land there, and `ci`, on by
   default except on the host the engine runs on, lets the pool start runners there. With agents on, every room's boxes
   can land on a host its room list allows, and admission prefers the host with the
   most free memory, so a CI host wants agents off and ci on.

4. Add the host. `--ssh` must be `user@address`, as the engine's user would type it:

   ```sh
   dyna host add pve --ssh build@ADDRESS --owner-only-admin --agents off --ci on
   ```

   For a host already added, `dyna host set pve --agents off --ci on` does the
   same. Boxes already on it stay until they stop. `dyna host list` shows both
   switches.

5. Turn the repository on. `--max` is the most runners it has at once, across
   sizes; without it, admission alone decides:

   ```sh
   dyna room repo ci resident-labs/dyna --on --max 4
   ```

   The first job on a host builds the runner image there, which takes some
   minutes. Check that the lease variable `DYNA_CI_POOL_UNTIL` appears on the
   repository.
6. Set the repository variable `DYNA_CI_SELF_HOSTED` to `true`, and give each pool
   job its size, as `runs-on: dyna-medium`.

Adding a host later is steps 2 to 4. Nothing else changes: no repository names its
hosts.

## Where a runner goes

A runner is placed as a box is, by the same `box.Admit`: a ready host with ci on,
with room for the runner's CPUs and memory and a free disk floor, the one with the
most free memory. Among those, a host that already has the current runner image wins,
whatever its free memory: a host that must build the image first makes the job wait
minutes. Only when no host with the image has room does a runner go to a host that
builds it, so a new host still gets its image. The pool builds the image on each ci
host in the background, outside its lock, when the engine starts or a host is added or
its image changes; a build reuses an image already there, so that is quick. Until a
host's build is done, it counts as not ready. A host where a session box is pending for CPU or memory takes no
new runner when only runners keep that box out, so the box gets the next free slot. Runners and boxes count against each other there, and several
runners share a host while it has room, up to 16, one per slot. A host with ci off
gets no new runner. The agents switch and the room list keep sessions off a host,
so a runner ignores both.

The repository's room is its default room, and its rule (`dyna room hosts ROOM
--only POOL,HOST`) holds its runners as it holds its boxes. With no rule, runners may use any host and any shared pool node. When no host takes a
job and the rule names a pool that can grow, the job waits for a node, as a box
does: the pool makes one of the runner's size, the next poll places the runner
there, and the pool takes the node back once nothing has held it for `--idle`. A
per-session pool makes a node for each job, and the node goes when its runner
does. A node has ci on from the start. While a node can still come, GitHub sees
capacity for it, so the job is assigned and waits.

What GitHub sees as a set's capacity is its runners, plus what admission would
still take, plus the nodes the room's pools could add, within `--max`. A host below
its disk floor or above 80% memory adds nothing, so GitHub stops assigning jobs
there.

## Turn it off

- **Stop new jobs now:** delete `DYNA_CI_SELF_HOSTED`, or set it to anything but
  `true`. New runs use GitHub-hosted runners. Jobs already on the pool finish. The
  switch only reaches `ci-runner.sh`, so a branch that sets `runs-on: dyna-large`
  itself still gets a runner. To stop every job, use `--off`.
- **Take one host out:** `dyna host set pve --ci off`. The pool starts no new
  runner there, and the runners it has finish their jobs. Its proxy goes with the
  last of them. Runners come back with `--ci on`. `dyna host remove` refuses a host
  while a runner is on it.
- **Remove the pool:** `dyna room repo ci resident-labs/dyna --off`. The next tick
  kills that repository's runners, even mid-job, and deletes its four scale sets
  and its registrations. The host's proxy goes with its last runner. On pve that
  took under a minute. GitHub still shows a killed job in progress for about 11
  minutes, then ends it. A queued job has nothing left to serve it, so it waits for
  `pool-watch`, or cancel it by hand.
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

The image is not `ubuntu-latest`: it has no Go or Node, an empty `/etc/machine-id`,
and Podman's default `/dev/shm`. A job installs its own tools, and a test must not
lean on the machine it runs on. A test that skips when a tool is missing passes
without running, so `next` installs Node for the tests in `box/agent`.

`next` fits `medium`, so its jobs say `dyna-medium`. In a `medium` container on pve
it took 2 minutes from a cold cache, and peaked at 2.6 of 6 GiB of tmpfs and 4 of 8
GiB of memory.

## What a runner can touch

Each runner is a rootless Podman container, built from
`next/box/images/ci-runner` and set in `next/box/podman/runner.go`:

- **Host:** two host mounts, the host's cache and the socket of its compiler cache,
  below. No Podman socket, no capabilities, `no-new-privileges`, a read-only image.
  Its process runs as a subordinate UID on the host, not as the host user.
- **Network:** `--network=none`. It has only loopback and no route. The only way
  out is an HTTPS proxy, reached over a Unix socket in the `dyna-ci-egress` volume.
  The proxy (`next/box/egress`) runs on the host and refuses everything but port
  443 on public addresses. It checks the address it dials, so a public name that
  resolves to a private address is refused. Tailnet, LAN, loopback and link-local
  addresses are all refused.
- **Other jobs:** every job on a host shares that proxy, and its socket is how the
  proxy tells them apart. The proxy serves 16 sockets, each in its own folder of the
  volume, and a runner mounts only the one the pool gave it. So a job cannot use
  another's socket, and one job's limits cannot run into another's: at most 256
  tunnels, then `429`, and an even share of 64 MiB/s among the jobs moving data at
  that moment. That rate sits below the uplink so that the proxy decides the shares.
  Left to TCP, the link splits by tunnel and goes to whoever holds the most. A job
  alone on the host gets all of it, and an idle job's long poll takes nothing.
- **Disk:** `/home/runner` and `/tmp` are each a tmpfs capped at three quarters of
  the memory limit, and `/dev/shm` is Podman's default, 64 MB and `noexec`. All of it
  counts against that limit. The one write path that outlives the job is its build
  folder in the host's cache. `/cache` (`DYNA_CI_CACHE`) is the host's cache, the
  folder its boxes mount at `/box/cache`, every room's alike, mounted read-only: a
  job reads what the boxes fetched, and cannot change their files. The build folder
  of the runner's slot, `/cache/ci/N` there, is the one writable mount, at `/build`
  (`DYNA_CI_BUILD`) in every slot. Two of its folders are mounted again where a box
  of the repository has them: `cargo` at `/box/cache/cargo` (`CARGO_HOME`) and
  `target` at `/box/work/<owner>/<repo>/target` (`CARGO_TARGET_DIR`). So Cargo's
  downloads and build stay warm, as on upstream projects' own runners, with no setup
  in the workflow. A slot holds one running runner on its host, and the pool gives slots out
  under its lock, so two jobs never share a build folder at once, whatever their
  sizes or repositories. A job sees the other slots' folders read-only. Only the
  repository's own pushes and pull requests reach the pool. Before a job starts, a
  build folder over 40 GiB is emptied. Everything else a job wrote dies with it.
- **Compiler cache:** Cargo compiles through sccache, which reads and writes the
  host's compiler cache, the one its boxes use, through a socket mounted at
  `/run/dyna-build-cache` that the entrypoint relays to `127.0.0.1:3129` (spec 5.6).
  So a job can change what a box links, which the owner accepts. sccache's keys hold
  the build's paths and every `CARGO_` variable, so a job and a box of its
  repository each find what the other compiled because they share `CARGO_HOME` and
  `CARGO_TARGET_DIR`, except a crate that takes a derive macro or C code, whose keys
  hold what each image's C toolchain made. A job that sets either variable itself,
  or another `CARGO_` variable such as `CARGO_TERM_COLOR`, finds only entries made
  the same way. A job that sets
  `RUSTC_WRAPPER` to nothing builds without it.
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
Add a host like pve with `--agents off`, so no session is placed on it.

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
   repository. If an approved fork rewrites `runs-on: dyna-large`, this is what stops it.

A refusal has to kill the runner's worker. The runner treats a failed hook as a
failed step and carries on, so a step with `if: always()` and any action's pre
step would still run on this host. Any failure in the hook does the same, so a
hook that breaks refuses the job. A hook the runner could not start would count
as one failed step, so the entrypoint checks that it can be read and parsed, and a
runner whose hook cannot be takes no job. That job stays queued for `pool-watch`,
and each failed start leaves an offline registration until the hourly reap.

The killed worker leaves nothing behind. The listener sees it die, fails the job
and exits, as it does after any job, and the pool starts the next runner. On real
runners on pve the job failed about 8 seconds after it started, 3 of them the
hook's pause, and its container and registration were gone at once. The job shows
as failed, with no conclusion on its steps. It may name a worker crash, and the
reason is in its "Set up runner" log. A fork can still spend a runner's start each
time its run is approved.

Do not use `pull_request_target` or `workflow_run` to build pull request code. The
runner refuses both, and they would run a fork's code with this repository's token.
Job-level `env:` does not reach the hook, and a `container:` job cannot move it: the
runner gives the hook an empty environment plus its own `GITHUB_*` values, and runs
it outside any job container.

## When things die

- **The lease:** the engine sets `DYNA_CI_POOL_UNTIL` 15 minutes ahead every 5
  minutes, on every pass, for as long as the pool is alive for the repository: a
  listener of it is running and has polled (one that replaced a failed listener
  counts only after a poll reaches it), and at least one host that is ready, has ci
  on and is allowed by the room's rule answered that pass's survey. A pass whose
  store reads fail aborts with no write. The write comes right after the survey,
  before build-ahead and the sweep, on its own 30-second timeout. A restart, or a
  survey of up to 10 minutes, opens no gap; a survey longer than that (it runs
  under the 20-minute limit) can still let the old lease lapse. Free slots, `--max`, disk, memory and the
  polls' capacity do not matter, so a pool with every slot busy keeps its lease and extra jobs
  queue (see "Pool is busy"). The first pass after an engine start writes it at
  once, so a restart opens no gap.
- **No pool at all:** no host that is ready, has ci on and is allowed answered, or
  the listeners keep failing before they poll (a revoked token, a broken scale
  set), so the lease is not renewed and runs out within 15 minutes. After that, new runs
  use GitHub-hosted runners.
- **Pool is busy:** a job past what the hosts hold, or past `--max`, stays queued on
  GitHub and takes the next free runner. It can wait as long as it needs: nothing
  cancels a job for waiting while the pool is working.
- **Pool dies after a run was routed to it, or cannot start runners:** GitHub would
  let the job wait 24 hours. The `pool-watch` job, on a hosted runner, steps in once
  the run has had work queued and no progress for 20 minutes. It then looks at pool
  jobs across the repository, and cancels the run only if two looks 3 minutes apart
  find none running or just finished. That takes 25 minutes at most. A rerun goes
  hosted once the lease is gone. A job that is stuck in progress holds no one up
  for longer than its own `timeout-minutes`.
- **Engine or listener dies:** jobs in progress finish, since runners do not need
  the engine. Queued jobs wait. The next listener reads the assigned count and
  starts their runners. A session left by a crash holds the set for a few seconds,
  and the engine retries until it clears. Two engines cannot share a set.
- **Host drops off the network mid-job:** the host leaves the capacity GitHub sees,
  and jobs go to other hosts. GitHub shows the runner offline within about a minute
  and fails its job after about 10. A rerun picks another runner. The hourly reap
  deletes the registrations of idle runners on that host, and a runner whose
  registration is gone exits by itself within a minute of the host's return.
- **Proxy dies:** a crash restarts it. If it was stopped, or the host restarted, the
  pool's next tick starts it, within 30 seconds. Runners keep their sockets, and a
  job loses only the tunnels it had open.
- **Disk below the floor, or memory above 80%:** admission refuses the host, and
  capacity falls to what is already running.

## Requests

Long polls and JIT configs go to the Actions service, not the REST API, so they
spend none of the budget in spec 7.7. REST use is one lease write every 5 minutes (valid for 15),
one registration list an hour, and the token exchange when the client's admin
token expires. Below 20% of quota the REST client pauses at least 10 minutes.

`github.com/actions/scaleset` is a public preview, MIT licensed. Only
`next/engine/ci/scaleset.go` imports it, behind the `Scaleset` interface. It is
pinned to v0.4.0 because v0.5.0 needs Go 1.27.3 and `next/` is on 1.26.
