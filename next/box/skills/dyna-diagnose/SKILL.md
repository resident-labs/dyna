---
name: dyna-diagnose
description: Root only. Use when a session, box, turn, workflow or CI job looks stuck, slow or failed (a failure notice, a stall check-in), a host is full, low on disk or unreachable, or before you report a Dyna bug.
dyna-audience: root
---

# Diagnosing sessions, boxes and hosts

Each session has a box, a container on one host, and runs turns in it. Something stuck is in one of
three places: the box (not `ready`), the turn (running without progress, or failed) or the host. Read
in the order below, cheapest first, and stop as soon as you know which; then take the cheapest
action that fixes it, because the later actions lose work.

## 1. Which part is stuck

Start with dyna_children (free): `name: profile, state`, then `Starting on HOST · step (N min)` once a
start passes a minute, the goal item it is on, and `waiting: …` when no turn runs. Then `session list`
(omit `parent` to search the room; `parent` gives only immediate children); note its `seq` for step 3.

| `session list` shows | Means | Next |
|---|---|---|
| `boxState` pending, `boxReason` set | waiting for a place | table in step 4 |
| creating or starting for minutes | a start step is slow | `start.step`, step 4 |
| `turn` and `activity` set | a turn is running | step 2 |
| `waiting` set, no `turn` | waiting on its jobs, children, a workflow or the owner | normal |
| `stopped` | quiet for 10 minutes; the next input starts it again | normal |
| `failed` | the box failed | its cause, step 3 |
| `lost` | its host stopped answering; it waits for that host, never moves | step 5 |
| `lastOutcome` failed | the last turn failed; its input is kept | its cause, step 3 |

`activity` `waiting for model` for many minutes points at the model or account (step 4, turns).

## 2. What the turn did

`transcript read` on the session: the last turns and one line per tool call. Look for the same call
repeated, a command that never returns, a run of failed calls, or a final text that asked a question
nobody answered.

## 3. What the engine saw

