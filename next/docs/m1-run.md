# Run the M1 engine

Build the binary and the Pi package together. The engine reads `pi.tgz` from
the directory that contains its executable. A Go build alone does not create
this package.

From the repository root, with Go, Node and npm installed:

```sh
mkdir -p "$HOME/dyna-m1/bin"
(cd next && go build -o "$HOME/dyna-m1/bin/dyna" ./cmd/dyna)
next/third_party/pi/scripts/pi-pack.sh "$HOME/dyna-m1/bin/pi.tgz"
```

Keep both files in that directory. Build and test on the configured build node.
The package script builds the vendored Pi fork and verifies the package.

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

## Trial deployment and rollback

Run `next/scripts/deploy-trial.sh --dry-run` on the trial host to build and
check the pair without installing it. Run it without `--dry-run` to install
and check it. The script accepts `--prefix`, `--node-bin`, `--socket`, `--unit`,
`--web-url`, `--web-host` and `--data` for other installations. It never
restarts a second time after a failure. After recovery, remove
`<prefix>/bin/.deploy-pending` before another deploy. The marker preserves
the rollback pair until an owner resolves the failure.

An older binary cannot open a store after a schema step. To roll back that
step, stop the unit first. Restore `bin/dyna-prev` and `bin/pi-prev.tgz` to
`bin/dyna` and `bin/pi.tgz`. Restore the matching engine copy from
`<data>/backups/pre-upgrade-v<old>-<time>.db` as `<data>/store.db`, and remove
`<data>/store.db-wal` and `<data>/store.db-shm` before starting the unit.
This discards store changes made since that copy. Check the unit start limit
before restarting. Without a schema step, the previous pair alone can be used.
