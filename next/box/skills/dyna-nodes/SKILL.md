---
name: dyna-nodes
description: Use when you need to add nodes to Dyna, or a room needs its own or disposable machines
---

# Add nodes to Dyna

Dyna decides when a node is needed. A small script, a node pool, knows how to get one, so Boat, EC2 or anything else works and Dyna never knows which. A node is an ordinary host: the host check, heartbeats, the shared folder and images all work as they do on any other.

## The script

Write it in any language. It answers three commands:

```
pool.sh create NAME SIZE   # prints JSON: {"ssh": "user@host:port"}
pool.sh destroy NAME
pool.sh list               # prints a JSON list of names it still has
```

- `create` returns only when the machine answers SSH. `SIZE` is the box's size: agent, small, medium, large or xlarge. Print the JSON and nothing else on stdout.
- `destroy` of a name it no longer has still exits 0.
- `list` names only machines this pool made, so give them a prefix of their own.
- Anything on stderr ends up in the engine's log and in the failure notice. A non-zero exit is a failure.

The script runs on the engine host as the owner's user, with a timeout of 15 minutes for `create` and 5 minutes for the others, and with no secrets from Dyna. It gets `PATH`, `HOME`, `USER`, `LANG` and `XDG_RUNTIME_DIR`, and it reads what credentials it needs the way the owner's own commands do. Keep it under 64 KiB. No box can reach it.

## Host prerequisites

Every host, including an exec-only host, needs SSH access to an account with bash and procps `pgrep` on its command path. Dyna uses `pgrep` to confirm process-group termination when canceling remote work.

A node that runs boxes or CI must also pass the full host check, so the script must prepare these additional requirements, or start from a ready image:

- rootless Podman 5 or later with the systemd cgroup manager, and delegated `cpu`, `memory` and `pids` controllers
- FUSE, with `user_allow_other` in `/etc/fuse.conf`
- OpenSSH with stream-local forwarding, `curl` and `git`
- the engine's SSH key in the node user's `authorized_keys`; Dyna takes the node's host key on first contact
- the path `/home/pip/.local/share/dyna-next-host` writable by the node's user, so the usual user is `pip`
- more than 4 GiB of memory, and 25 GiB of free disk or more for the smallest box

A node that fails the check is destroyed and counts as a failed create.

A complete example, which rents a server by name from a made-up `rent` command:

```bash
#!/usr/bin/env bash
set -euo pipefail
PREFIX=boat-

case "$1" in
  create)
    name=$2 size=$3
    case "$size" in
      agent|small) plan=s ;;
      medium) plan=m ;;
      *) plan=l ;;
    esac
    # A ready image holds Podman, FUSE, the pip user and the engine's public key.
    address=$(rent create "$PREFIX$name" --plan "$plan" --image dyna-node --output ip)
    for _ in $(seq 1 60); do
      nc -z "$address" 22 2>/dev/null && break
      sleep 5
    done
    printf '{"ssh": "pip@%s:22"}\n' "$address"
    ;;
  destroy)
    rent delete "$PREFIX$2" --if-exists
    ;;
  list)
    rent list --output names | sed -n "s/^$PREFIX//p" | jq -R . | jq -s .
    ;;
  *)
    echo "unknown command: $1" >&2
    exit 2
    ;;
esac
```

## Test it once

Run `create` and `destroy` by hand before you add it, and check that the machine really goes:

```bash
./boat-pool.sh create test-1 agent
ssh -p 22 pip@ADDRESS true
./boat-pool.sh list
./boat-pool.sh destroy test-1
./boat-pool.sh list
```

## Add it

```bash
dyna pool add boats --script ./boat-pool.sh --per-session --max 6
dyna room hosts horizon --only boats
dyna pool list
```

- `--max` is required. It is the most nodes the pool may hold, counting those being created.
- `--per-session` gives each box its own node, which goes when the session closes.
- Without `--per-session`, sessions share nodes, and a node destroyed after it has held no box for `--idle`, 30 minutes by default.
- `dyna room hosts ROOM --only POOL[,HOST...]` keeps a room's new boxes on one pool and these hosts, and the pool grows only for such a room. A box already placed stays where it is. `--clear` removes the rule. A room with no rule may use any host and any shared pool node, but a pool grows only for a room whose rule names it. Clearing a rule frees the room to use shared pool nodes as well as hosts; it does not make a pool grow for the room.
- From a box, `dyna pool add` and `dyna room hosts` ask the owner, because the script runs on the engine host and costs money.
- Only the owner changes a pool's `--max`, `--idle` and `--per-session` later, in Settings > Pools; no box command does.

## Add one machine by hand

Only the owner adds a host, so from a box the call asks the owner.

