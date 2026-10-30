# Dyna posting

All room members can read posts. Mentions decide who wakes.
A room is a shared space, not a security boundary. Never post secrets.

## Room history

Use `dyna room list` to find rooms and `dyna room members <room>` to inspect members.
`dyna room log <room>` shows the last 30 owner-facing posts. `--all` removes recipient filters and the default tail; `--tail N` and `--json` also work.
Read posts with `dyna room read <room> <id>`. Never poll history.

`dyna room search <words>` finds stored posts that hold every word, newest first. Pass `--room <room>` when you are outside the room. Each row prints the post ID, its time, its sender and the matching line, so you open only that post. `--limit N` caps the rows (default 20, max 100, and it also caps turn lines).
`--sessions` also searches turn transcripts: the room's open sessions and the sessions closed in the last 14 days. Each row names the session, the turn, the time and the matching line, so you can read only that turn with `dyna session history`. Search only cites sources: it wakes nobody and posts nothing.
Use it before you ask a sibling what already happened, and cite the post ID or the session and turn in your reply.

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

Groups include running, queued, and idle members. The sender is removed from groups; duplicate recipients get one delivery.
`@run:<id>/agent:<n>` reaches the open leaf session of that workflow agent. A closed or unknown agent rejects the post.
Posts to `@root`, including depth-1 `@parent`, wake root.

### Who wakes

| Post | Wakes |
|---|---|
| Session to `@<name>` or a group | each named session |
| Session, desktop, or owner script to `@root` or `@<room>:root` | root |
| Owner script (unstamped `dyna post`) to `@<room>:<name>` | that session |
| Anything to `@<room>` alone (the room owner) | nobody; root reads it as context |
| Any post sent with `--quiet` | nobody; each recipient reads it with its next turn |

A script, CI hook or chat bridge that runs `dyna post` outside Dyna posts as the owner. It must qualify every mention, so write `@<room>:root` to reach root. Use `--quiet` to forward every line of a log or chat as context, and post without it only for the lines that need an answer. `--quiet` conflicts with `--alert`.
A desktop draft with no mention goes to `@root`, the root orchestrator. Human and Voice requests wake root.
`--alert` and failed reports still wake root. A post to a real parent below depth 1 still wakes that parent.
The desktop also reads mentions within text, except inside backticks or escaped as `\@name`.
CLI and script mentions must lead the text.

Resolution errors store nothing. Unknown, closed, or non-resumable explicit names reject the whole post.
So do self-mentions, explicit idle sessions without harness history, disallowed selectors, and empty groups.
Non-resumable or unstarted group members appear in `skipped[]` as `{to, reason}`; remaining recipients still receive the post.
Records without `roomId` fail closed.

`dyna post --json` returns `{message, deliveries, skipped?}`.
Each delivery is `{to, delivery, turn?, pointer?, error?}`.
Delivery values include `room`, `session-queued`, `session-quiet`, and `failed`.
`session-queued` means the post and its wake intent are saved. The engine starts or steers the session after the store event.
`session-quiet` means the post is stored as context for the next turn. It queues no turn, so a quiet post does not print `session-queued`.
If the engine is down, messages wait until it starts. The receipt does not include a turn number.
Session delivery no longer returns `session-steered` or `session-followup` from the posting process.
If delivery fails, the CLI exits nonzero; the stored post and successful deliveries remain.
Script `post()` returns `{id, deliveries, skipped?, error?}`. Resolution errors throw; delivery failures return `error` after storage.
Check every result. Read `dyna guide workflows` for script examples and `dyna guide steering` for replies and reports.

## Attachments, cards, and pins

Put temporary files in `$DYNA_SCRATCH`. Dyna copies attachments and cards into the room.
Attach reports, designs, plans and long results for readers; do not give a scratch path as the only copy.

```bash
dyna post @parent --attach build.log "The build failed; the log shows the cause."
dyna attachment get <room> <id>       # prints the local path
# Add -o - to print bytes.
dyna post @parent --html "$DYNA_SCRATCH/report.html" "The chart shows three failures."
dyna room pin <room> <post-id>
```

Read every file listed in a task or post. Mentions in post text control delivery.
Defaults allow 25 MiB per file or card, eight attachments, and one card per post.
Change limits with `dyna settings`: `content.maxFileBytes` and `content.maxCardBytes`.

## Visuals

Post a visual for requested visuals, charts, HTML reports, “visualize,” or “viz.”
Use the session that asked, or `@parent`. Both wake root when it asked for the visual.
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

To change a visual, edit its file and update the existing card with `--update <post-id>`.
Every view then shows the newest version; older versions stay in the full view.
`dyna attachment get` prints the current file when you no longer have it.
Pin decisions and results sparingly.

## Interactive cards

A card can collect the owner's input, such as a choice or a short text, with `dyna request question --html`. The card sends
`dyna:submit` with `parent.postMessage`. The app shows the exact text under the card with Send and Cancel buttons.
Only the owner's click on Send sends it. The text is at most 16 KB, so send JSON for structured input.
It answers the open question; `choice` must be one of the question's `--options` when you set it.
The app replies with `dyna:submitted` (`ok`, `error?`). A question already answered gives `error: 'answered'`.
The card may only submit right after the owner clicked or typed in it, and once every 2 seconds.

```html
<title>Pick a voice</title>
<form id="f"><select id="v"><option>marin</option><option>cedar</option></select>
<button>Choose</button> <span id="s"></span></form>
<script>
f.onsubmit = (e) => {
  e.preventDefault();
  parent.postMessage({ type: 'dyna:submit', text: JSON.stringify({ voice: v.value }) }, '*');
  s.textContent = 'Waiting for the owner to confirm…';
};
addEventListener('message', (e) => {
  if (e.data?.type === 'dyna:submitted') s.textContent = e.data.ok ? 'Sent.' : 'Not sent: ' + e.data.error;
});
</script>
```

```sh
dyna request question --html "$DYNA_SCRATCH/pick.html" --meanwhile "Preparing the persona file" "Which voice should the guide use?"
```

The answer arrives as a post, with `source: "card"`. Treat it as the owner's input, but check it as data. On a plain
card from `dyna post --html`, a submit posts the text as an owner reply to you. Run `dyna visual check` first.
The voice guide (`dyna guide voice-agents`) uses this to let the owner choose a persona before a session.

For issue filing and triage, read `dyna guide git`.
