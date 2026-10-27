# Agent computers

Each Linux agent session has a computer: a small headless desktop. GUI apps that
the agent starts use that desktop. Registering a computer starts no compositor.
The first Wayland or X11 client starts labwc. Status reports `starting` until
labwc is ready. Other computers stay responsive during startup. labwc starts Xwayland only when an
X11 client connects. The owner can watch and control the desktop in Dyna.

A single `dyna computers` supervisor holds every session's public sockets.
The shared installer writes `dyna-computers.service` and `dyna-engine.service`.
The engine unit requests the computer service through `Wants=` and `After=`.
The supervisor keeps running when the engine restarts. Computer commands reuse
it, or start the installed service when its endpoint is absent. Linux installs
without a user service manager keep the existing detached-process fallback.
The supervisor preserves the owner's D-Bus and keyring.

Each running computer also gets its own D-Bus session bus, a `bus` socket in its
runtime directory. An app started with `dyna computer open` or `dyna computer run`
uses it, so a desktop portal request, such as a file chooser, starts a portal on
that bus, with the computer's private display. Without it, the request reached the
owner's portal and the chooser opened on the owner's display. If `dbus-daemon` is
missing, the computer works as before. Shells keep the missing-socket address
described below.

A new build installs without stopping busy computers: the service restarts later.
For the first 10 minutes after that restart becomes pending, the service refuses
to start new desktops; after that it starts them again, so a few busy computers
never hold the rest back. The service restarts at the first moment no computer has
an app open; a running desktop with no app, however recent its input, does not
hold it back. `dyna computer list` shows "restart pending since <time>" while one
waits.

The 10-minute limit runs inside the computer service, so a service built before it
keeps refusing for 30 minutes until it restarts onto this build. That happens once,
on the upgrade that brings this change: the old service cannot be told the new
limit, and a marker backdated to shorten its window would be ignored by a service
that started recently, which would refuse nothing at all.

During the first upgrade, an active transient `dyna-computers.service` keeps
its sockets and desktops. The installer writes and enables the persistent unit
without restarting the transient service. The new definition takes effect after
the transient unit stops and is collected, or after reboot.
Later hook installs restart the persistent computer service only when its code
or listed runtime helpers changed. Such a restart ends its desktops and apps.
Unrelated engine updates preserve them.

## Requirements and settings

Install `labwc`, `Xwayland`, `wayvnc`, `wlr-randr`, `grim`, and `wtype`. Install
`ffmpeg` and a working VA-API driver for hardware stream encoding. The runtime
also needs `setpriv` from util-linux. `foot` and
`xeyes` are useful smoke test clients. Computers need Linux and `XDG_RUNTIME_DIR`.

`computers.enabled` defaults to true. The feature takes effect only when Linux,
labwc, and the runtime directory are available. Use
`dyna settings set computers.enabled false` to disable injection on future turns.
`DYNA_COMPUTERS_ENABLED=0` also disables injection. `dyna computer status` reports
why a computer is unavailable. Go tests disable automatic registration by default.
Neither registration nor the daemon starter can start a daemon from a Go test binary. The daemon
sets `DYNA_COMPUTERS_DAEMON=1` and cannot recursively start another daemon.

Dyna injects `WAYLAND_DISPLAY`, `DISPLAY`, `XDG_SESSION_TYPE=wayland`,
`XDG_CURRENT_DESKTOP=labwc`, and a browser shim directory at the front of `PATH`.
It preserves `XDG_RUNTIME_DIR`, `DBUS_SESSION_BUS_ADDRESS`, and
`HYPRLAND_INSTANCE_SIGNATURE`. It saves the owner's original displays in
`DYNA_OWNER_WAYLAND_DISPLAY` and `DYNA_OWNER_DISPLAY`, for tools that deliberately
operate the owner's desktop.

### Managed shells

A Dyna shell runs on the display its owner may use, never on the owner's display
by accident. Starting a shell never starts a computer.

- An interactive owner terminal, the internal root session, and a session that
  root or the owner granted owner display keep the owner display. After computer
  injection these shells get it back from `DYNA_OWNER_DISPLAY` and
  `DYNA_OWNER_WAYLAND_DISPLAY`; a saved value that is empty means the owner had
  no display. They also drop the session computer's browser shims from `PATH`
  and drop `AGENT_BROWSER_*`, `DYNA_CHROMIUM_*`, and `DYNA_COMPUTER_PROCESS`.
