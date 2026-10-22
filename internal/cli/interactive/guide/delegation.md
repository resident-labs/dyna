# Dyna delegation

Use a workflow for independent tasks with a join; read `dyna guide workflows` before writing one.
Use a subagent for work that needs memory across turns. Delegate only through Dyna, never through harness subagents or workflows.
Workflow workers are leaves: they cannot delegate, start runs, control subagents, or read guides. Read-only inspection commands remain available.
Keep small or tightly coupled changes yourself. Run read-only scouts in parallel. Give each set of files one writer.

## Commands

Choose a profile with `dyna profiles list`; `--all` includes disabled profiles.
A profile's uses switch on workflows, agents (subagent sessions) and services separately; `dyna profiles set <name> --use agents,services` picks any subset, and `dyna profiles list` shows the limits.
Address sessions by full address (`<room>:<name>`), ID (`ses_...`), or open name.
Names must be unique among open sessions in a room.

```bash
dyna subagent start --goal "<outcome; done when>" --profile <name> --name <label> [--dir <path>] [--schema <file>] <brief | @file>
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
It prints the address and starts a detached turn. Inside a session, `--dir` defaults to the parent session's directory; elsewhere it defaults to cwd. Without `--dir`, a start outside a git work tree fails when the room has a repository.
The goal states the outcome and completion test; the brief states how.
Dyna restores both after compaction. `start --json` returns the goal ID as `goal`.
Writers use `dyna git worktrees new <branch>`; never edit the main checkout.

Output prints a finished turn; `--raw` prints harness stdout. Show includes prompts, steering, deferred deliveries, and results.
List shows open sessions; `--all` includes closed ones.
Stop cancels only the current turn. Cancel children and runs separately.
Close stops the session. Open PR roles or live workflow runs block close unless `--force` is set.
Goals return to their setter; questions withdraw; non-kept shells stop. Empty children close; other children move to the parent.
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

| Variable | Workflow worker | Subagent worker |
|---|---|---|
| `DYNA_MODE` | `workflow` | `subagent` |
| `DYNA_WORKER_RUN`, `DYNA_WORKER_AGENT` | run and agent IDs | not set |
| `DYNA_SESSION`, `DYNA_DEPTH` | not set | session ID, launch depth |
| `DYNA_ROOM`, `DYNA_ROOM_ID` | room name and ID | room name and ID |
| `DYNA_ADDRESS` | not set | `<room>:<name>` |
| `DYNA_AGENT_JOURNAL` | journal path | not set |
| `DYNA_BIN` | Dyna binary | Dyna binary |

## Questions

Only root uses `dyna ask` to ask the human owner. Other sessions use `dyna post @parent` for questions.
Root can use these question commands:

```bash
dyna ask "Which format?" --meanwhile "Inspect the parser" --options "JSON,CSV"
dyna ask
dyna ask withdraw <id> --why "Resolved"
dyna ask answer <id> --choice JSON
```

Ask returns immediately; answers arrive as posts. It also accepts `--html` and `--attach`.
One answer covers matching room questions. Continue useful work; follow turn rules in `dyna guide steering`.

## Goals

Start creates the child's goal. For other work across turns, use `dyna goal "<outcome; done when>"`.
The caller holds it by default. Select holders with `--for @name`, `@children`, `@siblings`, or `@room`; `--under g1` sets its parent.

```bash
dyna goal                         # your goals and other open room goals
dyna goal --all                   # includes done and dropped goals
dyna goal show g1 --json
dyna goal edit g1 "New outcome"
dyna goal done g1 "Summary"
dyna goal drop g1
dyna goal wait g1 --until 2h       # waiting on something; not progress
```

Goal changes print one short status line. Add `--json` for the full record and keeper history.
Edits wake nobody. The goal keeper gives each goal a progress deadline.
Progress is a head or state change on a tracked PR tied to the goal, a child goal change, or a goal edit.
A holder with one goal gets credit for its tracked PRs automatically.
A post that names the goal ID moves the deadline once but is not progress.
When the deadline passes, the keeper sends a check-in, then a judge, then escalates up the agent chain, never to the owner.
While you wait on something, run `dyna goal wait <id> --until <duration>`.

## A complete brief

Include decisions the child cannot find. Save the brief in `$DYNA_SCRATCH/decoder-brief.md`:

```text
Context: Work in /src/app-decoder. Read docs/packet-format.md.
Truncated packets must return ErrTruncated.
Owned files: internal/decoder/packet.go and internal/decoder/packet_test.go.
Other agents own all other files. Do not edit them.
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

Do not wait or poll for children. Use a foreground workflow for a join.
Read `dyna guide steering` for reports and handoffs.
Close accepted children unless a follow-up is likely.
When done, run `dyna goal done <id> "Summary"`.
End or move PR roles, other goals, questions, children, shells, and runs. Then run `dyna subagent finish`.
It lists remaining responsibilities and commands to resolve them.
Finish makes the session dormant; messages wake it. Dyna closes it after 7 dormant days.
Keep PR authors and reviewers open as required by `dyna guide git`.
Use `dyna guide recovery` for transfers.
