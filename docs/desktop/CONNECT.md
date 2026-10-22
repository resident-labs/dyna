# Resident Labs Connect

Resident Labs Connect lets you open a computer's Dyna web app from anywhere
and sign in with a passkey. It adds four things to `dyna engine run`
(see [WEB.md](WEB.md)):

- **Accounts.** You sign in to `connect.residentlabs.dev` with a passkey. No
  password, no email. Accounts are invite-only. Each account has its own
  machines, devices and activity log.
- **A managed tunnel.** Linking a computer gives it a Cloudflare tunnel and
  two names: `m-<id>.residentlabs.dev` (the app) and
  `mc-<id>.residentlabs.dev` (its cards). No router setup.
- **Short-lived sessions.** A phone signs in through the relay, but the
  computer itself makes the sign-in code. The session's access lasts 15
  minutes and renews in the background with a key that never leaves the
  browser. Revoke a device on the relay page and its sessions end in
  seconds.
- **Several routes and push.** The app moves to a nearer route (this
  computer, Tailscale) when one answers with the machine's signature.
  Notifications come straight from the computer through Web Push.

The pairing code and the device cookie from WEB.md keep working with or
without Connect, and with no relay at all.

The design follows T3 Code's T3 Connect (MIT): the relay brokers, the
environment mints. Nothing is copied from its code.

## Start

### 1. The first account (once, by the owner)

The relay's first account comes from a bootstrap invite. `relay/deploy.sh`
made it:

```sh
echo "https://connect.residentlabs.dev/enroll#$(cat ~/.config/dyna/connect/production/bootstrap-invite)"
```

Open that link on your phone or computer and make a passkey. You are the
admin. The relay then shows 10 recovery codes: save them. It asks for a
second passkey (another device or a security key): add one. The bootstrap
invite stops working as soon as any account exists, even if the file stays.

### 2. Link a computer

On the computer, from your own terminal (not from an agent session):

```sh
dyna engine connect link
```

It prints a link, an 8-character code and the machine key's fingerprint.
Open the link where you are signed in to the relay, check that the page
shows the same code and fingerprint, and choose **Link to my account**. The
relay asks for your passkey again. It then makes the tunnel and the two
names, and the command ends with the app address.

### 3. Run the server

```sh
dyna engine run
```

When the computer is linked, `dyna engine run` starts `cloudflared` for the managed
tunnel (from `PATH`, or `--cloudflared`), reports its routes to the relay,
and prints the app address.

### 4. Open it on a phone

Open `https://m-<id>.residentlabs.dev`, or open the relay page and choose
**Open** next to the machine. Choose **Sign in with Resident Labs**, then
**Open <machine>** on the relay page. You are back in the app, signed in.

