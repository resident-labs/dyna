---
name: dyna-nodes
description: Root only. Use when boxes or CI jobs wait for a host (session list shows pool-node, pool-failed, room-host or no-ready-host), the room needs its own or disposable machines (a node pool), or a machine is to be added, removed or given a GPU.
dyna-audience: root
---

# Machines for boxes: hosts and node pools

Boxes and CI runners run on hosts, machines the engine reaches over SSH. You add a host by hand,
or a node pool adds them: a script on the engine host rents a machine when a box of a room whose
rule names the pool has nowhere to go, and gives it back once it is empty.

The `dyna …` commands below run with bash, host set to the engine host named in your prompt;
`dyna COMMAND --help` is the reference. From your box, `host …` and `pool remove` are refused as
owner-only. Host switches, CI scale and the rooms a host serves are in the dyna-admin skill, which
also says when a change that reaches every room is yours to make.

## Why a box waits

`session list` gives each box's `boxReason`. A CI job waits the same way (runners: dyna-ci skill).

| boxReason | Meaning | Next step |
|---|---|---|
| `pool-node` | its node is being created (the script has 15 minutes) | nothing |
| `pool-failed` | a create failed; retry in 1 minute, doubling to 1 hour | `dyna pool list` |
| `room-host` | no host fits the room's rule and serves the room | `dyna host list` |
| `no-ready-host` | no host is ready | `dyna host list` |
| `cpu`, `memory`, `disk` | every allowed host is full | add a host or a pool |
| `no-gpu`, `gpu` | no ready host has a card, or every card is taken | A host with a GPU |

## Write a pool script

Any language, starting with `#!`, at most 64 KiB. It answers three commands:

```
pool.sh create NAME SIZE   # stdout holds only {"ssh": "user@host"} or {"ssh": "user@host:port"}
pool.sh destroy NAME       # exits 0 also when NAME is already gone
pool.sh list               # prints a JSON list of the NAMEs it still has
```

- NAME is the pool's name followed by `-` and 8 hex digits, so `list` filters on that prefix.
- `create` returns once the machine answers SSH, within 15 minutes; the others have 5 minutes.
- SIZE is the box's: `agent`, `small`, `medium`, `large` or `xlarge`, which need 5, 10, 20, 40 and
  80 GiB of disk above the host's floor of 20 GiB (or a tenth of the disk, if more).
- It runs on the engine host as the engine's user, with only `PATH`, `HOME`, `USER`, `LANG` and
  `XDG_RUNTIME_DIR` set, so it reads credentials as the owner's own commands do.
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

Test it on the engine host, in the same bare environment, before adding it:

```bash
env -i PATH="$PATH" HOME="$HOME" USER="$USER" LANG="$LANG" XDG_RUNTIME_DIR="$XDG_RUNTIME_DIR" \
  ./boats.sh create boats-test0001 agent
./boats.sh list                    # shows "boats-test0001"
./boats.sh destroy boats-test0001
./boats.sh list                    # no longer shows it; check the provider too
```

## Add the pool and point the room at it

```bash
dyna pool add boats --script ./boats.sh --max 6 --per-session
dyna room hosts ROOM_ID --only boats
dyna pool list                     # boats  0 of 6 nodes  per session
```

- `--script` is a path on the engine host; the engine keeps a copy, so a later edit changes nothing.
- `--max` is required: the most nodes at once, counting those being created. `--per-session`
  gives each box a node of its own, which goes when the session closes. Without it sessions share
  nodes, and one with no box or runner goes after `--idle` (default `30m`, reset by an engine
  restart).
- `room hosts` replaces the rule whole: at most one pool, plus hosts that are not pool nodes. No
  command prints the rule, so pass the full list each time. Placed boxes stay where they are. A
  pool grows only for a room whose rule names it, though a room with no rule uses its shared nodes.
- Only the owner changes `--max`, `--idle` or `--per-session` later, in Settings > Pools. To change
  the script, undo and add the pool again.
- Undo: `dyna room hosts ROOM_ID --clear` in each room that names the pool, then
  `dyna pool remove boats` once `dyna pool list` shows 0 nodes.

When dyna_room lists pools or host rules among the approvals the owner keeps, ask the owner
instead. Otherwise a child's `pool add` or `room hosts` call waits for you as an approval request.
`room log` with types `op.requested` gives its op ID and card, which holds the script and the max;
op decide approves or denies it.

## What a node costs and when it goes

- A per-session node stays while its session is open, dormant included: the stopped box holds it.
- Every 10 minutes the pool compares `list` with its nodes. A machine Dyna has no record of is
  reported once and never destroyed: check it is not the owner's, then run `destroy` yourself.
- A node the script no longer lists is a lost host, and its box waits. If it is gone for good,
  `dyna host remove NODE --discard-boxes` fails its boxes (work not pushed is lost) and prints
  their sessions; `dyna session restart SESSION` gives each a new box.

## Add one machine by hand

```bash
dyna host add NAME --ssh user@address --owner-only-admin   # --room ROOM_ID: serve that room only
dyna host list                                              # NAME is listed, with its disk
```

- `--ssh` takes no port. Without `--room` the host serves every room.
- `--owner-only-admin` confirms that only the owner administers the machine; a host that runs
  boxes or CI needs it. `--agents off --ci off` adds an exec-only host instead: it takes host-exec
  runs only and needs just SSH, bash and `pgrep`.
- A failed add names the first check that failed; fix it and add again. Without FUSE the host is
  added for CI runners only, and `dyna host list` says so.
- Undo: `dyna host remove NAME`; it refuses while a box is on the host, and names it. It runs
  nothing on the machine, which keeps the host data folder, the user units `dyna-share.service`
  and `dyna-build-cache.service`, and Podman images. Files CI runners wrote belong to mapped IDs:
  `podman unshare rm -rf PATH` removes them where `rm` fails.

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

Once, on a host whose NVIDIA driver works, install `nvidia-container-toolkit` and run
`sudo nvidia-ctk cdi generate --output=/etc/cdi/nvidia.yaml`; run it again after a driver or card
change. Dyna reads the cards with `nvidia-ctk cdi list` and `nvidia-smi` about every 5 minutes, and
Settings > Hosts lists them. A box asks for one with `gpu` (GiB) on session start or in a
workflow's `meta`.