- A shell whose session computer already runs gets the display that
  `dyna computer env` prints, and starts nothing. It differs from that
  environment on purpose: `DBUS_SESSION_BUS_ADDRESS` names a missing socket, so
  a D-Bus singleton cannot activate an app on the owner desktop;
  `WAYLAND_SOCKET`, `HYPRLAND_INSTANCE_SIGNATURE`, `DYNA_OWNER_DISPLAY`, and
  `DYNA_OWNER_WAYLAND_DISPLAY` are removed, because a connected socket wins over
  `WAYLAND_DISPLAY` and the `hypruse` shim would put a window back on the owner
  display; and `CHROME_USER_DATA_DIR` and `CHROME_CONFIG_HOME` name the
  computer's own private profile, so an absolute Chromium launch joins that
  browser instead of one of the shared account config.
- A shell with no running computer has no display. `DISPLAY`, `WAYLAND_SOCKET`,
  `XDG_SESSION_TYPE`, and `DYNA_OWNER_*` are unset, and `WAYLAND_DISPLAY` names a
  missing absolute socket, so a GUI program fails at once with its normal
  cannot-open-display error. `DBUS_SESSION_BUS_ADDRESS` names a missing socket,
  so neither dbus autolaunch nor `/run/user/<uid>/bus` finds the owner's bus.
  `XDG_RUNTIME_DIR` stays, so `dyna computer open` and `dyna computer run` still
  reach the control socket. `CHROME_USER_DATA_DIR` and `CHROME_CONFIG_HOME` name
  private directories: Chromium finds a running browser through its profile
  before it needs a display, so an absolute launch from a desktop entry must not
  join one that runs on the owner display. The job's stderr gets one hint that
  names these commands, printed before the command runs, so its own PID and exit
  status are kept.

The browser shims keep each computer on its own profile: Chromium gets
`--user-data-dir`, Firefox gets `--profile` without a shared remote, and the
blocked bus does the rest. Firefox removed `--no-remote` and `MOZ_NO_REMOTE` in
2024, so a separate profile and no session bus are what keep it apart.

## Browser settings

Open desktop Settings and find Browser. Changes apply at the next Chromium start.
The same values are available with `dyna settings set <key> <value>`:

| Key | Default | Effect |
| --- | --- | --- |
| `browser.path` | Empty | Find Chromium automatically, or use this executable. |
| `browser.extraFlags` | Empty | Add Chromium arguments. Quote values that contain spaces. |
| `browser.useFlagsFile` | `true` | Read the owner's `chromium-flags.conf`. |
| `browser.profileMode` | `shared` | Select `shared`, `separate`, or `copy-mine`. |
| `browser.ownerProfile` | `Default` | Choose the owner's Chromium profile directory for `copy-mine`. |
| `browser.saveBack` | `true` | Save login state back to the shared Dyna profile when a computer stops. |

Shared mode lets computers start from one Dyna profile. Separate mode keeps each
computer's profile separate. Copy my profile starts from a copy of an owner's
Chromium profile. Profile names come from Chromium's Local State file under
`$XDG_CONFIG_HOME/chromium`, or `~/.config/chromium`.

Each computer uses its own working copy. Dyna never launches Chromium on the
owner's live profile or on the shared master. A lock protects master copies and save-back. Copy my profile never saves back to the owner's profile.
Shared mode closes Chromium before it saves login state. Under the master lock,
save-back merges cookie rows by their actual unique key. A newer
`last_update_utc` wins. Login Data rows use `date_password_modified` instead.
Rows from other computers remain in the master. Logging out in one computer
does not delete master rows, so a later computer can receive that saved login.
Web Data, Local Storage, and IndexedDB save back only if that computer changed
them since startup. If both computers change the same storage, the last save
wins. An unchanged computer leaves the master untouched.
Only one Chromium runs on a computer's private profile. A second URL command
opens a tab in that browser; a second independent automation launch is rejected.
The wrapper reserves a loopback debugging port for orderly shutdown.
Snapshots include Cookies, Login Data, Web Data, Local State, and Preferences.
Save-back keeps login databases and Local State encryption metadata. It preserves
the master's Preferences instead of saving computer preference changes.
Shared copies include database write-ahead logs (WAL) when present. Copy my profile
uses the sqlite3 command to take a consistent read-only snapshot of live databases.
Install sqlite3 for copy-mine snapshots and shared database merges. It never copies database shared-memory files.
Local Storage and IndexedDB are each copied only when the directory is at most
16 MiB. Dyna leaves out caches.

