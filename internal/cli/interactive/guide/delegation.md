# Dyna delegation

Delegate with subagents. Workflows are root's tool for large parallel jobs. If your task needs one, post the job to @root.
Use a subagent for work that needs memory across turns. Delegate only through Dyna, never through harness subagents.
Workflow agents are leaf sessions: they cannot delegate or start runs or subagents.
Keep small or tightly coupled changes yourself. Run read-only scouts in parallel. Give each set of files one writer.

For a replication or port project, build and verify one small end-to-end vertical slice before fanning out implementation. Trace a representative input through every integration seam to a visible output. Use what it reveals to define the parallel work; passing unit tests alone cannot show that the parts are wired together.

## Choosing a profile

Ratings are out of 10, and higher is better. `taste` is choosing the solution. `diagnostics` is finding what is wrong with a solution (reviews, debugging), with few false positives. `intelligence` is executing a given solution on hard multi-step work. `cost` means higher is cheaper. Send reviews by diagnostics, design by taste, and bulk work by cost.

## Commands

Manage sessions with `dyna session`; `dyna subagent` is an alias for every command below.
Choose a profile with `dyna profiles list`; `--all` includes disabled profiles.
A profile's uses switch on workflows, agents (subagent sessions) and services separately; `dyna profiles set <name> --use agents,services` picks any subset, and `dyna profiles list` shows the limits.
Address sessions by full address (`<room>:<name>`), ID (`ses_...`), or open name.
Names must be unique among open sessions in a room.

```bash
dyna subagent start --goal "<outcome; done when>" --profile <name> --name <label> [--dir <path>] [--schema <file>] [--under gN] <brief | @file>
dyna subagent output <session> [--turn N] [--transcript | --raw | --json]
dyna subagent show <session> [--turns N | --all] [--tools] [--json]
dyna subagent list [--all]
dyna subagent stop <session>
dyna subagent promote <session>
dyna subagent adopt <session> --under <parent>
dyna subagent close <session> [--force]
dyna subagent reopen <address|session-id>
```

