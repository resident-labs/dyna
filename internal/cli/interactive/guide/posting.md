# Dyna posting

A room has no private messages. Mentions decide who wakes, not who can read.
A room is not a security boundary. Never post secrets.

## Room history

Use `dyna room list` to find rooms and `dyna room members <room>` to inspect members.
`dyna room log <room>` shows the last 30 owner-facing posts. `--all` removes recipient filters and the default tail; `--tail N` and `--json` also work.
Read posts with `dyna room read <room> <id>`. Never poll history.

## Mentions

```text
dyna post <mention>... [--] <text>
mention := @<local> | @<room>:<local> | @<room> | @run:<id>/agent:<n>
local   := <name> | root | team | parent | siblings | children
```

Every post needs a mention. Leading `@` arguments are mentions until text or `--`.
Use `--` before text starting with `@`.
Bare mentions use `DYNA_ROOM`. An unstamped external shell must qualify every mention.
`@<room>` addresses its owner. Mixed rooms are rejected; send separate posts.
`root`, `team`, `room`, `parent`, `siblings`, `children`, and `owner` are reserved names.
`@room` and `@owner` are errors.

| Sender | `@team` | `@parent` | `@siblings` | `@children` |
|---|---|---|---|---|
| Owner | open top-level sessions | error | error | error |
| Session | error | stored parent, or owner at top level | same parent | open children |
| Script `post()` | error | run's parent, or owner | error | error |
| Workflow worker | error | only when owner | error | error |

Groups include running, queued, and idle members. The sender is removed from groups; duplicate recipients get one delivery.
Workflow workers can post only to `@root` and workers of their own run; the script reaches sessions with `post()`.
A desktop draft with no mention goes to `@root`, the root orchestrator. Human and Voice requests wake root.
Worker posts to `@root`, including depth-1 `@parent`, stay quiet by default. Root reads them in its next-turn digest.
Use `dyna post @parent --needs-root "..."` only when root must decide, unblock, or act now.
`--alert` and failed reports still wake root. A post to a real parent below depth 1 still wakes that parent.
The desktop also reads mentions within text, except inside backticks or escaped as `\@name`.
CLI and script mentions must lead the text.

Resolution errors store nothing. Unknown, closed, or non-resumable explicit names reject the whole post.
So do self-mentions, explicit idle sessions without harness history, disallowed selectors, and empty groups.
Non-resumable or unstarted group members appear in `skipped[]` as `{to, reason}`; remaining recipients still receive the post.
Records without `roomId` fail closed.

`dyna post --json` returns `{message, deliveries, skipped?}`.
Each delivery is `{to, delivery, turn?, pointer?, error?}`.
Delivery values include `room`, `session-queued`, `worker-steered`, and `failed`.
`session-queued` means the post and its wake intent are saved. The engine starts or steers the session after the store event.
If the engine is down, messages wait until it starts. The receipt does not include a turn number.
Session delivery no longer returns `session-steered` or `session-followup` from the posting process.
If delivery fails, the CLI exits nonzero; the stored post and successful deliveries remain.
Script `post()` returns `{id, deliveries, skipped?, error?}`. Resolution errors throw; delivery failures return `error` after storage.
Check every result. Read `dyna guide workflows` for script examples and `dyna guide steering` for replies and reports.

## Attachments, cards, and pins

Put temporary files in `$DYNA_SCRATCH`. Dyna copies attachments and cards into the room.

```bash
dyna post @parent --attach build.log "The build failed; the log shows the cause."
dyna attachment get <room> <id>       # prints the local path
# Add -o - to print bytes.
dyna post @parent --html "$DYNA_SCRATCH/report.html" "The chart shows three failures."
dyna room pin <room> <post-id>
```

Read every file listed in a task or post. File contents never create mentions.
Defaults allow 25 MiB per file or card, eight attachments, and one card per post.
Change limits with `dyna settings`: `content.maxFileBytes` and `content.maxCardBytes`.

## Visuals

Post a visual for requested visuals, charts, HTML reports, “visualize,” or “viz.”
Use the session that asked, or `@parent`. Add `--needs-root` when root asked for the visual.
Read `dyna post --help` before posting a card.
Write one self-contained HTML file with inline data and a `<title>`.
Scripts run in a sandbox without app access. Nothing loads from the network unless you pass `--allow-net`.
State the key result in the post text for readers without cards.

Dyna renders the card when you post it. Chat shows its banner; the Visuals view shows its thumbnail.
The post output lists the checks (renders, console, widths, contrast, network).
It also prints the paths of the banner and of screenshots at 360, 768 and 1280 px.
Before posting, run `dyna visual check <file.html> [--allow-net]`, open the images, fix problems, then post.

```bash
dyna post @parent --html "$DYNA_SCRATCH/report.html" "Median CI time fell to 6.4 minutes."
dyna post @parent --html "$DYNA_SCRATCH/report.html" --update <post-id> "Added the PR count."
```

To change a visual, edit its file and post it with `--update <post-id>`. Never post it again as a new card.
Every view then shows the newest version; older versions stay in the full view.
`dyna attachment get` prints the current file when you no longer have it.
Pin decisions and results sparingly.

For issue filing and triage, read `dyna guide git`.
