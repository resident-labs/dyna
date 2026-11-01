# Import an existing OAuth sign-in

`accounts.import_oauth` is an owner-only API method. It imports one copy of a
Kimi Code or xAI OAuth sign-in. It does not read a v1 store. Prepare the request
on the control plane and send it to the owner socket. Do not put credentials in
command arguments, shell history, room posts, or logs.

The request has these fields:

- `provider`: `kimi` or `xai`.
- `label`: the account label.
- `expiresAt`: the access token expiry as an RFC 3339 timestamp.
- `credential`: a JSON-encoded string containing an object with `access_token`
  and `refresh_token`. For Kimi, the object may also contain `domain`
  (`kimi.com` or `kimi.ai`) and `device_id`.

For v1 Kimi native files, convert the expiry from Unix seconds. Preserve the
Kimi domain and device ID when present. For v1 Grok, use the OIDC entry for
`https://auth.x.ai` and the Grok client; its `key` is the access token, and its
`expires_at` is the expiry. Pi OAuth entries use `access`, `refresh`, and a
Unix-millisecond `expires` value. Send only the mapped values to this method.

The proxy stages the credential before Dyna binds the account. The proxy then
activates it and manages refresh. If activation fails, Dyna attempts to remove
both the credential and the binding. If cleanup fails, the account remains
visible for removal. Unbound staged credentials are pruned at proxy startup.

Do not import the same sign-in twice into v2. Copying does not transfer
exclusive ownership of a refresh token. If v1 and v2
both refresh it, a provider may invalidate the older token. The operator must
account for that risk before importing. Do not refresh either copy merely to
prepare the import.
