# Dyna desktop

The Wails window loads the web app from the running Dyna server.
The server owns the services. The window only shows the app.
Agents keep using the CLI.

Start the server with
`systemctl --user start dyna-engine`, then run `dyna-desktop`.
Use `dyna-desktop --addr 127.0.0.1:PORT` when the server uses a different
`engine run --addr` value. The desktop pairs automatically as one device.
Opening another desktop window replaces that device and ends the previous
window's session. A desktop launch also replaces any unused phone pairing code.

The same app also runs as a web app for a browser or a phone:
`dyna engine run`. It binds loopback only, and a phone reaches it through
`tailscale serve` or a Cloudflare named tunnel after a one-time pairing
code. See [WEB.md](WEB.md). Resident Labs Connect (`dyna engine connect
link`) adds a managed tunnel, passkey sign-in and push notifications; see
[CONNECT.md](CONNECT.md).

## What you can do

- **Start a session:** type `/<profile> <task>` in a room composer. `/`
  lists your profiles. The line under the composer shows the session name
  Dyna picks from the task, the model, and the directory it will work in.
  `#global` uses your home folder. Other rooms need a repo. Add files before
  sending to start the session with them. Dyna first saves your task and files
  as a normal owner post, then includes their names and absolute paths in the
  first prompt. Images keep an image extension so the agent can read them
  with its image tool. A new session cannot start as a reply; cancel the reply
  first. `//text` posts a literal `/text`.
- **Type a command:** the same `/` list holds app commands. `/room <name>`
  switches rooms (it completes names; Enter on one opens it). `/new <path>`
  opens New room on that path. `/files [path]`, `/changes`, `/terminal`,
  `/jobs` and `/workflows` open those views. `/run <template> [input]`
  starts a workflow; the input is JSON when it parses as JSON. `/rename
  <name>`, `/pin`, `/unpin`, `/archive`, `/unarchive` and `/pair` act on
  the room or open pairing. A command with nothing after it opens its
  dialog. A profile with the same name as a command wins, so `/<profile>
  <task>` always starts a session.
- **Control a session:** right-click a member, press the Menu key or
  `Shift+F10` on it, or use its `⋯` button. Message, open its profile,
  mention it, copy its address, stop its turn, or close it. Stop and Close
  ask first. **Promote to lead** makes an open subagent a top-level lead
  without stopping its work. Its children, conversation and history stay
  intact; descendant depths adjust. Closing a session also closes its children
  that own no work; children that still own work move up to its parent. **Transfer
  to new model** opens one model picker. Clicking a model starts immediately.
  It asks for a written
  handoff, with a bounded transcript fallback. The address, children, files,
  old turns and tracked PR ownership stay together. Cancel is available
  while waiting. Opening the picker resumes a saved transfer; closing it
  leaves the transfer running.
- **Run workflows:** `#workflows` in the room sidebar lists the room's runs
  with live progress; a run opens to its phases, agents, log and result.
  **Run workflow** starts a template or a host script with an input.
  **Cancel run** stops a live run, after a confirm. **Steer** on a working
  agent sends it a message.
