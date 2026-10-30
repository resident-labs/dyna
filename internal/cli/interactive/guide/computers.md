# Computers

Use `dyna browser` for web work. It starts or reuses your computer's headed Chromium.
The browser shares the profile and visible desktop used by `dyna computer act`.
Use Playwright for pages and raw Chrome DevTools Protocol (CDP) for browser tools.

```sh
dyna browser open https://example.com --status "Checking the example page"
node --input-type=module <<'JS'
import { execFileSync } from 'node:child_process';
const [endpoint, importLine] = execFileSync('dyna', ['browser'], { encoding: 'utf8' }).split('\n');
const { chromium } = await import(JSON.parse(importLine.match(/import\((.*)\)/)[1]));
const browser = await chromium.connectOverCDP(endpoint);
const context = browser.contexts()[0];
const page = context.pages().at(-1);
console.log(await page.title());
const cdp = await context.newCDPSession(page);
await cdp.send('Performance.enable');
console.log(await cdp.send('Performance.getMetrics'));
await browser.close();
JS
dyna browser stop
```

To measure a page (rule r13), run `dyna browser trace start [URL]`, use the page, then `dyna browser trace stop`.
Stop prints JSON: `time_to_usable_ms` (first `performance.mark`, else DOMContentLoaded), `long_tasks_over_200ms`,
`heap_growth_bytes`, `request_count`, `console_errors` and a `screenshot` path. The trace adds some CPU load itself.
It targets the page marked `globalThis.__dynaReadOnlyQA === true` when one exists, else the last page.

To see a page as a phone or desktop, run `dyna browser preset phone` (390x844, 3x, touch, mobile user agent),
`preset desktop` (1280x800) or `preset off`. It applies to every tab, including tabs opened later, until `off` or until the browser stops.
A new Playwright connection resets a page's emulation. In Playwright scripts, make a context from
`dyna browser preset phone --options` (prints `browser.newContext` options as JSON) instead.

To record the whole screen (any app), run `dyna computer record start [--max 20] [--fps 5] [--bitrate 1000]`
and later `dyna computer record stop [--post @mention]` (`dyna browser record` is the same command).
The clip is an MP4 of at most 60 seconds (default 20, then it stops by itself) in the computer's folder.
`--post` sends it to the room as an attachment. Start refuses a length and bitrate that could pass the 24 MiB limit.
A recording shows everything on screen, including secrets: check the screen first. A red box is burned into every frame.
GitHub has no file upload API, so link the room post from the PR body.

The example needs only Node.js. Do not run `npm install`. `dyna browser` installs
`playwright-core` once into the Dyna data folder, then prints the endpoint and a
four-line example. The first line imports `chromium` from that managed copy by
absolute path, so it works in CommonJS and ES module scripts. Reuse that endpoint for profiling,
tracing, network checks, and performance measurements.
The installed app (`http://127.0.0.1:7457`) opens in a new tab, already signed in. Do not pair
by hand with `dyna engine run --pair`. Each `dyna browser` call pairs a device named
`dyna browser · <session>` for this computer's browser only, and it shows in Settings > Devices.
The device is revoked when the browser stops (`dyna browser stop`, idle stop or a crash),
and it expires after 2 hours if no revoke runs. Each `dyna browser` call renews it.
The token goes straight into the browser as a session cookie and is never printed.
The random debug port binds only to `127.0.0.1`. Processes under the same Unix account
can still reach it; computers do not provide a security boundary between those processes.

Use `dyna computer act` for non-web apps, things Playwright cannot reach, and quick looks.
To click a human-check box (Cloudflare Turnstile and similar), use `dyna computer act` too, because Playwright and CDP clicks are synthetic and fail these checks.

A computer shares the owner's files and Unix account. It is not a sandbox.
Identity comes only from the DYNA_* stamp. No stamp means root.
A stamped session uses its own computer. Root uses one per room, `root-<room id>`.
`dyna browser`, `env`, `shot`, and `act` create it on first use. `status` and `stop` do not.
`dyna computer` takes no acting `--session` flag. `status` takes no session argument.

