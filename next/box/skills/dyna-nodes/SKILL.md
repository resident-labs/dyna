---
name: dyna-nodes
description: Root only. Use when the room needs more or other machines, boxes wait with pool-node or pool-failed, or a host, a node pool or a GPU machine is to be added or removed.
dyna-audience: root
---

# Add machines: hosts and node pools

Boxes and CI runners run on hosts, machines the engine reaches over SSH. You add a host by hand,
or a node pool adds them: a script on the engine host rents a machine when a box of a room whose
rule names the pool has nowhere to go, and gives it back once it is empty.

The `dyna …` commands below are the owner CLI (dyna-admin): bash with host set to the
engine host named in your prompt. Host switches, CI scale, the rooms a host serves and when a
change that reaches every room is yours to make are there too.

## Why a box waits for a pool

`session list` gives each box's `boxReason`; dyna-diagnose explains the others. When every allowed
host stays full (`cpu`, `memory`, `disk`) or none has a free card (`gpu`, `no-gpu`), add a pool or
a host below. A CI job waits the same way (runners: dyna-ci).

| boxReason | Meaning | Next step |
|---|---|---|
| `pool-node` | its pool is creating a node (up to 15 minutes), or is at its max | `dyna pool list`; at max (`6 of 6 nodes`), close sessions that hold nodes or ask the owner to raise it |
| `pool-failed` | a create failed; retry in 1 minute, doubling to 1 hour | `dyna pool list` |

## Write a pool script

Any language, starting with `#!`, at most 64 KiB. It answers three commands:

```
pool.sh create NAME SIZE   # stdout holds only {"ssh": "user@host"} or {"ssh": "user@host:port"}
pool.sh destroy NAME       # exits 0 also when NAME is already gone
pool.sh list               # prints a JSON list of the NAMEs it still has
```

- NAME is the pool's name followed by `-` and 8 hex digits, so `list` filters on that prefix.
- `create` returns once the machine answers SSH, within 15 minutes; the others have 5 minutes.
- SIZE is the box's size; the node needs that size's disk request (table in dyna-images) above
  the host's floor of 20 GiB (or a tenth of the disk, if more).
- It runs on the engine host as the engine's user, with only `PATH`, `HOME`, `USER`, `LANG` and
  `XDG_RUNTIME_DIR` set, so it reads credentials as the owner's own commands do.
- It runs in a private folder that holds only the engine's copy of the script, not in the folder
  you added it from: name helpers and credential files by absolute path, or keep it self-contained.
- A non-zero exit is a failure; the end of stderr goes into the room's notice and `dyna pool list`.
- A node that fails the host check (Prerequisites, below) or is too small for its box is destroyed
  as a failed create, so the script starts from an image that has everything ready.

An example over a made-up `rent` command, for a pool named `boats`:

```bash
#!/usr/bin/env bash
set -euo pipefail
case "$1" in
  create)
    case "$3" in agent|small) plan=s ;; medium) plan=m ;; *) plan=l ;; esac
    address=$(rent create "$2" --plan "$plan" --image dyna-node --output ip)
    for _ in $(seq 1 60); do nc -z "$address" 22 2>/dev/null && break; sleep 5; done
    printf '{"ssh": "dyna@%s"}\n' "$address" ;;
  destroy) rent delete "$2" --if-exists ;;
  list) rent list --output names | grep '^boats-' | jq -R . | jq -s . ;;
esac
```

Test it on the engine host before adding it, from another folder and in the same bare environment:

```bash
cd /tmp; bare="env -i PATH=$PATH HOME=$HOME USER=$USER LANG=$LANG XDG_RUNTIME_DIR=$XDG_RUNTIME_DIR"
$bare /path/to/boats.sh create boats-test0001 agent
$bare /path/to/boats.sh list       # shows "boats-test0001"
$bare /path/to/boats.sh destroy boats-test0001
$bare /path/to/boats.sh list       # no longer shows it; check the provider too
```

## Add the pool and point the room at it

```bash
dyna pool add boats --script ./boats.sh --max 6 --per-session
dyna room hosts ROOM_ID --only boats
dyna pool list                     # boats  0 of 6 nodes  per session
```

