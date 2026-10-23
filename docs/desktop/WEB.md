# Dyna web app

`dyna engine run` runs the desktop app as a web app. You get the same
rooms, feed, composer, sessions, workflows and pins in a browser or on a
phone. It is the same Svelte front end and the same Go API as the desktop
window: no second copy of either.

The server listens on loopback only (`127.0.0.1`). To reach it from a phone,
you put a proxy in front of it that you control: `tailscale serve` (your
tailnet only) or a Cloudflare named tunnel on your own domain. Neither proxy
counts as proof of who you are. A one-time pairing code and then a device
cookie are the only proof.

## Start it

```sh
./install.sh                  # builds web assets and the root binary
dyna engine run               # app on :7457, cards on :7458
```

The root binary includes the web app. The engine needs no GTK, WebKitGTK,
or display. To build manually, run npm ci and npm run build in web/, then
run go build -o dyna ./cmd/dyna at the repository root.

At start, the engine prints a pairing code. Open http://127.0.0.1:7457,
type the code, and the browser is paired.

An already paired device or the desktop app can pair the next one: Settings,
then Devices, then **Pair a device**. It shows a one-time code and a QR code
of the pairing link (`<web app address>/#pair=<code>`), drawn in the page.
Set the web app address to the name the phone uses, such as a Tailscale or
Cloudflare address. Scanning the code opens the pairing screen with the code
filled in; the app removes it from the address bar. The same pane lists
paired browsers and Connect sign-ins, each with **Revoke**.

| Command | What it does |
|---|---|
| `dyna engine run` | Run the server. Prints a pairing code. |
| `dyna engine run --pair` | Print a new pairing code for the running server. The old code stops working. |
| `dyna engine run --devices` | List paired devices: ID, name, paired, last seen, expiry. |
| `dyna engine run --revoke <id>` | Remove one device (or `all`). It loses access on its next request, and its open event stream ends within 15 seconds. |
| `--route <app origin>,<card origin>` | One more way to reach the server (repeatable). See below. |
| `--read-only` | Never write: no posts, pins, attachments or read marks. |
| `--addr`, `--card-addr` | The two loopback listeners. Default `127.0.0.1:7457` and `127.0.0.1:7458`. A non-loopback address is refused. |

A pairing code is 10 characters and works once. It lives 5 minutes, and 5
wrong tries kill it. The owner can make one with the CLI or the authenticated device API.
A paired or signed-in device has the same host authority as the desktop.
A code with no `--scope` grants every scope to a browser pairing on this
computer (`127.0.0.1` or `localhost`), and read plus operate to a device
pairing from anywhere else. A code with `--scope` grants exactly those
scopes, wherever it is used.

A paired device acts as you, the room owner, with the full power of the
desktop app: it posts, starts and stops sessions, and reads every room. A
device stays paired while you use it: its 30-day expiry slides forward
again each day of use, and the browser cookie follows it. Treat a paired
phone like an unlocked
laptop. Revoke a lost device at once.

`dyna engine run`, `--pair`, `--devices` and `--revoke` refuse to run when a Dyna
agent session started them (`DYNA_SESSION`, `DYNA_RUN_ID`, `DYNA_AGENT_ID`,
`DYNA_WORKER_RUN` or `DYNA_ADDRESS` is set), unless you pass
`--allow-agent`. This stops an agent from pairing itself by accident. It is
not a security boundary: any process that runs as your OS user can read the
Dyna store anyway.

## Reach it from a phone

The simplest way is [Resident Labs Connect](CONNECT.md): `dyna engine
connect link` gives this computer a managed tunnel and passkey sign-in, with
no router or DNS setup. The ways below need no account.