Set the desktop size with `--size WIDTHxHEIGHT` on `env` or `open`.
The size is saved for later app launches and daemon restarts. `env --size` requires
`--status` because it changes the desktop. For example:

```sh
dyna computer env --size 390x844 --status "Checking phone layout"
dyna computer open --size 844x390 --status "Checking landscape layout" -- foot
```

A desktop width is rounded up to a multiple of 4 (390 becomes 392) so screenshots work; the command prints the real width. For an exact 390 px phone layout, use the browser's device emulation inside the window.

Use `dyna computer env`, `dyna computer status`, `dyna computer shot`, and:

```sh
dyna computer act --status "Checking the search field" '[{"type":"click","x":200,"y":120,"button":"left"}]'
```

Run `dyna computer act --help` for the generated action schema, fields, and limits.

A native or frame-driven app (for example an egui window) can miss a fast drag. Pace
it with `step_ms` on `drag`, such as `{"type":"drag","step_ms":120,"path":[...]}`, so
each point and the press last a few frames. Or split it into `pointer_down`, `move` and
`pointer_up` actions with a `wait` between them. Without these fields the timing stays as
before. To hold a key, such as a game's Use button for several ticks, send
`{"type":"keypress","keys":["e"],"hold_ms":600}`, or `key_down`, a `wait`, and `key_up`.
A person who takes control releases every held key and button at once.
Its schema uses the same definition as input validation.

Every input batch and `dyna browser open` require a short `--status` saying what you intend to do.
Use a few words, for example "Opening PR #869 to check Forward".
Dyna trims the status and accepts at most 80 characters on one line.
The schema below marks input actions with `requires --status`.
Mixed batches need a status if any action changes input.
Screenshot and wait batches do not take `--status`; neither do `shot`, `status`, `list`, or `env`.
Stopping the computer also requires a status:

```sh
dyna computer stop --status "Finished checking the page"
```

The floating pill shows the latest status and its age while the owner watches.

While the computer runs, `dyna computer status` includes a `viewer` URL field.
Paste it in a post so a person can watch or take control.

`shot` captures a new frame when the desktop is running. Capture errors are returned directly.

Use `dyna computer open` to start a non-web GUI app. It waits for a new window
and prints the app details. Standard output and errors go to the returned log path.
Pass the command and its arguments after `--`. Opening an app requires `--status`.
For example, open a terminal and type a command into it:

```sh
dyna computer open --status "Opening the terminal" -- foot
dyna computer act --status "Checking terminal input" '[{"type":"type","text":"printf \"Hello from Dyna\\n\""},{"type":"keypress","keys":["ENTER"]}]'
```

Use `dyna computer run` for a foreground command in the computer environment.
It streams standard output and errors and returns the command's exit status.
`--cwd` selects its working directory. `--tty` provides a terminal for interactive
programs; terminal output combines stdout and stderr. A run requires `--status`.
For example:

```sh
dyna computer run --status "Checking the working directory" -- pwd
dyna computer run --tty --status "Inspecting the terminal" -- sh
```

A wait accepts optional integer `ms`. The action schema below gives its bounds and default.

```sh
dyna computer act '[{"type":"wait","ms":2500},{"type":"screenshot"}]'
```

The desktop starts when an app connects. Status explains setup failures.
A human can take control; your computer input waits until they release it.
Set `secret:true` when typing secrets. Action events show up to 24 typed characters.

Run containers with --user $(id -u):$(id -g), so nothing under $DYNA_SCRATCH is root-owned.

## Desktop and GPU apps

A computer runs native Wayland apps and X11 apps (through Xwayland), including OpenGL apps and games.
They use the host GPU through `/dev/dri`. Qt 6 apps need the `qt6-wayland` plugin or `QT_QPA_PLATFORM=xcb`.

```sh
dyna computer run --status "Checking GL" -- glxinfo -B   # Xwayland; use eglinfo -B for Wayland
dyna computer open --status "Opening the app" -- <app> <args>
dyna computer shot
```

An agent session has a private `HOME`, so an app can start with empty data and show first-run setup.
Copy the app's data folder to `$DYNA_SCRATCH` and pass the copy with the app's data-folder flag.
Never point the app at the owner's own folder without the owner's consent: it holds their sign-in tokens, and two
instances that write one folder can corrupt it. Use a separate test account for online games or services.