Run a command with `--help` for flags. Start requires a goal and non-empty brief.
It prints the address and starts a detached turn. Without `--dir`, a start in a git repository creates its own branch worktree from the repository's main; `--dir` keeps the parent or current directory. A start outside a git repository keeps the current directory.
The goal states the outcome and completion test; the brief states how.
When the work fixes a room issue (D-N) or a GitHub issue (#N), name that issue in
the brief and tell the builder to pass `--fixes D-5,#207` when it opens or tracks
the PR. The merge then closes the issue; nobody needs to close it by hand.
Dyna restores both after compaction. Start prints `assigned item iN on goal gN` for an item, or `holds goal gN` for a standalone goal.
`start --json` returns the goal ID as `goal` and adds `item` when assigned.
Writers use `dyna git worktrees new <branch>`; never edit the main checkout.

Output prints a finished turn; `--raw` prints harness stdout. Show includes prompts, steering, deferred deliveries, and results.
List shows open sessions; `--all` includes closed ones.
Stop cancels only the current turn. Cancel children and runs separately.
Close stops the session and hands PR authorship and held goals to its nearest open ancestor.
A top-level session hands PR authorship to the room's root. Reviewer roles return to the pool.
Root and the owner hold no goals: when only they are left above a session, close is refused
until each held goal is done, dropped, or transferred with `dyna goal edit <id> --for @agent`
to an open agent. The refusal names each goal and its commands, and the app offers the same choices.
A forced close does not move goals to the owner. If only the owner can unblock the work,
root asks the owner first with `dyna request question` for a decision, or files `dyna request terminal --reason "<why>" -- <command>` for a command only the owner can run, then resolves the goal.
Live workflow runs are canceled because their running coordinator cannot change owners.
Questions withdraw; non-kept shells stop. Empty children close; other children move to the heir.
The command and app show what moved or stopped. A failed handoff keeps the session open;
retry after fixing the error, or use `--force` to close with other unresolved work.
Reopen restores history without starting a turn. An address selects its latest closed history; an ID selects exact history.
An occupied name blocks reopen.

Subagent turns have no timeout, no idle expiry, no turn cap. Stop stuck work.
Promote moves an open session to depth 1 and adjusts descendant depths without changing ongoing work or ownership.
Adopt is the inverse: it moves an open session and its descendants under another open session in the same room.
Depths follow the new parent. Adopt refuses a cycle, a service parent, or a depth past `subagent.maxDepth`.
Goals, PR roles, worktrees and the running turn stay. Root, the owner or the current parent may adopt.
After a move, @parent names the new parent. The old and the new parent each get one notice that does not wake them.
Stored hierarchy controls parent selectors and depth, even when `DYNA_DEPTH` predates promotion.
`subagent.maxDepth` (default 3) limits nesting. Work started outside a session has depth 1; children add 1.

## Structured results

`--schema file.json` validates the first turn as JSON, with up to three attempts in the same conversation.
`dyna subagent output` then prints compact JSON. Follow-ups use plain text; request any needed shape in the message.

## Environment

| Variable | Subagent worker | Workflow agent |
|---|---|---|
| `DYNA_MODE` | `subagent` | `subagent` |
| `DYNA_SESSION`, `DYNA_DEPTH` | session ID, launch depth | session ID, launch depth |
| `DYNA_ROOM`, `DYNA_ROOM_ID` | room name and ID | room name and ID |
| `DYNA_ROOM_SLICE` | the slice the room runs in: the room's own when limits are on, `dyna.slice` when they are off | the slice the room runs in: the room's own when limits are on, `dyna.slice` when they are off |
| `DYNA_ADDRESS` | `<room>:<name>` | `<room>:<name>` |
| `DYNA_WORKER_RUN` | not set | run ID |
| `DYNA_AGENT_JOURNAL` | not set | journal path |
| `DYNA_BIN` | Dyna binary | Dyna binary |

Every process you start, including shells and builds, runs in your room's
slice, so its memory and process count cannot take the machine down.
`DYNA_ROOM_SLICE` names that slice. Start a long-lived daemon with
`systemd-run --user --unit=<name> <command>`; the `systemd-run` on your PATH
adds `--slice="$DYNA_ROOM_SLICE"` itself, unless the call names a slice. A
daemon or supervisor started any other way runs outside the room's limits.

## Questions

Only root uses `dyna request question` to ask the human owner, and only for decisions and outside choices. For a command only the owner can run, root files `dyna request terminal --reason "<why>" -- <command>`. Other sessions use `dyna post @parent` for questions.
A worker blocked on something only the owner can do, such as a sign-in, a live check on a
device, a secret, or an outside decision, posts to @root or to its parent root. The post states
what is needed and why the work stops without it. Root turns that post into one `dyna request question`
question. Workers ask their parent.
For a step that needs a person in a terminal, such as a sign-in or `sudo`, any session files `dyna request terminal --reason "<why>" -- <command>`. The request climbs your parent chain: each holder runs `dyna request approve <id>` or `dyna request deny <id> --reason "<why>"`, and root's approval shows it to the owner. `dyna request list` shows yours and the ones you hold.
Root can use these question commands:

```bash
dyna request question "Which format?" --meanwhile "Inspect the parser" --options "JSON,CSV"
dyna request question
dyna request withdraw <id> --reason "Resolved"
dyna request answer <id> --choice JSON
```

Ask returns immediately; answers arrive as posts. It also accepts `--html` and `--attach`.
One answer covers matching room questions. Continue useful work; follow turn rules in `dyna guide steering`.

## Goals

Start uses explicit `--under gN` first. Otherwise it uses the starter's unique open goal with an open assigned item.
If none exists, it uses the starter's single open held goal set by someone else.
It ignores self-set held goals. Multiple assigned item goals or eligible held goals require `--under gN`.
If no goal is eligible, the child holds a new standalone goal, for any parent role.
Placement does not change session hierarchy.

Root holds no goals. A start from root's session makes the child the holder of a new
top-level goal, unless `--under gN` names a goal that another agent holds. Implicit
lookup never uses a goal root holds, and an explicit `--under` on one is refused.
When a closed session would hand a goal to root, close is refused until the goal is
resolved. Root asks the owner about the blocker when only the owner can clear it, then
drops or finishes the goal, or hands it to an open agent.

Create every goal with `dyna goal "<outcome; done when>"`. A subagent start
with new goal text uses that same creation path. To give a session an existing
goal, use `dyna subagent start --goal g12` instead of naming new text.
The caller holds a new goal by default. Select holders with `--for @name`, `@children`, `@siblings`, or `@room`.
Root cannot be a holder: `dyna goal` fails for root, and `--for @root` is refused.
Link room issues with `--fixes D-35,D-40`. With no text, one issue supplies the
goal title. `dyna goal edit g1 --fixes D-40` links an existing goal.

A brief belongs to the goal. Only the owner or root can correct it later with
`dyna goal edit g1 --amend "<text>"`. An amendment is appended and never replaces the
brief: the holder reads it at once, every later refresh carries it, and the goal
screen shows it. `--amend` takes no new text, `--for` or `--fixes`.

```bash
dyna goal                         # your goals and other open room goals
dyna goal --all                   # includes done and dropped goals
dyna goal show g1 --json
dyna goal "Fix the parser" --for @agent --fixes D-35,D-40
dyna goal --fixes D-35             # use the issue title
dyna subagent start --goal g1 --profile <name> "<brief>"
dyna goal edit g1 "New outcome"
dyna goal edit g1 --fixes D-40
dyna goal edit g1 --amend "Own internal/decoder only"  # owner or root: correct the brief
dyna goal done g1 "Summary"
dyna goal drop g1
dyna goal item done g1 i2
dyna goal item move g1 i2 --to g3
dyna goal item move g1 i2 --to new
dyna goal expedite g1 [--replace]  # owner or root: room priority
dyna goal expedite --off
```

Each room has at most one expedited goal. Only the owner or root sets it. It ends when
the goal is done or dropped, with `--off`, or after 24 hours. Replacing it needs
`--replace`. Expediting the same goal preserves its original expiry. Its holders
and their descendants share priority; PRs use their saved goal link. A holder that
is root or the owner never carries the badge, and neither do its children.

Linked changes get priority for the next available review slot in their room.
Eligible expedited PRs get an isolated merge trial, with merge priority per repository.
When normal work is waiting, it gets a slot after three expedited selections.
Review fairness is per room; merge fairness is per repository. Existing review,
dependency, draft, mergeability, and CI gates still apply. Running reviews, merge
trials, and CI continue. Expedite does not change session, workflow, or CI scheduling.

Goal changes print one short status line. Add `--json` for the full record.
Edits wake nobody. Every 15 minutes, Dyna asks parents with running, queued,
or idle direct children to check those children.
The same rule applies to root. Dormant and closed children do not count.
Parents remain responsible for their children and their work.
The check-in lists children stalest first with state, duration, goal/item, and PR
state. Act on stuck work. If everything is on track, end your turn without posting.
Check-ins are the `parent-checkins` built-in hook. Change its interval in
Settings → Hooks or with `dyna git hooks edit parent-checkins --global
--every-minutes 30`; switching it off stops check-ins, and `0` is not a valid
interval. The old `sessions.checkInInterval` setting is imported once into that
hook.
Goal status uses running turns, turn failures, tracked PRs, and item counts.
Use `dyna goal note <id> "<progress>"` to record progress.

Move repairs one open item between open goals in the same room. It preserves the full text, brief, and assignee under a fresh destination item ID.
`--to new` promotes the item to a standalone goal held by its assignee, or by the caller when unassigned.
Matching spawn instructions follow the work. Tell the assignee its new item reference.
History stays at the source, and both locations get cross-linked move events.
The command returns both references in plain output and JSON. It does not move sessions or other items.

## A complete brief

Include decisions the child cannot find. Save the brief in `$DYNA_SCRATCH/decoder-brief.md`:

```text
Context: Work in /src/app-decoder. Read docs/packet-format.md.
Truncated packets must return ErrTruncated.
Owned files: internal/decoder/packet.go and internal/decoder/packet_test.go.
Other agents own all other files; edit only your assigned files.
Create a worktree with dyna git worktrees new fix/truncated-packets.
Checks: Add a regression case; run go test ./internal/decoder/...
Return: At most 1200 characters: cause, files, tests, risks.
Save long evidence in $DYNA_SCRATCH and return its path.
Post to @parent only for a blocker or a decision. Send ready work as your final result.
```

```bash
dyna subagent start --name decoder --profile sol --dir /src/app-decoder \
  --goal "Fix truncated-packet crashes; done when the regression test passes" \
  @"$DYNA_SCRATCH/decoder-brief.md"
```

## Finishing

Do not wait or poll for children. Root can use a foreground workflow for a join.
Read `dyna guide steering` for reports and handoffs.
Close accepted children unless a follow-up is likely.
When an assigned item is done, run `dyna goal item done <goal> <item>`.
The goal stays open after its last item, and its holder gets a post that the
items are all closed. Only a holder ends the goal, and root, the owner and any
member of a shared goal may end it too.
When a held goal is done, run `dyna goal done <id> "Summary"`.
Merged and closed PR notices wake the author. Mark the goal done when its last PR
ends, clean up its worktree, and end your turn.
End or move PR roles, other goals, questions, children, shells, and runs.
A session with no responsibilities at the end of a turn goes dormant by itself; messages wake it.
The shell job a turn runs in ends with that turn and never keeps it awake.
Dormant keeps the session available. Close ends it and automatically hands work up the parent chain.
Dyna closes a dormant session after 7 dormant days.
Keep authors open while they handle their changes. If an author must close, the handoff described in `dyna guide git` applies.
Use `dyna guide recovery` for transfers.