`room log` with `types` and `beforeSeq` set to the `seq` from step 1 plus 1 (it is exclusive; without
it the log starts at the room's oldest event). If the failure is not there, page back with `beforeSeq`
set to the oldest seq returned. There is no session filter: filter bodies by session or box ID in codemode.

| Type | Carries |
|---|---|
| `box.changed` | `from`, `to`, `host`, and for a failure `cause`, `phase` and `reason` |
| `turn.ended` | `outcome`, and for a failed turn `cause` and `error` |
| `session.failed`, `session.notice` | what failed, or what a box could not set up |
| `box.pressure` | `kind` `high`: memory throttled; `max`: at its memory limit or a process OOM-killed |
| `tool.failed`, `image.failed`, `host.disk_low`, `engine.restarted`, `checkin.due` | as named |

`dyna subscription events` describes most types. For a workflow, `workflow status` gives its status
lines and step journal; a step's own output is on the web, and reload or resume is in dyna-workflows.

## 4. Causes and what to do

A failed box (`box.changed` `cause`):

| Cause | Do |
|---|---|
| `create_failed`, `start_failed` | `session restart`. A new box may already have had its one automatic retry on another host. Several on one host: `dyna host list`, step 5 |
| `no_hello` | the box agent never connected: `session restart`; again on the same host, step 5 |
| `engine_restarted` | the engine restarted while it created the box: `session restart` |
| `host_lost` | its host was removed as gone for good: `session restart` gives a new, empty box elsewhere |
| `nested_device_missing`, `kvm_device_missing` | the host lacks the device: start the work again with `session start` `host` on another host |

A failed turn (`turn.ended` `cause`; the next input starts a new turn):

| Cause | Do |
|---|---|
| `model_failed` | it retries by itself 8 times over about 5 hours. Read dyna_usage, then `dyna account quota`; move it with `session set_profile` to another provider's profile, or `dyna session set-account SESSION ACCOUNT` |
| `output_limit` | post to it to continue in smaller pieces |
| `harness_failed` | the agent program stopped; a third crash without a completed turn fails the box. Post once; on another failure, `session restart` |
| `setup_failed` | the box could not set up the repository: the turn's `error` says why |

A pending box (`boxReason`):

| Reason | Do |
|---|---|
| `cpu`, `memory` | hosts are full: close finished sessions, or start the next one smaller |
| `disk` | the host is below its disk floor: close finished sessions, which frees their volumes |
| `room-limit` | the room's CPU or memory cap: close sessions, or raise it (dyna-admin) |
| `room-host` | the room's host rule allows no ready host (dyna-admin, dyna-nodes) |
| `no-ready-host` | no host takes it: `dyna host list` for `degraded` or `agents off` |
| `pool-node`, `pool-failed` | the room's pool is bringing a node, or its script failed: step 5, dyna-nodes |
| `gpu`, `no-gpu` | every card is taken, or no ready host has one |
| `create-retry`, `other-host-create-retry` | the engine is retrying a failed creation: wait |

A slow start step: `updating Dyna on host` follows a deploy and waits for the new binary on a slow
link; `preparing image` builds the image (`image.failed` if it fails); `mounting shared folder`
stalls where `dyna host list` says the shared folder mount is unavailable.

A stall check-in names a goal holder with nothing moving. Read its transcript: idle, post to it;
blocked on a failed box or turn, the tables above; on an undecided request (`op.requested`), decide
it (dyna-admin).

A slow turn in a small box is often the box itself: an agent-size box has 1 CPU, and a build there
starves the agent. Move heavy work to a child started with a larger `size`.

## 5. The machine

Owner commands run with bash with host set to the engine host named in your prompt.
- `dyna host list`: disk `ok` or `low`, `degraded` (three failed probes: no new boxes or runners),
  `box creation failed` (three in a row), `agents` and `ci` switches, runners, CI image build failures.
- A lost box comes back with its host, and `session restart` refuses it until it is failed. A host
  gone for good is removed with `dyna host remove NAME --discard-boxes`, which fails its boxes and
  loses their unpushed work.
- The engine journal: `systemctl --user list-units 'dyna*' --no-pager` names the engine's unit; set
  `UNIT` to it and run `journalctl --user -u "$UNIT" --since -1h --no-pager`. Search the output for
  the box ID: `lifecycle step BOX_ID: …` is the raw error of a failed creation; `pool NAME:` lines are
  a pool script's output; `git route: upstream request failed` is a push or fetch GitHub refused.
- On the box's host (`host` in `session list`): `podman ps -a --filter name=BOX_ID` shows its
  container, and `systemctl --user status dyna-BOX_ID.slice` its CPU, memory and tasks.

Host changes (`dyna host remove`, `dyna host set NAME --agents off`, `--clear-create-problem` once
the cause is fixed) reach every room: dyna-admin says when they are yours to run.

## 6. Act, cheapest first

| Action | When | Cost |
|---|---|---|
| wait | stopped, waiting, pending on capacity, lost | none |
| post to the session | idle stall, failed turn, a question it is stuck on | one turn |
| `session stop` | a turn looping or running away, or a model retry to cancel | the turn's work in flight |
| `session set_profile` | its model or account keeps failing | a cold prompt cache |
| `session refresh` | a live box that misbehaves | a container restart |
| `session restart` | a failed box only | the volume; no undo |
| `session close` | finished, or not worth saving | the box and its whole subtree; no undo |
| `room pause` | a runaway that spends money, such as sessions starting sessions | the whole room stops |

Stop a hung turn before a refresh, which waits for it. Before a close, read `session
responsibilities`, and `session move` out any child to keep. A profile, account or parent change is
an experiment: note the current `profile` and `parent` in `session list` first and set them back the
same way if it does not help (`dyna session set-account SESSION auto` undoes an account pin). A
paused room starts no box and delivers no input to any session, yours included; turns already
running go on, so stop them. Resume (`room pause` with `paused` false) in the same turn, or only
the owner can. After any action, check that `session list` shows the box `ready` and a new `turn`
or later `lastOutcome`, and `transcript read` shows the kept input making progress.

## CI jobs

First the checks in the dyna-ci skill, "When runs look stuck". Then `dyna host list`: hosts with `ci`
on, their runners, `degraded`, and a room's CI runner image that failed to build.

## Reporting a Dyna bug

Send it where AGENTS.md says Dyna bugs go, with: room, session and box IDs, host, the UTC time
window, the event seqs, the `cause`, `reason` and `error` you read, the journal lines, and what you
tried with each result.
