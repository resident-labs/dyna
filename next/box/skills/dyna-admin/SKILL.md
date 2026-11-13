---
name: dyna-admin
description: Root only. Use when a session is refused as owner-only or by a room setting (GitHub writes, hosts, limits, workflows), a request waits for your decision, or the owner asks to change the room or machine (repositories, CI, hosts, profiles, model accounts, add-ons, Discord).
dyna-audience: root
---

# Run your room's settings

Most of what blocks a session is a setting, and the owner CLI changes it. Under host exec on the engine
host, plain `dyna …` reaches the owner socket and acts as the owner, so its commands run at once with no
approval. Your room's settings are yours to keep right; some reach every room, and a few need a secret
only the owner holds.

## Run an owner command

- Use bash with host set to the engine host named in your prompt (its Files line), and run `dyna …`.
- ROOM is your room's ID from your prompt's "This session" line; not every command takes the name.
- `dyna COMMAND --help` is the reference for flags.
- A refusal of the form "`room.set` is the owner's …" names the method: `room.set` is `dyna room set`.
- `context show`, `room log`, `session list`, `skill list`, `package list`, `mcp list`, `op decide` and
  `hostexec request_grant` are your own tool actions; everything else below is `dyna …` on the engine host.

## Whose change it is

- **Yours**: a setting of your own room. Make it, check it, and put the change and its undo command in
  your report.
- **Every room**: hosts, profiles, model accounts, global add-ons, global workflow settings. Make it when
  the owner asked for it, or when your room is blocked and one command undoes it; say so in the report.
  Otherwise ask the owner with question ask and go on with other work.
- **The owner's**: no command exists, or it needs a secret: connecting an account or setting its key,
  web search keys, devices and Connect, a room's GitHub account token, the Discord bot token. Ask the
  owner, naming the exact setting and value you need.

## From symptom to setting

Before a change, read the current value and save it: giving it back with the same command is the undo,
unless a paragraph below says otherwise. Read again after the change. Rows marked * have a paragraph.

| You see | Change | Read | Whose |
|---|---|---|---|
| GitHub write refused* | `room set ROOM --github-writes GROUPS@REPOS` | the refusal | yours |
| A repository is not in the room | `room repo add ROOM owner/repo` | `room repo list ROOM` | yours |
| `runs-on: dyna-…` jobs sit queued | `room repo ci owner/repo --on [--max N]` | dyna_room | yours |
| New boxes wait or go to the wrong host* | `room hosts ROOM --only [POOL,]HOST,…` | none | yours |
| Boxes wait while hosts have room* | `room limit ROOM --cpus N --mem SIZE` | `room log` | yours |
| Workflow refused, or runs capped* | `workflow set --room ROOM SETTING=VALUE` | `workflow settings` | yours |
| A session's recorded branch is wrong | `session set-branch SESSION BRANCH` | its "Your branch" | yours |
| A child needs a host (deploy, GPU) | `hostexec request_grant`, to the child | `host list` | yours |
| A session needs another account* | `session set-account SESSION ACCOUNT` | none | yours |
| Out of quota, or the wrong account* | `account limit ACCOUNT …` | `account list` | every room |
| An add-on breaks, or only root needs it* | `addon set KIND NAME …` | `skill list` etc. | see below |
| A host is full, degraded or off* | `host set NAME …` | `host list` | every room |
| A host serves the wrong rooms* | `host limit NAME --room ROOM` or `--all` | Settings > Hosts | every room |
| A profile's model, ratings or cap | `profile set NAME --FIELD VALUE` | `profile show NAME` | every room |
| The room should answer on Discord* | `room discord ROOM --server ID …` | `room discord ROOM` | yours |
| Commits carry the wrong GitHub identity | none: a token goes on stdin | `room github ROOM` | the owner's |
| A request waits for a decision | see "Decide sessions' requests" | `op list ROOM` | yours |

Other subjects have their own skill: adding a skill, package or MCP server is dyna-extend; long-running
containers are dyna-services; node pools and adding machines are dyna-nodes; box images and compose files
are dyna-images; a local harness on Dyna's accounts is dyna-local-proxy; stuck sessions are dyna-diagnose.

## What the table cannot hold

**GitHub writes.** The groups are `issues`, `pulls`, `projects` and `actions`; REPOS are the room's
repositories or `*` (every repository the room lists, later ones too), as in `issues,pulls@owner/repo`.
The command replaces the whole setting, so give every group and repository you keep. The refusal names the
stored value; dyna_room has a GitHub writes line only while it is on, with `*` expanded, so keep `*` from
the refusal. A repository must be in the room first. Undo with the saved value or `--github-writes off`.
Merges, deletes, approving reviews, pushes and file writes through the API, releases and repository
settings stay refused with every group on; those go through a host grant and `gh` on a host, or to the owner.