Limits: actions move the pointer to absolute positions only, so mouse-look in a game that locks the pointer does not
turn. A computer has no audio output. The computer shares the owner's files and sign-ins. Menus, keys, chat and
screenshots work. For sound, see `dyna guide voice-agents`: `dyna voice say` and `dyna voice session` write
audio to your program, not to the computer.

## Read-only branch web QA

Use the shared launcher to build the branch web app and keep QA running:

```sh
bash ./scripts/qa-branch.sh --web web/dist --status "Check branch web changes"
```

The launcher limits QA builds to two on one computer. Other builds print
`waiting for a QA build slot` until a slot is free. Web and Go builds run at
nice level 10 with `GOMAXPROCS=4`. The build slot is free while you use QA.

The command opens a dedicated browser context at the installed engine URL,
`http://127.0.0.1:7457`. That context serves the branch's built shell and assets.
The installed app stays unchanged in other tabs. The QA shell keeps the real
engine's client metadata and security headers. Browser pairing and CSRF checks
stay unchanged. No extra engine, proxy, route, or port is needed.

The shared engine binding table permits read calls only. Write calls, unknown
API paths, external requests, service workers, and WebSockets are blocked.
The command prints `QA restriction: blocked ...` for intentional restrictions.
These restrictions include read marks, terminal control, profile probes, model
discovery, update checks, and live account usage refresh. Cached account usage
remains available. A restriction can show an error in a write control; record
that restriction separately from an unexpected app error. This mode tests read
flows. It cannot test a flow that needs a real write.

The ready banner names what this mode cannot show: settings writes, live
streams that need a WebSocket (the computer viewer and voice calls), and server
fields newer than the installed engine. The room's live event stream is an
EventSource over an allowed read, so room updates do arrive; only a socket
stream stays closed, and a write control keeps its old value. The banner names
the call to make.

When a step times out, ask the QA page instead of guessing. Evaluate
`globalThis.dynaQAReport()` in the QA page. It returns every blocked request
kind with its count, such as `POST API request` with 4 and `socket`, and saves
the page screenshot, whose path it returns in `screenshot`. The command prints
both lines too. Read them together: a control that needs a write, a socket
stream or a newer server field cannot work in this tab, and a branch bug looks
different. The report never holds a URL, a header or a cookie.

The ready output prints `QA page CDP target:` and `QA page CDP websocket:`.
Keep the launcher running and give the websocket URL to a CDP client to drive
that exact page from another script. The target id also identifies the page in
CDP target lists. You can still use the existing `dyna browser` connection and
select the page where `globalThis.__dynaReadOnlyQA === true` in a page evaluation.

For a bounded run, pass `--steps FILE`. The file holds a JSON list, and each
item has exactly one action. This example captures desktop and phone views and
then exits:

```json
[
  {"viewport":{"width":1280,"height":800}},
  {"goto":"/"},
  {"waitFor":"body"},
  {"screenshot":"/tmp/qa-desktop.png"},
  {"viewport":{"width":390,"height":844}},
  {"screenshot":"/tmp/qa-phone.png"}
]
```

The other actions are `{"click":"button.save"}` and
`{"type":{"selector":"input[name=name]","text":"QA"}}`. A `goto` is a
path or URL on the QA origin. `waitFor` waits for a visible selector. Relative
screenshot paths start at the repository root. Use paths in `$DYNA_SCRATCH`
for QA files. Run `bash ./scripts/qa-branch.sh --help` for the full schema.
The same read-only request policy applies to every step. Do not send posts or
other writes as a QA test.

For manual performance QA, set the viewport to desktop size and then 390 px
width. Capture screenshots, a CDP performance trace, time to usable, long tasks
over 200 ms, request count, and heap growth after repeated use. Keep trace and
screenshot files in `$DYNA_SCRATCH`.

Close the QA tab or press Ctrl+C to close the QA context and stop your QA
browser. The installed engine stays running. The command
must stay running while the preview is in use. Rebuild and start the command
again after changing branch assets.
