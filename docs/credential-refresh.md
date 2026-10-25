# Saved login refresh

Dyna desktop and the desktop web server check saved logins every minute.
Claude and Codex refresh when access tokens have five minutes
or less left. Active and inactive accounts use the same account lock. The
registry lock also serializes account changes across Dyna processes.

A refresh saves rotated tokens before writing active native files. If that
write fails, Dyna keeps the saved rotation and retries native repair. Private
native accounts use the harness switch gate, so refresh waits for live turns.
Each background private refresh attempt has a 20-second timeout.

Muse, OpenCode V2, and Pi have no supported
Dyna refresh operation. The account API reports known expiry through `expiresAt`, and
explains unsupported refresh through `refreshMessage`. An unknown expiry is
reported as unknown. Dyna does not guess provider OAuth endpoints.

Native harness processes can also renew credentials. Claude refresh honors
its native locks. Codex does not expose a shared token lock;
stop external native writers when Dyna manages those credentials.

The legacy Claude helper can invalidate Dyna's refresh tokens. Disable its
timer with `systemctl --user disable --now claude-accounts-usage.timer`. Dyna
never disables the timer itself. Do not run the helper manually afterward.

On an invalid grant, Dyna first looks for a newer unexpired native credential
with the same account identity. Claude also checks the legacy helper snapshots.

The account API exposes exhausted `five_hour` and `seven_day` windows through
`quotaBlocked`, `quotaBlockedUntil`, and `blockingQuotaWindows`.

Sign-in uses the Harnesses page or `dyna harness login`. Account responses
never contain token payloads.

## Sign-in fallback for Muse and OpenCode v2

Dyna has no verified refresh operation for these two adapters. A refresh token
in a credential file does not establish an endpoint, client contract, or safe
rotation order. Dyna keeps automatic refresh disabled until that contract has
an offline test. It does not try guessed OAuth endpoints.

| Harness | Available evidence | Current recovery |
| --- | --- | --- |
| Muse | File-backed Meta credentials can contain `expires_at` and a refresh token. The adapter has no verified refresh command or token endpoint. | A known expired credential without an API-key fallback marks the private account as not signed in. Use Sign in. |
| OpenCode v2 | Dyna stores a reference to a credential in the native database. The redacted auth list does not expose expiry or a standalone refresh contract. | Use Sign in when OpenCode requests authentication. Dyna runs login in the private database scope. |

Harnesses exposes Sign in in the account detail even when expiry is unknown.
Known expiry is returned as `expiresAt`; an unknown expiry stays absent.
Reading status does not refresh, rotate, or rewrite credentials. Muse sign-in
uses Dyna's private account HOME and preserves the personal CLI login.

An API-key fallback remains usable when an access-token timestamp has expired.
Dyna preserves that account's signed-in state. It still reports the access-token
expiry, without treating it as the expiry of the API key.