To install it as an app and get notifications, see [Push](#push).

## Run the engine as a service

To keep `dyna engine run` running across logouts and reboots, run it as a systemd
user service. Save this as `~/.config/systemd/user/dyna-engine.service`:

```ini
[Unit]
Description=Dyna web app and Resident Labs Connect tunnel (dyna engine run)
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%h/.local/bin/dyna engine run
Restart=on-failure
RestartSec=5
# Agent turns that the web app starts run as children of the engine. With the
# default KillMode, a restart kills them too. "process" stops only the engine;
# the engine stops its own cloudflared on the way out.
KillMode=process

[Install]
WantedBy=default.target
```

Then run `systemctl --user enable --now dyna-engine`, and
`loginctl enable-linger $USER` if it should start at boot before you log
in. After you install a new build, run `systemctl --user restart
dyna-engine`. Logs: `journalctl --user -u dyna-engine`.

## Commands

| Command | What it does |
|---|---|
| `dyna engine connect link [--relay URL] [--name NAME] [--addr ADDR] [--card-addr ADDR]` | Link this computer. `--addr` and `--card-addr` must match what `dyna engine run` uses (default `127.0.0.1:7457` and `:7458`). |
| `dyna engine connect status` | The link, the owner account, the key fingerprint, the relay's view, and the Connect sessions. |
| `dyna engine connect sessions` | List Connect sessions. |
| `dyna engine connect revoke ID\|DEVICE\|all` | End one session (`cs_…`), every session of one relay device (`dev_…`), or all. Works with no relay. |
| `dyna engine connect unlink [--local]` | Unlink. The relay deletes the tunnel and names; every session ends; the machine key is replaced. `--local` forgets the link here when the relay cannot be reached; then unlink the machine on the relay page too. |
| `dyna engine run --devices` | Paired devices and Connect sessions, with a TYPE column. |
| `dyna engine run --revoke ID\|all` | Also takes Connect session and device IDs. |

`dyna engine run` flags for Connect:

| Flag | Default | What it does |
|---|---|---|
| `--connect-grace` | `15m` | How long a session may renew while the relay cannot be reached. `0` to `1h`. |
| `--connect-access` | `15m` | The access window between renewals. `30s` to `15m`. Shorter is for tests. |
| `--no-tunnel` | off | Do not run `cloudflared`. |
| `--no-tailscale` | off | Do not add the route found in `tailscale serve status`. |
| `--no-push` | off | Do not send Web Push. |
| `--cloudflared` | `cloudflared` | The `cloudflared` binary. |

`DYNA_CONNECT_RELAY` sets the relay for `connect link` (default
`https://connect.residentlabs.dev`).

The `connect` commands refuse to run when a Dyna agent session started them
(the same guard as `dyna engine run --pair`), unless you pass `--allow-agent`. An agent
must not link a machine or end sessions by habit. This stops accidents only.

## Accounts

- **Passkeys only.** WebAuthn with user verification, relying party
  `connect.residentlabs.dev`. Machine hosts are other names, so they cannot
  use the passkeys. Every sign-in is a passkey.
- **Step-up.** Linking a machine, unlinking it, revoking a device, adding a
  passkey, making recovery codes and making invites need a passkey check from
  the last 5 minutes. The page asks for it.
- **Invites.** An admin makes an invite for a named person and a role (user
  or admin). It works once, for 24 hours. The relay stores only its SHA-256.
  The token rides in the link's fragment, so it never reaches a server log.
- **Recovery codes.** 10 one-time codes at sign-up (hashed on the relay). A
  code adds a new passkey, revokes every relay device of the account, and
  bumps the epoch of every machine, so every Connect session must sign in
  again. Redeeming is limited to 5 tries an hour per address.
- **Recovery invites.** An admin can make a recovery invite for any account.
  **Trust decision:** this gives the admin a way to take over that account.
  The activity log records who made it, and redeeming it resets the account
  like a recovery code does. Send it only to the account's owner.
- **Identity.** The relay user ID (`usr_…`) is the identity; the name comes
  from the invite. Two machines belong to one account because the same
  passkey session approved both links.
- **Devices.** Each signed-in browser on the relay is a relay device
  (`dev_…`). It lasts 30 days. The relay page lists them; revoke one there.
- **One session per device.** A browser that signs in again, or moves to
  another route, ends its older sessions: one login leaves one live
  session. A tab left on the old route shows sign-in.

## How it works

### Who holds what

| Where | What | Never |
|---|---|---|
| Relay (one Worker, one Durable Object with SQLite) | accounts, passkey public keys, hashes of invites, recovery codes, device sessions and poll secrets; machines with their public key, tunnel ID, routes and epoch; replay nonces; rate counters; the activity log | room content, session tokens of machines, push subscriptions, the tunnel token |
| Relay secrets | `CF_API_TOKEN` (runtime token), `RELAY_SIGNING_KEYS`, `BOOTSTRAP_INVITE_HASH` | in logs or answers |
| Machine, `<data dir>/connect` (mode 0700) | `machine.key` (Ed25519 seed), `link.json`, `tunnel-token`, `jwks.json` (relay keys), `sessions.json` (token hashes), `audit.log`, `push/` (VAPID key, subscriptions) | a relay password or account secret |
| Browser | a non-extractable P-256 key per origin and the pinned machine key (IndexedDB), the Connect cookie | the private key in any exportable form |

The relay is never in the traffic path. A browser talks to the machine
through a route; the relay only helps a browser sign in.

### Linking

1. `connect link` makes a new Ed25519 machine key and a poll secret. It sends
   the public key, the hash of the poll secret, a name and the two loopback
   ports to `POST /v1/link/start`, signed with the new key.
2. The relay answers with a code. The owner approves it on `/link` with a
   step-up. The relay refuses a key that is linked already.
3. The relay picks the machine ID (so nobody chooses a host name), makes the
   tunnel `rl-connect-<id>` with its ingress (app host to
   `127.0.0.1:<app port>`, card host to `127.0.0.1:<card port>`, else 404)
   and two proxied CNAMEs. Each step is a checkpoint with a generation
   number, so a retry or a late cleanup cannot undo newer work. Records carry
   the comment `rl-connect <id>`, and the relay changes only its own.
4. The machine polls with a signed request and the poll secret, then fetches
   the tunnel token once and stores it. The relay fetches the token from
   Cloudflare for each request and never stores or logs it.

Unlink revokes first (status and epoch), then deletes the names and the
tunnel. A failed teardown stays on the machine row to retry.

### Signing in (the broker step)

1. The page on a route has no session. It makes (or loads) its P-256 key
   and goes to `/open` on the relay with the machine ID, its origin, its key
   thumbprint and a random state.
2. The relay page names the machine and the address. Nothing happens on
   the GET. The owner chooses **Open**.
3. The relay checks that the user owns the machine and that the origin is a
   route the machine reported. It sends `POST /connect/v1/mint` to the
   machine's tunnel host (built from its own record, never from a request,
   no redirects). The body is a JWS from the relay key: operation, machine,
   user, relay device, epoch, key thumbprint, origin, nonce, 60-second life.
