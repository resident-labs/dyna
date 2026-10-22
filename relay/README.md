# Resident Labs Connect relay

A Cloudflare Worker with one Durable Object (SQLite): passkey accounts,
machine links, managed tunnels, and the broker step that asks a machine to
mint a sign-in code. It is never in the traffic path between a browser and a
machine.

Read [docs/desktop/CONNECT.md](../docs/desktop/CONNECT.md) first: it has the trust
model, the flows, the deploy steps and the key rotation steps.

```sh
npm ci
npm test            # workerd, software passkey, fake Cloudflare API
npm run typecheck
./deploy.sh         # https://connect.residentlabs.dev
./deploy.sh test    # https://connect-test.residentlabs.dev
```

| File | What it holds |
|---|---|
| `src/worker.ts` | routes |
| `src/http.ts` | request rules: Origin, `X-RL`, JSON, cookies, rate limits |
| `src/auth.ts` | invites, passkeys, recovery, relay devices |
| `src/machines.ts` | linking, provisioning, the machine API, `/open` |
| `src/directory.ts` | the Durable Object and its tables |
| `src/cloudflare.ts` | tunnel and DNS calls |
| `src/ui.ts` | the relay pages |
