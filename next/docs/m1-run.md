# Run the M1 engine

Build the binary. PiG, the agent harness, is part of it, so there is nothing else to build.

From the repository root, with Go, Node and npm installed:

```sh
mkdir -p "$HOME/dyna-m1/bin"
(cd next && go build -o "$HOME/dyna-m1/bin/dyna" ./cmd/dyna)
```

Build and test on the configured build node.

Create `$HOME/dyna-m1/profiles.json` with a profile for the model allowed by
the owner's proxy sign-in. For example:

```json
{
  "example": {
    "provider": "anthropic",
    "model": "claude-opus-5-5",
    "allowedModels": ["claude-opus-5-5"],
    "thinking": "xhigh"
  }
}
```

For a text-only model, add `"images": false` to its profile. Omit it for models that accept images. Boxes read these settings at start; refresh running sessions after adding or changing a text-only profile.

Start the engine. Use an absolute path on the host for `--host-data`. The
host check creates this directory.

```sh
"$HOME/dyna-m1/bin/dyna" engine \
  --data "$HOME/dyna-m1/data" \
  --profiles "$HOME/dyna-m1/profiles.json" \
  --host-data /absolute/path/on/the/host
```

In another terminal, register the host and create a room. Replace the example
SSH address with the owner's host.

```sh
dyna host add hermeslab --ssh user@host --owner-only-admin
dyna room create example
```

Use the returned room ID in `dyna session start --room ID --name worker
--profile example --goal '...' --brief '...'`. Read its events with
`dyna room log ID --follow`. The box can run `dyna post 'message'` without
a room flag; its socket identifies its room. Close the session with
`dyna session close SESSION_ID`.

M1 does not upgrade a running box across engine versions. Close all boxes
before an engine upgrade. The proxy must already have a valid sign-in in its
auth directory.

## Trial deployment and fix forward

Run `next/scripts/deploy-trial.sh --dry-run` on the trial host to build and
check the pair without installing it. The script accepts `--prefix`, `--node-bin`,
`--socket`, `--unit`, `--web-url`, `--web-host`, `--data`, `--host-data` and
`--stage-wait` for other installations. `--stage-wait` is seconds from 0 to 1200,
defaulting to 1200. Paths are configuration, not derived from a systemd ExecStart
or a particular machine's name.

To deploy, SSH to the trial host and run the script from a clean checkout of
the commit you want. It needs Go and the Node in `--node-bin` on that host, and
it restarts the trial's user service:

```sh
git fetch origin && git checkout --detach origin/main
next/scripts/deploy-trial.sh
```

### Host binary staging

Before restarting the engine, deploy runs an independent user unit to stage the
new executable on the ready, non-exec-only hosts in a read-only snapshot. The
snapshot is taken before compression or transfers. The helper does not open the
engine store, migrate it or take its lock. Newly added or newly ready hosts after
that snapshot use the new engine's ordinary installation path instead.

A host that already has the exact uncompressed SHA receives no payload. A cold
host must decode a small canary: working zstd is preferred, with gzip as the
existing-host fallback. Neither tool gives a typed failure instead of publishing
an unverified executable. The engine and helper share the same content-addressed,
checksum-verified, atomic staging primitive. Staging never switches `current` or
starts new agents; the new engine activates its own checksum later.

Deploy waits at most 20 minutes, logs per-host elapsed times and lists missed
hosts. At the cap it restarts while the unit continues for up to 24 hours. The
helper executable and reports live under `<prefix>/staging/<binary-sha>/`, outside
the pruned build `stage/` tree. `pending`, `done` and `staging.log` record its
progress. Keep that artifact while its unit is alive; these retained artifacts are
not automatically pruned by deploy. Multiple engine subscribers share one host
transfer. An independent helper and the new engine may both copy the same SHA
after the wait cap; this accepted duplicate traffic has no interprocess lease,
and private verified temporary files prevent partial publication or an older
stager changing the active version.

The current trial installation uses these explicit paths:

| Purpose | Path | Configuration |
| --- | --- | --- |
| Engine data | `/home/pip/.local/share/dyna-next` | `--data` (default `$HOME/.local/share/dyna-next`) |
| Host data | `/home/pip/.local/share/dyna-next-host` | `--host-data` (default `$HOME/.local/share/dyna-next-host`) |
| Engine runtime | `/run/user/1001/dyna-next` | `XDG_RUNTIME_DIR`; deploy's default owner socket lives here |
| Installed binary | `/home/pip/.local/opt/dyna-next/bin/dyna` | `--prefix` (default `$HOME/.local/opt/dyna-next`) |

Staging uses a separate private SSH runtime directory under `XDG_RUNTIME_DIR`
(or `/run/user/$(id -u)`), so an engine restart does not close its SSH masters.
The hidden `stage-host-binaries` helper accepts absolute `--data`, `--host-data`,
`--runtime` and `--report-dir` paths when invoked directly. Match the engine's
actual data and host-data configuration on either side of an engine relocation.
This does not move the engine, change host identity, or change any node's network.
Service-active and web-200 checks still run after restart.

The trial never rolls back. Fix failures forward on main. An older binary
cannot open a store after a schema step.