WSL and systemd-nspawn use the basic password store. Copy my profile is unavailable
there because the owner's keyring is unavailable. A stored choice remains visible
with an explanation so you can select another mode.

## Commands

See `dyna guide computers`.

A shot or action never starts a stopped compositor. It returns `no windows yet`
and the last screenshot if one exists. Screenshot requests from UI tiles are
limited to one capture every five seconds. No screenshot timer runs when nobody
looks. Actions can capture immediately and keep distinct frame files.

While a human has control, agent input waits. Screenshots still work. To ask
for a handoff, run `dyna computer control human --reason "Please sign in"`
(the reason is required, 80 characters at most). The pill shows it until
control returns. Each viewer
owns a lease on a persistent control connection. Closing the viewer, losing its
WebSocket, or restarting websvc releases that lease and its human control.
An unwatched computer renders at 10 Hz. Opening a viewer lease changes it to
60 Hz. Closing the last lease restores 10 Hz, including after a lost connection.
Status reports the current `refresh_hz`. wayvnc stops 15 seconds after the last viewer leaves. Agent input and screenshots
use labwc's private socket and do not need wayvnc.

## Actions

See `dyna guide computers`.

An action batch contains 1–50 action objects. Dyna validates the whole batch
before sending any input. Unknown fields, unknown action types, missing fields, fractional or
boolean coordinates, and unknown key names are errors. Execution is ordered,
but a runtime error can leave earlier actions applied.

| Type | Fields |
| --- | --- |
| `click` | `x`, `y`, `button`: `left`, `middle`, or `right`; optional `keys`. |
| `double_click` | `x`, `y`; optional `keys`. Uses the left button. |
| `move` | `x`, `y`. |
| `drag` | `path`: 2–200 objects with exactly `x` and `y`, such as `{"x":80,"y":46}`. A point written as `[80, 46]` is accepted too. Optional `keys`. Optional `step_ms` (0–500, default 10) sets the pause after each point; when set, drag also pauses after the press. |
| `pointer_down` | `x`, `y`, optional `button` (default `left`). Moves, then presses and holds the button. |
| `pointer_up` | Optional `x` and `y` together, optional `button` (default `left`). Moves, then releases the button. |
| `scroll` | `x`, `y`, `scroll_x`, `scroll_y`: integers from -3000 to 3000. |
| `keypress` | `keys`: 1–12 key names. Pressed together and released in reverse order. Optional `hold_ms` (0–10000) sets how long they stay down; the default is 150 ms. |
| `key_down` | `keys`: 1–12 key names. Presses them and keeps them held until `key_up`, a takeover, or the end of the batch. |
| `key_up` | `keys`: the keys to release, in reverse order. List the same keys as the `key_down`, including `SHIFT`, because Shift changes the key names. Releasing a key that is not held does nothing. |
| `type` | `text`: valid Unicode text. Maximum 10,000 characters across a batch. Optional `secret`: `true` keeps the text out of the event feed and log. |
| `wait` | Waits one second. Maximum five per batch. |
| `screenshot` | Captures at this point. Maximum ten per batch. |

Coordinates are integers from the upper-left corner, with `0 <= x < width` and
`0 <= y < height`. The default size is 1280 × 800. Capture rejects an unexpected
output size. Positive scroll values move down or right. Scroll values are
relative amounts, not exact pixel guarantees.

Key aliases include `CTRL`, `ALT`, `SHIFT`, `META`, `SUPER`, `CMD`, `ENTER`,
`ESC`, `TAB`, `BACKSPACE`, and arrow names. X11 key names such as `F4` also work.
Text uses wtype's Wayland virtual keyboard. Newlines and tabs work. CRLF and CR
become newlines. Other control characters and invalid Unicode are rejected.
Application support still determines the displayed text.