- **Read the repo:** click **Review** to open Files, Changes and PRs beside
  chat. Choose a session to review its worktree. Terminal remains in the
  sidebar. See [Workspace views](#workspace-views).
- **Tidy rooms:** click the room name at the top of the sidebar, right-click
  a rail tile, or use a room's `⋯` on the home screen. Rename, pin to the
  rail, archive or unarchive, or delete. Delete asks you to type the room
  name.
- **Pair a phone:** Settings, then Devices, then **Pair a device** shows a
  one-time code and a QR code of the pairing link. The same pane lists
  paired browsers and Connect sign-ins, each with **Revoke**.
- **Edit profiles and settings:** the gear next to "you" opens Settings.
  Profile env values are write-only. Every `dyna settings` key is there.
- **See every completion:** session and workflow completions reach root and
  show their full result in the feed. Successful reports and normal manual
  stops go only to the owner. Failed child turns alert their current available
  parent and the owner once, including usage limits, provider errors, schema
  failures and crash repairs. The parent can inspect the exact failed turn or
  choose a working model with **Transfer to new model**. Explicit `@parent` posts,
  owner messages and PR automation notices keep their normal routing. Workflow
  and shell completions still reach their parent session.
- **Use it from a phone:** `dyna engine run`, then pair the phone. Rooms,
  posts, sessions, workflows, pins and Settings work there. See [WEB.md](WEB.md), or [CONNECT.md](CONNECT.md) to sign in
  with a passkey from anywhere and get notifications.

## Build and run

Linux needs `webkit2gtk-4.1` and `gtk3`. The `webkit2_41` build tag selects
WebKitGTK 4.1. You do not need the wails CLI.

```sh
desktop/build.sh                  # builds only the desktop window
desktop/build/bin/dyna-desktop    # opens the window on the running server
```

- The engine wakes sessions through its own executable.
- Run the engine with --read-only to disable posts and read marks.
- The server drops inherited `DYNA_*` stamps at start. The web app acts as
  the human room owner.
- Once a day, through `dyna update`'s cache, the server checks for a newer
  `dyna` release. The app shows a notice. It never installs one; run
  `dyna update`. `updater.autoCheck=false` or `DYNA_NO_AUTO_UPDATE=1` on
  the server turns the check off.

## Layout

| Column | What it shows |
|---|---|
| Room rail | Rooms as tiles, unread badge, gold dot for live work; the top tile opens the room list |
| Room sidebar | Room name (click for the room menu) and repo, Messages and Issues, Work and Machine views, sessions working now, the Settings gear |
| Feed | Day dividers, a New line, sender groups, replies, full completion results, a `Message #room` composer with `@` and `/` completion |
| Members | The owner, with the room's root seat under it: the internal root session (labeled `root`; open it to see its turns and tool calls) and a line naming the seat holder, "internal root" or "external orchestrator since <time>" with a Take back seat button. Then sessions as a tree with gold/green/red status, each with a context menu. Finished (dormant) sessions sort after their other siblings, with each subtree kept together, and fold into a Finished group with coral status dots, a Dismiss action, and a confirmed Clear finished action. A post still wakes a finished session with its full conversation. Dismissal preserves transcripts and reports retained dirty or unmerged worktrees in the room. Subagents fold under their parent; see below |
| Drill-down | A member's profile card and session view in the center column, with Messages, Media, Computer and Shell tabs when available |

The room has **Messages** and **Issues** tabs. Messages shows top-level
agents speaking to each other, their final results, and explicit messages
to the owner. The review workspace's PRs tab shows open pull requests and
saved merge progress. Issues shows local Dyna issues and the repository's GitHub issues.

The member list folds subagents under their parent, like categories in
Discord. A chevron left of the avatar opens or closes a subtree. By default,
the focused session and its ancestors open, so you see that team; every
other subtree stays folded. In Messages and workspace views only top-level
sessions show. A folded row shows how many subagents it hides,
and a gold "N live" chip when some of them are working. A chevron click lasts
for that room and view. Top-level trees with only finished or closed sessions
move into a folded **Finished** group at the end of the list. The group opens by
itself when it holds the focused session. **Select** mode opens every subtree
and the Finished group, so every session can be picked.

Saved links to old agent channels open Messages. Pins open the Messages
tab at the pinned post, including posts in older history. Opening a workspace
view does not mark hidden messages as read.

Plain messages in Messages go to the top-level team. Explicit recipients
and session commands keep their normal behavior. The recipient preview uses
the same Go parser as send, so unknown inline tokens, escaped mentions, and
mentions inside code do not override the default recipients.

The header's left button, or `Ctrl+B`, hides the room sidebar and gives the
feed its width; the rail stays. The app keeps that choice and the member list
choice in the webview's local storage (`dyna.layout`). The same key holds
the last open room and its view. Review links also preserve the selected tab. After a
restart the app opens that room again, in the desktop and in the web app (each
browser keeps its own). If the room no longer exists, the app opens the first
active room. A link that names a room (`#room=…`) wins.

## Pull requests

The app shows the PR watcher (`dyna git pr`, see [docs/pr-tracking.md](../pr-tracking.md)) in four places:

- **Agent rows:** an agent with a tracked PR shows a small chip with the PR
  number and its state in the member list and under Working now: CI running,
  CI failed (n of 3), In review, Changes requested, Approved, Paused, Merged.
  Coral means CI failed, gold means someone must act, sage means good.
- **Session view:** a Pull requests tab shows each tracked PR as a card: a
  CI, Review and Verdict progress line, the CI checks, the review round, the
  verdict and its summary, and why automation is paused, if it is. Open on
  GitHub, Resume (after a pause) and Untrack act on it. Merged, closed and
  untracked PRs fold into a short list; Track again restarts watching.
- **Room menu, Pull requests:** set the reviewer (off, a new session from a
  profile for each review, or one existing session). The dialog states the
  current setting in plain words. The palette has the same command.
- **Feed:** watcher posts show as compact rows: the PR, what happened, who it
  went to, the text in short, and the CI log tail folded under a toggle.

Closing a session whose PR Dyna still watches asks first. The dialog names
the PR and offers Close anyway.

A check without a details link can use the same bot's latest PR comment or
review. The PR page is the fallback when neither link is available.

## Workspace views

The **Review** button opens Files, Changes and PRs beside chat. Its workspace
selector chooses the repository or a session's worktree; Changes requires a
session. Terminal remains in the sidebar, and a session's Shell tab shows
its shells. On a phone, Review fills the screen; **Back to chat** closes it.

- **Files:** a folder tree beside the file view. Folders load one level at
  a time. Arrow keys move through the tree: Right opens a folder, Left closes
  it or goes to its parent, Enter opens. The eye button shows dot files.
  Code has line numbers and syntax colors; markdown opens rendered, with a
  Source switch; images show inline; a binary file gets a short note. On a
  phone the tree becomes one folder list at a time with a path bar, and a
  file opens full width with a back button.
- **Changes:** git status as Staged, Changes (not staged) and Untracked
  groups, as git shows them. Each file row opens to its diff, with old and
  new line numbers and added or removed lines on a green or red wash.
  Expand all and Collapse all work on every row. A short list starts open.
  Recent commits come last.
  The view reloads when the window gets focus, or with the reload button.
- **Wrap:** long lines wrap by default in files and diffs, with the line
  number kept beside the first part. The Wrap button turns it off, and the
  app remembers the choice (`dyna.code` in local storage).
- **Terminal:** shells on the host, one tab each, opened in the room's repo
  or the session's folder. `+` (or `Ctrl+Shift+T`) opens another;
  `Ctrl+Shift+W` closes one, after a confirm while it runs;
  `Ctrl+PageUp` and `Ctrl+PageDown` switch tabs. Shells live on the host:
  closing the view or reloading the page reattaches with the scrollback.
  When a shell exits, a bar offers a new one. On a phone a key row adds
  Esc, Tab, Ctrl (hold for the next letter) and the arrows. A room view
  lists every shell in the room, its sessions' too, named by session. The
  terminal shows only in builds with a terminal renderer
  (`TermPane.svelte` exports `available`).
- **Shell jobs:** background `dyna shell` commands in the room, newest
  first, with a gold, green or red state dot and the exit code. A job opens
  to the last 400 lines of its log, which follows a running job.

Automatic shell-job results do not show in channel feeds, and they never
count as unread or send a push. `#shell-jobs` lists each job with its state
and log. The result post still reaches the job's parent session, so agent
delivery does not change. A link or pin to a shell result opens that job in
`#shell-jobs`.

A session view shows cards, images, and files inline. Your attachments appear below
your prompt without repeating its text. **Show earlier turns** loads six more
turns and keeps your place in the log.

A session view lists its steering queue above the composer: messages you
sent while its turn runs. A waiting message has a remove button until the
turn takes it. In the transcript, a steering message reads *delivering…*
until the model reads it, then *delivered*.

The command palette (`Ctrl+K`, or the search button on a phone) runs these
too: open files, changes, terminal or shell jobs; the session tabs while a
session is open; Run a workflow, cancel a running run; rename, pin,
archive or delete the room; pair a device; manage devices.

## Speed

A room opens with one call, `OpenRoom`: the newest 50 posts, the members, the
composer choices, the pins and the runs. Older posts load when you scroll up
(100 more each time) or tap "Show N earlier posts". A filtered feed view, a
reply line or a pin that points at an older post loads the whole log first.
Unread posts are never cut off: the first screen reaches back to the oldest
unread one, so opening a room still marks everything it shows as read.

Go keeps caches so a store with thousands of old sessions, runs and shells
stays cheap:

- `subagent.Store.Info` keeps parsed session records and last-turn status,
  keyed by their SQLite row revisions. It checks the record revision on
  each read, including closed sessions.
- `room.Store.LiveRoomIDs` does the same for sessions, runs and shells, and
  skips shells it has seen finish.
- `roomsvc.Rooms` counts unread posts from a per-room cache and reads only
  the log bytes added since the last call.
- The room list refreshes after room, post and member events, not after pin
  or run events.
- `dyna engine run` compresses replies over 2 KB and the front end files
  with gzip when the browser accepts it.

`MarkReadMany` marks all unread posts of a room in one call.

## Command palette and New room

`Ctrl+K` (`Cmd+K` on a Mac) opens the command palette. On a phone, the search
button at the right of the header opens it. Esc, the close button on a phone,
or a click outside closes it.

The root list shows rooms first (name, repo, unread count; archived rooms
last), then matching sessions, runs, PRs, messages in the current room, and
commands. Type to filter rooms, work and commands, and search the room log.
Arrow keys and Enter pick a row on the desktop. A tap picks a row on a phone.
Backspace on an empty input goes back one view. The app's own commands are the
`paletteCommands` list in `App.svelte`; add a command there. The component is
`web/src/lib/ui/Palette.svelte`, and its text rules are in `web/src/lib/ui/palette.ts`.

**New room** switches the palette to path mode. The input starts as `~/`. Path
mode is on while the text starts with `/`, `~/` or `~`. A Folders list shows the
folders in the text up to its last slash, filtered by the text after it (case
insensitive, prefix). A `..` row comes first when the text ends with a slash,
except at `/`. Folders that start with a dot stay hidden unless the filter
starts with a dot. A `git` badge marks a folder that has a `.git` entry. Enter
or a tap on a folder row adds `name/` and lists that folder. Enter with no row
highlighted, `Ctrl+Enter`, or the Create room button uses the typed path.

Using a path calls `InspectPath`. A repo that already has a room opens that
room. Otherwise a confirm view shows the path, what it is, and the room name
(default: the repo name, editable):

- A git repo, or a folder inside one: Create room. The room uses the repo's
  main worktree, as `dyna room create --repo` does.
- A folder that is not a repo: "Make it a git repository and create room"
  runs `git init` there first.
- A path that does not exist: "Create folder and room" runs `mkdir -p`, then
  `git init`, then creates the room.

The Go side is in `internal/filesvc/browse.go`: `BrowseInfo` (the home
folder), `BrowseDirs` (one level of folders, sorted, no files; at most 500
entries, 2 seconds, 512 characters, no NUL; folders only, symlinks to folders
included). `InspectPath` (read only) and `CreateRoom` stay in
`internal/roomsvc/browse.go`. `CreateRoom` is gated
like `Send`: read-only mode and a broken client refuse it. It never clones.
There is no browse root. A paired device has the same reach as a shell, so
the picker lists any folder the app's user can read.

## Pinned posts

Hover a post and press Pin to keep it. The pin icon in the room header
(with a count) opens a Pinned panel in place of the member list; Esc closes
it. Each item has Jump (scroll the feed to the post) and Unpin. Pins live in
`dyna.db`, at most 50 per room. Agents and scripts
use `dyna room pin|unpin|pins <room> [<post-id>]`. A pin from the CLI shows
in the app at once: `roomsvc.Watch` emits a `pins` event when the stored
revision changes.

## Go surface

The engine binds service methods directly in internal/engine/calls.go.
Every browser API name has one method and an explicit read or write decision.
websvc.Bind validates the table before the engine serves requests.
See [WEB.md](WEB.md#api).

- Rooms: `Rooms`, `OpenRoom`, `Feed`, `FeedTail`, `Members`, `MentionChoices`,
  `Preview`, `MarkRead`, `MarkReadMany`,
  `Pin`, `Unpin`, `Pins`, `Health`, `DataDir`.
- New room: `BrowseInfo`, `BrowseDirs`, `InspectPath`, `CreateRoom`.
- Sending: `Send` is the composer's only send call. Go parses the draft at
  send time and returns `{kind: "post"}` or `{kind: "spawn"}`.
- Sessions: `Profiles` (names and models only), `Transcript`, `TranscriptMore`, `Profile`,
  `SendToSession`, `StopSession`, `CloseSession`, `ForceCloseSession`,
  `PromoteSession`, `TransferSession`,
  `SessionTransfer`, `CancelSessionTransfer`.
- Workflows: `Runs`, `RunDetail`, `ListTemplates`, `StartRun`, `CancelRun`.
- Files and changes: `ListEntries(roomRef, sessionRef, path, showHidden)`,
  `ReadFile(roomRef, sessionRef, path)`, `GitStatus(roomRef, sessionRef)`,
  `GitDiff(roomRef, sessionRef, path, staged)`. An empty session reference
  selects the room repo; a session reference selects its working directory.
- Devices: `Devices`, `RevokeDevice`, `CreatePairing`.
- Session queue: `SessionQueue`, `DeletePendingSteer`.
- Room housekeeping: `ArchiveRoom`, `UnarchiveRoom`, `RenameRoom`,
  `DeleteRoom`, `PinRoom`, `UnpinRoom`.
- Config: `ConfigProfiles`, `SaveProfile`, `RemoveProfile`,
  `SetDefaultProfile`, `SetProfileDisabled`, `Settings`, `SetSetting`,
  `UpdateNotice`.
Go also serves room bytes over plain HTTP (`roomsvc.FilesHandler`), so the
web app loads them the same way: `/attachments/<room>/<id>` (images, and
other files never as HTML) and `/rich/<room>/<id>` (HTML cards; the web app
serves these from a separate card origin only).

`roomsvc.Watch` polls the stores every 500 ms and emits `dyna:event`
(`posts`, `rooms`, `members`, `pins`, and `runs` every fourth tick) to the
front end.

## Attachment previews

Open a file in the feed or Pins to preview it. Markdown and Word `.docx`
documents use Dyna's reading style. CSV and TSV files open as scrollable
tables with a header toggle. JSON opens with indentation and preserves
large integers. PDFs have page controls and a selectable text view.
Markdown, tables, JSON, and HTML cards also have a Source view.

Download saves the original file: a native save dialog on desktop, or a
browser download on the web and phone. Other formats, including legacy
`.doc` files, show a download action. Text previews support files up to
2 MB. Table previews show up to 500 data rows and 100 columns; the download
always contains the complete file. Word previews preserve document
structure and use Dyna's fonts and colors, so page layout can differ from
Word.

## Card and file size settings

Cards and file attachments each default to **25 MiB**.
Open **Settings → Cards and files** on desktop, paired web, or Connect to edit
`content.maxCardBytes` and `content.maxFileBytes` independently. Both settings
accept sizes such as `25 MiB`, `25MB`, `25m`, or a positive byte count.
MB means MiB (binary units). The CLI uses the same settings file:

```sh
dyna settings set content.maxCardBytes "25 MiB"
dyna settings set content.maxFileBytes 50MB
dyna settings get content.maxFileBytes
```

Changes apply to each new upload and post without restarting the host.
An upload already in flight keeps its transport allowance; posting staged
attachments validates them against the current settings. A browser reads the
host's file limit again before each upload. Existing posts, cards, previews,
and downloads remain readable after a limit reduction. Preview format limits
(such as the 2 MB text preview) stay unchanged.

A post still accepts at most eight attachments, including at most one card.
The total byte allowance is derived as
`7 * maxFileBytes + max(maxFileBytes, maxCardBytes)` (200 MiB by default), so
all eight permitted attachments can reach their individual limits. Web and
Connect JSON requests allow base64 expansion, per-file padding, and 1 MiB of
metadata above that total. Limits must fit overflow-safe arithmetic and JavaScript
integers; invalid values report the accepted numeric range. Raising a limit
can increase host and browser memory use. Card scripts and network policy stay
the same.

## HTML cards

An agent can post one HTML file as a card: `dyna post @root --html card.html --
"summary"`. Agents and `dyna room read` see the post text and a file line,
never the HTML.

The feed shows a card in `<iframe sandbox="allow-scripts">`: its scripts run
with an opaque origin, so they reach no app storage and no app routes. Forms,
popups, dialogs and top navigation stay off. Cards can load remote content
over HTTPS (fetch, images, fonts, styles), so a card can show live data and
can also tell its author that it was viewed. Plain `http:` is blocked, so a
card cannot reach localhost or LAN devices.

In the desktop window, scripts are safe only because of the IPC gate
(`gate_linux.go`).
WebKitGTK lets every frame of the page, even a sandboxed or cross-origin one,
post to Wails' message handler, and Wails checks only the top page's URL. At
start the gate replaces that handler. A message reaches Wails only with a
secret made at each launch, and only the top page holds it: a script that
WebKit injects into the top frame alone. If the gate does not install (a host
without WebKitGTK 4.1, or a Wails change), the app logs it and cards run with
no scripts. Each launch logs the install outcome. The web app needs no gate:
it serves cards from their own origin (see [WEB.md](WEB.md#cards)).

A card shows inline in the chat, at its full height, with no frame or header.
Source and Expand appear on hover. The server adds a small script to every
card that reports the card's height as it changes, so cards fit their content
up to 12000 px without a scrollbar of their own. Cards that cannot run scripts
keep their declared or default height.

A card talks to the app only with `parent.postMessage`: `{type: 'dyna:height',
height}` resizes it, and `{type: 'dyna:open', url}` opens an http or https
link in the owner's browser, only right after a click in that card. A card
that navigates itself by a link is restored once, then stopped.

A card can also send the owner's input back with `{type: 'dyna:submit', text,
choice?}`. `text` is up to 16 KB; a card that needs structure sends JSON in it.
The app takes a submit only while the card has focus and the owner has just
clicked or typed in it, and at most once every two seconds. It then shows the
exact text under the card with Send and Cancel buttons that belong to the app,
outside the frame. Only a click on Send sends it. A newer submit replaces a
waiting one, and Cancel gives the card `ok: false` and `error: 'cancelled'`.
What is sent is marked as written by a card (post `source: "card"`, shown as a
small "card" label). If the card is the
post of an open question (`dyna request question --html`), the submit answers the question,
with `choice` when it is one of the question's options and with `text`
otherwise. On any other card it posts `text` as an owner reply to the card's
author, in the post's conversation, so the author wakes. The app answers the
card with `{type: 'dyna:submitted', ok, error?}`. A question that is already
answered gives `ok: false` and `error: 'answered'`. The owner confirms each
submit, so a card cannot send anything by itself.

## Repository files and Git previews

The shared API lists one directory level, reads text, serves image previews,
and shows Git changes for a room repo or a session working directory. Lists
put directories first and sort names. `showHidden` includes dot files such
as `.env`; `.git` entries stay out of directory lists. There is no secret-file
deny list. Paths can be relative to the selected directory or absolute, and
symlinks are followed. This follows the paired-device shell-access model.
`#global` opens your home folder without marking it as the room's repo.
Git status there reports that the folder is not a repository, unless your
home folder is itself a Git repository.

Text previews and unified diffs stop at 2 MiB and set `truncated`. Binary
files return `binary: true` with no text. Supported inline images are PNG,
JPEG, GIF and WebP. `ReadFile.url` loads the image through
`/attachments/<room>/workspace?session=<session>&path=<path>` with the same
content security policy and type checks as attachments. Workspace responses
use `no-store`, because their contents can change. Oversized image requests
return HTTP 413. HTML and SVG files are never served as active documents.

`GitStatus` returns the branch, upstream, ahead/behind counts when available,
the last five commits, and a list parsed from Git porcelain v2. Each path
includes both index and working-tree status. A file can have both `staged`
and `unstaged` set. Renames include `originalPath`. Paths use the selected
working directory, including `../` for files outside a session subdirectory.
Status output is capped
at 2 MiB and marks `truncated` if some rows do not fit. Unborn branches have
no commits, and detached heads use Git's `(detached)` branch label.

`GitDiff` uses the index for working-tree changes and HEAD for staged
changes. An empty path includes all tracked changes for that selection.
A single untracked file has an addition preview, or a binary-file notice;
untracked files do not appear in the whole-repo diff. Git path arguments
are literal filenames. Git commands have an 8-second request timeout,
ignore inherited `GIT_*` variables, and disable filesystem monitors,
external diffs and text conversion. These APIs emit no new live events;
clients refresh them as needed.

## Workflow, device, queue and room APIs

These APIs work on desktop, paired web devices, and signed-in Connect
sessions. Writers honor read-only mode.

`ListTemplates()` reads the same template registry as `dyna templates list`.
`StartRun(roomRef, {template?, script?}, input)` returns `{id}`. Choose one
source: a template name or a host script path. Relative scripts resolve in
the room's working directory. `input` is any JSON value, passed as `--args`,
at most 256 KiB. Global uses your home folder; other rooms need a repo.
Start uses `dyna run --detach --room --dir`; Cancel uses `dyna runs cancel`.
A cancelled run shows "Canceled" in the feed. Failed runs still show their
error. Each agent in RunDetail carries its `session` ID. Steer an agent
with `SendToSession` on that ID; there is no run-wide broadcast or workflow
script steering.

`Devices()` lists paired browsers and live Connect sessions without token
hashes or keys. Each row has `current`: true only for the paired browser or
Connect session that makes the call. Desktop calls return false on every row.
`RevokeDevice(kind, id)` accepts `paired`, `connect`, or
`all` as kind, and a device/session ID or `all` as id. It returns the number
removed. Revocation uses the same stores as `serve --revoke`.
`CreatePairing(origin)` returns `{code, url, expires}`. The code works once
for five minutes and replaces any previous code. The URL ends in
`/#pair=CODE`; the client can render it as a QR code and prefill its pairing
form from that fragment. An empty origin uses the linked Connect origin,
or `http://127.0.0.1:7457`. Supply the reachable Tailscale or Cloudflare app
origin for a phone. This call does not start a server or tunnel.

`SessionQueue(ref)` lists room posts addressed to that session, with
`id`, `text`, `from`, `ts` (Unix milliseconds), `turn`, `state`, and
`cancellable`. State is `waiting`, `delivering`, `delivered`, or `cancelled`.
Delivered means the harness received the message; it does not mean the
agent has acted on it. `DeletePendingSteer(ref, messageID)` cancels only
an owner's waiting post to that session. The room post stays in history,
and delivery to other recipients stays unchanged. The runner claims input
under the same lock used for cancellation. A claimed message cannot be
deleted. `queue.json` stores only claim and cancellation IDs; existing turn
records supply delivery acknowledgements. Queue changes emit `members`
events so clients can reload the queue.

Archive hides a room until new work or a post revives it. Global never
archives, and live work keeps a room active. Unarchive restores a room and
refreshes its activity time. Rename changes `displayName`, a human label;
`name` remains the stable routing address, so existing agent stamps and
history still work. Labels are 1 to 128 bytes on one line.
`PinRoom(ref, order)` pins a room before unpinned rooms, with smaller
nonnegative orders first. `UnpinRoom(ref)` clears it. Tied pin orders use
the normal rail ordering. `DeleteRoom(ref)` runs `dyna room delete`, with
all its checks: global, open sessions, and live runs or shells block it.
The UI must ask for the typed room name before calling DeleteRoom.
All room metadata changes emit `rooms` events to desktop and web clients.

## Host terminals and background jobs

The terminal API opens the host user's `$SHELL` as an interactive login
shell, with `TERM=xterm-256color`. If `$SHELL` is empty it uses `/bin/sh`.
A room ID opens its repository; `#global` opens your home folder. A session
ID opens that session's working directory, including a linked worktree.
Owner terminals do not inherit `DYNA_*` agent stamps. A paired or signed-in phone has the same shell access.

Terminals stay alive when a view closes, a page reloads, or a connection
fails. Each keeps the last 256 KiB of raw output in memory. Reattach resumes
from a byte offset. When output has fallen out of the buffer, `reset: true`
tells the view to clear its display before replay. Shells end when closed,
after one hour without input or output, or when the host app stops. Web
serve accepts `--terminal-idle 2h`; a negative duration disables idle reap.
There are at most 64 terminal records, including exited shells awaiting close.
Input is capped at 64 KiB per call, and dimensions at 1000 by 1000 cells.

`app.go` binds `TermOpen(ref, cols, rows)`, `Terms()`,
`TermAttach(id, after)`, `TermWrite(id, base64)`, `TermResize(id, cols, rows)`,
`TermClose(id)`, `ShellJobs(roomID)`, `ShellLog(id, lines)` and
`ShellLogRange(id, offset, limit)`. Terminal
records contain `id`, `title`, `ref`, `alive`, `cwd`, `roomId`, optional
`sessionId`, `shell`, `cols`, `rows`, `created`, `lastActivity`, `exited` and
`exitCode`. `Terms` lists all rooms; filter on `ref` or `roomId` in the view.
`TermAttach` returns `{id,data,start,offset,reset?,exited,exitCode}`. `data`
is base64 raw bytes, not UTF-8 text. Offsets count raw bytes, starting at zero.
Desktop live output uses the same shape on Wails event `dyna:terminal`.

The transport-independent `api.termAttach(id, callback, after = 0)` returns
`Promise<detach>`. It sends scrollback and live output to the callback.
Call `detach()` when the view closes; use `api.termClose(id)` to end the
shell. Input must be encoded as UTF-8 bytes, then base64. Other wrappers are
`api.termOpen`, `api.terms`, `api.termWrite`, `api.termResize`, `api.termClose`,
`api.shellJobs` and `api.shellLog`. No component needs transport-specific code.

`ShellJobs` lists the room's `dyna shell` jobs, including completed jobs:
`{id,command:[...],name?,cwd,state,code,started,ended?}` plus, when set,
`signal`, `stopBy` (`agent`, `owner` or `cleanup`), `backgroundAt` (when a
foreground call outlived its limit) and `outputLines` (known once the job ended).
While a job is open or has just ended, a row also has `outputBytes` and `tail`,
its last 6 lines as plain text. States are `starting`,
`running`, `exited` and `lost`. `ShellLog` returns `{text,truncated}` with
ANSI controls removed. It defaults to 200 lines and caps requests at 2000
lines and the last 256 KiB. The view can refresh this call while a job runs.
`ShellLogRange` reads the whole log forward: it returns `{text,next,size}` for up
to `limit` bytes (at most 256 KiB) from `offset`, ending at a line end when more
follows, and never inside a character (a chunk may run up to 3 bytes past `limit`
to finish one). The text is raw: it keeps escape codes and carriage returns, so
chunks join exactly; remove the codes and collapse progress updates after joining.
Read on from `next` until it equals `size`.

A session transcript item for a call of Dyna's `shell`, `shell_output` or
`shell_stop` tool has `jobId` and `jobOp` (`run`, `output` or `stop`). A `run`
item also has `job`, the job's row with its tail.

## PR tracking

Open **Review → PRs** to browse the room's open GitHub pull requests and merge
progress. A focused session filters its PR list. Open the room's **Issues**
tab to browse issues. Search narrows the lists.
Read descriptions, comments, reviews, checks, and PR diffs.
This uses the host's `gh` login; install the GitHub CLI and run `gh auth login`
if needed. GitHub lists show up to 60 results; search narrows larger lists.

Tracked PRs reuse the session's status chip and PR card. **Track** links a PR
to an open session in the room. **Untrack** stops its watcher. **Mention in chat**
adds the PR or issue as a reference in the room composer.
You can write a message before sending it. The command palette and `/prs`
and `/issues` commands open the same browser.

Agents can link a PR to their session with `dyna git pr track <number|url>`.
The host watcher batches CI failures and useful comments to the author,
starts the room's reviewer alongside CI, and sends Dyna approvals to the
owner. "Ready to land" and CI escalations walk from the author up its parent
chain to the first open session. The engine merges approved PRs after their
merge batch passes CI, or root explicitly accepts every failing test as flaky.
CI failures do not stop active author work. See [PR tracking and agent review](../pr-tracking.md) for the
commands, hidden comment markers, pause reasons, and watcher lifecycle.
PR status, check details, room reviewer settings, untrack, and resume are
available through the same App APIs on desktop and web. PR changes emit live
`prs` events. Session close transfers PR authorship to the nearest open
ancestor and returns reviewer roles to the pool.
Agents can teach repo-scoped bot noise templates with `dyna git pr noise`.
Every wake reports the drop count and matching rule IDs. Human comments and
real review content stay protected; `dyna git pr noise rm` removes a wrong rule.
CodeRabbit rate-limit notices stay quiet even when they include the standard
Autofix control. Changed controls, inline comments and appended findings still
reach the author. Previously delivered feed posts remain in the history.

The room menu's **Pull requests** dialog lists learned bot noise rules for
the room repository and its tracked PR repositories. Each rule shows the
bot, repository, reason, comment template, match count, and source link when
available. **Remove** takes effect immediately for every room that shares
that repository. Reload refreshes the list without changing reviewer roles.
Desktop, paired web, and Connect use the same room-scoped list and removal
methods; read-only mode blocks removal.

Both PR views show how many comments or reviews the filter skipped. This
counts each item's latest saved decision, including built-in and learned
rules. Delivered updates do not reset it. If a comment changes and its latest
decision is to keep it, that item leaves the count. Removing a rule does not
rewrite past decisions or replay comments.

## Transfer to a new model

The member menu's **Transfer to new model** opens one model picker. Click a
model to start immediately. Progress, cancellation and retry stay in the same
picker. A failed old agent uses the transcript immediately. Other agents have
up to 30 seconds to write a handoff; a handoff starts the switch as soon as it
arrives. Otherwise the transfer uses transcript context at the deadline.

Opening an active transfer resumes its saved worker automatically. If a worker
cannot start, select that same model to retry. A failed replacement lets you
choose a model again. Startup has a 60-second model-output limit and restores
the prior model and native conversation on failure. Identity, children, history
and tracked PRs stay together. Old turns keep their own model labels. Advanced
CLI recovery commands remain available in `dyna guide recovery`.

## Review publishing

Agent reviews stay in Dyna, even when the room retains a GitHub publishing
setting. Only the owner can publish GitHub comments or reviews and change
the publishing setting. Local Markdown feedback, including file:line references,
appears in the shared PR card in session and repository views. Old reviews
with GitHub review IDs remain labeled **Published on GitHub**. Desktop, paired
web and Connect share PRSettings and SetPRSettings.

## Harness installations

Configuration → Harnesses manages agent CLI installations from desktop, web, and mobile. See [HARNESSES.md](HARNESSES.md) for installer ownership, repair, and recovery behavior.

Settings → Usage shows the plan and usage limits of Dyna's private harness accounts. Sign in and switch accounts on the Harnesses page. See [private harness accounts](../accounts.md).

## Select sessions and run shared actions

In the member list, choose **Select**, then click or tap open session rows.
Selected rows use the existing highlight, without checkboxes. Ctrl/Cmd-click
also enters selection mode and toggles a row. Outside selection mode, ordinary
clicks still open profiles. Row menus remain separate from selection. Enter or
Space toggles a focused row. Escape or **Cancel** leaves selection.

The selected-count bar opens **Actions…**. **Select all sessions** chooses all
eligible visible sessions. Selection keeps stable IDs when hierarchy or finished
ordering changes, clears on a room change, and drops sessions that close or
leave the hierarchy. The 16-session bound applies only to **Consolidate**.

- **Message** addresses the room composer to selected agents and keeps its
  existing message text. Sending remains an explicit composer action.
- **Stop**, **Promote to lead**, and **Close sessions** use one shared screen.
  Close asks once for the selection. Tracked pull request guards still apply;
  forcing remaining refusals requires another explicit shared confirmation.
- **Transfer model** chooses one configured model and applies it to each selected
  agent's own session. Accepted requests can still be waiting or starting. The
  screen follows each transfer's progress, and failed attempts can be retried.

Every shared action shows per-session acceptance or refusal. Successful results
remain visible, and refused or failed sessions stay selected for retry when
still eligible. Larger selections dispatch sequential chunks of at most 512;
a later request failure keeps earlier confirmed results. Unknown outcomes and
unsubmitted sessions remain clearly marked for explicit retry. Member and PR
lists refresh after dispatch, rather than using result snapshots as a hierarchy.
**Recent actions** reopens results while this app instance remains open. Hiding
a result screen leaves chunk dispatch and accepted work running. Reloading the
app can lose unsubmitted chunks and local bulk results; accepted host transfers
remain durable and accessible through the member's model-transfer menu.

### Consolidate into one agent

Select 2–16 sessions and choose **Consolidate…**. One screen chooses the agent
to keep and, optionally, another configured model. The default keeps that
agent's current model. Choosing an agent or model does not start work; press
**Consolidate** once. Other selected sessions hand off their work before they
are archived. The kept agent continues with their combined context. Backend
eligibility errors stay inline if a session closed, entered a transfer, or
became reserved meanwhile.

Progress stays in the same screen. **Close** leaves durable work running.
The member list's **Consolidations** links recover saved work after closing the
screen, changing rooms, or reloading the app. Opening an active operation
resumes the same saved worker. Failed work offers **Retry consolidation** with
the same kept agent and model. Waiting work can be cancelled; interrupted
cancellation offers **Finish cancellation**. Model and kept-agent choices stay
fixed once work starts. Source names open their saved session histories, and
**Open kept agent** continues the finished conversation.

Desktop, paired web and Connect use the same controls. Tab stays inside the
shared screen; Escape closes it without cancelling accepted work.

## Root seat

Each room has one root seat. The internal root session holds it by default: a top-level `root` session (default profile `opus`) that Dyna starts on the first post the human sends to @root. While it holds the seat, posts to the owner wake it: the human's posts to @root, agent posts and notices to @root, and turn reports owed to the owner. Its final responses are posted in the room as `root`. It runs in root mode (`DYNA_MODE=root`), so its commands act as the room owner.

An external orchestrator takes the seat with `dyna root attach --room <room>`. Attach stops a running internal root turn. While the seat is external, posts to @root wait in the room log for the orchestrator's `dyna room watch`. `dyna root detach --room <room>` gives the seat back; the internal root never sees the posts the external orchestrator handled, and `--handoff <file|->` passes a note that becomes its next turn input. Members shows the holder, and Take back seat (`ReturnSeat`) detaches for an orchestrator that stopped without detaching. See `dyna guide external-orchestration`.

## Pause all, resume all, and clear dormant sessions

The **Sessions** header menu offers **Pause all**, **Resume all**, and
**Clear dormant sessions…** on desktop, paired web and Connect. No selection
mode or checkboxes are needed. The member list shows **Sessions paused**,
the running and held session counts, and a direct **Resume all** action.
Room status refreshes after events, on room entry, and every five seconds.

**Pause all** stores a room-wide hold. Current harness calls finish naturally;
new turns, room-post delivery and later continuations wait. New owner-started
sessions can remain queued. This hold applies to agent sessions. Existing
workflows and owned shell jobs continue; their posts wait for delivery. **Resume all** releases the room hold
and can recover held work after a restart. It preserves each workflow's separate
pause choice. Held counts include queued sessions and sessions with pending
posts, and exclude workflow workers.

**Clear dormant sessions…** asks once, then closes the room's dormant
sessions. A session goes dormant by itself when a turn ends with an empty
responsibility list, so no idle timer ever makes a session dormant. The host
checks responsibilities again and keeps any session that gained work after it
went dormant. Once a day the host also closes sessions dormant and inactive for 7
days. Neither forces a close or deletes files or history.

Progress and results appear under the Sessions header. Results preserve the
host's address order; refusals expand automatically and show their reasons.
Room changes and teardown ignore late callbacks from the old visit. Hiding
the member list does not cancel an accepted host action. The pause state is
durable; the displayed mutation results belong to the current room visit.

The equivalent CLI commands are `dyna room pause <room>`,
`dyna room resume <room>`, and `dyna room clear-dormant <room>`.
`dyna room session-controls <room> --json` reads the same shared state.

## Session messages

Open a member's session to focus on that agent. The tabs are **Messages**,
**Media** (when the agent has attachments), **Computer** (when available),
and **Shell** (when it has shells). Media shows images, HTML cards, and files
sent by or addressed to this agent, including owner uploads and posts from earlier turns. It groups
the newest 200 posts by day. Click an item to open its file or card viewer.
Files, Changes and PRs open in the review workspace beside chat. Terminal
remains in the sidebar.

Messages share an avatar and header when the same author writes within
seven minutes. Turn dividers mark progress. Each turn groups tools, shell commands
and child activity into expandable steps. Posts to root show inline, with their
HTML cards and files.

Posts between agents form one thread per counterpart. Click a thread chip or
**Threads** to open the panel. The app remembers whether the panel is open.
The panel also shows the agent's children, tracked PRs, and shell jobs. Its
composer posts to the counterpart as you. On small screens, the panel overlays
the messages. The main composer keeps its file uploads and steering queue.