```bash
dyna host add NAME --ssh user@addr --owner-only-admin
```

- `--ssh` is `user@host`, with no port. `--owner-only-admin` confirms that only the owner administers the machine. A host that runs boxes or CI runners needs it, and `host add` refuses without it. A host added `--agents off --ci off` is exec only, takes host-exec runs and nothing else, and needs neither the flag nor the additional box/CI requirements: it passes the base host checks described above.
- For any other host the check needs what the pool nodes need, from rootless Podman 5 down to the free disk, and it reports each item. FUSE is not checked on the host the engine runs on.
- The engine runs plain `ssh user@addr` as its own user and names no key, so the engine user's ssh config picks the key per host (on hermeslab, entries point at `~/.ssh/dyna-engine_ed25519`); a machine with no entry uses the engine user's default keys or agent. The public key of whichever applies has to be in `authorized_keys` of `user` on the machine, as a plain key with no forced command. Dyna takes the machine's host key on first contact and keeps it in a list of its own, apart from the owner's `known_hosts`.

## What `dyna host remove` leaves

`dyna host remove NAME` removes the row, the engine's SSH connection and its forwards. It runs nothing on the machine, which keeps:

- the host data folder (`--host-data` of the engine, `~/.local/share/dyna-next-host` here): `cache/`, `bin/` with the Dyna binaries, `share/`, `git/` and `rooms/`
- the user units `dyna-share.service` and `dyna-build-cache.service`, started with `systemd-run --user`, which run until stopped (`systemctl --user stop`) or the machine restarts
- Podman's images, among them `localhost/dyna-ci-runner:*` and `localhost/dyna-ci-room-*`, which `podman rmi` removes

Runners run rootless as user 1001, so files they wrote belong to IDs that Podman maps into the user's subordinate range. The host's user gets `Permission denied` from `rm` on them, and `podman unshare rm -rf PATH` removes them, since it runs as the container's root.

## What to expect

- A box waits with `pool-node` while its node comes up.
- A failed create removes the node and leaves the box waiting with `pool-failed`. The pool tries again after a wait of a minute, doubling up to an hour. `dyna pool list` shows the last problem, and the room gets one notice for each spell of failures.
- A dormant session keeps its node, because its box is stopped, not gone. The node costs money until the session closes.
- The pool audits `list` every 10 minutes. A machine Dyna has no record of is reported once and never destroyed. A node the script no longer lists is treated as a lost host, and its box waits for it. If the machine is gone for good, the owner closes the session with `dyna session close --discard`.
- A Run from a subscription never uses a pool's node.
- CI runners of the room's repositories use its pool as its boxes do: a job no host takes gets a node of its size, which goes after `--idle` once its runner has gone.
- `dyna host remove NAME` removes a node through its script once nothing is on it.
- `dyna pool remove NAME` needs an empty pool that no room names.

## CI capacity

CI runners go where boxes go, through the same admission, and balance by themselves, so a root rarely needs to change a switch:

- A host takes runners when its `ci` switch is on. New hosts start with it on, except the host the engine runs on, so a host the owner adds takes CI with no flag. Hosts added before this keep the switch they had; turn it on with `dyna host set NAME --ci on`.
- A runner starts on the ready host with the most free memory, up to its memory and twice its CPUs, so a busy host takes fewer.
- Session boxes come first: no runner starts on a host where a session box that runners are keeping out waits for CPU or memory.
- A room with no rule may use shared pool nodes for runners and boxes. A rule restricts both to what it names.
- A room's rule holds its repository's runners too, and a job no host takes waits for a node of the pool the rule names.

To change this, a root asks the owner with the owner op: `dyna host set NAME --ci on|off` for one host, and `dyna room hosts ROOM --only ...` or `--clear` for a room's rule. Check `dyna host list` first.

## A host with a GPU

A host offers its cards to boxes once its NVIDIA Container Toolkit is set up, which is a one-time step on that host, as the user that runs Podman:

```
sudo apt install nvidia-container-toolkit   # or the distribution's package; the NVIDIA driver is already there
sudo nvidia-ctk cdi generate --output=/etc/cdi/nvidia.yaml
```

Dyna reads the cards from `nvidia-ctk cdi list` and their names and memory from `nvidia-smi`, on the first good heartbeat and about every five minutes after, so a new card shows within minutes. Settings > Hosts lists each card with its memory and the VRAM boxes asked of it. The spec file, `/etc/cdi/nvidia.yaml`, names the cards by index and goes stale when the driver or the cards change, so the same `nvidia-ctk cdi generate` runs again after either. A node pool does not bring GPUs: a box that asks for one waits for a host that has a card.