- `--max` counts nodes being created. `--per-session` gives each box its own node, which goes when
  the session closes; a shared node with no box or runner goes after `--idle` (default `30m`, reset
  by an engine restart). Only the owner changes these later, in Settings > Pools. The engine keeps
  its own copy of the script, so to change the script, undo and add the pool again.
- `room hosts` replaces the rule whole: at most one pool, plus hosts that are not pool nodes. A
  pool grows only for a room whose rule names it. Read and record the rule before you replace it,
  as dyna-admin says under "Room hosts and limits".
- Check: the room's waiting boxes show `pool-node` in `session list`, then land on a node that
  `dyna host list` marks `pool boats`.
- Undo: in each room that names the pool, `dyna room hosts ROOM_ID --only OLD_LIST`, or `--clear`
  if the room had no rule before; then `dyna pool remove boats` once `dyna pool list` shows 0 nodes.

A child's `pool add` or `room hosts` call waits for you as an approval request, unless dyna_room
lists it among the approvals the owner keeps; deciding requests is in dyna-admin.

## What a node costs and when it goes

- A per-session node stays while its session is open, dormant included: the stopped box holds it.
- Every 10 minutes the pool compares `list` with its nodes. A machine Dyna has no record of is
  reported once and never destroyed: check it is not the owner's, then run `destroy` yourself.
- A node the script no longer lists is a lost host, and its box waits. If it is gone for good, run
  `dyna host remove NODE --discard-boxes`, then `dyna session restart` for each session it prints.

## Add one machine by hand

```bash
dyna host add NAME --ssh user@address --owner-only-admin   # --room ROOM_ID: serve that room only
dyna host list                                              # NAME is listed, with its disk
```

- `--ssh` takes no port. Without `--room` the host serves every room.
- `--owner-only-admin` confirms that only the owner administers the machine; a host that runs
  boxes or CI needs it. `--agents off --ci off` adds an exec-only host instead: it takes host-exec
  runs only and needs just SSH, bash and `pgrep`.
- A failed add names the check that failed. Without FUSE the host serves CI runners only.
- Undo: `dyna host remove NAME`, then `dyna host list` no longer shows it. It refuses, naming what
  holds the host, while a box, CI runner, service or subscription run is on it or a room's rule
  names it. Drain first: `dyna host set NAME --agents off --ci off` (running jobs finish), a rule
  without it in each such room, services moved or removed (dyna-services), runs ended.
  `--discard-boxes` is only for a machine gone for good, and gets past none of the others.
- Removal leaves the machine's host data folder, user units and Podman images. Files CI runners
  wrote belong to mapped IDs: `podman unshare rm -rf PATH` removes them where `rm` fails.

### Prerequisites, for hosts and pool nodes

- SSH to a user with bash and procps `pgrep`. The engine runs plain `ssh user@address` as its own
  user, so the public key its ssh config picks for that address goes in the user's
  `authorized_keys`, with no forced command. Dyna accepts the host key on first contact.
- The engine host's CPU architecture; cgroup v2 with `cpu`, `memory` and `pids` delegated to the
  user's systemd; rootless Podman 5 or later with the systemd cgroup manager; `git`, `curl`,
  OpenSSH with stream-local forwarding; FUSE, with `user_allow_other` in `/etc/fuse.conf`.
- The engine's host data folder, one absolute path on every host, creatable by the user (so the
  same user and home as the other hosts); `pgrep -af -- --host-data` on the engine host shows it.
- Over 4 GiB of memory, which the host keeps for itself, and 25 GiB free for the smallest box.

## A host with a GPU

A box that asks for a GPU never makes a pool create a node, since a new node brings no card. Add
a machine that has one by hand (above). On it, once `nvidia-smi` shows the card, copy any existing
`/etc/cdi/nvidia.yaml` aside, install `nvidia-container-toolkit` and run
`sudo nvidia-ctk cdi generate --output=/etc/cdi/nvidia.yaml`; again after a driver or card change.
Check: `nvidia-ctk cdi list` names the card, Settings > Hosts lists it within 5 minutes, and a test
session started with `gpu` sees it with `nvidia-smi`. Undo: restore the old file or delete the new.
