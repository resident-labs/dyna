---
name: dyna-services
description: Root only. Use when the room needs a container that runs without a session (a relay, bot or public endpoint), or a service needs a deploy, a secret, a restart, a rollback or its logs, or shows pending, crashing, failed or rolled_back.
dyna-audience: root
---

# Run a room's services

A service is a long-running container that Dyna keeps on one host for the room: no agent, no box, no
`/box` folders. You declare it and the engine places, deploys and watches it. It holds capacity on a host
that every room's boxes share, so declare one only for something that must run with no session open.

| Need | Use |
|---|---|
| Runs with no session open, or is reached from outside: a relay, bot, webhook endpoint | a service |
| A database or server a session's tests need | a compose sidecar in that session's box (dyna-images) |
| Work on a schedule or on an event | a subscription with a run (`subscription add`) |

## Running the commands

Every `dyna service` command is the owner's: run it with bash, host set to the engine host named in your
prompt. ROOM is your room's ID from the prompt (`room_…`); the name is not accepted. Sessions cannot run
these; a child that needs a service asks you. Each call returns at once and the engine works after it, so
check with `show`:

```bash
dyna service list ROOM            # one line per service
dyna service show ROOM NAME       # the same line, each secret set or missing, and the last error
```

A line reads `NAME HOST STATE REASON CAUSE generation G, running D`. A deploy is done when the state is
`running`, the reason is empty and D equals G.

## Declare or change a service

`dyna service set ROOM --file F` declares the service, or replaces the declaration of the one with that
name. It replaces the whole declaration: a field left out goes back to its default and a secret left out
loses its value. `show` does not print the declaration, so the file is the only copy you can read. Keep
it in `/box/shared/services/NAME.json` and pass `--file` the path the prompt's Files line gives for
`/box/shared` on the engine host. The CLI ignores an unknown field without a word, so check spelling.

```json
{
  "name": "relay",
  "image": "ghcr.io/owner/relay@sha256:<64 hex digits>",
  "command": ["relay", "--listen", ":8443"],
  "ports": ["8443"],
  "env": {"LOG_LEVEL": "info"},
  "secrets": ["RELAY_TOKEN"],
  "data": "/var/lib/relay",
  "cpus": 0.5,
  "memory": "256MiB",
  "health": ["relay", "--check"]
}
```

| Field | Rule (default) |
|---|---|
| `name` | 1 to 40 of `a-z`, `0-9`, `-`, starting with a letter; required |
| `image` | a room image name, or a registry reference pinned by digest; a tag alone is refused; required |
| `command` | replaces the image's CMD (the image's) |
| `ports` | `PORT`, `PORT/udp` or `HOSTPORT:PORT/tcp`, published on every address of the host (none) |
| `env`, `secrets` | plain values, and names whose values you set apart; no name starts with `DYNA_` |
| `data` | absolute path, without space `:` `,` `"` `%` `\` `$` `=`, of a volume that survives restarts, updates and rollbacks (none) |
| `cpus`, `memory` | hard limits and reservations: 0.25 to 8 in steps of 0.25 (1); 64MiB to 16GiB (512MiB) |
| `disk` | accounting for the volume and logs, up to 100GiB, not a quota (1GiB) |
| `host` | a host your room may use (admission picks one) |
| `restart`, `health` | `always` or `on-failure` (always); a command that gates an update (10 s active) |

The root filesystem is read-only: the service writes only to `data` and a 64 MiB `/tmp`. It has its own
network namespace and cannot reach the host's loopback. Prefer a small registry image pinned by digest;
for a room image, see dyna-images.

Each `set` redeploys, with a few seconds of downtime. Undo: `set` again with the earlier file, then
set again the value of any secret the change dropped; the file restores only its name. Before dropping
a secret, make sure whoever holds its value can set it again.

**Cost.** A service reserves its full CPU and memory on its host. Boxes there lose four times that, and
it counts against the room's limits. A host gives services at most a quarter of its CPU and memory. A
stopped service keeps its reservation; only `remove` frees it.

## Secrets

Declare the name in `secrets` first, then pipe the value in on the engine host; nothing reads it back:

```bash
openssl rand -hex 32 | dyna service secret set ROOM NAME RELAY_TOKEN
```

A value you generate this way never enters your transcript. A token only the owner holds (an API key, a
bot token) is the owner's to set: ask them to run the command. Until every secret has a value the
service waits with `secret_missing`. Setting a value redeploys the service. A replaced value is undone
only by setting the previous value again.

## Ship a new version, roll back, restart

- **Room image:** save the new Containerfile with `image save`, then `dyna service update ROOM NAME`.
- **Registry image:** put the new digest in the file and `set` it.
- An update builds or pulls while the old version runs, then restarts and waits up to 2 minutes for
  `health`. If any step fails, the old image comes back by itself: `running` with reason `rolled_back`
  and the failed step as the cause.
- `dyna service rollback ROOM NAME` runs the previous image with the current declaration. It exists only
  after a deploy that changed the image.
- `dyna service restart ROOM NAME` restarts the unit and clears `crash_loop`.
- `dyna service stop ROOM NAME` and `dyna service start ROOM NAME` turn it off and on; stop is done
  when the state is `stopped`. `set`, `update` and `rollback` keep a stopped service stopped: run
  `start` when it should run, and only then wait for the running check above.
- An engine restart leaves services running. A service stays on its host for good; moving it means
  `remove` and `set` again, which loses the data volume.

## Logs and failure notices

```bash
dyna service logs ROOM NAME --lines 500 --since 1h   # 1 to 2000 lines, 200 by default
```

Log lines are not masked: a service that prints a secret shows it to you, so do not quote such lines.
By default nothing wakes you when a service fails. To hear of it, add a subscription
`{scope: room, on: [service.failed], wake: root}`; it fires on `crash_loop`, `rolled_back` and `failed`.
For history, use the `room log` tool with types `service.changed` and `service.failed`.

## Why a service is not running

| State, reason | Fix |
|---|---|
| pending `cpu`, `memory`, `disk` | no allowed host has room; free some or add a host (dyna-nodes) |
| pending `room-host` | no host fits: never the engine's host, a pool node or an exec-only host (`dyna host list` marks them), and the room's hosts rule must allow it (dyna-admin) |
| pending `room-limit` | the room's CPU or memory limit (dyna-admin) |
| pending `service_host` | the host lacks linger, the quadlet generator or pasta; `show` names it |
| `set` refused, or pending `port_taken` | another service on that host declares the host port |
| `set` refused, or failed, `port_privileged` | the port is below the host's `net.ipv4.ip_unprivileged_port_start` |
| failed `port_in_use` | a process outside Dyna holds the port on the host |
| failed `image_build`, `image_pull`, `start_failed` | `show` has the error, `logs` the rest; fix, then `set` or `update` |
| crashing `crash_loop` | 5 restarts in 10 minutes; read `logs`, fix, then `restart` |
| unreachable | the host is down; it keeps its last state and comes back with the host |

A `failed` service stays failed until a `set`, `update`, `start` or `rollback`; `restart` does not clear it.

## Whose change it is

Declaring, updating, restarting and reading your room's services is yours. Ask the owner first for
`remove`, which deletes the data volume and secrets with no undo, for a new host, and for host setup:
linger (`loginctl enable-linger`), the firewall for a published port, and the lowest unprivileged port.