If the batch has no `screenshot`, Dyna captures one final frame. Explicit
screenshots suppress that automatic frame. Add a trailing screenshot to see
later actions. Each frame has its own path, size, RFC 3339 `captured_at`,
zero-based `action_index` (null for an automatic frame), and `offset_ms`.
The top-level image fields point to the last frame. All protocol times use
RFC 3339; elapsed values use milliseconds or seconds as their field names state.
The action event feed and event log keep typed text, cut to 24 characters.
With `"secret": true`, the event carries no text,
only the character count, and the UI says "Typed a password". An authenticated
device already acts as a shell, so showing typed text to the owner is fine.

## Lifecycle and files

State is in `<Dyna data>/computers/<session>/`: metadata, state, logs, action
frames, browser profiles, and `last.png`. Runtime sockets are in
`$XDG_RUNTIME_DIR/dyna/computers/`. The shared daemon control socket is
`control.sock`. Its newline-delimited JSON requests include `session` and `op`.
The Go client in `internal/computers` handles requests and viewer leases.
`memory_current_kib` reports memory charged to the computer’s cgroup when
resource controls are available. This includes charged shared memory and cache.
`rss_kib` remains summed resident memory from the computer’s processes; it can
count shared pages more than once. Without a cgroup, status uses tracked trees. The fallback can miss a child that reparents before it
is tracked. Normal status calls do not scan every process environment; fallback
cleanup still uses that scan to find adopted desktop children.
`Subscribe` streams state and redacted action events; `events.jsonl` is a log.

The daemon relays both bytes and file descriptors for Wayland and X11. It reserves
both filesystem and abstract X11 sockets. The public X11 display is stored and
reused after a daemon restart. Internal screenshot, input, and stream clients
connect to the private compositor socket. They do not count as app activity.

After ten minutes with no public app connection, viewer, or agent input, the
daemon stops labwc. A stopped event explains why. The last PNG stays. A new app
connection starts labwc again. Closing a session, including a
consolidation source, destroys its computer and browser profiles. Logs and
screenshots remain with the session history. A machine restart loses processes.
When the daemon starts, it restores registered endpoints from the records on disk.

The compositor uses GLES2 when a render node works, with a pixman software
fallback. wayvnc enables GPU features after a successful VA-API H.264 encode probe.
The viewer negotiates its actual encoding and can use normal Tight/ZRLE encodings.

## Desktop apps, GPU apps and games

A computer runs native Wayland apps and X11 apps through Xwayland. Qt 6 apps
need the `qt6-wayland` plugin or use Xwayland with `QT_QPA_PLATFORM=xcb`.
Apps get the host GPU through `/dev/dri`, so OpenGL apps render with hardware
acceleration when the host has a working driver. Check what an app sees
with `dyna computer run --status "check GL" -- glxinfo -B` (Xwayland) or
`eglinfo -B` (Wayland).

Open an app with `dyna computer open -- <app>`, then drive it with
`dyna computer act` and check it with screenshots. For example, a game
launcher such as Prism Launcher can join a Minecraft server directly:
`dyna computer open -- prismlauncher --launch <instance> --server host:port`.

An agent session can run with a private `HOME` for its harness, so an app may
start with empty data and show its first-run setup. To use the owner's existing
app data, pass the app's data folder flag, for example
`prismlauncher --dir /home/<user>/.local/share/PrismLauncher`.

Limits for game QA:

- Actions move the pointer to absolute positions only. A game that locks the
  pointer and reads relative motion, such as mouse-look, does not turn. Menus,
  keys, chat and screenshots work.
- A computer has no audio output. Most games run without sound.
- The computer shares the owner's files and sign-ins (see below). A game that
  allows one session per account disconnects the owner's own session when an
  agent joins with that account. Use a separate test account, or ask the owner
  first.

## Trust and sharing gaps

A computer shares the owner's Unix account and files. It is not a sandbox.
Dyna uses its existing authenticated device and Connect access. Screen contents
are untrusted task data.

Browser shims cover `chromium`, `google-chrome`, `google-chrome-stable`, `brave`,
and `firefox`. They remove computer shim directories from `PATH` before finding
the real binary. Chromium browsers get a per-computer user data directory.
Firefox gets a per-computer profile and `--no-remote`.

An absolute-path browser launch bypasses the shim. Electron apps such as `code`
and Slack can still contact the owner's running instance. GApplication apps and
notifications can also reach the owner's session because Dyna preserves the
owner's D-Bus. Keep these limits in mind when an app does not appear on the
computer.

See [the computer UI guide](computers-ui.md) for the owner controls and viewer.