4. The machine answers only on its tunnel host. It checks the relay key
   (cached key set, at most 7 days old for new mints), the operation, that
   the user is its owner, the time, the nonce (once), the epoch and the
   origin. It makes a one-time code (5 minutes) bound to the key thumbprint,
   origin and relay device, and signs `nonce` plus the code's hash with the
   machine key.
5. The relay checks that signature with the machine key it stored at link
   time, then sends the browser to `<origin>/connect#code=…&state=…&mid=…&mk=…`.
6. The page checks the state, checks that `mid` and `mk` match the page's
   config, pins the machine key, clears the fragment, and posts the code
   with a proof (a JWS by its key over the method, URL, time and a one-time
   ID) to `/api/connect/session`. The machine checks the proof against the
   bound thumbprint, spends the code (one try, right or wrong), and sets the
   session cookie.

**Trust.** The machine trusts the relay's signing key to ask for mints. A
stolen relay signing key could ask any linked machine to mint a code. The
code is bound to a key the thief must hold and to a route origin the machine
reported, but this is still the relay's one strong power. Keep the signing
keys secret and rotate them if in doubt.

### Sessions and the cookie

- Cookie `__Host-dyna_connect` on HTTPS routes (`dyna_connect` on a loopback
  route): HttpOnly, SameSite=Strict, Secure on HTTPS, path `/`. The machine
  stores only its SHA-256. The `__Host-` prefix stops another
  `residentlabs.dev` host (any account's machine) from planting it.
- **Access window**: 15 minutes. After it, every call answers 401 with
  `code: "renew"`.
- **Renew**: `POST /api/connect/renew` with a fresh proof by the session's
  key, plus a grant check with the relay (`POST /v1/machine/grant`: is this
  relay device live and the owner's, and what is the link epoch), cached 60
  seconds. The page renews about 2 minutes before the window ends, and on
  wake.
- **Rotation**: each renew sets a new cookie value. The newest earlier value
  still works in full for 30 seconds (requests in flight). After that, and
  for the 2 values before it, a request may only renew, which needs the key.
  A copied cookie alone gets nothing. This rule comes from a real run:
  Chromium writes cookies to disk lazily, so a browser closed right after a
  renew comes back with the earlier value. A logout with an earlier value
  clears only that cookie; it does not end the session.
- **Absolute end**: 30 days after the first sign-in. Renew never passes it.
  A handoff to another route keeps the same end and ends the session it
  moved.
- **Relay down**: renew may skip the grant check for `--connect-grace`
  (default 15 minutes, at most 1 hour) after the last good check, only when
  the relay cannot be reached or answers 5xx, 429 (busy) or 408. Each such renew goes to the
  machine's `audit.log`. A relay answer that says no ends the session at
  once.
- **Revoke**: revoking a relay device or unlinking makes the relay send a
  signed `POST /connect/v1/revoke` to the machine (best effort). The grant
  check catches anything it misses. `dyna engine connect revoke` works with
  no relay.
- **Streams**: the event stream rechecks the session on every heartbeat (15
  seconds) and ends when it is revoked or its access window ends.
- **Limits**: renew counts only proofs that pass (10 a minute per session),
  so bad proofs from a cookie thief cannot block the honest renew. Mint 30 a
  minute, session exchange 20 a minute, hello 120 a minute, handoff 10 a
  minute per session.
- **Methods**: a Connect session may call a method only when its grant
  includes that method's scope (`internal/engine/calls.go`). A sign-in
  through the relay is the owner's own login (the mint proves the account),
  so it holds every scope; an owner who wants less narrows the session's
  grant in Settings. CardURL needs read. Pairing and device
  management need admin.

### Routes

A route is a pair of origins (app and cards), as in WEB.md. The machine's
routes come only from its own configuration: the loopback names, each
`--route`, a `tailscale serve` setup that points at both ports, and the
managed tunnel. No client and not the relay can add one. The machine reports
the HTTPS and loopback routes to the relay, which then allows `/open` to send
a browser there.

- `POST /api/connect/routes` (signed in) lists them. The public page config
  never does: it holds only the machine ID, name, key and relay.
- `GET /connect/v1/hello?nonce=…` is public, with `Access-Control-Allow-Origin: *`
  and no credentials. It answers with the machine ID, the route origin and
  the nonce, signed by the machine key. The page checks it with the key it
  pinned from the relay. TLS proves the host; the hello proves the same
  machine answers there.
- Ranking: loopback, then Tailscale, then a custom route, then the tunnel.
  A Connect session moves to the best route that answers, through
  `POST /api/connect/handoff` (needs a fresh proof by the source key; the
  code lives 60 seconds, works once, and is bound to the session and the
  target origin). The new origin gets its own key under the same relay
  device. No route switch goes to plain HTTP except loopback.
- If the event stream is down for 20 seconds, the page tries every other
  route and goes to the first that answers.
- The sidebar footer shows the route in use, for example "via tunnel".

The fast local path is Tailscale: `tailscale serve` on this computer (see
WEB.md), and Tailscale uses the direct LAN path between two devices on the
same network.

### Push

The machine sends Web Push itself. The relay is not involved and never sees
a subscription. The web service owns subscriptions and notification policy.
Paired devices can use push without a Connect link or machine key.

- The machine has its own VAPID key (`push/vapid.pem`). The browser
  subscribes on the machine's origin through `public/sw.js`.
- One subscription per device: the relay device for a Connect session (so a
  phone on two routes gets one push), or the paired device. The last
  subscribe wins. Revoking the device or ending its last session deletes it.
- Events: a post addressed to the owner by someone else, and a session's turn
  completion. Never the owner's own posts. One push per room per 20 seconds,
  with a topic so a newer push replaces an unread one.
- Payload, encrypted by the machine (RFC 8291): type, room ID and post ID.
  The default text is "New post in Dyna". A device can turn on room and
  sender names in the bell menu. Post text is never sent.
- Push hosts are an allowlist: `fcm.googleapis.com`, `jmt<N>.google.com`
  (Chrome since 2026), `web.push.apple.com`, `*.push.services.mozilla.com`,
  `*.notify.windows.com`. No redirects. A 404 or 410 deletes the
  subscription.
- A tap opens `/#room=<id>` on the worker's own origin. The URL never comes
  from the payload.
- On iPhone, Web Push needs the app on the Home Screen.

## Security rules

- Every relay API call is a POST with the relay's own `Origin`, `X-RL: 1` and
  a JSON body. The relay sends CORS on no route. Every relay GET is free of
  side effects.
- Relay cookies are `__Host-rl_session`, SameSite=Lax. The relay pages have a
  strict content security policy (`script-src 'self'`, no framing).
- Every machine request to the relay is a JWS by the machine key over the
  method, path, body hash, a 60-second life and a one-time ID.
- Rate limits on the relay: link starts 10 an hour per address, invites and
  sign-ups 10 an hour per address, sign-in 30 per 10 minutes per address,
  recovery 5 an hour per address, `/open` 30 an hour per account, machine
  calls 120 a minute per machine.
- `connect.residentlabs.dev` is never an app origin and never in a card's
  `frame-ancestors`.

### Known risks and debt

- **Same site.** Every account controls a host on `residentlabs.dev` (its
  `m-` and `mc-` names serve its own pages and cards). Those hosts are the
  same site as the relay, so SameSite gives no protection between them. The
  rules above (`__Host-` cookies, Origin checks, no CORS) carry the weight.
  A hostile account could also make a page that looks like another
  machine's. Debt: move user machines to a separate zone.
- **Relay signing key.** See [Trust](#signing-in-the-broker-step).
- **Admin recovery.** See [Accounts](#accounts).
- **Bad proofs.** A flood of bad proofs makes the machine check P-256
  signatures at request rate. It needs a live cookie value to reach renew.

## Relay operations

The relay lives in `relay/` (TypeScript, Cloudflare Worker, Durable Object
with SQLite). See `relay/wrangler.jsonc`.

```sh
cd relay
npm ci
npm test              # workerd tests with a software passkey and a fake Cloudflare API
npm run typecheck
./deploy.sh           # production: https://connect.residentlabs.dev
./deploy.sh test      # a separate relay: https://connect-test.residentlabs.dev
```

Two Cloudflare tokens, files in `~/.config/dyna/connect/`, mode 0600:

| File | Permissions | Used by |
|---|---|---|
| `deploy-token` | Account Workers Scripts Edit; zone `residentlabs.dev` Workers Routes Edit and DNS Edit | `wrangler` on this computer |
| `runtime-token` | Account Cloudflare Tunnel Edit; zone `residentlabs.dev` DNS Edit | the relay (secret `CF_API_TOKEN`) |

`deploy.sh` keeps the signing keys and the bootstrap invite in
`~/.config/dyna/connect/<env>/` (mode 0700) and uploads them as secrets with
the deploy. It prints none of them.

**Rotate the signing key.** Add a new key at the end of
`signing-keys.json` (`node scripts/keygen.mjs k<date>`), deploy, and wait a
day (machines refetch the key set when they see an unknown key ID). Then
move the new key to the front (the first key signs), deploy, and later
remove the old one. A machine refuses new mints when its cached key set is
older than 7 days and the relay cannot be reached.

The existing `dyna-web` tunnel (`dyna.residentlabs.dev`) is separate and
unchanged. Connect may replace it later.

## Tests

```sh
go test ./internal/connect/ ./internal/webpush/ ./internal/websvc/
cd desktop && CGO_ENABLED=0 go test . && CGO_ENABLED=0 go vet .
cd relay && npm test
```

`internal/connect` runs the full flow against a fake relay: mint, exchange,
renew, rotation and stale values, grace, refusal, the 30-day end, revoke and
epoch, handoff, hello and the page meta. The relay tests run in workerd:
bootstrap, invites, step-up, Origin rules, recovery, linking and
provisioning, replay, account separation, `/open` with the machine's
signature, grants and teardown.

## Not in this build

- **Sharing a machine between accounts.** A Connect session acts as the
  machine's owner. Dyna's event hub sends every room to every stream, and
  roomsvc has no per-user rules. Sharing waits for that work. Do not fake it
  by giving a second account an owner session.
- **LAN-direct HTTPS.** A public name pointing at a LAN address, an ACME
  certificate and a TLS listener on the LAN would break the loopback-only
  rule in WEB.md. It needs its own design review. Use Tailscale.
- **Email** (magic links, notices). The relay sends no mail.
- **A separate zone for user machines** (see Known risks).
- **An idle-tunnel reaper and a pinned, auto-updated `cloudflared`.**
  `dyna engine run` runs the `cloudflared` on `PATH`.
- **Native apps** (APNs, FCM tokens). Web Push only.

## Session promotion

Signed-in devices can use **Promote to lead**. It uses the same API calls and
host trust as the desktop and paired web app. See [WEB.md](WEB.md#session-promotion).

## Model transfer

Signed-in devices can choose **Transfer to new model** and click a model to
start. Progress and retry stay in the picker; cancellation is available during
the handoff wait. Opening a saved transfer resumes it automatically. They use the
same API calls and host trust as the desktop and paired web app. See
[WEB.md](WEB.md#transfer-to-a-new-model).


## PR review publishing

The Pull requests dialog uses the same room reviewPublishing setting as the
desktop: local by default, or an explicit github choice for future assignments.
Local Markdown reviews appear in both tracked PR views. Existing public
reviews stay public. See [Review publishing](README.md#review-publishing).


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

Connect exposes the same **Settings → Cards and files** controls and host limits
as desktop and paired web. See [Card and file sizes](WEB.md#card-and-file-sizes).

## Consolidate sessions

Signed-in devices use the same four consolidation APIs as desktop and paired
browsers. See [Session consolidation API](WEB.md#session-consolidation-api)
for request/state shapes and recovery rules. Operations stay on the host;
reconnecting reads their saved progress. Old source transcripts remain
readable, and original worktrees and dirty files stay intact. A failed target
startup can retry the saved model or cancel safely before source archival.

## Bulk session actions

Connect uses the same highlighted member selection and shared bulk actions as
desktop and paired web. Options, per-session results, tracked-PR close guards,
and read-only behavior are identical. See [Bulk session actions](WEB.md#bulk-session-actions)
for the API contract and request limits.

## Room session controls

Signed-in Connect devices have the same **Pause all**, **Resume all**, and
**Clear dormant sessions…** controls as desktop and paired web. The hold is
stored on the host and applies only to agent sessions in the current room.
Existing workflow and shell execution continues. Its messages to paused
sessions wait for delivery. See [Pause and resume room sessions](WEB.md#pause-and-resume-room-sessions)
for the four API methods, ordered results and dormant
cleanup rules.