The app and the cards need two origins (see [Cards](#cards)). Each route
names both, and `dyna engine run` needs a `--route` for every name a browser uses.
A request for any other host name gets `421` with that name in the message.

### Tailscale

Your tailnet only, HTTPS, no ports open to the internet.

```sh
tailscale serve --bg --https=443  http://127.0.0.1:7457
tailscale serve --bg --https=8443 http://127.0.0.1:7458
dyna engine run --route https://<machine>.<tailnet>.ts.net,https://<machine>.<tailnet>.ts.net:8443
```

Find `<machine>.<tailnet>.ts.net` with `tailscale status --json` (the
`Self.DNSName` field, without the final dot). Open
`https://<machine>.<tailnet>.ts.net` on the phone.

Do not use `tailscale funnel` for Dyna. The server refuses every request
that Funnel marks (`Tailscale-Funnel-Request`), so turning Funnel on by
mistake shows nothing. The server also ignores `Tailscale-User-*` identity
headers: any local process can send them to the loopback port.

### Cloudflare named tunnel

For a phone without Tailscale. The tunnel dials out to Cloudflare, so no
port is open on your router, and you get fixed HTTPS names on your domain.
You need a Cloudflare account with your domain on it.

```sh
desktop/web-tunnel.sh dyna.example.com dyna-cards.example.com   # once
cloudflared tunnel --config ~/.config/dyna/cloudflared/config.yml run
dyna engine run --route https://dyna.example.com,https://dyna-cards.example.com
```

`web-tunnel.sh` logs in with its own certificate in
`~/.config/dyna/cloudflared`, makes a new tunnel (default name `dyna-web`),
and adds the two DNS records. It never touches `~/.cloudflared` or another
tunnel. It stops if the tunnel name exists, and cloudflared stops if a DNS
record for either name exists. If it stops after it made the tunnel (for
example, a DNS record already exists), delete that tunnel in the Cloudflare
dashboard, fix the cause, and run the script again.

These names are public: certificate transparency logs list every HTTPS
name. Anyone can load the pairing page. Pairing and the device cookie carry
all the weight.

Do not set `originRequest.httpHostHeader` in the tunnel config. Dyna checks
the host name the browser used. A rewritten host gets `421`.

Optional second layer: put [Cloudflare Access](https://developers.cloudflare.com/cloudflare-one/policies/access/)
in front of both names. Dyna does not read Access tokens; Access only
filters who reaches the pairing page. When the Access session expires, API
and event requests get a login page instead of JSON, so the app looks broken
until you reload the page and sign in again.

A quick tunnel (`cloudflared tunnel --url http://127.0.0.1:7457`) works for
a short test with random `trycloudflare.com` names: start one for each port
and pass both names with `--route`. Quick tunnels do not pass Server-Sent
Events, so the app falls back to long polls (see [Live events](#live-events)).

## Install on the phone

The web app has a manifest and icons, so a browser can install it as an app
with its own icon and no browser bar. On Android, open the app in Chrome and
choose **Add to Home screen** or **Install app**. On iPhone, use Safari's
**Share**, then **Add to Home Screen**. An iPhone home-screen app keeps its
own storage, so you pair it once more.

An older release also served a small Android call app from `<data dir>/android`
(`dyna-call.apk` and `version.json`). Dyna no longer serves that app. The engine
ignores those files as legacy state; delete the folder to get the space back.

A small service worker (`public/sw.js`) shows push notifications and opens
the right room on a tap. It has no fetch handler and caches nothing, so the
app still needs the server to load. The bell in the sidebar turns
notifications on for this device; see [CONNECT.md](CONNECT.md#push).

## On a phone screen

Under 760 px wide the layout has one column. The feed takes the full width.
The menu button at the top left opens a drawer with the room rail and the
room sidebar. The members button and the pin button open their panels as a
sheet from the right. A tap on a post shows its actions (reply, pin). Each
session row has a visible actions button; a long press opens the same menu
on Android. The composer's plus button opens the phone's file picker, which
includes the camera and the photo library. The desktop window never gets
this narrow (its minimum width is 960 px), so it does not change.

A search button at the right of the header opens the command palette. It
lists rooms and commands. New room lets you type a folder path on this
computer, pick folders from a list, and make a room for the repo there; see
[README.md](README.md#command-palette-and-new-room). The four New room calls
(`BrowseInfo`, `BrowseDirs`, `InspectPath`, `CreateRoom`) are on the engine call table
(`internal/engine/calls.go`), so web and Connect clients can use them. `CreateRoom` honors read-only mode.

The web app has the same Settings as the desktop: the gear next to "you"
opens it. A paired device can already start agents that run shell commands
on this computer, so hiding Settings from it would protect nothing. One
rule stays: a profile's stored env values never leave Go. The editor shows
env key names only, and a value can be replaced but not read back, so a
secret never appears on a screen. A sign-out button next to the gear
forgets this device. Authenticated clients can also list and revoke paired
devices or Connect sessions and create a pairing code and URL through
`Devices`, `RevokeDevice`, and `CreatePairing`. These reuse the CLI stores;
see [README.md](README.md#workflow-device-queue-and-room-apis) for arguments.

## How it works

`dyna engine run` (`internal/engine/run.go`) builds the call table from
`internal/engine/calls.go` and mounts `internal/websvc`. The desktop window
(`desktop/main.go`) is a thin Wails shell that loads this server's web app
over HTTP. websvc has no Wails or cgo code.

### API

Each call is `POST /api/<Method>` with the method's arguments as a JSON
array, for example `POST /api/Feed` with `["room_…"]`. The reply is the
result as JSON, or `{"error": "<message>"}` with a 4xx status. Each route
calls the service method named in internal/engine/calls.go. Every binding
declares read or write access. Missing access fails at startup, and one
call-table wrapper refuses writes in read-only mode.

`web/src/api.ts` keeps one set of names over both transports.
The server puts `<meta name="dyna-web">` in the page with the card origin
and the features that exist; `api.features` reads it, so no component checks
the transport.

Files and changes use the same `ListEntries`, `ReadFile`, `GitStatus`, and
`GitDiff` calls as desktop. They are available to paired devices and Connect
sessions. Workspace image URLs use the authenticated `/attachments/` route;
card origins do not serve workspace files. Absolute paths and symlinks have
the owner's shell-access trust, as on desktop. See
[Repository files and Git previews](README.md#repository-files-and-git-previews)
for limits and result fields.

Web-only routes: `POST /api/pair`, `POST /api/logout`, `POST /api/CardURL`,
`GET /api/events` and `GET /api/events/poll`. Connect adds `/api/connect/*`,
`/api/push/*` and `/connect/v1/*` (see [CONNECT.md](CONNECT.md)).

Static WebAssembly (`.wasm`), JavaScript, CSS and SVG assets use gzip when
accepted by the browser and smaller than the original. The server computes
each compressed asset once and caches it until restart. Responses preserve
the original content type, including `application/wasm`, and set
`Vary: Accept-Encoding`. Fonts and raster images keep their original bytes.

### Auth and request checks

- Every route needs the device cookie or a Connect session (see
  [CONNECT.md](CONNECT.md#sessions-and-the-cookie)), except the page itself,
  its scripts, styles, manifest, icons and service worker, `POST /api/pair`,
  and the Connect sign-in routes. A request without a valid
  cookie gets `401`, and the page shows the pairing screen.
- The cookie (`dyna_device`) is a random 256-bit token: `HttpOnly`,
  `SameSite=Strict`, and `Secure` on an HTTPS route. The server stores only
  its SHA-256 hash.
- Every `POST` must come from the route's own origin (`Origin` must match; a
  missing `Origin` fails), carry `X-Dyna: 1`, and have a JSON body. The
  custom header forces a CORS preflight, and the server answers no CORS. The
  pairing request follows the same rules.
- The page has a strict content security policy: scripts from the app only,
  frames only from the card origin, and no framing of the app.
- The server keeps its key in `<data dir>/web/key` (mode 0700 directory; it
  signs card tickets and keys pairing codes). Paired devices (token hashes)
  and the live pairing code (keyed hash, expiry, tries and scopes) are in
  `dyna.db`. Commands and the server change them in SQLite writer
  transactions, so a revoke takes effect on the next request. The engine
  imports an older `devices.json` and `pair.json` once at start and then
  ignores them.

### Cards

HTML cards come from a second listener, the card origin. It serves only
`/rich/<room>/<id>`, so a card frame is cross-origin to the app before its
sandbox even applies. Cards keep the desktop rules: `sandbox="allow-scripts"`
and the same content security policy. The desktop IPC gate is not needed in
a browser, so card scripts run.

A card on a Cloudflare route lives on a different host name, which never
gets the app cookie. So the card origin uses no cookie. Instead the app asks
`POST /api/CardURL` for a URL with a ticket: an HMAC of the room, the card
and an expiry, valid for that one card for 10 minutes. The ticket sits in a
URL, so it can reach browser history, screenshots and Cloudflare's edge
logs. It grants a read of that one card, and only until it expires. The
server never logs query strings.

The card's `frame-ancestors` lists the app origins from `--route` and the
loopback names. It never comes from a request. If an app origin is missing,
the card stays blank. An app on a non-default HTTPS port must name that port
in its `--route` origin.

### Live events

`GET /api/events` is a Server-Sent Events stream of the same events the
desktop gets: `posts`, `rooms`, `members`, `runs` and `pins`. Every stream
gets every room's events. That is right for one owner and wrong for any
future multi-user mode. A heartbeat goes out every 15 seconds, and the server
ends the stream at the next heartbeat after a revoke. A client that falls
64 events behind is dropped; the browser reconnects and reloads what it
shows.

Some proxies hold a stream's body until it ends. Cloudflare quick tunnels
do. Every stream starts with a `hello` event. If the page gets none within 6
seconds, it closes the stream and long-polls `GET /api/events/poll?after=<n>`
instead. The server keeps the last 256 events for polls and holds a poll up
to 25 seconds for the next one. A poll that falls further behind gets
`reset`, and the page reloads.

## Tests

```sh
go test ./internal/websvc/ ./internal/roomsvc/
cd desktop && CGO_ENABLED=0 go test . && CGO_ENABLED=0 go vet .
```

The desktop tests need no WebKit.

## Not yet

- A "Share to phone" view in the desktop app with a QR code.
- A Tailscale login allowlist, and checking Cloudflare Access tokens.
- A direct non-loopback bind with TLS.
- More than one user on one machine. Connect has many accounts, but each
  machine has one owner; see [CONNECT.md](CONNECT.md#not-in-this-build).

### Remote terminals

Paired devices and Connect sessions have full host shell access. The API
allows `TermOpen`, `Terms`, `TermAttach`, `TermWrite`, `TermResize`,
`TermClose`, `ShellJobs`, `ShellLog` and `ShellLogRange`, with the same arguments and results
as the desktop app. See [the terminal API](README.md#host-terminals-and-background-jobs).
Read-only mode refuses terminal open, input, resize and close calls.

`POST /api/terminal/attach` streams output as Server-Sent Events. Its JSON
body is `{id,after}`, where `after` is the last raw byte offset consumed.
It uses the same device cookie or Connect session, allowed host, matching
Origin, `X-Dyna: 1` and JSON checks as other POST routes. Connect must also
allow `TermAttach`. The stream sends a `hello` event, output messages shaped
like `TermAttach`, and a heartbeat every 15 seconds. Auth is checked again
at each heartbeat, as on the room event stream. Output is base64, so binary
and partial UTF-8 sequences survive transport. Input and resize use ordinary
POST API calls. SSE avoids a separate WebSocket auth handshake.

The shared `api.termAttach` helper resumes interrupted streams by byte
offset. A proxy that holds the stream for six seconds triggers a fallback
to `TermAttach` snapshot calls every 250 ms. Slow readers use only the
server's bounded scrollback; overwritten bytes produce `reset: true`.
Detaching never closes a shell. Restarting the server does close it.
`dyna engine run --terminal-idle 2h` changes the idle lifetime; negative disables it.
The default is one hour without input or output, even with a view attached.

## Session promotion

The member menu offers **Promote to lead** for an open subagent. Promotion
keeps active work, children and history intact. Closing a session closes its
children that own no work and moves the rest to its nearest open ancestor.
PR authorship moves to that ancestor, or the room's root. Held goals move
to the nearest open worker. If only root or the owner could inherit them,
close is refused until each goal is done, dropped, or transferred. Force
close releases those goals for the owner to resolve.
Reviewer roles return to the pool, and live workflow runs are canceled.
The returned member has `closeSummary` lines that the app keeps visible until dismissed.
Force close is offered only after a handoff fails. `PromoteSession`, `CloseSession` and
`ForceCloseSession` are on the engine call table (`internal/engine/calls.go`), so web and Connect clients can use them. They use the same write gates as other session controls.

## Transfer to a new model

The member menu offers the same single-click model picker and progress
view as the desktop. `TransferSession`, `SessionTransfer` and
`CancelSessionTransfer` are on the engine call table (`internal/engine/calls.go`), so web and Connect clients can use them.
Transfer and cancel use the usual read-only and CLI health gates. Status
remains readable. All three surfaces keep the Dyna session ID, children,
old history and tracked PR ownership. See [README.md](README.md#transfer-to-a-new-model).


## PR review publishing

Agent reviews stay in Dyna, even when the room retains a GitHub publishing
setting. Only the owner can publish GitHub comments or reviews and change
the publishing setting. Local Markdown reviews appear in both tracked PR
views. Existing public reviews stay public.
See [Review publishing](README.md#review-publishing).


## Child failure alerts

Successful session reports and normal manual stops reach only the owner.
A failed child alerts its current available stored parent and the owner once,
including usage/credit/quota limits, provider errors, schema failures and crash
repairs. The alert links the exact failed turn and gives a transfer command.
The parent can choose a working model; history and children stay together.
Top-level failures or
unavailable parents reach only the owner. Desktop, paired web and Connect
show the same stored alert and full owner result.

## Card and file sizes

Paired web and Connect use the host's **Settings → Cards and files** controls
and the same `content.maxCardBytes` / `content.maxFileBytes` settings as desktop
and CLI. Each defaults to 26214400 bytes (25 MiB). The host derives the post
total from eight files, or one card and seven files, and derives the request
limit with base64 padding and JSON overhead. The former fixed 28 MiB request
cap no longer applies. New operations read effective settings without a host
restart; stored content stays readable if a limit decreases. See
[Card and file size settings](README.md#card-and-file-size-settings) for commands,
units, the total formula, and the in-flight upload boundary.

## Session consolidation API

Paired browsers and signed-in Connect devices have the same consolidation
capability as desktop. `ConsolidateSessions(roomID, options)` accepts
`{sessions: [id, ...], target: selectedID, profile?: name, model?: name}`.
Select 2 to 16 distinct open sessions from one room. Omitted profile/model
retain the kept session's choice. Active transfers, reservations and consumed
sources are rejected before new work starts.

`SessionConsolidations(roomID)` returns saved operations, newest first,
including earlier operations. `ResumeSessionConsolidation(roomID, id)` and
`CancelSessionConsolidation(roomID, id)` act on that exact operation. All
mutations return its state. Writes respect read-only mode. Existing member
events and bounded status polling supply progress; there is no new event type.

State includes `id`, `roomId`, `target`, `sessions`, `profile`, `model`,
`phase`, `requested`, `deadline`, optional `ended`/`error`/`cancelPending`,
and `sources` with `session`, `address`, `phase`, `source`, `dir` and
preserved history references. Poll responses omit large internal handoff packets;
`dyna subagent consolidate status <id> --room <room> --json` reads the full context. Phases are `waiting`, `stopping`,
`starting`, `committing`, `completed`, `failed`, `cancelled`. Healthy sources
share one maximum 30-second handoff window. Failed sources use
transcript fallback. Only token-bound successful replacement startup permits
source archival. Failed startup is recoverable with the old native session.
Retry never changes the selected model or creates a second operation.

Cancellation works before startup and after startup failure. Repeat cancel
or resume when `cancelPending` is true. A post-startup commit must resume to
completion. The survivor keeps task charters and source histories across
reloads and future transfers. Original IDs still open original transcripts.
Old addresses forward delivery. Worktrees, files and branches stay intact.
Pending inbox origins retain their receipts. Shared posts deliver once.
PR roles move without bypassing independent review or erasing old verdicts.

## Bulk session actions

Member selection uses highlighted rows on desktop, paired web and Connect.
Choose shared options once for Stop, Transfer model, Promote, Move under…, or
Close. Move under… picks one open parent; targets that would make a cycle,
sit under a service, or pass `subagent.maxDepth` are disabled with the reason. Message addresses the normal composer to the selected sessions. A close
confirmation applies to the selection. Each close hands work up the parent chain.
Goals must be resolved before a close would hand them to root or the owner.
A refused close needs resolution, a retry, or explicit force close. Each session reports its own result,
so a refusal does not cancel successful actions on other sessions.

`BulkSessionAction(roomID, options)` uses the individual session services.
Options contain `sessions` (IDs), `action` (`stop`, `transfer`, `promote`,
`adopt`, or `close`), and shared values: `profile`/`model` for transfer,
`parent` for adopt, and an explicit `force` boolean for close. Transfers request a 30-second
handoff and keep their existing durable recovery path.

The response contains `action`, ordered `results`, `succeeded`, and `failed`.
Each result has `session`, `ok`, an optional `error`, and an optional `member`
or `transfer` value. Transfer acceptance means the saved transfer has
started; its `phase` still determines progress and completion. Refresh member
and PR views after a batch because hierarchy changes can affect other rows.

Each request accepts up to 512 distinct sessions and runs at most eight
actions concurrently. Larger selections use multiple requests. Each request uses
a 60-second cancellation budget. Lock waits cancel before mutation; a short
hierarchy write already holding all its locks finishes consistently. Items
that have not begun committing report cancellation and can be retried. Invalid shared options or duplicate resolved sessions reject the
request before any action. Stale, missing, wrong-room, or guarded sessions
report individual errors. Read-only mode rejects all bulk mutations.

## Pause and resume room sessions

The Sessions header menu provides **Pause all**, **Resume all**, and
**Clear dormant sessions…** on desktop, paired web, and Connect.
Pause applies to agent sessions in the current room. Current provider calls
finish naturally. New session calls and post delivery wait, including posts
from workflows and shell jobs. Workflows and shell jobs themselves continue.
The room hold persists across reconnects and host restarts. It does not change
session activity timestamps.

`RoomSessionControls(roomID)` returns `{roomId, paused, pausedAt?, resumedAt?,
running, held, dormant}`. All three counts are present, including zero.
`running` and `held` count sessions, not workflow workers. `dormant` counts
open sessions that finished.

`PauseRoomSessions(roomID)`, `ResumeRoomSessions(roomID)`, and
`ClearDormantSessions(roomID)` return `{state, results, succeeded, failed}`.
Each address-ordered result contains `session` (the stable session ID), `ok`,
`status`, and an optional `error`. Status is `paused`, `running`, `held`,
`idle`, `resumed`, `closed`, `kept`, or `error`. A refused session does not
cancel accepted changes for other sessions. Mutations respect read-only mode.
Room events and bounded status reloads update other connected clients.

Resume keeps the exact native conversation, drains held posts through existing
cursor and delivery evidence. Repeating resume
can recover an incomplete release. Closing the member list or disconnecting a
browser does not undo an accepted host operation.

Clear asks for one confirmation. The host rechecks the strict age threshold
of more than 30 minutes and eligibility at the close boundary. It keeps
sessions with live or pending work, held messages, active transfers or
consolidations, open children, live owned runs or shells, tracked PRs, and
sessions needed by goals or open questions. Results explain why each refused
candidate stayed open. Clear uses ordinary close semantics without force;
archived transcripts, files, branches, and worktrees remain intact.

## Agent computers

A session's headless desktop (its **computer**) shows in the app as a tab, a
grid and a live viewer. The web server adds two routes beside
`/api/terminal/attach`; everything else is on the normal Calls table.

| Route or call | What it does |
|---|---|
| `GET /api/computers/<session>/shot.png` | The computer's last picture. `?fresh=1` asks for a new one, at most once every 5 seconds per computer. |
| `GET /api/computers/<session>/view?k=<key>` | WebSocket. Opens a viewer lease and relays binary RFB to wayvnc. The Origin must be the app origin. The device or Connect session is checked again every 20 seconds. |
| `Computers(room)`, `Computer(session)`, `ComputerControl(session, key, mode)` | List, status and control. `ComputerControl` respects read-only mode. |
| `computer` events on `/api/events` | State changes and the agent's actions. |

A Connect session needs the `computers` scope for the two routes. The
page's CSP names the app's `ws:` or `wss:` origin in `connect-src` when
computers are on. Behind a proxy, the proxy must pass WebSocket upgrades:
`tailscale serve` does, and a Cloudflare named tunnel does.
See `docs/computers-ui.md`.
