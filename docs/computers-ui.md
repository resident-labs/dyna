# Agent computers: the screens and the web API

This page covers what the owner sees and how the app talks to the
computers daemon. For what a computer is, the agent CLI and the action
schema, read `docs/computers.md`.

## What the owner sees

- **Floating preview (Messages).** In a session's Messages view, a glass card
  floats at a corner of the message area while that session's computer is
  `running` or `starting`, or `stopped` with a last picture. The picture comes
  first. A caption sits on a dark fade: a gold dot, the app name,
  the window title and the picture age. Hover shows two small buttons:
  **minimize** and **hide**. Minimized, it is a pill ("lets-work is in
  Chromium") that lifts the picture on hover. Hide lasts until the computer
  changes state. Drag the card to move it; it snaps to the nearest corner and
  the corner is remembered per session (local storage, key
  `dyna.computer.preview.<session>`). A click opens the Computer tab. It shows
  the latest screenshot only and **never opens a stream**. It hides on the
  Computer tab, on the computers page, and below 700 px wide. On a phone a
  "Computer · live" chip in the session header opens the tab instead.
  The component is `ComputerPreview.svelte`, separate from the rest, so
  another variant can replace it (A, the glass card, is built; C is its
  minimized form).
- **Computer tab.** Opening it **connects at once**; there is no Watch live
  step. While it connects, the last picture shows full size with a small
  "Connecting…" badge. When the first frame arrives, the stream cross-fades
  in (0.4 s) and the badge says "Live" and fades. Leaving the tab, switching
  session, or hiding the page closes the stream and the viewer lease.
  Coming back reconnects. A computer with no window yet shows "No window yet.
  It starts when this agent opens an app." and opens no stream. A failed
  connect keeps the picture and shows "Could not connect · Retry".
- **Viewer top line.** A small-caps state word with a gold dot ("LIVE", "YOU
  ARE DRIVING"), one outlined button (**Take control** / gold **Return to agent**)
  and a round **⋯** menu. The menu holds Sharp / Smooth, Post screenshot to
  room, Open in its own window (Use the whole app window on the desktop) and
  the stream stats line (codec, encoder, fps, bitrate).
- **Control.** Taking control puts a 1.5 px gold hairline and a soft gold
  glow around the frame. **Nothing is drawn over the stream while the owner
  drives.** The footer hint reads "Press Esc twice to return control." Esc
  pressed twice within 600 ms returns control. Closing the viewer, leaving
  the tab or losing the connection also gives control back.
- **Floating chat.** A glass pill on the bottom edge of the frame shows the
  agent's last action or reply in one line and an unread dot.
  It has three shapes. **Tucked** is the pill. **Composer**: click the pill
  or press `/`; it widens into an input with the last line above it, and Enter
  sends. **Chat**: click the last line or press `Ctrl+/`; the recent thread
  grows upward. Esc, the ⌄ button or a click on the stream tucks it. The glass
  gets more opaque as it grows (0.86, 0.93, 0.95). While the owner drives, the
  tucked pill sinks into the gap below the frame. Keys go to the computer, so
  only a click or `Ctrl+/` reaches the chat, and Esc works only while the chat
  has focus. Taking control tucks an open chat. Sending does not return
  control. The chat reads the session data and sends through the same path as
  the Messages tab, so a message shows in both. It reuses `MessageInput` and
  `DmMessage` (the `compact` variant). The pop-out window has no chat.
  Rules: `computers/chatState.ts`. Parts: `computers/ComputerChat.svelte`.
- **Shared tokens.** `lib/ui/tokens.css` holds the `.state-label` and
  `.act-line` styles, hairlines
  (`--hair`), two radii (`--r-sm`, `--r-lg`) and three glass levels
  (`--glass-pill` 0.86, `--glass-composer` 0.93, `--glass-panel` 0.95). Use them
  instead of local copies. `MessageInput` has one skin prop: `docked`
  (Messages and rooms, the default) and `glass` (the floating chat). The
  expanded tool rows use the `.act-line` style. Dyna uses no serif font, and
  the session header and viewer top line show no "lets-work is working"
  narration (owner, 2026-10-17: it made the top bar busy).
- **Footer.** A quiet hint on the right (and the last agent action on the
  left, in the pop-out, which has no chat).
- **Right bar.** In a channel or room view, the member list shows each
  session's computer under its name: a 180 px, 16:10 glass card (gold dot,
  app name, picture age) for a running or starting computer. It
  refreshes the picture every 5 s and never streams. A stopped computer
  shows the faint line "computer stopped". A click opens that session's
  Computer tab. The floating preview stays only in a session's own Messages
  view, which has no right bar. Parts: `computers/ComputerThumb.svelte`,
  `thumbKind` in `preview.ts`.
- **All computers.** The **computers** item in the sidebar opens a grid of
  `shot.png` tiles for the room (columns of 280 px or more). Each tile shows
  the whole 16:10 picture and a caption row: state dot, session name, app and
  age. Tiles never stream. A stopped computer is dimmed. A session without a
  computer shows "No desktop yet". A total tile shows live computers, open
  streams and RAM. A click opens that session's Computer tab.
- **Sidebar.** A small monitor glyph marks sessions whose computer runs.
- **States.** `none` (no computer), `idle`, `starting`, `running`, `stopped`.

## How the app reaches the daemon

```
Svelte UI ──▶ API calls (Computers, Computer, ComputerControl)
          ──▶ GET /api/computers/<session>/shot.png
          ──▶ WebSocket  /api/computers/<session>/view
                   │
        internal/computerweb.Host ──▶ internal/computers.Client ──▶ daemon
```

| Part | Where | Notes |
|---|---|---|
| List, status, control | API calls `Computers(room)`, `Computer(session)`, `ComputerControl(session, key, mode)` | On the engine call table (`internal/engine/calls.go`), so paired and Connect clients can use them. `ComputerControl` is a write: read-only mode refuses it. |
| Picture | `GET /api/computers/<session>/shot.png` | `?fresh=1` asks the daemon for a new picture. The server allows one real capture per computer every 5 seconds. Other calls get the file on disk. |
| Stream | `GET /api/computers/<session>/view?k=<key>` (WebSocket) | Same Origin check as other writes, then the same device or Connect auth. The server opens a viewer lease, dials wayvnc's Unix socket and relays binary RFB. It checks the device again every 20 seconds. |
| Events | `computer` events on `/api/events` | `{kind:"computer", session, roomId, type, computer, action}`. The daemon's `subscribe` stream feeds them. |

Both routes need the `computers` scope. A default Connect grant does not include it.

### The viewer key

The page makes a random key `k` for each viewer. The WebSocket carries it.
`ComputerControl` carries it too. `computerweb.Host` maps the key to the
daemon's viewer lease, so the page never sees the daemon's viewer ID and
control always belongs to a viewer that is really open. A control call with
an unknown key fails.

### Desktop window

The desktop window loads the web app from the engine over HTTP. It uses the
same picture and stream routes as the web, with the same device auth.

## The stream client

noVNC (`@novnc/novnc`, pinned to 1.7.0) runs in `computerStream.ts`. It
loads on first use, in its own chunk.

- The viewer starts **view only**. No key or pointer event leaves the page
  until the owner takes control.
- **H.264.** noVNC asks for H.264 only when WebCodecs decodes a real test
  frame. If the engine cannot decode H.264, noVNC does not ask for it, and
  wayvnc sends Tight or ZRLE. No error shows. If decoding fails later in the
  stream, the viewer turns H.264 off for the page and reconnects once.
  Before it loads noVNC, the page hides `VideoDecoder` from noVNC when a
  safe check fails, because noVNC runs its own check at load.
- **Sharp and Smooth.** The toolbar has a **Sharp | Smooth** choice. Sharp
  (the default, saved in this browser) never asks for H.264 and uses Tight
  at quality 9. Smooth asks for H.264, which moves better and is a little
  softer. wayvnc and neatvnc have no setting for H.264 quality or bitrate,
  so this choice is the control. A change reconnects, so it is off while the
  owner has control.
- **Sharp text in WebKitGTK.** WebKitGTK scales a canvas with CSS by
  dropping pixel rows, so small text looked garbled ("Sct Cct") when the
  picture shrank. This was the scaling, not the codec. When the picture
  shrinks, the viewer paints a second canvas with `drawImage` and high-quality
  smoothing, and keeps noVNC's canvas on top but invisible for pointer
  events. Pinch zoom paints the visible part from the same source.
- **Stats.** The codec on the stats line is what this page decoded in the
  last two seconds. Frames per second and bitrate come from the page.
  Latency is the time from the owner's last key or pointer event to the next
  reply, and shows "—" when there was no input in 10 seconds. noVNC has no
  stats API, so `computerStream.ts` reads three private fields of the pinned
  version. A noVNC update that moves them loses the stats, not the stream.
- **Phone.** The toolbar wraps and loses its text labels. Pinch zooms the
  picture inside the frame, and one finger pans while zoomed. **Fit** resets.
  noVNC handles touch itself (it stops the events), so the page listens in
  the capture phase. In control mode noVNC owns touch and the zoom resets.
- **Pop out.** In a browser, "Open in its own window" opens `#pop=<room>:<session>` in a
  small window with only the viewer, and closes the viewer in the app (one
  viewer at a time). The desktop window cannot open a second window, so the
  button reads Full window: the viewer fills the app window.

## Files

| File | Job |
|---|---|
| `internal/websvc/computers.go` | shot.png and view routes, the stream relay |
| `internal/computerweb/host.go` | view model, viewer leases, events, action summaries |
| `internal/engine/computers.go` | the computer host the engine mounts |
| `web/src/lib/computers/computers.ts` | pure helpers (states, ages, totals, events) |
| `web/src/lib/computers/rfbStats.ts` | the stats meter |
| `web/src/lib/computers/computerStream.ts` | noVNC, fallback, instrumentation |
| `web/src/lib/Computer*.svelte`, `Computers.svelte` | the screens |
| `web/src/lib/computers/chatState.ts`, `ComputerChat.svelte` | the floating chat |

## Test it

`npm test` in `web` and `go test ./internal/websvc/ ./internal/computerweb/`.
The read-only branch QA tab keeps the computer's view socket closed, so a
branch that changes the viewer cannot show the stream there. That socket
relays desktop input, and QA is not read-only with it open. Check the viewer
with `dyna browser` against the app the engine serves, at 1280 px and 390 px.
For the real thing, start `labwc` with `WLR_BACKENDS=headless`, run an app
and `wayvnc -g unix:///path/rfb.sock` inside it, and point a daemon at the
socket. H.264 needs `WLR_RENDERER=gles2` on a render node. With the pixman
renderer, wayvnc has no GPU buffers and sends Tight.