## Setup failures and credential prompts

A computer setup failure does not fail an agent turn. The turn continues with
its original environment. `dyna computer status` reports the setup error, and
`<Dyna data>/computers/daemon.log` keeps the diagnostic. Session close continues
its questions, goals, and event cleanup even if computer cleanup fails. A stale
control socket uses offline cleanup. When the daemon starts, it restores the
registered endpoints from the records on disk.

Some credential prompts use the client's display. SSH askpass and graphical
GPG pinentry can therefore open on the computer. Watch the computer when an
agent waits for such a prompt. Dyna keeps the owner's D-Bus and keyring access.

The private Xwayland shim uses `setpriv` to keep a parent-death signal after wlroots double-forks. Daemon children
have a parent-death signal, and the daemon reaps adopted children. The systemd
service additionally owns a delegated cgroup. A public display client and its
GUI process tree join the computer's cgroup, so stop and destroy end that tree.

The `hypruse` shim deliberately keeps the owner’s desktop reachable. It restores
nonempty `DYNA_OWNER_WAYLAND_DISPLAY` and `DYNA_OWNER_DISPLAY` values before
running the real command. Empty owner values leave the current display unchanged.

## Browser automation

Dyna gives agent-browser a private socket directory and namespace for
each computer. Even an absolute agent-browser path, such as Porch's bundled
native binary, inherits these settings. Two Dyna sessions can use the same
agent-browser `--session` name without sharing a daemon or browser. Each daemon
uses its computer's private browser profile. Dyna replaces inherited profile settings
with this path. Tool options that require a fresh temporary profile may reject it.

Dyna removes inherited `AGENT_BROWSER_CDP`, `AGENT_BROWSER_AUTO_CONNECT`, and
`AGENT_BROWSER_DAEMON` settings, along with inherited browser profiles. On Linux
it overrides the executable default with a computer-specific Chromium wrapper.
The wrapper selects the raw system Chromium binary, or an installed
agent-browser, Puppeteer, or Playwright Chromium cache when system Chromium is unavailable. This avoids desktop launcher flags such as the
owner's fixed debugging port. Computer stop and destruction also stop private
agent-browser daemons, including during offline cleanup. Idle desktop stop also
ends these daemons; headless browser activity does not reset the desktop idle
timer. Existing owner daemons stay untouched.

Playwright launches through the wrapper inherit the computer display and use its
private Chromium profile. Keep the inherited environment when passing Playwright's
`env` option. The wrapper replaces profile arguments with the computer's private profile. Explicit CDP connections, auto-connect flags, custom tool config,
owner display overrides, and shared persistent profile paths can bypass these
defaults. Computers share the owner's Unix account; they are not a sandbox.

The live agent-browser test uses the installed tool and system Chromium. The
optional Playwright live test runs when `DYNA_COMPUTERS_PLAYWRIGHT_MODULE` points
to an installed Playwright module. These tests do not download dependencies.


## Browser flags and resource controls

Chromium shims, agent-browser, and the Playwright wrapper read one file:
`<Dyna data>/computers/<session>/config/chromium.flags`. Dyna creates the file
once and preserves edits. It injects its path as `DYNA_CHROMIUM_FLAGS_FILE`.
Put one argument on each line. Empty lines and lines starting with `#` are
ignored. Spaces stay within an argument; the file is never evaluated as shell
code. A missing per-computer file leaves only flags from Settings and the browser SDK.

The default flags select native Wayland and disable background networking,
extensions, component updates, and sync. Remove a flag when an app needs that
feature. Low-end device mode stays optional because real-app measurements did
not establish a consistent benefit. Browser SDKs can add their own arguments.

See `dyna guide computers`.

This wrapper also joins the computer cgroup before starting Chromium. Headless
browsers then receive the same resource controls as headed browsers. Launches
that bypass the wrapper can join later when their display peer is identified;
memory charged before migration can remain charged to the original cgroup.

The delegated daemon enables per-computer memory, CPU, and process accounting.
Each computer has a soft `memory.high` threshold of 2 GiB. Above this threshold,
the kernel reclaims memory and slows allocations. It is not a hard cap or a
memory reservation. The existing `dyna.slice` limits still protect the owner’s
desktop. See [computer resource controls](computers-cgroups.md) for configuration,
accounting limits, and fallback behavior.