**GitHub account.** Attribution is the owner's choice: do not set or change a room's GitHub account. If
commits read under the wrong name, tell the owner what `room github ROOM` prints.

**Room hosts and limits.** No command prints a room's host rule, so `--only` names the whole rule and your
report records it. Undo with `--clear` when the room had no rule; ask the owner for an older rule you do not
know. Boxes already placed stay; `session list` shows where new ones start. `room limit` needs both `--cpus`
and `--mem` (`0` is unlimited); `room log` type `room.limits_changed` shows the last values set.

**Accounts.** ACCOUNT is the label or ID that `account list` prints; use the ID when labels repeat.
`account limit ACCOUNT --rooms ROOM,…|all` sets the rooms it serves, `--prefer ROOM,…|none` the served rooms
that use it first while it has quota. Omitted flags preserve their lists, so change only the list you need.
A session's choice (`session set-account`, SESSION from `session list`) wins over a room's preference. No
command prints a session's current choice (the owner sees it on its About panel), so set one only on a
session with none, undo with `session set-account SESSION auto`, and ask the owner to change an existing
one. Connecting, removing or renaming an account and its key are the owner's, in Settings > Accounts.

**Add-ons.** `addon set KIND NAME [--room ROOM] [--enabled on|off] [--root-only on|off]`: with `--room`
it changes your room's add-on and is yours; without, a global one for every room. Omitted switches stay.

**Hosts.** `host list` prints each host's agents and ci switches, and its CI scale while ci is on.
`host set NAME --agents on|off --ci on|off --ci-scale N` changes them; N is an integer from 1 to 4.
Turning agents or ci on for an exec-only host also needs `--owner-only-admin`, the owner's word that only
they administer it: use it only when the owner asked. Do not change a scale you could not read.
`host set NAME --clear-create-problem` clears a create failure that blocks boxes. What runs on a host stays
until it stops; a degraded host takes no new boxes until a probe succeeds. Adding a host is dyna-nodes.

**Host rooms.** `host limit` replaces the rooms a host serves. The CLI does not print them; the owner sees
them as Rooms it serves in Settings > Hosts. Unless the host serves every room, ask the owner for that list
before you change it, and undo with it or `--all`.

**Discord.** `room discord ROOM` lists the mappings; save the one you change. Undo a new mapping with its
`--server` (and `--channel`) and `--remove`; undo a change by mapping again with the saved session and
instructions (`--session NAME --instructions-file FILE`).

**Workflow settings.** Without `--room`, `workflow set` changes every room that sets none of its own;
`workflow set --help` lists the settings. Undo with `workflow set --room ROOM --clear SETTING`.

## Decide sessions' requests

A request reaches you as an input "as an approval request", with its card and the line "Decide it with op
decide, id …". Decide it with `op decide`; the requester reads your reason. A pending request expires
after 24 hours, except a host grant, which waits until it is decided or its session closes.

Which kinds you decide: host grants, when you are the nearest session above the requester that holds the
host; and global add-ons, room host rules and node pools, except the kinds your dyna_room section lists
under "Approvals the owner keeps". With no such line you decide all three; the owner's kinds wait for her.

Decide by reading what the card asks for, not only its reason:
- **Host grant.** Approve a named host the task needs: a GPU, a device on a desktop, a deploy the room's
  AGENTS.md allows. Deny the engine host and every host (`*`): the card warns that they give full owner
  power. Run the owner command for the session yourself instead.
- **Global add-on** (a skill, package or MCP server for every room, or its removal). Read the skill text,
  the package source or the MCP command. Approve what is useful beyond this room and does nothing you have
  not read. Deny with "add it to the room without --global" when only this room needs it; a room add needs
  no approval. Approve a removal only when the add-on is broken or replaced: every room loses it.
- **Room host rule.** Check `host list`: approve when the named hosts exist, are healthy and have agents on.
  Deny a rule whose hosts would leave new boxes waiting forever (degraded, agents off, exec only).
- **Node pool.** It spends money, and its script runs on the engine host. Read the whole script: approve
  when it only calls its provider to create, destroy and list nodes, the room needs the machines (boxes
  waiting for capacity, a GPU no host has) and `max` is no more than that need. Deny anything else.
  Name the pool and its `max` in your report to the owner. dyna-nodes describes what a pool script does.

After you approve, the operation runs and the requester hears its result. Check what it changed: `host
list`, `pool list`, `skill list`, or the requester's next box.

## After a change

- Retry the call that was refused, or post to the session that was refused that it can retry.
- Read the setting again, or `context show dyna_room` for room lines.
- In your report: what you changed, why, its undo command, and whether it reaches every room.
