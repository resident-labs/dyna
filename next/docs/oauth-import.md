# Import an existing OAuth sign-in

`accounts.import_oauth` is an owner-only API method. It imports one copy of a
Claude, ChatGPT, Kimi Code, or xAI OAuth sign-in. It does not read a v1 store.
Prepare the request on the control plane and send it to the owner socket. Do not
put credentials in command arguments, shell history, room posts, or logs.

The request has these fields:

- `provider`: `anthropic`, `openai`, `kimi`, or `xai`.
- `label`: the account label.
- `expiresAt`: the access token expiry as an RFC 3339 timestamp.
- `credential`: a JSON-encoded string containing an object with `access_token`
  and `refresh_token`, plus any fields for that provider:
  - `anthropic`: `access_token` must be an OAuth token that starts with
    `sk-ant-oat`. `email` is optional.
  - `openai`: `account_id` is required. `email` is optional.
  - `kimi`: `domain` (`kimi.com` or `kimi.ai`) and `device_id` are optional.
  - `xai`: there are no other fields.

For v1 Claude Code sign-ins, send `accessToken`, `refreshToken`, and the account
email as `access_token`, `refresh_token`, and `email`. Convert `expiresAt` from
Unix milliseconds. Do not send `scopes` or `subscriptionType`. For v1 Codex
entries in Pi, also send `accountId` as `account_id`. For v1 Kimi native files,
convert the expiry from Unix seconds. Preserve the Kimi domain and device ID
when present. For v1 Grok, use the OIDC entry for `https://auth.x.ai` and the
Grok client; its `key` is the access token, and its `expires_at` is the expiry.
Pi OAuth entries use `access`, `refresh`, and a Unix-millisecond `expires`
value. Send only the mapped values to this method.

The proxy refreshes both sign-ins with the OAuth clients that Claude Code and Pi
use. A Claude sign-in has no account UUID or device ID at import. The first
request reads the UUID from the Anthropic OAuth profile and creates the device
ID. That request fails if the profile lookup fails with anything but a 403. Send
one request before you reconnect the account. Reconnect compares the UUID, so it
rejects a credential that still has only an email, and it is unavailable for a
credential with neither.

A Pi Codex entry has no `id_token`. Requests do not need one: they use only
`access_token` and `account_id`, and the access token's own expiry takes
precedence over `expiresAt`. Without the `id_token`, the import also has no plan
or email. The proxy treats an unknown plan as Pro, so it can offer a model that
the account's plan refuses upstream. A refresh requests the `openid` scope and
stores the returned `id_token`, plan, email, and account ID. The proxy registers
models only at activation and start, so the plan's model list applies at the
next proxy start after that refresh. If a refresh response had no `id_token`,
the proxy would keep `account_id`, clear `email`, and set the plan to `free`.

The proxy stages the credential before Dyna binds the account. The proxy then
activates it and manages refresh. If activation fails, Dyna attempts to remove
both the credential and the binding. If cleanup fails, the account remains
visible for removal. Unbound staged credentials are pruned at proxy startup.

Do not import the same sign-in twice into v2. Copying does not transfer
exclusive ownership of a refresh token. If v1 and v2
both refresh it, a provider may invalidate the older token. The operator must
account for that risk before importing. Do not refresh either copy merely to
prepare the import. The proxy refreshes a Claude sign-in 4 hours before expiry
and a Codex sign-in 96 hours before expiry, so v2 may refresh immediately after
activation. Stop v1 from using a sign-in before importing it.
