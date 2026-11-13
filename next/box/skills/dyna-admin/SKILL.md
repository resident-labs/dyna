---
name: dyna-admin
description: Root only. Use when a session is refused as owner-only or by a room setting (GitHub writes, hosts, limits, workflows), a request waits for your decision, or the owner asks to change the room or machine (repositories, CI, hosts, profiles, model accounts, add-ons, Discord).
dyna-audience: root
---

# Run your room's settings

Most of what blocks a session is a setting, and the owner CLI changes it. You hold host exec on the engine
host, and there plain `dyna …` reaches the owner socket and acts as the owner, so its commands run at once
and need no approval. Your room's settings are yours to keep right; some settings reach every room, and a
few changes need a secret only the owner holds.

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

"Read" also serves to check the change.

| You see | Change | Read | Undo | Whose |
|---|---|---|---|---|
| GitHub write refused: `writes_off`, `group_off`, `repo_not_allowed` | `room set ROOM --github-writes GROUPS@REPOS` | `context show dyna_room` (GitHub writes line) | the old value, or `--github-writes off` | yours |
| A repository is not in the room | `room repo add ROOM owner/repo` | `room repo list ROOM` | none: removal is the owner's | yours |
| Jobs with `runs-on: dyna-…` sit queued | `room repo ci owner/repo --on [--max N]` | `context show dyna_room` (CI runners line) | `room repo ci owner/repo --off` | yours |
| New boxes wait for a host, or go to the wrong one | `room hosts ROOM --only [POOL,]HOST,…` | `session list` (start host and step) | the old `--only`, or `--clear` | yours |
| Boxes wait while hosts have room | `room limit ROOM --cpus N --mem SIZE` | `room log`, types `room.limits_changed` | the old values; `0` is unlimited | yours |
| Workflow start refused, or runs or workers capped | `workflow set --room ROOM SETTING=VALUE` | `workflow settings --room ROOM` | `workflow set --room ROOM --clear SETTING` | yours |
| A session's recorded branch is wrong | `session set-branch SESSION BRANCH` | the session's prompt ("Your branch") | the old branch | yours |
| A child needs a host (deploy, GPU, a device) | `hostexec request_grant` tool, to the child | `host list` for names | close the child | yours |
| A session needs another account | `session set-account SESSION ACCOUNT` | `account list` | `session set-account SESSION auto` | yours |
| Out of quota, or the room uses the wrong account | `account limit ACCOUNT --rooms … --prefer …` | `account quota`, `account list`, dyna_usage | the old lists | every room |
| An add-on breaks sessions, or only root needs it | `addon set KIND NAME [--room ROOM] --enabled off` or `--root-only on` | `skill list`, `package list`, `mcp list` | the opposite value | yours with `--room`, else every room |
| A host is full, degraded, off or has disk low | `host set NAME --agents on\|off --ci on\|off --ci-scale 1-4` | `host list` | the opposite value | every room |
| A host's create failure blocks boxes | `host set NAME --clear-create-problem` | `host list` | none needed | every room |
| A host serves the wrong rooms | `host limit NAME --room ROOM` (repeat) or `--all` | not printed by the CLI | the old rooms, or `--all` | every room |
| A profile has the wrong model, ratings or cap | `profile set NAME --FIELD VALUE` | `profile show NAME` | the old values from `profile show` | every room |
| The room should answer a Discord server or channel | `room discord ROOM --server ID [--channel ID] [--session NAME]` | `room discord ROOM` | the same flags with `--remove` | yours |
| Commits or PRs carry the wrong GitHub identity | none: a token goes on stdin | `room github ROOM` | | the owner's |
| A request waits for a decision | see "Decide sessions' requests" | `op list ROOM` | | yours |

Other subjects have their own skill: adding a skill, package or MCP server is dyna-extend; long-running
containers are dyna-services; node pools and adding machines are dyna-nodes; box images and compose files
are dyna-images; a local harness on Dyna's accounts is dyna-local-proxy; stuck sessions are dyna-diagnose.

## What the table cannot hold

**GitHub writes.** The groups are `issues`, `pulls`, `projects` and `actions`; REPOS are the room's
repositories or `*`, as in `issues,pulls@owner/repo` or `issues,pulls,projects,actions@*`. The command
replaces the whole setting, so read the current line first and give every group and repository you keep.
`*` means every repository the room lists at the time of each write, so it covers repositories added later.
A repository must be in the room before the setting can name it: run `room repo add` first. Merges, deletes,
approving reviews, pushes and file writes through the API, releases and repository settings stay refused
with every group on; those go through a host grant and `gh` on a host, or to the owner.

**GitHub account.** Attribution is the owner's choice: do not set or change a room's GitHub account. If
commits read under the wrong name, tell the owner what `room github ROOM` prints.

**Accounts.** ACCOUNT is the label or ID that `account list` prints. `--rooms all` lets an account serve
every room; `--prefer` names the rooms that use it first while it has quota, and `none` clears that. A
preferred room must be one the account serves. The method sets both lists at once, so give both flags
with the values you want kept. A session pin (`session set-account`) wins over a room's preference. SESSION is the
session's ID, which `session list ROOM` prints. Connecting, reconnecting, removing or renaming an account
and setting its key are the owner's, in Settings > Accounts.

**Hosts.** `host set` and `host limit` change what every room can use. What already runs on a host stays
there until it stops. A degraded host takes no new boxes until a probe succeeds. Adding or removing a host
is dyna-nodes.

**Room limits.** `room limit` needs both `--cpus` and `--mem`, so give the one you keep as well.

**Workflow settings.** Without `--room`, `workflow set` changes the value for every room that sets none of
its own; with `--room`, only yours. `dyna workflow set --help` lists the settings.

## Decide sessions' requests

A request reaches you as an input "as an approval request", with its card and the line "Decide it with op
decide, id …". Decide it with the `op decide` tool: approve true, or false with a reason, which the
requester reads. A pending request expires after 24 hours, except a host grant, which waits until it is
decided or its session closes. `dyna op list ROOM` on the engine host lists what is pending in the room.

Which kinds you decide: host grants, when you are the nearest session above the requester that holds the
host; and global add-ons, room host rules and node pools, for each kind your dyna_room section's Approvals
line gives you. The owner sets those kinds; a request of a kind you do not hold waits for the owner.

Decide by reading what the card asks for, not only its reason:
- **Host grant.** Approve a named host the task needs: a GPU, a device on a desktop, a deploy the room's
  AGENTS.md allows. Deny the engine host and every host (`*`): the card warns that they give full owner
  power. Run the owner command for the session yourself instead. A grant lasts until the session closes.
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
- Read the setting again with its Read command, or `context show dyna_room` for room lines.
- In your report: what you changed, why, its undo command, and whether it reaches every room.
